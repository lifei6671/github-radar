package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
	"gorm.io/gorm"
)

// cachedReadme 检查旧翻译测试关注的草稿状态，发布门槛由清理流程测试验证。
func cachedReadme(db *gorm.DB, repoID int64, _ bool) (ReadmeView, error) {
	var doc storage.RepoReadmeDocument
	err := db.Find(&doc, "repo_id = ?", repoID).Error
	state := consts.StatusPending
	if doc.Complete {
		state = consts.StatusReady
	} else if doc.RepoID != 0 && doc.ReadmeBytes == 0 {
		state = consts.StatusMissing
	}
	return ReadmeView{State: state, Language: doc.Language, SourceURL: doc.SourceURL, ChineseHTML: doc.ChineseHTML,
		Complete: doc.Complete, Truncated: doc.Truncated}, err
}

func TestSummaryPublishesShortReadmeTranslationFromSameCall(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	source.body = "This toolkit runs grouped scripts\n\n" +
		"[Usage](docs/usage.md)\n\n```sh\nrun-workflow\n```\n"
	calls := 0
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		calls++
		if !strings.Contains(prompt, `待翻译片段：["This toolkit runs grouped","scripts","Usage"]`) ||
			strings.Contains(prompt, "run-workflow") || strings.Contains(prompt, "docs/usage.md") {
			t.Fatalf("合并请求应只附带可翻译的正文: %s", prompt)
		}
		if _, err := e.Queue.ManualTranslation(t.Context(), 1, "overlapping-translation", true, time.Now()); err != nil {
			t.Fatal(err)
		}
		if job, err := e.Queue.claim(t.Context(), time.Now(), true, true); err != nil || job != nil {
			t.Fatalf("合并请求尚未完成时不能领取同仓库的翻译: %+v %v", job, err)
		}
		return ModelResult{Content: strings.TrimSuffix(modelAnswer(false), "}") + `,"t":["这个工具包用于分组运行","脚本","用法"]}`}
	})
	if _, err := e.Queue.Manual(t.Context(), 1, "summary-and-readme", false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	saved := row[storage.AISummary](t, db, "repo_id=1")
	if calls != 1 || len(source.etags) != 1 || saved.TitleZh == nil || *saved.TitleZh != "本地脚本编排工具" || saved.SummaryStatus != "ready" {
		t.Fatalf("单次请求应保存小标题和导读: calls=%d %+v", calls, saved)
	}
	view, err := cachedReadme(db, 1, false)
	if err != nil || !view.Complete || !strings.Contains(view.ChineseHTML, "这个工具包用于分组运行") ||
		!strings.Contains(view.ChineseHTML, "run-workflow") || !strings.Contains(view.ChineseHTML, "docs/usage.md") {
		t.Fatalf("合并译文应保留 README 结构: %+v %v", view, err)
	}
	e.Queue.AI.Summary.AutoDailyCalls = 1
	e.Translate = func(context.Context, string) ModelResult {
		t.Fatal("已合并译文的手动任务应复用缓存")
		return ModelResult{}
	}
	step(t, e)
	schedule(t, e)
	var jobs int64
	if err := db.Model(&storage.Job{}).Where("repo_id=1 AND type=?", TranslationJob).Count(&jobs).Error; err != nil || jobs != 1 {
		t.Fatalf("同版本译文已准备好，调度器无需再次翻译: jobs=%d err=%v", jobs, err)
	}
}

type localizedSourceFixture struct {
	*sourceFixture
	readme gh.ReadmeResult
	paths  []string
	err    error
}

func (s *localizedSourceFixture) LocalizedReadme(_ context.Context, _, _, file string, _ int, _ gh.Policy) (gh.ReadmeResult, error) {
	s.paths = append(s.paths, file)
	return s.readme, s.err
}

func (s *localizedSourceFixture) ReadmeSHA(ctx context.Context, owner, name, file string, p gh.Policy) (string, error) {
	if s.readme.Path == file {
		return s.readme.SHA, nil
	}
	return s.sourceFixture.ReadmeSHA(ctx, owner, name, file, p)
}

type unlinkedSourceFixture struct {
	*localizedSourceFixture
}

func (*unlinkedSourceFixture) ReadmeSiblings(context.Context, string, string, string, gh.Policy) ([]string, error) {
	return []string{"docs/README.zh-CN.md", "README_CN.md"}, nil
}

func TestBackfillOfficialReadmeWithoutModel(t *testing.T) {
	e, source, _, _ := fixture(t)
	e.Queue.AI.Summary.AutoDailyCalls = 0
	db := e.Queue.DB
	sha := strings.Repeat("c", 40)
	localized := &localizedSourceFixture{sourceFixture: source, readme: gh.ReadmeResult{
		SHA: sha, URL: "https://github.com/owner/project/blob/main/README_CN.md",
		Body: []byte("# 开发工作流\n\n安装并运行命令行工具，即可配置本地开发工作流。"),
	}}
	e.GitHub = &unlinkedSourceFixture{localizedSourceFixture: localized}
	source.missing = true
	old := storage.AISummary{RepoID: 1, SummaryZh: "项目中文导读", SummaryStatus: "ready", ReadmeSHA: &sha,
		SourceFingerprint: "readme:" + sha, SourceQuality: "sufficient", SourceLanguage: "zh",
		FeaturesJSON: "[]", AudienceJSON: "[]", ValueTagsJSON: "[]"}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := saveCheck(db, storage.SummaryCheck{RepoID: 1, ReadmeETag: &sha, LastSeenReadmeSHA: &sha,
		SourceFingerprint: old.SourceFingerprint, SourceLanguage: "zh", NextCheckAt: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	step(t, e)
	view, err := cachedReadme(db, 1, false)
	if err != nil || !view.Complete || !strings.Contains(view.ChineseHTML, "开发工作流") ||
		strings.Join(localized.paths, ",") != "README_CN.md" || strings.Join(source.etags, ",") != "" {
		t.Fatalf("现有导读应由后台补齐独立中文版: view=%+v paths=%v etags=%v err=%v", view, localized.paths, source.etags, err)
	}
}

func TestLinkedChineseReadmeAvoidsModelTranslation(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# Workflow toolkit\n\nThis toolkit organizes scripts for local development teams.\n\n" +
		"[外部中文](https://example.test/README.zh-CN.md) [简体中文](docs/README.zh-CN.md)\n"
	localized := &localizedSourceFixture{sourceFixture: source, readme: gh.ReadmeResult{
		SHA: strings.Repeat("c", 40), ETag: "localized-tag", URL: "https://github.com/owner/project/blob/main/docs/README.zh-CN.md",
		Body: []byte("# 项目使用指南\n\n这个开源项目可以整理本地开发任务、配置命令并查看每一步的执行结果。"),
	}}
	e.GitHub = localized
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		if strings.Contains(prompt, combinedTranslationInstruction) || !strings.Contains(prompt, "项目使用指南") {
			t.Fatalf("导读应基于已有中文正文: %s", prompt)
		}
		return ModelResult{Content: modelAnswer(false)}
	})
	if _, err := e.Queue.Manual(t.Context(), 1, "official-readme", false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	check := row[storage.SummaryCheck](t, e.Queue.DB, "repo_id=1")
	if check.ReadmeETag != nil || check.LastSeenReadmeSHA == nil || *check.LastSeenReadmeSHA != localized.readme.SHA {
		t.Fatalf("源检查应使用中文版 SHA: %+v", check)
	}
	view, err := cachedReadme(e.Queue.DB, 1, false)
	if err != nil || !strings.Contains(view.ChineseHTML, "项目使用指南") || view.SourceURL != localized.readme.URL {
		t.Fatalf("详情应直接展示仓库提供的中文版: %+v %v", view, err)
	}
	if strings.Join(localized.paths, ",") != "docs/README.zh-CN.md" {
		t.Fatalf("发布复核不应重复请求 GitHub 的中文 README: %v", localized.paths)
	}
	localized.err = &gh.Failure{Code: "github_invalid_readme", Status: 200}
	checked, err := e.readReadme(t.Context(), storage.Repository{RepoID: 1, Grade: "A"}, false)
	if err != nil || !strings.Contains(string(checked.Body), "Workflow toolkit") {
		t.Fatalf("无效的独立中文版应回退到默认 README: %+v %v", view, err)
	}
}

func TestChineseReadmeRemovesNoiseFromBothSourceAndTranslatedHeadings(t *testing.T) {
	data := []byte("# 工具用法\n\n```sh\nrun-workflow\n```\n\n## 赞助\n\n无关推广\n\n## 目录结构\n\n列出全部文件\n\n## 安装\n\n[安装说明](docs/setup.md)")
	const source = "https://github.com/owner/project/blob/main/README.md"
	clean, err := renderReadme(data, source, nil)
	if err != nil || strings.Contains(clean, "无关推广") || strings.Contains(clean, "列出全部文件") ||
		!strings.Contains(clean, "run-workflow") || !strings.Contains(clean, "docs/setup.md") {
		t.Fatalf("中文整理内容错误: %s %v", clean, err)
	}
	translated := []byte("# 工具用法\n\n## 感谢支持项目的朋友\n\n无关推广\n\n## 安装\n\n安装说明")
	original := []byte("# Usage\n\n## Sponsors\n\nPromotion\n\n## Installation\n\nInstall instructions")
	clean, err = renderReadme(translated, source, original)
	if err != nil || strings.Contains(clean, "无关推广") || !strings.Contains(clean, "安装说明") {
		t.Fatalf("译文须根据原文结构清洗: %s %v", clean, err)
	}
}

func TestReadmeTranslationIncludesNonLatinText(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# Установка\n\nЭтот инструмент помогает организовать задачи для локальной разработки."
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		if !strings.Contains(prompt, "Установка") || !strings.Contains(prompt, "локальной") || !strings.Contains(prompt, "разработки") {
			t.Fatalf("非拉丁文字未发送给翻译任务: %s", prompt)
		}
		return ModelResult{Content: `{"t":{"1":"安装","2":"这个工具用于整理本地开发任务","3":"。"}}`}
	}
	if _, err := e.Queue.ManualTranslation(t.Context(), 1, "non-latin", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	step(t, e)
	view, err := cachedReadme(e.Queue.DB, 1, false)
	if err != nil || !view.Complete || !strings.Contains(view.ChineseHTML, "本地开发任务") {
		t.Fatalf("非拉丁文字的译文应当发布: %+v %v", view, err)
	}
}

func TestTranslationDiscardsSnapshotWhenUpstreamVersionChanges(t *testing.T) {
	e, source, _, _ := fixture(t)
	oldSHA := source.sha
	source.body = "# Workflow toolkit\n\nThis tool organizes local development workflows."
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		var input struct{ Segments map[string]string }
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		for i := range input.Segments {
			input.Segments[i] = "这个工具用于组织本地开发工作流。"
		}
		raw, _ := json.Marshal(map[string]map[string]string{"t": input.Segments})
		source.sha = strings.Repeat("b", 40)
		return ModelResult{Content: string(raw)}
	}
	decision, err := e.Queue.ManualTranslation(t.Context(), 1, "outdated-translation", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step(t, e)
	step(t, e)
	job := row[storage.Job](t, e.Queue.DB, "id = ?", decision.Job.ID)
	view, err := cachedReadme(e.Queue.DB, 1, false)
	if err != nil || view.Complete || job.Status != "canceled" || len(source.etags) != 1 {
		t.Fatalf("发现源文件版本变化后应撤下旧快照且不重复下载: job=%+v view=%+v err=%v", job, view, err)
	}
	var count int64
	if err := e.Queue.DB.Model(&storage.RepoReadmeDocument{}).Where("repo_id = ? AND readme_sha = ?", 1, oldSHA).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("旧快照仍存在: count=%d err=%v", count, err)
	}
}

func TestReadmeTranslationResumesAndPublishesMatchingSource(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Updates(map[string]any{
		"full_name": "owner/project", "name": "project",
	}).Error; err != nil {
		t.Fatal(err)
	}
	source.body = "# Workflow toolkit\n\n" +
		strings.Repeat("Install and configure the command-line runner for repeatable local workflows.\n\n", 12) +
		"Read the [usage guide](docs/usage.md). Use `PRIVATE_CODE_MARKER` here.\n\n" +
		"```sh\nPRIVATE_SHELL_MARKER\n```\n\n<script>alert(1)</script>\n"
	e.Queue.AI.Summary.OutputTokens = 256
	calls := 0
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		calls++
		var input map[string]map[string]string
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(prompt, "PRIVATE_") || strings.Contains(prompt, "docs/usage.md") || strings.Contains(prompt, "<script>") {
			t.Fatal("代码、HTML 或链接地址被发送给模型")
		}
		translated := make(map[string]string, len(input["segments"]))
		for i := range input["segments"] {
			translated[i] = "使用工作流工具"
		}
		out, _ := json.Marshal(map[string]map[string]string{"t": translated})
		return ModelResult{Content: string(out)}
	}
	decision, err := e.Queue.ManualTranslation(t.Context(), 1, "readme", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step(t, e)
	first := row[storage.RepoReadmeTranslation](t, db, "repo_id=1")
	if first.Complete || calls != 1 {
		t.Fatalf("首批应留下续译进度: %+v", first)
	}
	partial, err := cachedReadme(db, 1, false)
	if err != nil || partial.State != "pending" || partial.Complete || partial.ChineseHTML != "" {
		t.Fatalf("部分译文响应错误: %+v %v", partial, err)
	}
	for range 8 {
		job := row[storage.Job](t, db, "id=?", decision.Job.ID)
		if job.Status == "succeeded" {
			break
		}
		step(t, e)
	}
	view, err := cachedReadme(db, 1, false)
	if err != nil || !view.Complete || calls < 2 || len(source.etags) != 1 || !strings.Contains(view.ChineseHTML, "使用工作流工具") ||
		!strings.Contains(view.ChineseHTML, "PRIVATE_SHELL_MARKER") ||
		!strings.Contains(view.ChineseHTML, "https://github.com/owner/project/blob/main/docs/usage.md") ||
		strings.Contains(view.ChineseHTML, "<script>") {
		t.Fatalf("完整译文未保留 Markdown 结构: %+v %v", view, err)
	}
	saved := row[storage.RepoReadmeTranslation](t, db, "repo_id=1")
	if strings.Contains(saved.PartsJSON, "PRIVATE_") || strings.Contains(saved.PartsJSON, "Workflow toolkit") {
		t.Fatal("译文缓存包含 README 原文")
	}
	before := source.body
	source.body += "\nMore text after the former read limit."
	refreshReadmeFixture(t, e, "readme-resized")
	if resized, err := cachedReadme(db, 1, false); err != nil || resized.ChineseHTML != "" {
		t.Fatalf("同一 SHA 读取范围扩大时不能沿用旧片段下标: %+v %v", resized, err)
	}
	source.body = before
	source.sha = strings.Repeat("b", 40)
	refreshReadmeFixture(t, e, "readme-new-version")
	view, err = cachedReadme(db, 1, false)
	if err != nil || view.ChineseHTML != "" || view.State != "pending" {
		t.Fatalf("新版本不应拼接旧译文: %+v %v", view, err)
	}
}

func TestCachedTranslationRunsWhileGitHubIsPacedAndPersistsBeforeVerification(t *testing.T) {
	e, source, _, _ := fixture(t)
	q, db, now := &e.Queue, e.Queue.DB, time.Now()
	if err := db.Model(&storage.Repository{}).Where("repo_id = ?", 1).Update("full_name", "owner/project").Error; err != nil {
		t.Fatal(err)
	}
	// 导读先写入原文，模拟已有导读但还没排入翻译的旧数据。
	source.body = "# Setup\n\nThis tool runs scripts."
	refreshReadmeFixture(t, e, "seed-source")
	reads, identityChecks, versionChecks := len(source.etags), source.identityChecks, source.versionChecks
	q.AI.Summary.AutoDailyCalls = 1
	if _, err := q.insert(db, JobType, 2, "upstream-backlog", payload{}, 100, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	q.Pacing = storage.JobPacing{{Key: "readme", Types: []string{JobType, TranslationJob},
		Resource: gh.ReadmeCheckResource, Window: time.Hour, Budget: 120}}
	rate := storage.RateLimit{Resource: gh.ReadmeCheckResource, LocalWindow: now.UnixMilli(), LocalRequests: 120}
	if err := db.Create(&rate).Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	job := row[storage.Job](t, db, "repo_id = 1 AND type = ?", TranslationJob)
	calls := 0
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		calls++
		var input struct{ Segments map[string]string }
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		for i := range input.Segments {
			input.Segments[i] = "运行本地任务"
		}
		raw, _ := json.Marshal(map[string]map[string]string{"t": input.Segments})
		return ModelResult{Content: string(raw)}
	}
	step(t, e)
	saved := row[storage.RepoReadmeTranslation](t, db, "repo_id = 1")
	call := row[storage.AICall](t, db, "job_id = ?", job.ID)
	if calls != 1 || saved.Complete || call.Status != consts.StatusSucceeded ||
		len(source.etags) != reads || source.identityChecks != identityChecks || source.versionChecks != versionChecks {
		t.Fatalf("GitHub 额度用尽时应从原文生成并保存译文: calls=%d saved=%+v call=%+v", calls, saved, call)
	}
	if done, err := e.Step(t.Context()); err != nil || done {
		t.Fatalf("发布复核须等待 GitHub: done=%v err=%v", done, err)
	}
	if err := db.Model(&rate).Where("resource = ?", rate.Resource).Update("local_requests", 0).Error; err != nil {
		t.Fatal(err)
	}
	e.Provider, e.Translate = nil, nil
	source.shaFailures = 1
	step(t, e)
	failed := row[storage.Job](t, db, "id = ?", job.ID)
	if failed.Status != consts.StatusFailed || failed.LastErrorCode == nil || *failed.LastErrorCode != consts.CodeGitHubNetwork {
		t.Fatalf("发布复核应保留 GitHub 错误码: %+v", failed)
	}
	if err := db.Model(&failed).Update("available_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	step(t, e)
	view, err := cachedReadme(db, 1, false)
	if err != nil || !view.Complete || calls != 1 || len(source.etags) != reads ||
		source.identityChecks != identityChecks+2 || source.versionChecks != versionChecks+2 {
		t.Fatalf("发布重试应复用落盘译文，只复核身份与版本: view=%+v calls=%d err=%v", view, calls, err)
	}
}

func TestManualCachedTranslationChecksCurrentVisibility(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id = ?", 1).Update("full_name", "owner/project").Error; err != nil {
		t.Fatal(err)
	}
	refreshReadmeFixture(t, e, "seed-for-manual")
	source.private = true
	e.Translate = func(context.Context, string) ModelResult {
		t.Fatal("已转为私有的仓库不能发起手动译文调用")
		return ModelResult{}
	}
	decision, err := e.Queue.ManualTranslation(t.Context(), 1, "private-cache", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step(t, e)
	job := row[storage.Job](t, db, "id = ?", decision.Job.ID)
	repo := row[storage.Repository](t, db, "repo_id = ?", 1)
	if job.Status != consts.StatusDead || repo.State != consts.StatusUnavailable {
		t.Fatalf("手动续译应识别转私并撤回公开状态: job=%+v repo=%+v", job, repo)
	}
}

func TestTranslationVerificationDoesNotTranslateReplacementSnapshot(t *testing.T) {
	e, source, _, _ := fixture(t)
	q, db := &e.Queue, e.Queue.DB
	source.body = "# Setup\n\nThis tool runs scripts."
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		var input struct{ Segments map[string]string }
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		for i := range input.Segments {
			input.Segments[i] = "运行本地脚本"
		}
		raw, _ := json.Marshal(map[string]map[string]string{"t": input.Segments})
		return ModelResult{Content: string(raw)}
	}
	decision, err := q.ManualTranslation(t.Context(), 1, "translated-a", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step(t, e)
	job := row[storage.Job](t, db, "id = ?", decision.Job.ID)
	if job.Status != consts.StatusPending || !strings.Contains(job.PayloadJSON, source.sha) {
		t.Fatalf("复核应绑定已完成翻译的来源: %+v", job)
	}
	replacement := strings.Repeat("b", 40)
	if err := db.Model(&storage.RepoReadmeDocument{}).Where("repo_id = ?", 1).Update("readme_sha", replacement).Error; err != nil {
		t.Fatal(err)
	}
	reads, identityChecks, versionChecks := len(source.etags), source.identityChecks, source.versionChecks
	e.Translate = func(context.Context, string) ModelResult {
		t.Fatal("旧版本的发布复核不能翻译新版本")
		return ModelResult{}
	}
	step(t, e)
	job = row[storage.Job](t, db, "id = ?", decision.Job.ID)
	doc := row[storage.RepoReadmeDocument](t, db, "repo_id = ?", 1)
	if job.Status != consts.StatusCanceled || doc.ReadmeSHA != replacement || doc.Complete ||
		len(source.etags) != reads || source.identityChecks != identityChecks || source.versionChecks != versionChecks {
		t.Fatalf("旧复核应取消并保留新快照: job=%+v doc=%+v", job, doc)
	}
}

func TestChineseReadmeUsesSingleBodyAndRespectsVisibility(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# 项目文档\n\n这个开源项目可以整理本地开发任务、配置命令并查看每一步的执行结果。\n\n## 广告\n\n推广标识"
	refreshReadmeFixture(t, e, "readme-first")
	view, err := cachedReadme(e.Queue.DB, 1, false)
	if err != nil || !strings.Contains(view.ChineseHTML, "项目文档") || strings.Contains(view.ChineseHTML, "推广标识") || !view.Complete {
		t.Fatalf("中文 README 应直接显示正文: %+v %v", view, err)
	}
	// 正文下载后仓库转私有时，返回空响应并停止发布。
	source.onRead = func() { source.private = true }
	refreshReadmeFixture(t, e, "readme-withdrawn")
	if view, err := cachedReadme(e.Queue.DB, 1, false); err != nil || view.ChineseHTML != "" {
		t.Fatalf("仓库转私有后仍返回了正文: %+v %v", view, err)
	}
	if stored := row[storage.Repository](t, e.Queue.DB, "repo_id=1"); stored.State != "unavailable" {
		t.Fatalf("已确认仓库转私有时应同步撤回公开详情: %+v", stored)
	}
}

func refreshReadmeFixture(t *testing.T, e *Engine, id string) {
	t.Helper()
	if e.Provider == nil {
		e.Provider = modelFixture(func(context.Context, string) ModelResult { return ModelResult{Content: modelAnswer(false)} })
	}
	if _, err := e.Queue.Manual(t.Context(), 1, id, true, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
}

func TestLongReadmeContinuesCombinedCallAfterRestart(t *testing.T) {
	e, source, _, dbPath := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Updates(map[string]any{
		"full_name": "owner/project", "name": "project",
	}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 48; i++ {
		source.body += fmt.Sprintf("\n\nStep %d: Configure the command-line runner for repeatable local workflows.", i)
	}
	var seen []string
	answerParts := func(segments []string) []string {
		seen = append(seen, segments...)
		out := make([]string, len(segments))
		for i := range out {
			out[i] = "工作流工具"
		}
		return out
	}
	guideCalls := 0
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		guideCalls++
		_, tail, _ := strings.Cut(prompt, combinedTranslationInstruction)
		line, _, _ := strings.Cut(tail, "\n")
		var segments []string
		if err := json.Unmarshal([]byte(line), &segments); err != nil || len(segments) == 0 {
			t.Fatalf("长 README 的导读请求应携带首批片段: %v", err)
		}
		encoded, _ := json.Marshal(answerParts(segments))
		return ModelResult{Content: strings.TrimSuffix(modelAnswer(false), "}") + `,"t":` + string(encoded) + `}`}
	})
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		var input struct{ Segments map[string]string }
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		segments := make([]string, len(input.Segments))
		for i := range segments {
			segments[i] = input.Segments[fmt.Sprint(i+1)]
		}
		items := make(map[string]string, len(segments))
		for i, value := range answerParts(segments) {
			items[fmt.Sprint(i+1)] = value
		}
		data, _ := json.Marshal(map[string]map[string]string{"t": items})
		return ModelResult{Content: string(data)}
	}
	if _, err := e.Queue.Manual(t.Context(), 1, "summary-for-translation", false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	first := row[storage.RepoReadmeTranslation](t, db, "repo_id=1")
	guide := row[storage.AISummary](t, db, "repo_id=1")
	if first.Complete || guide.SummaryStatus != "ready" || guideCalls != 1 || len(source.etags) != 1 {
		t.Fatalf("首批应发布导读、保留部分译文并且只下载一次: guide=%+v first=%+v", guide, first)
	}
	oldPool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := oldPool.Close(); err != nil {
		t.Fatal(err)
	}
	pool, err := storage.OpenBootstrap(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	db, err = storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	// 新实例从同一磁盘数据库恢复，后续批次不会再次下载正文。
	q := e.Queue
	q.DB = db
	e = &Engine{Queue: q, GitHub: source, Provider: e.Provider, Translate: e.Translate, Logger: e.Logger}
	original := source.body
	e.Queue.AI.Summary.AutoDailyCalls = 1
	if err := db.Model(&storage.Repository{}).Where("repo_id IN (2,3)").Update("topics_json", "[]").Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	schedule(t, e)
	job := row[storage.Job](t, db, "repo_id=1 AND type=?", TranslationJob)
	parts := readmeParts(readmeBody([]byte(original)), unicode.IsLetter)
	// 此用例的短片段可装满一批，另留一次领取给完成后的来源复核。
	batches := (len(parts) - len(seen) + translationBatchItems - 1) / translationBatchItems
	e.Queue.AI.Summary.AutoDailyCalls = batches
	for range batches + 1 {
		step(t, e)
		if row[storage.Job](t, db, "id=?", job.ID).Status == "succeeded" {
			break
		}
	}
	result := row[storage.RepoReadmeTranslation](t, db, "repo_id=1")
	if !result.Complete || result.ReadmeSHA != first.ReadmeSHA || guideCalls != 1 || len(source.etags) != 1 {
		t.Fatalf("自动译文未生成: %+v", result)
	}
	if len(parts) != len(seen) {
		t.Fatalf("分批续译的片段数量不符: got=%d want=%d", len(seen), len(parts))
	}
	for i, part := range parts {
		if seen[i] != part.text {
			t.Fatalf("第 %d 个片段未从原始进度恢复", i)
		}
	}
	if view, err := cachedReadme(db, 1, false); err != nil || !view.Complete || !strings.Contains(view.ChineseHTML, "工作流工具") {
		t.Fatalf("续译后应发布中文正文: %+v %v", view, err)
	}
	e.Queue.AI.Summary.AutoDailyCalls = 1
	schedule(t, e)
	var jobs int64
	if err := db.Model(&storage.Job{}).Where("repo_id=1 AND type=?", TranslationJob).Count(&jobs).Error; err != nil || jobs != 1 {
		t.Fatalf("同版本译文被重复调度: jobs=%d err=%v", jobs, err)
	}
	if err := db.Model(&storage.Job{}).Where("repo_id IN (2,3)").Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("优先准备译文后不应为长尾仓库超额入队: jobs=%d err=%v", jobs, err)
	}
}

func TestTranslationRejectsSourceEchoAndKeepsNamesAsPlaceholders(t *testing.T) {
	english := "The project organizes command line tasks for developers."
	if _, ok := translationBatch(`{"t":["The project organizes command line tasks for developers."]}`, []string{english}); ok {
		t.Fatal("英文段落回显不能视为中文译文")
	}
	if _, ok := translationBatch(`{"t":["译 The project organizes command line tasks for developers."]}`, []string{english}); ok {
		t.Fatal("包裹原文的输出不能视为中文译文")
	}
	parts, ok := translationBatch(`{"t":["项目用于整理开发命令。","GitHub"]}`, []string{english, "GitHub"})
	if !ok || parts[0] != "项目用于整理开发命令。" || parts[1] != "" {
		t.Fatalf("原样保留的名称应记录占位: %+v", parts)
	}
}

// 编号错位时缩小同一批次，下一次只请求原批次的前半段，并按编号复原顺序。
func TestTranslationRepairsMisalignedBatch(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# Workflow toolkit\n\n" +
		strings.Repeat("Install the command-line runner for repeatable local workflows.\n\n", 16)
	calls := 0
	var first map[string]string
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		calls++
		var input struct{ Segments map[string]string }
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		if calls == 1 {
			if len(input.Segments) != 12 {
				t.Fatalf("首批应包含 12 个片段，实际为 %d", len(input.Segments))
			}
			first = input.Segments
			return ModelResult{Content: `{"t":{"1":"工作流工具"}}`}
		}
		if len(input.Segments) != len(first)/2 || input.Segments["1"] != first["1"] {
			t.Fatalf("重试没有从同一位置缩小批次: %v", input.Segments)
		}
		output := make(map[string]string, len(input.Segments))
		for key := range input.Segments {
			output[key] = "译文" + key
		}
		raw, _ := json.Marshal(map[string]map[string]string{"t": output})
		return ModelResult{Content: string(raw)}
	}
	d, err := e.Queue.ManualTranslation(t.Context(), 1, "misaligned", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step(t, e)
	job := row[storage.Job](t, e.Queue.DB, "id = ?", d.Job.ID)
	var p payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &p); err != nil || job.Status != consts.StatusFailed || p.ReadmeBatchItems != len(first)/2 {
		t.Fatalf("缩小批次未持久化: %+v %+v %v", job, p, err)
	}
	if err := e.Queue.DB.Model(&job).Update("available_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	step(t, e)
	partial := row[storage.RepoReadmeTranslation](t, e.Queue.DB, "repo_id = ?", 1)
	var parts []string
	if err := json.Unmarshal([]byte(partial.PartsJSON), &parts); err != nil || len(parts) != len(first)/2 || parts[0] != "译文1" || parts[1] != "译文2" {
		t.Fatalf("按编号恢复的译文不正确: %q %v", parts, err)
	}
}

// 旧协议失败的自动任务恢复一次，保留原 ID；新协议失败后不会循环重排。
func TestOldInvalidTranslationIsRescheduledOnce(t *testing.T) {
	e, _, _, _ := fixture(t)
	q := e.Queue
	now := time.Now()
	sha := strings.Repeat("a", 40)
	guide := storage.AISummary{RepoID: 1, ReadmeSHA: &sha, SummaryStatus: consts.StatusReady, SummaryZh: "开发工具",
		SourceLanguage: "en", SourceQuality: "sufficient", FeaturesJSON: "[]", AudienceJSON: "[]", ValueTagsJSON: "[]"}
	if err := q.DB.Create(&guide).Error; err != nil {
		t.Fatal(err)
	}
	if err := saveCheck(q.DB, storage.SummaryCheck{RepoID: 1, LastSeenReadmeSHA: &sha, SourceLanguage: "en"}); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf(consts.ReadmeAutoDedupFormat, 1, sha, q.translationKey())
	job, err := q.insert(q.DB, TranslationJob, 1, key, payload{}, 30, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	markDead := func() {
		t.Helper()
		if err := q.DB.Model(&job).Updates(map[string]any{"status": consts.StatusDead, "last_error_code": consts.CodeInvalidOutput}).Error; err != nil {
			t.Fatal(err)
		}
	}
	markDead()
	if n, err := q.scheduleTranslations(q.DB, now.UnixMilli(), 1, false); err != nil || n != 1 {
		t.Fatalf("旧任务未恢复: %d %v", n, err)
	}
	got := row[storage.Job](t, q.DB, "id = ?", job.ID)
	if got.Status != consts.StatusPending {
		t.Fatalf("原任务未回到待执行: %+v", got)
	}
	markDead()
	if n, err := q.scheduleTranslations(q.DB, now.UnixMilli(), 1, false); err != nil || n != 0 {
		t.Fatalf("新协议死任务不应再次恢复: %d %v", n, err)
	}
}

func TestShortEnglishReadmeRejectsEntirelyUntranslatedResult(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# Installation\n\nInstall the package.\n\nStart the server.\n\nOpen your browser."
	e.Translate = func(context.Context, string) ModelResult {
		return ModelResult{Content: `{"t":{"1":"Installation","2":"Install the package.","3":"Start the server.","4":"Open your browser."}}`}
	}
	decision, err := e.Queue.ManualTranslation(t.Context(), 1, "echo-short-readme", true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	step(t, e)
	job := row[storage.Job](t, e.Queue.DB, "id=?", decision.Job.ID)
	if job.LastErrorCode == nil || *job.LastErrorCode != "invalid_output" {
		t.Fatalf("整篇英文原样返回应当重试: %+v", job)
	}
	view, err := cachedReadme(e.Queue.DB, 1, false)
	if err != nil || view.SourceURL == "" || view.ChineseHTML != "" || view.State != "pending" {
		t.Fatalf("译文未通过校验时只返回来源和等待状态: %+v %v", view, err)
	}
}
