package summary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

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
	if calls != 1 || saved.TitleZh == nil || *saved.TitleZh != "本地脚本编排工具" || saved.SummaryStatus != "ready" {
		t.Fatalf("单次请求应保存小标题和导读: calls=%d %+v", calls, saved)
	}
	view, err := CachedReadme(db, 1)
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
	view, err := CachedReadme(db, 1)
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
	view, err := CachedReadme(e.Queue.DB, 1)
	if err != nil || !strings.Contains(view.ChineseHTML, "项目使用指南") || view.SourceURL != localized.readme.URL {
		t.Fatalf("详情应直接展示仓库提供的中文版: %+v %v", view, err)
	}
	if strings.Join(localized.paths, ",") != "docs/README.zh-CN.md,docs/README.zh-CN.md" {
		t.Fatalf("只有后台读取和复核才应请求 GitHub 的中文 README: %v", localized.paths)
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
		return ModelResult{Content: `{"t":["安装","这个工具用于整理本地开发任务","。"]}`}
	}
	if _, err := e.Queue.ManualTranslation(t.Context(), 1, "non-latin", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	view, err := CachedReadme(e.Queue.DB, 1)
	if err != nil || !view.Complete || !strings.Contains(view.ChineseHTML, "本地开发任务") {
		t.Fatalf("非拉丁文字的译文应当发布: %+v %v", view, err)
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
		var input map[string][]string
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(prompt, "PRIVATE_") || strings.Contains(prompt, "docs/usage.md") || strings.Contains(prompt, "<script>") {
			t.Fatal("代码、HTML 或链接地址被发送给模型")
		}
		translated := make([]string, len(input["segments"]))
		for i := range translated {
			translated[i] = "使用工作流工具"
		}
		out, _ := json.Marshal(map[string][]string{"t": translated})
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
	partial, err := CachedReadme(db, 1)
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
	view, err := CachedReadme(db, 1)
	if err != nil || !view.Complete || calls < 2 || !strings.Contains(view.ChineseHTML, "使用工作流工具") ||
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
	if resized, err := CachedReadme(db, 1); err != nil || resized.ChineseHTML != "" {
		t.Fatalf("同一 SHA 读取范围扩大时不能沿用旧片段下标: %+v %v", resized, err)
	}
	source.body = before
	source.sha = strings.Repeat("b", 40)
	refreshReadmeFixture(t, e, "readme-new-version")
	view, err = CachedReadme(db, 1)
	if err != nil || view.ChineseHTML != "" || view.State != "pending" {
		t.Fatalf("新版本不应拼接旧译文: %+v %v", view, err)
	}
}

func TestChineseReadmeUsesSingleBodyAndRespectsVisibility(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# 项目文档\n\n这个开源项目可以整理本地开发任务、配置命令并查看每一步的执行结果。\n\n## 广告\n\n推广标识"
	refreshReadmeFixture(t, e, "readme-first")
	view, err := CachedReadme(e.Queue.DB, 1)
	if err != nil || !strings.Contains(view.ChineseHTML, "项目文档") || strings.Contains(view.ChineseHTML, "推广标识") || !view.Complete {
		t.Fatalf("中文 README 应直接显示正文: %+v %v", view, err)
	}
	// 正文下载后仓库转私有时，返回空响应并停止发布。
	source.onRead = func() { source.private = true }
	refreshReadmeFixture(t, e, "readme-withdrawn")
	if view, err := CachedReadme(e.Queue.DB, 1); err != nil || view.ChineseHTML != "" {
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

func TestReadmeTranslationSchedulesOnceBeforeNewLowPriorityTopics(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Updates(map[string]any{
		"full_name": "owner/project", "name": "project",
	}).Error; err != nil {
		t.Fatal(err)
	}
	source.body += strings.Repeat("Install and configure the command-line runner for repeatable local workflows.\n\n", 12)
	e.Provider = modelFixture(func(_ context.Context, prompt string) ModelResult {
		if strings.Contains(prompt, combinedTranslationInstruction) {
			t.Fatal("长 README 应使用分批翻译任务")
		}
		return ModelResult{Content: modelAnswer(false)}
	})
	e.Translate = func(_ context.Context, prompt string) ModelResult {
		var input struct{ Segments []string }
		if err := json.Unmarshal([]byte(prompt), &input); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(input.Segments))
		for i := range out {
			out[i] = "工作流工具"
		}
		data, _ := json.Marshal(map[string][]string{"t": out})
		return ModelResult{Content: string(data)}
	}
	if _, err := e.Queue.Manual(t.Context(), 1, "summary-for-translation", false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	e.Queue.AI.Summary.AutoDailyCalls = 1
	if err := db.Model(&storage.Repository{}).Where("repo_id IN (2,3)").Update("topics_json", "[]").Error; err != nil {
		t.Fatal(err)
	}
	schedule(t, e)
	schedule(t, e)
	job := row[storage.Job](t, db, "repo_id=1 AND type=?", TranslationJob)
	for range 6 {
		step(t, e)
		if row[storage.Job](t, db, "id=?", job.ID).Status == "succeeded" {
			break
		}
	}
	result := row[storage.RepoReadmeTranslation](t, db, "repo_id=1")
	if !result.Complete || result.ReadmeSHA != source.sha {
		t.Fatalf("自动译文未生成: %+v", result)
	}
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

func TestShortEnglishReadmeRejectsEntirelyUntranslatedResult(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# Installation\n\nInstall the package.\n\nStart the server.\n\nOpen your browser."
	e.Translate = func(context.Context, string) ModelResult {
		return ModelResult{Content: `{"t":["Installation","Install the package.","Start the server.","Open your browser."]}`}
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
	view, err := CachedReadme(e.Queue.DB, 1)
	if err != nil || view.SourceURL == "" || view.ChineseHTML != "" || view.State != "pending" {
		t.Fatalf("译文未通过校验时只返回来源和等待状态: %+v %v", view, err)
	}
}
