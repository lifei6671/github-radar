package summary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/storage"
)

func TestReadmeTranslationResumesAndPublishesMatchingSource(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Updates(map[string]any{
		"full_name": "owner/project", "name": "project",
	}).Error; err != nil {
		t.Fatal(err)
	}
	repo := row[storage.Repository](t, db, "repo_id=1")
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
	partial, err := e.ReadmeView(t.Context(), repo)
	if err != nil || partial.Complete || !strings.Contains(partial.ChineseHTML, "使用工作流工具") {
		t.Fatalf("部分译文响应错误: %+v %v", partial, err)
	}
	for range 8 {
		job := row[storage.Job](t, db, "id=?", decision.Job.ID)
		if job.Status == "succeeded" {
			break
		}
		step(t, e)
	}
	view, err := e.ReadmeView(t.Context(), repo)
	if err != nil || !view.Complete || calls < 2 || !strings.Contains(view.OriginalHTML, "Workflow toolkit") ||
		!strings.Contains(view.ChineseHTML, "PRIVATE_SHELL_MARKER") ||
		!strings.Contains(view.ChineseHTML, "https://github.com/owner/project/blob/main/docs/usage.md") ||
		strings.Contains(view.OriginalHTML+view.ChineseHTML, "<script>") {
		t.Fatalf("完整译文未保留 Markdown 结构: %+v %v", view, err)
	}
	saved := row[storage.RepoReadmeTranslation](t, db, "repo_id=1")
	if strings.Contains(saved.PartsJSON, "PRIVATE_") || strings.Contains(saved.PartsJSON, "Workflow toolkit") {
		t.Fatal("译文缓存包含 README 原文")
	}
	before := source.body
	source.body += "\nMore text after the former read limit."
	if resized, err := e.ReadmeView(t.Context(), repo); err != nil || resized.ChineseHTML != "" {
		t.Fatalf("同一 SHA 读取范围扩大时不能沿用旧片段下标: %+v %v", resized, err)
	}
	source.body = before
	source.sha = strings.Repeat("b", 40)
	view, err = e.ReadmeView(t.Context(), repo)
	if err != nil || view.ChineseHTML != "" || view.OriginalHTML == "" {
		t.Fatalf("新版本不应拼接旧译文: %+v %v", view, err)
	}
}

func TestChineseReadmeUsesSingleBodyAndRespectsVisibility(t *testing.T) {
	e, source, _, _ := fixture(t)
	source.body = "# 项目文档\n\n这个开源项目可以整理本地开发任务、配置命令并查看每一步的执行结果。"
	repo := storage.Repository{RepoID: 1, FullName: "owner/project", Grade: "A"}
	view, err := e.ReadmeView(t.Context(), repo)
	if err != nil || view.OriginalHTML != "" || !strings.Contains(view.ChineseHTML, "项目文档") || !view.Complete {
		t.Fatalf("中文 README 应直接显示正文: %+v %v", view, err)
	}
	// 正文下载后仓库转私有时，返回空响应并停止发布。
	source.onRead = func() { source.private = true }
	if view, err := e.ReadmeView(t.Context(), repo); err == nil || view.ChineseHTML != "" {
		t.Fatalf("仓库转私有后仍返回了正文: %+v %v", view, err)
	}
	if stored := row[storage.Repository](t, e.Queue.DB, "repo_id=1"); stored.State != "unavailable" {
		t.Fatalf("已确认仓库转私有时应同步撤回公开详情: %+v", stored)
	}
}

func TestReadmeTranslationSchedulesOnceBeforeNewLowPriorityTopics(t *testing.T) {
	e, source, _, _ := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Updates(map[string]any{
		"full_name": "owner/project", "name": "project",
	}).Error; err != nil {
		t.Fatal(err)
	}
	e.Provider = modelFixture(func(context.Context, string) ModelResult { return ModelResult{Content: modelAnswer(false)} })
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
	view, err := e.ReadmeView(t.Context(), storage.Repository{RepoID: 1, FullName: "owner/1", Grade: "A"})
	if err != nil || !strings.Contains(view.OriginalHTML, "Install the package") || view.ChineseHTML != "" {
		t.Fatalf("同一 ID 的公开仓库改名后仍可读取原文，原文不能标成译文: %+v %v", view, err)
	}
}
