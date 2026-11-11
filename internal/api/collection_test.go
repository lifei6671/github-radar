package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

func collectionRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "api.db")
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
	snapshot, err := (settings.Store{DB: db}).Initialize(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(db, pool, "admin", slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.WithRuntime(snapshot.Settings), nil), db
}

func request(t *testing.T, router http.Handler, method, path, body, token string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s 得到 %d %s，期望 %d", method, path, rec.Code, rec.Body, want)
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInitializeAndBusinessAuthentication(t *testing.T) {
	router, _ := collectionRouter(t)
	// 所有读取和写操作都先验证同一管理员令牌。
	for _, path := range []string{"/api/v1/repos", "/api/v1/categories", "/api/v1/trending", "/api/v1/system/limits", "/api/v1/jobs/1"} {
		request(t, router, "GET", path, "", "", 401)
		request(t, router, "GET", path, "", "admin", 503)
	}
	request(t, router, "POST", "/api/v1/setup/initialize", "", "", 401)
	request(t, router, "POST", "/api/v1/setup/initialize", "", "admin", 200)
	request(t, router, "POST", "/api/v1/setup/initialize", "", "admin", 409)
	if status := request(t, router, "GET", "/api/v1/setup/status", "", "admin", 200); status["initialized"] != true {
		t.Fatal("初始化状态未持久化")
	}
	response := request(t, router, "GET", "/api/v1/trending", "", "admin", 200)
	if response["batch"] != nil || response["updatedAt"] != nil || len(response["items"].([]any)) != 0 {
		t.Fatalf("无成功榜单时不能伪造批次: %+v", response)
	}
}

func TestRepositoryCursorAndClassification(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, repo := range []storage.Repository{
		{RepoID: 1, Owner: "one", Name: "repo", FullName: "one/repo", NameIsCurrent: true, HTMLURL: "https://github.com/one/repo", TopicsJSON: `["MCP","tts"]`, Grade: "A", State: "active", Stars: 100, DiscoveredAt: now, LastDiscoveredAt: now, NextFetchAt: now, UpdatedAt: now},
		{RepoID: 2, Owner: "two", Name: "repo", FullName: "two/repo", NameIsCurrent: true, HTMLURL: "https://github.com/two/repo", TopicsJSON: `[]`, Grade: "B", State: "active", Stars: 100, DiscoveredAt: now, LastDiscoveredAt: now, NextFetchAt: now, UpdatedAt: now},
		{RepoID: 3, Owner: "three", Name: "repo", FullName: "three/repo", NameIsCurrent: true, HTMLURL: "https://github.com/three/repo", TopicsJSON: `["mcp"]`, Grade: "A", State: "candidate", Stars: 999, DiscoveredAt: now + 60000, LastDiscoveredAt: now + 60000, NextFetchAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
	}
	first := request(t, router, "GET", "/api/v1/repos?sort=stars&limit=1", "", "admin", 200)
	items := first["items"].([]any)
	card := items[0].(map[string]any)
	if card["id"] != float64(1) || card["growth24h"].(map[string]any)["delta"] != nil || card["summaryStatus"] != "missing" {
		t.Fatalf("首次基线的字段契约不符: %+v", card)
	}
	cursor := first["nextCursor"].(string)
	second := request(t, router, "GET", "/api/v1/repos?sort=stars&limit=10&cursor="+url.QueryEscape(cursor), "", "admin", 200)
	if second["items"].([]any)[0].(map[string]any)["id"] != float64(2) || second["nextCursor"] != nil {
		t.Fatalf("同值排序键的分页丢失或重复: %+v", second)
	}
	request(t, router, "GET", "/api/v1/repos?sort=discovered&cursor="+url.QueryEscape(cursor), "", "admin", 400)
	request(t, router, "GET", "/api/v1/repos?cursor=invalid", "", "admin", 400)
	mcp := request(t, router, "GET", "/api/v1/repos?category=mcp&category=media", "", "admin", 200)
	if len(mcp["items"].([]any)) != 1 {
		t.Fatalf("分类多选应取并集且默认只包含 active: %+v", mcp)
	}
	other := request(t, router, "GET", "/api/v1/repos?category=other", "", "admin", 200)
	if len(other["items"].([]any)) != 1 || other["items"].([]any)[0].(map[string]any)["id"] != float64(2) {
		t.Fatalf("other 回退不符: %+v", other)
	}
	// 主题卡片采用与项目列表一致的 active 集合，多分类项目可以同时预览。
	if err := db.Create(&storage.Repository{
		RepoID: 4, Owner: "older", Name: "repo", FullName: "older/repo", NameIsCurrent: true,
		HTMLURL: "https://github.com/older/repo", TopicsJSON: `["mcp"]`, Grade: "A", State: "active",
		DiscoveredAt: now - 60000, LastDiscoveredAt: now - 60000, NextFetchAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	categories := request(t, router, "GET", "/api/v1/categories", "", "admin", 200)
	wantLatest := map[string]float64{"mcp": 1, "media": 1, "other": 2}
	for _, raw := range categories["items"].([]any) {
		category := raw.(map[string]any)
		slug := category["slug"].(string)
		want, hasRepo := wantLatest[slug]
		if !hasRepo {
			if category["count"] != float64(0) || category["latestRepo"] != nil {
				t.Fatalf("空主题不能返回项目预览: %+v", category)
			}
			continue
		}
		preview, ok := category["latestRepo"].(map[string]any)
		count := float64(1)
		if slug == "mcp" {
			count = 2
		}
		if !ok || category["count"] != count || preview["id"] != want || preview["discoveredAt"] != iso(now) {
			t.Fatalf("主题预览应来自最近发现的监控仓库: %+v", category)
		}
		delete(wantLatest, slug)
	}
	if len(wantLatest) != 0 {
		t.Fatalf("分类响应缺少预期主题: %+v", wantLatest)
	}
	request(t, router, "GET", "/api/v1/repos?category=unknown", "", "admin", 400)
	request(t, router, "GET", "/api/v1/repos?sort=followed", "", "admin", 400)
	request(t, router, "POST", "/api/v1/repos/1/follow", `{"follow":true}`, "admin", 200)
	var followed storage.Repository
	if err := db.Take(&followed, "repo_id=1").Error; err != nil || !followed.StarredByUser {
		t.Fatalf("设计契约的 follow 字段应能完成关注: %v", err)
	}
}

func TestRepositoryBlockAdminAndPublicContracts(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	repo := storage.Repository{RepoID: 21, Owner: "owner", Name: "blocked", FullName: "owner/blocked",
		NameIsCurrent: true, HTMLURL: "https://github.com/owner/blocked", TopicsJSON: "[]", Grade: "A", State: "active"}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/repos/21/block"
	request(t, router, "PATCH", path, `{"blocked":true}`, "", 401)
	request(t, router, "PATCH", path, `{}`, "admin", 400)
	request(t, router, "PATCH", path, `{"blocked":true}`, "admin", 200)
	request(t, router, "PATCH", path, `{"blocked":true}`, "admin", 200)
	request(t, router, "GET", "/api/v1/public/repos/by-name/owner/blocked", "", "", 404)
	request(t, router, "GET", "/api/v1/public/repos/21/readme", "", "", 404)
	request(t, router, "GET", "/api/v1/public/repos?blocked=true", "", "", 400)
	for _, list := range []string{"/api/v1/repos", "/api/v1/repos?blocked=true"} {
		items := request(t, router, "GET", list, "", "admin", 200)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["blocked"] != true {
			t.Fatalf("后台应能筛选和解除屏蔽: %+v", items)
		}
	}
	request(t, router, "POST", "/api/v1/repos/21/summary", `{"requestId":"blocked"}`, "admin", 409)
	request(t, router, "PATCH", path, `{"blocked":false}`, "admin", 200)
	item := request(t, router, "GET", "/api/v1/public/repos/by-name/owner/blocked", "", "", 200)
	if _, leaked := item["blocked"]; leaked {
		t.Fatal("公开详情不返回管理字段")
	}
}

func TestAITopicsClassificationAndGitHubPrecedence(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	repo := storage.Repository{RepoID: 1, Owner: "owner", Name: "repo", FullName: "owner/repo", NameIsCurrent: true,
		HTMLURL: "https://github.com/owner/repo", TopicsJSON: "[]", Grade: "A", State: "active"}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&storage.RepoAITopics{RepoID: 1, TopicsJSON: `["mcp"]`}).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		topics, category string
		inferred         bool
	}{{"[]", "mcp", true}, {`["unmapped"]`, "other", false}} {
		if err := db.Model(&repo).Update("topics_json", tc.topics).Error; err != nil {
			t.Fatal(err)
		}
		detail := request(t, router, "GET", "/api/v1/public/repos/by-name/owner/repo", "", "", 200)
		tags := detail["aiTopics"].([]any)
		if detail["categories"].([]any)[0] != tc.category || (len(tags) > 0) != tc.inferred ||
			(tc.inferred && tags[0] != "mcp") {
			t.Fatalf("详情分类或 AI Topics 来源不符: %+v", detail)
		}
		for _, category := range []string{"mcp", "other"} {
			items := request(t, router, "GET", "/api/v1/repos?category="+category, "", "admin", 200)["items"].([]any)
			if (len(items) == 1) != (category == tc.category) {
				t.Fatalf("分类 %s 的筛选结果不符: %+v", category, items)
			}
		}
		items := request(t, router, "GET", "/api/v1/public/categories", "", "", 200)["items"].([]any)
		for _, raw := range items {
			item := raw.(map[string]any)
			if (item["count"] == float64(1)) != (item["slug"] == tc.category) {
				t.Fatalf("分类计数与详情不一致: %+v", item)
			}
		}
	}
}

func TestTrendingResponseKeepsSelectedBatch(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	slot := now / 300000 * 300000
	for id := int64(1); id <= 2; id++ {
		repo := storage.Repository{
			RepoID: id, Owner: "owner", Name: strconv.FormatInt(id, 10), FullName: "owner/" + strconv.FormatInt(id, 10),
			NameIsCurrent: true, HTMLURL: "https://github.com/owner/" + strconv.FormatInt(id, 10),
			TopicsJSON: "[]", State: "active", Grade: "A", DiscoveredAt: now, LastDiscoveredAt: now, NextFetchAt: now, UpdatedAt: now,
		}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		observation := storage.TrendingObservation{RepoID: id, SourceKey: "all", Window: "daily", ObservedSlot: slot, Rank: int(id)}
		if err := db.Create(&observation).Error; err != nil {
			t.Fatal(err)
		}
	}
	marker := storage.MaintenanceState{Key: "trending:all:daily", Value: fmt.Sprintf(`{"observedSlot":%d,"observedAt":%d,"size":2}`, slot, now), UpdatedAt: now}
	if err := db.Create(&marker).Error; err != nil {
		t.Fatal(err)
	}
	// 在成员查询后发布新批次，模拟请求与 Worker 提交交错。
	var published atomic.Bool
	callback := "test:publish_trending"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "repositories" || len(tx.Statement.Joins) == 0 || !published.CompareAndSwap(false, true) {
			return
		}
		for id := int64(1); id <= 2; id++ {
			obs := storage.TrendingObservation{RepoID: id, SourceKey: "all", Window: "daily", ObservedSlot: slot + 300000, Rank: 3 - int(id)}
			if err := db.Create(&obs).Error; err != nil {
				t.Error(err)
			}
		}
		value := fmt.Sprintf(`{"observedSlot":%d,"observedAt":%d,"size":2}`, slot+300000, now+300000)
		if err := db.Model(&marker).Update("value", value).Error; err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(callback)
	response := request(t, router, "GET", "/api/v1/trending", "", "admin", 200)
	first := response["items"].([]any)[0].(map[string]any)
	entry := first["trendingEntries"].([]any)[0].(map[string]any)
	if !published.Load() || response["batch"].(map[string]any)["observedSlot"] != float64(slot) ||
		first["id"] != float64(1) || entry["rank"] != float64(1) || entry["observedAt"] != iso(now) {
		t.Fatalf("榜单批次、成员和名次必须来自同一槽: %+v", response)
	}
}
