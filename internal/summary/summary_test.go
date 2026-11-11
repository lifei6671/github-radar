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
	missing, private       bool
	etags                  []string
	unsolicited304         bool
	onRead                 func()
}

func (s *sourceFixture) RepositoryByID(_ context.Context, id int64, _ gh.Policy) (gh.RepositoryResult, error) {
	return gh.RepositoryResult{Repository: &sdk.Repository{ID: &id, FullName: new("owner/project"),
		Description: &s.description, Private: &s.private}}, nil
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
	return gh.ReadmeResult{SHA: s.sha, ETag: s.sha, URL: "https://github.com/owner/project/blob/main/README.md", Body: []byte(s.body)}, nil
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
			TopicsJSON: "[]", State: "active", Grade: "C", NameIsCurrent: true}
		if id == 1 {
			r.Grade = "A"
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
	value := strings.Repeat("项目用于组织本地开发任务，可以配置脚本分组并查看执行状态。它适合维护多服务工作区的团队，文档提供了本地配置示例，具体运行效果需要验证。", 4)
	evidence := "仓库 README"
	if short {
		value = "这个开源项目面向需要组织本地开发任务的团队，仓库简介只说明了基本用途，具体能力仍需查看文档确认。"
		evidence = "仓库 Description"
	}
	raw, _ := json.Marshal(answer{Language: "en", Summary: value, Features: []string{"脚本分组", "状态输出", "脚本分组"},
		Tags: []string{"自动化"}, Evidence: []string{evidence}, Uncertainty: []string{marker}})
	return string(raw)
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
		first.ReadmeSHA == nil || *first.ReadmeSHA != source.sha || calls != 1 {
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
	for _, file := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(file)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(marker)) {
			t.Fatalf("原文或瞬时模型字段写入 %s", filepath.Base(file))
		}
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
	job, err := q.claim(t.Context(), now)
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
	if newJob, err := q.claim(t.Context(), now); err != nil || newJob != nil {
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
	calls := 0
	e.Provider = modelFixture(func(context.Context, string) ModelResult {
		calls++
		return ModelResult{Content: modelAnswer(true)}
	})
	source.body = strings.Repeat("这是用于管理本地开发工作流的开源工具。它可以组织脚本，显示执行状态，帮助开发团队检查操作结果。", 8)
	schedule(t, e)
	step(t, e)
	check := row[storage.SummaryCheck](t, e.Queue.DB, "repo_id=1")
	if check.SourceLanguage != "zh" || calls != 0 {
		t.Fatalf("中文主导来源应跳过自动模型: %+v", check)
	}
	source.missing = true
	if _, err := e.Queue.Manual(t.Context(), 1, "no-readme", false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	result := row[storage.AISummary](t, e.Queue.DB, "repo_id=1")
	if result.SourceQuality != "insufficient_source" || result.ReadmeSHA != nil ||
		result.SourceFingerprint != descriptionKey(source.description) || calls != 1 {
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
	if calls != 2 {
		t.Fatal("description 变更后应重新生成")
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
	var calls int64
	if err := e.Queue.DB.Model(&storage.AICall{}).Count(&calls).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != "dead" || job.LastErrorCode == nil || *job.LastErrorCode != "summary_repo_unavailable" || calls != 0 {
		t.Fatalf("仓库在获取正文期间转私有，应终止且不计费: %+v calls=%d", job, calls)
	}
}

func TestModelInputBudgetAndCopiedSource(t *testing.T) {
	s := source{fullName: strings.Repeat("a", 39) + "/" + strings.Repeat("b", 100), language: "en",
		description: strings.Repeat("description ", 200), body: readmeText, quality: sufficient}
	prompt, sent := s.prompt(config.DefaultSummary().InputTokens)
	if sent.body == "" || sent.description == "" || !strings.Contains(prompt, sent.body) ||
		len(prompt)+len(systemPrompt)+128 > config.DefaultSummary().InputTokens {
		t.Fatal("提示词须在预算内携带真实证据")
	}
	body := strings.Repeat("本文档说明开发团队如何配置并使用自动化工具，按实际项目要求组织和检查本地工作。", 7)
	raw, _ := json.Marshal(answer{Language: "zh", Summary: "这是一份项目使用说明。\n" + body + "\n实际运行仍需核实。", Evidence: []string{"仓库 README"}})
	if _, ok := resultSummary(string(raw), source{body: "## " + body, quality: sufficient}, 1, "fixture", "test", 1, 0); ok {
		t.Fatal("摘要加上前后缀后仍不得保存完整原文")
	}
}
