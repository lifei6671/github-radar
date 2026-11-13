package summary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

// TestReadmeCleanupPublishesOnlySelectedBlocks 验证旧缓存补办清理后，导航、外链和原文版本都正确。
func TestReadmeCleanupPublishesOnlySelectedBlocks(t *testing.T) {
	e, upstream, _, _ := fixture(t)
	upstream.body = "# 使用说明\n\n使用该工具启动开发任务。[安装步骤](#安装) [首页锚点](https://github.com/owner/project#安装) [已移除标题](#链接集合) [文档](https://example.com/docs)。\n\n## 安装\n\n运行安装命令，然后开始使用。\n\n## 链接集合\n\n[项目无关推广](https://example.com/ads)"
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id = ?", 1).
		Updates(map[string]any{"full_name": "owner/project", "name": "project", "starred_by_user": true}).Error; err != nil {
		t.Fatal(err)
	}
	refreshReadmeFixture(t, e, "clean-readme-source")
	before, err := CachedReadme(db, 1)
	if err != nil || before.Complete || before.ChineseHTML != "" || before.State != "pending" {
		t.Fatalf("旧草稿不可公开: %+v %v", before, err)
	}
	calls := 0
	e.CleanReadme = func(_ context.Context, prompt string) ModelResult {
		calls++
		var blocks []struct {
			ID   int
			Text string
		}
		if err := json.Unmarshal([]byte(prompt), &blocks); err != nil || len(blocks) != 6 {
			t.Fatalf("模型未收到正确的草稿块: %+v %v", blocks, err)
		}
		return ModelResult{Content: `{"keep":[4,2,1,3]}`}
	}
	if err := e.Queue.scheduleReadmeCleanup(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	if waiting, err := CachedReadme(db, 1); err != nil || waiting.Complete || waiting.ChineseHTML != "" {
		t.Fatalf("模型结束但尚未复核 GitHub 的正文不能公开: %+v %v", waiting, err)
	}
	step(t, e)
	view, err := CachedReadme(db, 1)
	if err != nil || !view.Complete || !strings.Contains(view.ChineseHTML, "运行安装命令") ||
		strings.Contains(view.ChineseHTML, "项目无关推广") || strings.Contains(view.ChineseHTML, "链接集合") ||
		!strings.Contains(view.ChineseHTML, `id="readme-body-安装"`) || strings.Count(view.ChineseHTML, `href="#readme-body-%E5%AE%89%E8%A3%85"`) != 2 ||
		!strings.Contains(view.ChineseHTML, `href="https://example.com/docs" target="_blank" rel="noopener noreferrer"`) ||
		!strings.Contains(view.ChineseHTML, ">已移除标题</a>") || strings.Contains(view.ChineseHTML, `href="https://example.com/ads"`) {
		t.Fatalf("清理及锚点规则错误: %+v %v", view, err)
	}
	var callsBilled int64
	if err := db.Model(&storage.AIBudgetReservation{}).Where("operation = ? AND status = ?", "readme_cleanup", "succeeded").
		Count(&callsBilled).Error; err != nil || callsBilled != 1 || calls != 1 {
		t.Fatalf("清理应计入统一账本一次: calls=%d billed=%d err=%v", calls, callsBilled, err)
	}
	// 相同文件复查保留已清理页面，版本变更立即撤下旧内容。
	doc := row[storage.RepoReadmeDocument](t, db, "repo_id=1")
	source, _ := savedReadme(doc)
	for _, sha := range []string{doc.ReadmeSHA, "new-readme-sha"} {
		source.SHA = sha
		if err := db.Transaction(func(tx *gorm.DB) error {
			return saveReadme(tx, doc.RepoID, source, doc.SourceRepoName, doc.SourceJobID+1)
		}); err != nil {
			t.Fatal(err)
		}
		current, err := CachedReadme(db, 1)
		if err != nil || current.Complete != (sha == doc.ReadmeSHA) {
			t.Fatalf("版本 %s 的公开状态错误: %+v %v", sha, current, err)
		}
	}
}

// TestReadmeCleanupRefetchesLegacySource 验证迁移前无原文快照的缓存会补抓，且换版时废弃待复核结果。
func TestReadmeCleanupRefetchesLegacySource(t *testing.T) {
	e, upstream, _, _ := fixture(t)
	db := e.Queue.DB
	if err := db.Model(&storage.Repository{}).Where("repo_id = ?", 1).
		Updates(map[string]any{"full_name": "owner/project", "name": "project", "starred_by_user": true}).Error; err != nil {
		t.Fatal(err)
	}
	upstream.body = "# 安装方式\n\n使用工具组织本地开发任务，运行安装程序后按指南配置。"
	legacy := storage.RepoReadmeDocument{RepoID: 1, ReadmeSHA: upstream.sha, ReadmeBytes: len(upstream.body),
		Language: "zh", ChineseHTML: "<p>旧草稿</p>", Complete: true}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	e.CleanReadme = func(context.Context, string) ModelResult { return ModelResult{Content: `{"keep":[1,2]}`} }
	if err := e.Queue.scheduleReadmeCleanup(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	if len(upstream.etags) != 1 {
		t.Fatalf("旧版原文应补抓一次: %v", upstream.etags)
	}
	upstream.sha = "replaced-remotely"
	step(t, e)
	if view, err := CachedReadme(db, 1); err != nil || view.Complete || view.ChineseHTML != "" {
		t.Fatalf("清理期间换版，旧稿不能发布: %+v %v", view, err)
	}
}

// TestReadmeCleanupRejectsInvalidSelection 确认模型只能选择已有编号。
func TestReadmeCleanupRejectsInvalidSelection(t *testing.T) {
	for _, raw := range []string{`{}`, `{"keep":[1]}`, `{"keep":null}`} {
		if _, valid := renderCleanReadme(nil, raw, "", ""); valid {
			t.Fatalf("接受了非法清理结果 %s", raw)
		}
	}
	if output, valid := renderCleanReadme(nil, `{"keep":[]}`, "", ""); !valid || output != "" {
		t.Fatalf("没有合适正文时允许空结果: %q %v", output, valid)
	}
}
