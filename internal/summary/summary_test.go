package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/google/go-github/v88/github"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

const marker = "UNIQUE_README_TEST_BLOCK_27BB69"
const readmeText = `# Workflow Toolkit

Workflow Toolkit helps teams organize command-line tasks in local workspaces. It reads a project configuration, runs groups of scripts, and displays the status of each task. The documentation describes local workflows and configuration requirements for teams that manage multiple services.

## Features

Script groups and clear status output make repeatable work easier to review. The project provides examples for local development and shows how to run tasks in a chosen order.

![Badge](https://example.test/ignore.png)
` + "\n```sh\n" + marker + "\n```\n"

type sourceFixture struct {
	sha, body, description string
	topics                 []string
	missing, private       bool
	etags                  []string
	unsolicited304         bool
	onRead                 func()
	shaFailures            int
	identityChecks         int
	versionChecks          int
}

func (s *sourceFixture) RepositoryByID(_ context.Context, id int64, _ gh.Policy) (gh.RepositoryResult, error) {
	s.identityChecks++
	return gh.RepositoryResult{Repository: &sdk.Repository{ID: &id, FullName: new("owner/project"),
		Description: &s.description, Private: &s.private, Topics: s.topics}}, nil
}

func (s *sourceFixture) Readme(_ context.Context, _, _, etag string, _ int, _ gh.Policy) (gh.ReadmeResult, error) {
	s.etags = append(s.etags, etag)
	if s.onRead != nil {
		s.onRead()
	}
	if s.unsolicited304 {
		s.unsolicited304 = false
		return gh.ReadmeResult{NotModified: true}, nil
	}
	if s.missing {
		return gh.ReadmeResult{Missing: true}, nil
	}
	if etag == s.sha {
		return gh.ReadmeResult{NotModified: true}, nil
	}
	return gh.ReadmeResult{SHA: s.sha, ETag: s.sha, URL: "https://github.com/owner/project/blob/main/README.md", Path: "README.md", Body: []byte(s.body)}, nil
}

func (s *sourceFixture) ReadmeSHA(context.Context, string, string, string, gh.Policy) (string, error) {
	s.versionChecks++
	if s.shaFailures > 0 {
		s.shaFailures--
		return "", &gh.Failure{Code: consts.CodeGitHubNetwork}
	}
	if s.missing {
		return "", nil
	}
	return s.sha, nil
}

func TestGuideRetriesVerificationWithoutModelOrReadmeDownload(t *testing.T) {
	e, upstream, _, _ := fixture(t)
	upstream.shaFailures = 2
	calls := 0
	e.Provider = modelFixture(func(context.Context, string) ModelResult {
		calls++
		return ModelResult{Content: modelAnswer(false)}
	})
	decision, err := e.Queue.Manual(t.Context(), 1, "retry-verification", true, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stale", "stale", "ready"} {
		if err := e.Queue.DB.Model(&storage.Job{}).Where("id = ?", decision.Job.ID).Update("available_at", 0).Error; err != nil {
			t.Fatal(err)
		}
		step(t, e)
		if guide := row[storage.AISummary](t, e.Queue.DB, "repo_id = ?", 1); guide.SummaryStatus != want {
			t.Fatalf("发布状态应为 %s: %+v", want, guide)
		}
	}
	if calls != 1 || len(upstream.etags) != 1 {
		t.Fatalf("复核重试不应重新下载 README 或调用模型: model=%d downloads=%d", calls, len(upstream.etags))
	}
}

type modelFixture func(context.Context, string) ModelResult

func (f modelFixture) Generate(ctx context.Context, prompt string) ModelResult { return f(ctx, prompt) }

func fixture(t *testing.T) (*Engine, *sourceFixture, *bytes.Buffer, string) {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "summary.db")
	cfg.AI.BaseURL, cfg.AI.Model = "http://127.0.0.1:9876/v1", "fixture-model"
	pool, err := storage.Open(t.Context(), cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if _, err := migrations.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 3; id++ {
		r := storage.Repository{RepoID: id, Owner: "owner", Name: fmt.Sprint(id), FullName: fmt.Sprintf("owner/%d", id),
			TopicsJSON: `["workflow"]`, State: "active", Grade: "C", NameIsCurrent: true}
		if id == 1 {
			r.Grade, r.TopicsJSON = "A", "[]"
		}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := &sourceFixture{sha: strings.Repeat("a", 40), body: readmeText + "\n" + marker,
		description: "A project for organizing local workflows."}
	logs := &bytes.Buffer{}
	q := Queue{DB: db, AI: cfg.AI, LeaseSeconds: 30}
	return &Engine{Queue: q, GitHub: source, Logger: slog.New(slog.NewJSONHandler(logs, nil))}, source, logs, cfg.Storage.Path
}

func modelAnswer(short bool) string {
	value := "项目用于组织本地开发任务，可以配置脚本分组、重复执行命令并查看每一步的状态。它适合维护多服务工作区的团队，文档提供了启动命令和配置示例，可据此搭建本地工作流并验证执行结果。"
	evidence := "仓库 README"
	if short {
		value = "这个开源项目面向需要组织本地开发任务的团队，仓库简介只说明了基本用途，具体能力仍需查看文档确认。"
		evidence = "仓库 Description"
	}
	raw, _ := json.Marshal(struct {
		answer
		Topics []string `json:"topics"`
		Themes []string `json:"theme_slugs"`
	}{answer{Language: "en", Title: json.RawMessage(`"本地脚本编排工具"`), Summary: value, Features: []string{"脚本分组", "状态输出", "脚本分组"},
		RecommendationReason: json.RawMessage(`"维护多服务工作区的团队可以借鉴它的脚本分组方式，统一组织本地任务。"`),
		Tags:                 []string{"自动化"}, Evidence: []string{evidence}, Uncertainty: []string{"TRANSIENT_MODEL_FIELD_80FC"}}, []string{"workflow", "dev-tools"}, []string{}})
	return string(raw)
}

func TestThemeBackfillUsesREADMEAndResumesFromSQLite(t *testing.T) {
	e, upstream, _, _ := fixture(t)
	db := e.Queue.DB
	upstream.topics = []string{"unrelated"}
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Update("topics_json", `["unrelated"]`).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		calls++
		if !strings.Contains(prompt, "主题词表") || !strings.Contains(prompt, "mcp-servers") || !strings.Contains(prompt, "仓库 README") {
			t.Fatalf("缺少配置词表或 README: %q", prompt)
		}
		if calls == 1 {
			if !strings.HasPrefix(prompt, "任务：摘要和主题词") {
				t.Fatalf("导读和主题应使用同一请求: %q", prompt)
			}
			return ModelResult{Content: strings.Replace(modelAnswer(false), `"theme_slugs":[]`, `"theme_slugs":["mcp-servers","invented"]`, 1)}
		}
		if !strings.HasPrefix(prompt, "任务：主题词") || !strings.Contains(prompt, "已落库的 MCP 项目说明") {
			t.Fatalf("已有项目应复用已保存的中文 README: %q", prompt)
		}
		return ModelResult{Content: `{"topics":["mcp-server"],"theme_slugs":["mcp-servers"]}`}
	})
	if _, err := e.Queue.Manual(t.Context(), 1, "generate-with-topics", true, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	saved := row[storage.RepoAITopics](t, db, "repo_id=?", 1)
	if saved.ThemeSlugsJSON != `["mcp-servers"]` || saved.ThemeCatalogKey == "" || calls != 1 {
		t.Fatalf("仅能保存词表内标识: %+v", saved)
	}
	if err := db.Exec("UPDATE repo_readme_documents SET complete=1,chinese_html=? WHERE repo_id=1",
		"<p>已落库的 MCP 项目说明。项目提供 MCP 服务端。</p>").Error; err != nil {
		t.Fatal(err)
	}
	setting := storage.SystemSetting{Key: storage.ThemeCatalogKey, ValueJSON: `"changed"`}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job, err := e.Queue.insert(db, JobType, 1, "previous-github-budget", payload{}, 100, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&job).Updates(jobFields("failed", "github_local_budget", now.UnixMilli(), now.Add(time.Hour).UnixMilli(), 0)).Error; err != nil {
		t.Fatal(err)
	}
	reads := len(upstream.etags)
	// 耗尽 README 请求预算后，主题回填仍须通过正常的 Worker 领取流程完成。
	e.Queue.Pacing = storage.JobPacing{{Key: "readme", Types: []string{JobType, TranslationJob},
		Resource: gh.ReadmeCheckResource, Window: time.Hour, Budget: 120}}
	rate := storage.RateLimit{Resource: gh.ReadmeCheckResource, LocalWindow: now.UnixMilli(), LocalRequests: 120}
	if err := db.Create(&rate).Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	// 唤醒缓存任务后填满今日自动额度，验证领取被额度拦住。
	spent := storage.AICall{CallKey: "used-auto-budget", JobID: job.ID, Provider: ProviderKey(e.Queue.AI),
		Origin: consts.OriginAuto, Status: consts.StatusSucceeded, ReservedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}
	if err := db.Create(&spent).Error; err != nil {
		t.Fatal(err)
	}
	limit := e.Queue.AI.Summary.AutoDailyCalls
	e.Queue.AI.Summary.AutoDailyCalls = 1
	if done, err := e.Step(t.Context()); err != nil || done {
		t.Fatalf("AI 额度耗尽时不应领取缓存回填: done=%v error=%v", done, err)
	}
	e.Queue.AI.Summary.AutoDailyCalls = limit
	step(t, e)
	saved = row[storage.RepoAITopics](t, db, "repo_id=?", 1)
	if calls != 2 || len(upstream.etags) != reads || saved.ThemeCatalogKey != "changed" || saved.TopicsJSON != `["mcp-server"]` {
		t.Fatalf("回填应在原 GitHub 限流重试时间前完成: %+v, calls=%d reads=%v", saved, calls, upstream.etags)
	}
	var pacingWrites int64
	if err := db.Model(&storage.MaintenanceState{}).Where("key = ?", "job.pacing.readme").Count(&pacingWrites).Error; err != nil || pacingWrites != 0 {
		t.Fatalf("缓存回填不应重置 GitHub 任务开工间隔: count=%d error=%v", pacingWrites, err)
	}
	e.Queue.Pacing = nil
	catalog, err := storage.ReadThemeCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	compact := themePrompt(catalog, 2048/4)
	for _, term := range catalog.Terms {
		if !strings.Contains(compact, term.Slug) {
			t.Fatalf("最小输入预算遗漏主题 %s: %q", term.Slug, compact)
		}
	}
	if err := db.Model(&setting).Update("value_json", `"bad-response"`).Error; err != nil {
		t.Fatal(err)
	}
	e.Provider = modelFixture(func(context.Context, string) ModelResult { return ModelResult{Content: `{"topics":[]}`} })
	for _, kind := range []string{"cached", "upstream"} {
		if kind == "upstream" {
			if err := db.Exec("UPDATE repo_readme_documents SET complete=0 WHERE repo_id=1").Error; err != nil {
				t.Fatal(err)
			}
		}
		job, err := e.Queue.insert(db, JobType, 1, "bad-topics-"+kind, payload{}, 170, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		step(t, e)
		failed := row[storage.Job](t, db, "id=?", job.ID)
		guide := row[storage.AISummary](t, db, "repo_id=?", 1)
		if !jobTopicsOnly(failed) || failed.LastErrorCode == nil || *failed.LastErrorCode != "invalid_output" ||
			guide.SummaryStatus != "ready" {
			t.Fatalf("%s 回填失败不得撤下有效导读: job=%+v guide=%+v", kind, failed, guide)
		}
		if err := db.Model(&failed).Update("status", "canceled").Error; err != nil {
			t.Fatal(err)
		}
	}
	now = time.Now()
	recovery, err := e.Queue.insert(db, JobType, 1, "crashed-topics", payload{TopicsOnly: true}, 170, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	running, err := e.Queue.claim(t.Context(), now, true, true)
	if err != nil || running == nil || running.ID != recovery.ID {
		t.Fatalf("未领取待模拟中断的主题回填: %v, %v", running, err)
	}
	call, err := e.Queue.reserve(t.Context(), *running, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.sending(t.Context(), *running, call); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(running).Update("lease_until", now.Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Recover(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	recovered := row[storage.Job](t, db, "id=?", running.ID)
	if recovered.Status != "unknown" || row[storage.AISummary](t, db, "repo_id=?", 1).SummaryStatus != "ready" {
		t.Fatalf("主题回填发送后中断应等待确认并保留导读: %+v", recovered)
	}
}

func step(t *testing.T, e *Engine) {
	t.Helper()
	if done, err := e.Step(t.Context()); err != nil || !done {
		t.Fatalf("摘要任务未运行: done=%v error=%v", done, err)
	}
}

func schedule(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Queue.Schedule(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func row[T any](t *testing.T, db *gorm.DB, where string, args ...any) T {
	t.Helper()
	var out T
	if err := db.Where(where, args...).Take(&out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDrainPreservesModelResultAndReservation(t *testing.T) {
	for _, scenario := range []struct {
		name, jobStatus, callStatus, code string
		reserved, limit, unknown          bool
	}{
		{name: "已确认无效响应", jobStatus: "failed", callStatus: "failed", code: "invalid_output"},
		{name: "发送结果未知", jobStatus: "unknown", callStatus: "unknown", code: "ai_delivery_unknown", unknown: true},
		{name: "复用未发送预留", jobStatus: "failed", callStatus: "reserved", code: storage.MaintenanceInterrupted, reserved: true},
		{name: "已到任务重试上限", jobStatus: "dead", callStatus: "failed", code: storage.MaintenanceInterrupted, reserved: true, limit: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			e, _, _, _ := fixture(t)
			schedule(t, e)
			job := row[storage.Job](t, e.Queue.DB, "repo_id=?", 1)
			if scenario.limit {
				if err := e.Queue.DB.Model(&job).Update("max_attempts", 1).Error; err != nil {
					t.Fatal(err)
				}
			}
			started := make(chan struct{})
			if scenario.reserved {
				var first atomic.Bool
				if err := e.Queue.DB.Callback().Update().Before("gorm:update").Register("test:drain_sending", func(tx *gorm.DB) {
					if tx.Statement.Table == "ai_calls" && first.CompareAndSwap(false, true) {
						close(started)
						<-tx.Statement.Context.Done()
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			var modelCalls atomic.Int32
			e.Provider = modelFixture(func(ctx context.Context, _ string) ModelResult {
				if modelCalls.Add(1) > 1 {
					return ModelResult{Content: modelAnswer(false)}
				}
				close(started)
				<-ctx.Done()
				if scenario.unknown {
					return ModelResult{Code: "ai_delivery_unknown", Unknown: true}
				}
				return ModelResult{Content: "{}", InputTokens: new(int64(31)), OutputTokens: new(int64(5))}
			})
			workers := lifecycle.NewWorkers(t.Context(), func(ctx context.Context) error { _, err := e.Step(ctx); return err })
			workers.Resume()
			drain, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			select {
			case <-started:
			case <-drain.Done():
				t.Fatal("未到达模型调用边界")
			}
			if err := workers.Pause(drain); err != nil {
				t.Fatal(err)
			}
			saved := row[storage.Job](t, e.Queue.DB, "id=?", job.ID)
			call := row[storage.AICall](t, e.Queue.DB, "job_id=?", job.ID)
			if saved.Status != scenario.jobStatus || saved.LeaseToken != nil || saved.LastErrorCode == nil ||
				*saved.LastErrorCode != scenario.code || call.Status != scenario.callStatus || call.ReservedAt == 0 {
				t.Fatalf("排空后的任务 %v 调用 %v", saved, call)
			}
			if !scenario.reserved && !scenario.unknown && (call.InputTokens == nil || *call.InputTokens != 31) {
				t.Fatal("确定的用量未提交")
			}
			if scenario.reserved && modelCalls.Load() != 0 {
				t.Fatal("排空后仍发送模型请求")
			}
			if scenario.callStatus == "reserved" {
				if err := e.Queue.DB.Model(&saved).Update("available_at", 0).Error; err != nil {
					t.Fatal(err)
				}
				e.Provider = modelFixture(func(context.Context, string) ModelResult { return ModelResult{Content: modelAnswer(false)} })
				step(t, e)
				reused := row[storage.AICall](t, e.Queue.DB, "job_id=?", job.ID)
				if reused.CallKey != call.CallKey || reused.Status != "succeeded" {
					t.Fatalf("预留未复用: %+v", reused)
				}
			}
		})
	}
}

func TestSummaryCacheSourceChangeAndPrivacy(t *testing.T) {
	e, source, logs, path := fixture(t)
	db := e.Queue.DB
	calls := 0
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		calls++
		if !strings.Contains(prompt, marker) || !strings.Contains(prompt, "Script groups") ||
			strings.Contains(prompt, "ignore.png") || strings.Contains(prompt, "```sh") {
			t.Error("正文提取或提示词输入不符")
		}
		active := row[storage.AICall](t, db, "status='sending'")
		if active.Origin != "auto" {
			t.Error("应在调用前持久化自动计量")
		}
		return ModelResult{Content: modelAnswer(false), InputTokens: new(int64(310)), OutputTokens: new(int64(218))}
	})
	schedule(t, e)
	step(t, e)
	first := row[storage.AISummary](t, db, "repo_id=1")
	if first.SummaryStatus != "ready" || first.SourceLanguage != "en" || first.SourceQuality != "sufficient" ||
		first.ReadmeSHA == nil || *first.ReadmeSHA != source.sha || calls != 1 ||
		first.RecommendationReason == nil || *first.RecommendationReason != "维护多服务工作区的团队可以借鉴它的脚本分组方式，统一组织本地任务。" {
		t.Fatalf("首次摘要错误: %+v calls=%d", first, calls)
	}
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Update("stars", 500).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&storage.SummaryCheck{}).Where("repo_id=1").Update("next_check_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	step(t, e)
	if calls != 1 || source.etags[len(source.etags)-1] != source.sha {
		t.Fatal("仅 Star 变化的 304 不应重新调用模型")
	}
	decision, err := e.Queue.Manual(t.Context(), 1, "cache", false, true, time.Now())
	if err != nil || !decision.Cached {
		t.Fatalf("已验证缓存应可直接复用: %+v %v", decision, err)
	}

	// 新提示词须无条件读取正文，意外 304 也不能构造正文。
	e.Queue.AI.Summary.PromptVersion++
	source.unsolicited304 = true
	before := len(source.etags)
	schedule(t, e)
	step(t, e)
	if calls != 2 || source.etags[before] != "" || source.etags[before+1] != "" {
		t.Fatal("提示词升级未无条件重新读取 README")
	}
	source.sha = strings.Repeat("b", 40)
	if err := db.Model(&storage.SummaryCheck{}).Where("repo_id=1").Update("next_check_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	step(t, e)
	current := row[storage.AISummary](t, db, "repo_id=1")
	if calls != 3 || *current.ReadmeSHA != source.sha || current.PromptVersion != 2 {
		t.Fatalf("README 变更未刷新摘要: %+v", current)
	}
	if err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		t.Fatal(err)
	}
	document := row[storage.RepoReadmeDocument](t, db, "repo_id=1")
	if string(document.SourceMarkdown) != source.body || document.ReadmeSHA != source.sha {
		t.Fatal("快照未保存当前版本的 README")
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("TRANSIENT_MODEL_FIELD_80FC")) {
		t.Fatalf("模型的瞬时字段不能写入数据库: %v", err)
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatal("日志出现 README 原文")
	}
	job := row[storage.Job](t, db, "type=?", JobType)
	if strings.Contains(job.PayloadJSON, marker) {
		t.Fatal("任务中出现 README 原文")
	}
}

func TestUnknownCallRecoveryAndAcknowledgement(t *testing.T) {
	e, _, _, _ := fixture(t)
	q, db := e.Queue, e.Queue.DB
	now := time.Now()
	request, err := q.Manual(t.Context(), 1, "original", true, true, now)
	if err != nil {
		t.Fatal(err)
	}
	same, err := q.Manual(t.Context(), 1, "original", false, false, now.Add(time.Hour))
	if err != nil || same.Job.ID != request.Job.ID || same.Conflict {
		t.Fatal("相同请求应返回原任务")
	}
	conflict, err := q.Manual(t.Context(), 1, "second", true, true, now)
	if err != nil || !conflict.Conflict || conflict.Job.ID != request.Job.ID {
		t.Fatal("同仓库未决摘要应冲突")
	}
	job, err := q.claim(t.Context(), now, true, true)
	if err != nil || job == nil {
		t.Fatalf("领取失败: %v", err)
	}
	call, err := q.reserve(t.Context(), *job, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.sending(t.Context(), *job, call); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&storage.Job{}).Where("id=?", job.ID).Update("lease_until", now.Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.Recover(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	old := row[storage.Job](t, db, "id=?", job.ID)
	bill := row[storage.AICall](t, db, "job_id=?", job.ID)
	if old.Status != "unknown" || bill.Status != "unknown" {
		t.Fatal("已发送的过期租约应转未决")
	}
	if newJob, err := q.claim(t.Context(), now, true, true); err != nil || newJob != nil {
		t.Fatal("未决模型调用不得被重领")
	}
	if err := q.settle(t.Context(), *job, outcome{status: "succeeded", attempts: job.Attempts, call: &call}); !errors.Is(err, errLease) {
		t.Fatalf("旧租约可写入: %v", err)
	}
	retry, err := q.RetryUnknown(t.Context(), job.ID, "ack", true, now)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := q.RetryUnknown(t.Context(), job.ID, "ack", false, now)
	if err != nil || replay.ID != retry.ID || retry.ID == job.ID {
		t.Fatal("确认重试必须可幂等回放")
	}
	old = row[storage.Job](t, db, "id=?", job.ID)
	bill = row[storage.AICall](t, db, "job_id=?", job.ID)
	if old.Status != "canceled" || bill.Status != "failed" || *bill.ErrorCode != "unknown_acknowledged" {
		t.Fatal("旧调用应原子关闭")
	}
}

func TestBudgetAndInvalidOutputRepair(t *testing.T) {
	e, _, _, _ := fixture(t)
	e.Queue.AI.Summary.AutoDailyCalls = 2
	e.Queue.AI.Summary.RepairAttempts = 1
	db := e.Queue.DB
	paid := 0
	e.Provider = modelFixture(func(context.Context, string) ModelResult {
		paid++
		return ModelResult{Content: "invalid " + marker}
	})
	schedule(t, e)
	step(t, e)
	job := row[storage.Job](t, db, "repo_id=1 AND type=?", JobType)
	if job.Status != "failed" || *job.LastErrorCode != "invalid_output" {
		t.Fatal("无效输出须先结算再重试")
	}
	if err := db.Model(&job).Update("available_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	step(t, e)
	job = row[storage.Job](t, db, "id=?", job.ID)
	if job.Status != "dead" || paid != 2 {
		t.Fatalf("结构重试超过配置: %+v paid=%d", job, paid)
	}
	if err := db.Model(&storage.Repository{}).Where("repo_id=2").Update("grade", "A").Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	step(t, e)
	limited := row[storage.Job](t, db, "repo_id=2 AND type=?", JobType)
	if limited.Status != "failed" || *limited.LastErrorCode != "ai_budget_exhausted" || limited.Attempts != 0 || paid != 2 {
		t.Fatalf("预算限制错误: %+v paid=%d", limited, paid)
	}
	manual, err := e.Queue.Manual(t.Context(), 3, "manual", true, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step(t, e)
	call := row[storage.AICall](t, db, "job_id=?", manual.Job.ID)
	if call.Origin != "manual" || paid != 3 || call.Status != "failed" {
		t.Fatal("手动预算应独立计量")
	}
}

func TestChineseAndDescriptionFallback(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.topics = []string{"workflow"}
	if err := e.Queue.DB.Model(&storage.Repository{}).Where("repo_id=1").Update("topics_json", `["workflow"]`).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.Provider = modelFixture(func(context.Context, string) ModelResult {
		calls++
		return ModelResult{Content: modelAnswer(source.missing)}
	})
	source.body = strings.Repeat("这是用于管理本地开发工作流的开源工具。它可以组织脚本，显示执行状态，帮助开发团队检查操作结果。", 8)
	schedule(t, e)
	step(t, e)
	check := row[storage.SummaryCheck](t, e.Queue.DB, "repo_id=1")
	chinese := row[storage.AISummary](t, e.Queue.DB, "repo_id=1")
	if check.SourceLanguage != "zh" || chinese.SummaryStatus != "ready" || calls != 1 {
		t.Fatalf("符合条件的中文来源应自动生成导读: %+v", check)
	}
	source.missing = true
	if _, err := e.Queue.Manual(t.Context(), 1, "no-readme", true, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	result := row[storage.AISummary](t, e.Queue.DB, "repo_id=1")
	if result.SourceQuality != "insufficient_source" || result.ReadmeSHA != nil ||
		result.SourceFingerprint != descriptionKey(source.description) || calls != 2 {
		t.Fatalf("缺 README 回退有误: %+v", result)
	}
	source.description = "A different public project description for workflow tests."
	if err := e.Queue.DB.Model(&storage.Repository{}).Where("repo_id=1").
		Updates(map[string]any{"description": source.description, "last_success_at": time.Now().UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	d, err := e.Queue.Manual(t.Context(), 1, "changed-description", false, true, time.Now())
	if err != nil || d.Cached {
		t.Fatal("新的 description 不应命中旧缓存")
	}
	step(t, e)
	if calls != 3 {
		t.Fatal("description 变更后应重新生成")
	}
}

func TestAITopicsBackfillAndAboutRefresh(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	calls := 0
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		calls++
		if !strings.Contains(prompt, source.description) || !strings.Contains(prompt, "Script groups") {
			t.Fatal("主题词模型输入缺少简介或 README")
		}
		if calls == 1 {
			if !strings.HasPrefix(prompt, "任务：摘要和主题词（") {
				t.Fatal("首次分析应合并摘要和主题词")
			}
			return ModelResult{Content: modelAnswer(false)}
		}
		if !strings.HasPrefix(prompt, "任务：主题词\n") {
			t.Fatal("已有有效摘要时只需生成主题词")
		}
		return ModelResult{Content: `{"topics":[" MCP ","mcp","dev-tools","无效主题","not a slug"],"theme_slugs":[]}`}
	})
	schedule(t, e)
	step(t, e)
	original := row[storage.AISummary](t, db, "repo_id=1")
	topics := row[storage.RepoAITopics](t, db, "repo_id=1")
	if topics.TopicsJSON != `["workflow","dev-tools"]` || topics.ModelName != e.Queue.AI.Model {
		t.Fatalf("首次主题词保存有误: %+v", topics)
	}
	// 模拟升级前只有摘要的仓库，已有摘要缓存仍能复用。
	if err := db.Delete(&topics).Error; err != nil {
		t.Fatal(err)
	}
	decision, err := e.Queue.Manual(t.Context(), 1, "ai-topics-backfill", false, true, time.Now())
	if err != nil || decision.Cached || decision.Job.ID == 0 {
		t.Fatalf("已有摘要缺少 AI Topics 时应入队: %+v %v", decision, err)
	}
	step(t, e)
	topics = row[storage.RepoAITopics](t, db, "repo_id=1")
	if topics.TopicsJSON != `["mcp","dev-tools"]` {
		t.Fatalf("主题词标准化有误: %+v", topics)
	}
	source.description = "A tool for developers to build a workflow automation server."
	if err := db.Model(&storage.SummaryCheck{}).Where("repo_id=1").Update("next_check_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	step(t, e)
	refreshed := row[storage.RepoAITopics](t, db, "repo_id=1")
	if calls != 3 || refreshed.SourceFingerprint == topics.SourceFingerprint {
		t.Fatalf("README 304 不能掩盖简介变更: %+v calls=%d", refreshed, calls)
	}
	summary := row[storage.AISummary](t, db, "repo_id=1")
	repo := row[storage.Repository](t, db, "repo_id=1")
	if summary.GeneratedAt != original.GeneratedAt || repo.TopicsJSON != "[]" {
		t.Fatal("补跑主题词不应重写摘要或 GitHub Topics")
	}
	// 确认来源已被清空后，旧主题词不能继续参与分类。
	source.missing, source.description = true, ""
	if err := db.Model(&storage.SummaryCheck{}).Where("repo_id=1").Update("next_check_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	step(t, e)
	var remaining int64
	if err := db.Model(&storage.RepoAITopics{}).Where("repo_id=1").Count(&remaining).Error; err != nil || remaining != 0 || calls != 3 {
		t.Fatalf("来源清空后应移除旧主题词且不再调用模型: rows=%d calls=%d err=%v", remaining, calls, err)
	}
}

func TestAITopicsForChineseRepository(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Update("grade", "C").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&storage.Repository{}).Where("repo_id<>1").Update("topics_json", `["cli"]`).Error; err != nil {
		t.Fatal(err)
	}
	source.body = strings.Repeat("这是用于管理本地开发工作流的开源工具。它可以组织脚本，显示执行状态，帮助开发团队检查操作结果。", 8)
	calls := 0
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		calls++
		if !strings.HasPrefix(prompt, "任务：主题词\n") {
			t.Fatal("中文仓库只应自动提取主题词")
		}
		if calls == 1 {
			return ModelResult{Content: `{"topics":["自动化工具"]}`}
		}
		return ModelResult{Content: `{"topics":[],"theme_slugs":[]}`}
	})
	schedule(t, e)
	step(t, e)
	job := row[storage.Job](t, db, "repo_id=1 AND type=?", JobType)
	if job.Status != "failed" || job.LastErrorCode == nil || *job.LastErrorCode != "invalid_output" {
		t.Fatalf("全无效的模型主题词应进入格式重试: %+v", job)
	}
	if err := db.Model(&job).Update("available_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	step(t, e)
	if topics := row[storage.RepoAITopics](t, db, "repo_id=1"); topics.TopicsJSON != "[]" {
		t.Fatalf("模型返回空主题时也应标记为已分析: %+v", topics)
	}
	var summaries int64
	if err := db.Model(&storage.AISummary{}).Where("repo_id=1").Count(&summaries).Error; err != nil || summaries != 0 {
		t.Fatalf("主题词任务不应创建中文摘要: %d %v", summaries, err)
	}
	if err := db.Model(&storage.SummaryCheck{}).Where("repo_id=1").Update("next_check_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	step(t, e)
	if calls != 2 {
		t.Fatal("空主题词结果也应复用缓存")
	}
}

func TestAITopicsScheduleUsesSpareBudget(t *testing.T) {
	e, _, _, _ := fixture(t)
	db := e.Queue.DB
	e.Queue.AI.Summary.AutoDailyCalls = 1
	if err := db.Model(&storage.Repository{}).Where("repo_id=2").Update("topics_json", "[]").Error; err != nil {
		t.Fatal(err)
	}
	e.Provider = modelFixture(func(context.Context, string) ModelResult { return ModelResult{Content: modelAnswer(false)} })
	schedule(t, e)
	step(t, e)
	schedule(t, e)
	var queued int64
	if err := db.Model(&storage.Job{}).Where("repo_id=2 AND type=?", JobType).Count(&queued).Error; err != nil || queued != 0 {
		t.Fatalf("自动预算由高优先级任务占用时，低优先级主题词不应排队: jobs=%d err=%v", queued, err)
	}
}

func TestReadmeVisibilityChangesBeforeModel(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.onRead = func() { source.private = true }
	e.Provider = modelFixture(func(context.Context, string) ModelResult {
		t.Error("已转为私有的仓库不能发送给模型")
		return ModelResult{}
	})
	schedule(t, e)
	step(t, e)
	job := row[storage.Job](t, e.Queue.DB, "repo_id=1 AND type=?", JobType)
	repo := row[storage.Repository](t, e.Queue.DB, "repo_id=1")
	var calls int64
	if err := e.Queue.DB.Model(&storage.AICall{}).Count(&calls).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "dead" || job.LastErrorCode == nil || *job.LastErrorCode != "summary_repo_unavailable" || calls != 0 {
		t.Fatalf("仓库在获取正文期间转私有，应终止且不计费: %+v calls=%d", job, calls)
	}
	if repo.State != "unavailable" || repo.FirstNotFoundAt == nil {
		t.Fatalf("已确认变私有的仓库应撤回公开展示: %+v", repo)
	}
}

func TestShortReadmeCanProduceGroundedRecommendation(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = readmeText[:350]
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		task, _, _ := strings.Cut(prompt, "\n")
		if !strings.Contains(task, "目标 50 至 80 个字符") {
			t.Fatalf("简短 README 应允许较短的摘要: %s", task)
		}
		return ModelResult{Content: `{"summary_zh":"这个项目能把本地脚本组织成可重复执行的工作流，方便开发者查看任务状态。","recommendation_reason":"维护多个本地服务的团队可以参考它通过项目配置统一组织脚本的方式。","evidence":["仓库 README"]}`}
	})
	if _, err := e.Queue.Manual(t.Context(), 1, "short-readme", false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	result := row[storage.AISummary](t, e.Queue.DB, "repo_id=1")
	if result.SourceQuality != insufficient || result.ReadmeSHA == nil || result.RecommendationReason == nil || *result.RecommendationReason == "" {
		t.Fatalf("应保存简短 README 的 AI 理由及来源: %+v", result)
	}
}

func TestModelInputBudgetAndCopiedSource(t *testing.T) {
	s := source{fullName: strings.Repeat("a", 39) + "/" + strings.Repeat("b", 100), language: "en",
		description: strings.Repeat("description ", 200), body: strings.TrimSpace(readmeText), quality: sufficient}
	prompt, sent := s.prompt(config.DefaultSummary().InputTokens, true, false, true)
	task, _, _ := strings.Cut(prompt, "\n")
	if sent.body == "" || sent.description == "" || !strings.Contains(prompt, sent.body) ||
		!strings.Contains(task, "目标 80 至 160 个字符") || !strings.Contains(task, "editorial_brief") ||
		len(prompt)+len(systemPrompt)+128 > config.DefaultSummary().InputTokens {
		t.Fatal("提示词须在预算内携带真实证据")
	}
	const lowBudget = 2048
	s.body = strings.Repeat(readmeText, 10)
	prompt, sent = s.prompt(lowBudget, true, true, true)
	task, _, _ = strings.Cut(prompt, "\n")
	if sent.body == "" || sent.quality != incomplete || !strings.Contains(task, "目标 50 至 80 个字符") || strings.Contains(task, "editorial_brief") ||
		len(prompt)+len(systemPrompt)+128 > lowBudget {
		t.Fatal("低输入预算截断正文时应保留证据并允许短摘要")
	}
	body := strings.Repeat("本文档说明开发团队如何配置并使用自动化工具，按实际项目要求组织和检查本地工作。", 3)
	raw, _ := json.Marshal(answer{Language: "zh", Summary: "这是一份项目使用说明。\n" + body + "\n实际运行仍需核实。", Evidence: []string{"仓库 README"}})
	if _, ok := resultSummary(string(raw), source{body: "## " + body, quality: sufficient}, 1, "fixture", "test", 1, 0); ok {
		t.Fatal("摘要加上前后缀后仍不得保存完整原文")
	}
	var generated answer
	if err := json.Unmarshal([]byte(modelAnswer(false)), &generated); err != nil {
		t.Fatal(err)
	}
	quote := "读者可按照仓库文档的调试步骤复现这套本地开发流程"
	generated.RecommendationReason, _ = json.Marshal(quote + "。")
	generated.Title = json.RawMessage(`"项目用于组织本地开发任务"`)
	raw, _ = json.Marshal(generated)
	saved, ok := resultSummary(string(raw), source{body: readmeText + "\n" + quote, quality: sufficient}, 1, "fixture", "test", 1, 0)
	if !ok || saved.RecommendationReason == nil || *saved.RecommendationReason != "" || saved.TitleZh == nil || *saved.TitleZh != "" {
		t.Fatalf("小标题或推荐理由重复时应保留有效摘要: %v %+v", ok, saved)
	}
}

func TestGeneratedSummaryLengthBounds(t *testing.T) {
	for _, tc := range []struct {
		size    int
		quality string
		valid   bool
	}{{19, insufficient, false}, {20, insufficient, true}, {79, sufficient, false},
		{80, sufficient, true}, {200, sufficient, true}, {201, sufficient, false}} {
		body := strings.Repeat("文", tc.size)
		raw, _ := json.Marshal(answer{Summary: body, Evidence: []string{"仓库 README"}})
		saved, ok := resultSummary(string(raw), source{body: readmeText, quality: tc.quality}, 1, "fixture", "test", 1, 0)
		if ok != tc.valid || (ok && saved.SummaryZh != body) {
			t.Fatalf("导读篇幅校验错误，字符数 %d，来源 %s，有效 %v", tc.size, tc.quality, ok)
		}
	}
}

func TestSummarySourceKeepsProjectContentAcrossNoiseSections(t *testing.T) {
	body := `# CLI workflow runner

Run grouped local scripts and review their status.

## Sponsors
Promotional text

### Partner program
Join this unrelated promotion.

## Usage
Run the tool from the workspace.

## Directory structure
An exhaustive file listing.

## Limitations
Tasks require a configured shell.
`
	value := prose([]byte(body))
	for _, keep := range []string{"CLI workflow runner", "grouped local scripts", "Run the tool", "configured shell"} {
		if !strings.Contains(value, keep) {
			t.Fatalf("归一化丢失项目正文 %q: %s", keep, value)
		}
	}
	for _, remove := range []string{"Sponsors", "Promotional", "Partner program", "unrelated promotion", "Directory structure", "exhaustive file listing"} {
		if strings.Contains(value, remove) {
			t.Fatalf("归一化保留了无关内容 %q: %s", remove, value)
		}
	}
}

func TestLegacySummaryTitleRefreshWithModelOnly(t *testing.T) {
	e, _, _, _ := fixture(t)
	e.Provider = modelFixture(func(context.Context, string) ModelResult { return ModelResult{Content: modelAnswer(false)} })
	schedule(t, e)
	step(t, e)
	if err := e.Queue.DB.Model(&storage.AISummary{RepoID: 1}).Update(storage.AISummaryColumns.TitleZh, nil).Error; err != nil {
		t.Fatal(err)
	}
	offline, err := e.Queue.Manual(t.Context(), 1, "offline-cache", false, false, time.Now())
	if err != nil || !offline.Cached {
		t.Fatalf("模型不可用时应复用有效的旧摘要: %+v %v", offline, err)
	}
	refresh, err := e.Queue.Manual(t.Context(), 1, "title-refresh", false, true, time.Now())
	if err != nil || refresh.Cached || refresh.Job.ID == 0 {
		t.Fatalf("模型可用时旧摘要应安排补齐小标题: %+v %v", refresh, err)
	}
	step(t, e)
	saved := row[storage.AISummary](t, e.Queue.DB, storage.AISummaryColumns.RepoID+"=1")
	if saved.TitleZh == nil || *saved.TitleZh != "本地脚本编排工具" {
		t.Fatal("摘要任务未保存模型提供的小标题")
	}
}
