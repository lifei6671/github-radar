package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/storage"
)

func jsonBody(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAdminConfigurationIsAuthenticatedAndApplied(t *testing.T) {
	router, db := collectionRouter(t)
	for _, suffix := range []string{"settings", "categories", "discovery-rules"} {
		path := "/api/v1/system/" + suffix
		request(t, router, http.MethodGet, path, "", "", 401)
		request(t, router, http.MethodPut, path, "{}", "", 401)
		request(t, router, http.MethodGet, path, "", "admin", 503)
	}
	request(t, router, http.MethodPost, "/api/v1/setup/initialize", "", "admin", 200)
	t.Setenv("GITHUB_TOKEN", "test-secret-value")
	const runtimePath = "/api/v1/system/settings"
	current := request(t, router, http.MethodGet, runtimePath, "", "admin", 200)
	values := current["settings"].(map[string]any)
	if current["restartRequired"] != false || strings.Contains(jsonBody(t, values), "test-secret-value") ||
		values["server"].(map[string]any)["listen"] != nil || values["storage"].(map[string]any)["path"] != nil {
		t.Fatalf("管理接口不应返回密钥值或部署入口: %+v", current)
	}
	values["github"].(map[string]any)["timeoutSeconds"] = 20
	update := jsonBody(t, map[string]any{"revision": current["revision"], "settings": values})
	saved := request(t, router, http.MethodPut, runtimePath, update, "admin", 200)
	if saved["restartRequired"] != true || saved["settings"].(map[string]any)["github"].(map[string]any)["timeoutSeconds"] != float64(20) {
		t.Fatalf("保存的进程参数应明确提示重启: %+v", saved)
	}
	request(t, router, http.MethodPut, runtimePath, update, "admin", 409)
	request(t, router, http.MethodPut, runtimePath, `{"revision":2,"settings":{"extra":true}}`, "admin", 400)

	now := time.Now().UnixMilli()
	repo := storage.Repository{
		RepoID: 1, Owner: "owner", Name: "repo", FullName: "owner/repo", NameIsCurrent: true,
		HTMLURL: "https://github.com/owner/repo", TopicsJSON: `["MCP"]`, State: "active", Grade: "A",
		DiscoveredAt: now, LastDiscoveredAt: now, NextFetchAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	categoryPath := "/api/v1/system/categories"
	request(t, router, http.MethodPut, categoryPath, `{"revision":2,"items":[{"slug":"other","name":"无效","anyTopics":["mcp"]}]}`, "admin", 400)
	categories := request(t, router, http.MethodPut, categoryPath,
		`{"revision":2,"items":[{"slug":"custom","name":"自定义","anyTopics":[" MCP ","mcp"]}]}`, "admin", 200)
	topics := categories["items"].([]any)[0].(map[string]any)["anyTopics"].([]any)
	if len(topics) != 1 || topics[0] != "mcp" {
		t.Fatalf("分类主题未归一化: %+v", topics)
	}
	public := request(t, router, http.MethodGet, "/api/v1/categories", "", "admin", 200)
	items := public["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["slug"] != "custom" || items[0].(map[string]any)["count"] != float64(1) {
		t.Fatalf("保存后分类列表和计数应立即生效: %+v", public)
	}
	repos := request(t, router, http.MethodGet, "/api/v1/repos?category=custom", "", "admin", 200)
	cards := repos["items"].([]any)
	if len(cards) != 1 || cards[0].(map[string]any)["categories"].([]any)[0] != "custom" {
		t.Fatalf("仓库筛选与卡片分类应使用同一份数据库映射: %+v", repos)
	}
	request(t, router, http.MethodGet, "/api/v1/repos?category=mcp", "", "admin", 400)
	var persisted storage.Repository
	if err := db.Take(&persisted, 1).Error; err != nil || persisted.TopicsJSON != repo.TopicsJSON {
		t.Fatalf("修改映射不应改写原始 topics: %v", err)
	}

	rulePath := "/api/v1/system/discovery-rules"
	request(t, router, http.MethodPut, rulePath,
		`{"revision":3,"items":[{"sourceType":"trending","sourceKey":"all:hourly","intervalMinutes":30}]}`, "admin", 400)
	request(t, router, http.MethodPut, rulePath,
		`{"revision":3,"items":[{"sourceType":"trending","sourceKey":"language:zig:daily","query":"","enabled":false,"intervalMinutes":30,"priority":10}]}`, "admin", 200)
	request(t, router, http.MethodGet, "/api/v1/trending?sourceKey=language:zig", "", "admin", 200)
	request(t, router, http.MethodGet, "/api/v1/trending", "", "admin", 400)
	request(t, router, http.MethodPut, rulePath, `{"revision":4,"items":[]}`, "admin", 200)
	rules := request(t, router, http.MethodGet, rulePath, "", "admin", 200)
	if len(rules["items"].([]any)) != 0 {
		t.Fatalf("已清空的规则不应被自动补齐: %+v", rules)
	}
}
