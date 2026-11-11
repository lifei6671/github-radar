package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/summary"
	"github.com/lifei6671/github-radar/internal/topics"
)

func TestSummaryAndTopicContracts(t *testing.T) {
	without, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.FixedZone("test", 8*3600))
	desc := "A tool for workflow automation"
	for id := int64(1); id <= 2; id++ {
		name := fmt.Sprintf("repo-%d", id)
		row := storage.Repository{RepoID: id, Owner: "owner", Name: name, FullName: "owner/" + name, NameIsCurrent: true, TopicsJSON: "[]",
			State: "active", Grade: "A", StarredByUser: true, Description: &desc, HTMLURL: "https://github.com/owner/" + name, DiscoveredAt: now.UnixMilli()}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/repos/1/summary"
	request(t, without, "POST", path, `{"requestId":"disabled"}`, "admin", 503)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := (settings.Store{DB: db}).Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Settings.AI.BaseURL, snapshot.Settings.AI.Model = "http://127.0.0.1:9876/v1", "test-model"
	cfg = cfg.WithRuntime(snapshot.Settings)
	t.Setenv(cfg.AI.APIKeyEnv, "test-key")
	t.Setenv(cfg.GitHub.TokenEnv, "test-token")
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(db, pool, "admin", slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	request(t, router, "POST", path, `{"requestId":"no-auth"}`, "", 401)
	one := request(t, router, "POST", path, `{"requestId":"one","force":true}`, "admin", 202)
	two := request(t, router, "POST", path, `{"requestId":"one","force":true}`, "admin", 202)
	if one["jobId"] != two["jobId"] {
		t.Fatal("重复请求创建了多份摘要任务")
	}
	request(t, router, "POST", path, `{"requestId":"other"}`, "admin", 409)
	id := int64(one["jobId"].(float64))
	if err := db.Model(&storage.Job{}).Where("id=?", id).Update("status", "unknown").Error; err != nil {
		t.Fatal(err)
	}
	bill := storage.AICall{CallKey: "test-unknown", JobID: id, Provider: "fixture", Origin: "manual", Status: "unknown", ReservedAt: now.UnixMilli()}
	if err := db.Create(&bill).Error; err != nil {
		t.Fatal(err)
	}
	retryPath := fmt.Sprintf("/api/v1/jobs/%d/retry", id)
	request(t, router, "POST", retryPath, `{"requestId":"confirmed"}`, "admin", 400)
	retry := request(t, router, "POST", retryPath, `{"requestId":"confirmed","acceptPossibleCharge":true}`, "admin", 202)
	if retry["jobId"] == one["jobId"] {
		t.Fatal("未知调用的人工重试必须产生新任务")
	}
	if err := db.Model(&storage.Job{}).Where("id=?", int64(retry["jobId"].(float64))).Update("status", "succeeded").Error; err != nil {
		t.Fatal(err)
	}
	sourceURL := "https://github.com/owner/repo-1/blob/main/README.md"
	row := storage.AISummary{RepoID: 1, SourceFingerprint: "readme:test", ReadmeSHA: new("test"), SourceQuality: "sufficient",
		SourceLanguage: "en", SummaryZh: "这个项目用于自动化。文档描述了工作流。", FeaturesJSON: `["工作流"]`,
		AudienceJSON: "[]", ValueTagsJSON: `["自动化"]`, SourceReadmeURL: &sourceURL, SummaryStatus: "ready", ModelProvider: summary.ProviderKey(cfg.AI),
		ModelName: cfg.AI.Model, PromptVersion: cfg.AI.Summary.PromptVersion, GeneratedAt: now.UnixMilli()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	check := storage.SummaryCheck{RepoID: 1, SourceFingerprint: row.SourceFingerprint, NextCheckAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := db.Create(&check).Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, "POST", path, `{"requestId":"cached"}`, "admin", 200)
	detail := request(t, router, "GET", "/api/v1/repos/1", "", "admin", 200)
	if detail["titleZh"] != "这个项目用于自动化" || detail["summaryZh"] != row.SummaryZh || detail["summaryModel"] != cfg.AI.Model {
		t.Fatalf("摘要详情契约错误: %+v", detail)
	}
	page := request(t, router, "GET", "/api/v1/repos?grade=A&tag="+url.QueryEscape("自动化"), "", "admin", 200)
	if len(page["items"].([]any)) != 1 {
		t.Fatal("已保存主题标签应可筛选仓库")
	}
	if err := db.Create(&storage.MaintenanceState{Key: metrics.ComputeKey, Value: "1", UpdatedAt: now.UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := topics.Generate(t.Context(), db, cfg.Recommendations, now); err != nil {
		t.Fatal(err)
	}
	items := request(t, router, "GET", "/api/v1/topics?limit=1", "", "admin", 200)
	topicID := int64(items["items"].([]any)[0].(map[string]any)["id"].(float64))
	if items["nextCursor"] == nil {
		t.Fatal("选题应能分页")
	}
	patchPath := fmt.Sprintf("/api/v1/topics/%d", topicID)
	request(t, router, "PATCH", patchPath, `{"status":"invalid"}`, "admin", 400)
	result := request(t, router, "PATCH", patchPath, `{"status":"accepted","feedback":"适合做工作流专题"}`, "admin", 200)
	if result["status"] != "accepted" || result["feedback"] != "适合做工作流专题" {
		t.Fatal("人工选题状态或反馈未保存")
	}
	accepted := request(t, router, "GET", "/api/v1/topics?status=accepted", "", "admin", 200)
	if len(accepted["items"].([]any)) != 1 {
		t.Fatal("选题状态筛选错误")
	}
	request(t, router, "PATCH", patchPath, `{"status":"candidate"}`, "admin", 200)
}
