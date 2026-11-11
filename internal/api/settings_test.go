package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
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

func TestDiscoveryTabsPersistWithoutChangingGlobalCategories(t *testing.T) {
	router, db := collectionRouter(t)
	const admin = "/api/v1/system/discovery-tabs"
	const public = "/api/v1/public/discovery-tabs"
	request(t, router, http.MethodGet, admin, "", "", 401)
	request(t, router, http.MethodPut, admin, "{}", "", 401)
	request(t, router, http.MethodGet, public, "", "", 503)
	request(t, router, http.MethodPost, "/api/v1/setup/initialize", "", "admin", 200)
	before := request(t, router, http.MethodGet, "/api/v1/public/categories", "", "", 200)
	defaults := request(t, router, http.MethodGet, public, "", "", 200)
	if len(defaults["items"].([]any)) != len(before["items"].([]any)) {
		t.Fatalf("旧库默认应跟随分类映射: %+v", defaults)
	}
	for _, invalid := range []string{
		`{"revision":1,"items":[{"slug":"unknown","label":"无效"}]}`,
		`{"revision":1,"items":[{"slug":"other"},{"slug":"other"}]}`,
		`{"revision":1,"items":null}`,
	} {
		request(t, router, http.MethodPut, admin, invalid, "admin", 400)
	}
	const custom = `{"revision":1,"items":[{"slug":"other","label":" 更多项目 "},{"slug":"mcp","label":""}]}`
	request(t, router, http.MethodPut, admin, custom, "admin", 200)
	request(t, router, http.MethodPut, admin, custom, "admin", 409)
	// 复用启动时的初始化入口，确认保存的标签和修订号没有被默认配置覆盖。
	if _, err := (settings.Store{DB: db}).Initialize(t.Context(), config.Config{}); err != nil {
		t.Fatal(err)
	}
	result := request(t, router, http.MethodGet, public, "", "", 200)
	if got := jsonBody(t, result); got != `{"items":[{"name":"更多项目","slug":"other"},{"name":"MCP","slug":"mcp"}]}` {
		t.Fatalf("公开响应应只返回排序后的显示字段: %s", got)
	}
	after := request(t, router, http.MethodGet, "/api/v1/public/categories", "", "", 200)
	if jsonBody(t, before) != jsonBody(t, after) {
		t.Fatal("发现页配置改变了全部项目的分类")
	}
	request(t, router, http.MethodPut, "/api/v1/system/categories", `{"revision":2,"items":[]}`, "admin", 200)
	result = request(t, router, http.MethodGet, public, "", "", 200)
	if got := jsonBody(t, result); got != `{"items":[{"name":"更多项目","slug":"other"}]}` {
		t.Fatalf("被删除的分类仍在标签中: %s", got)
	}
	request(t, router, http.MethodPut, admin, `{"revision":3,"items":[]}`, "admin", 200)
	result = request(t, router, http.MethodGet, public, "", "", 200)
	if got := jsonBody(t, result); got != `{"items":[]}` {
		t.Fatalf("空配置应保留为空数组: %s", got)
	}
}

func TestPublicDiscoveryTabsReadDuringWrite(t *testing.T) {
	router, db := collectionRouter(t)
	request(t, router, http.MethodPost, "/api/v1/setup/initialize", "", "admin", 200)
	// 保持一个未提交的写事务，前台仍可从 WAL 读取已提交的设置和分类。
	write := db.WithContext(t.Context()).Begin()
	if write.Error != nil {
		t.Fatal(write.Error)
	}
	defer write.Rollback()
	if err := write.Model(&storage.CategorySetting{}).Where("slug = ?", "mcp").Update("name", "未提交名称").Error; err != nil {
		t.Fatal(err)
	}
	result := request(t, router, http.MethodGet, "/api/v1/public/discovery-tabs", "", "", 200)
	if body := jsonBody(t, result); !strings.Contains(body, `"name":"MCP","slug":"mcp"`) || strings.Contains(body, "未提交名称") {
		t.Fatalf("公开标签应从已提交的快照读取: %s", body)
	}
}

func TestDiscoveryTabCreatesCategoryAtomically(t *testing.T) {
	router, db := collectionRouter(t)
	request(t, router, http.MethodPost, "/api/v1/setup/initialize", "", "admin", 200)
	const path = "/api/v1/system/discovery-tabs"
	const category = `{"slug":"cli","name":"终端工具","anyTopics":[" CLI "]}`
	request(t, router, http.MethodPut, path, `{"revision":1,"items":[{"slug":"missing"}],"newCategories":[`+category+`]}`, "admin", 400)
	var count int64
	if err := db.Model(&storage.CategorySetting{}).Where("slug = ?", "cli").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("标签校验失败时不应写入分类: %d, %v", count, err)
	}
	request(t, router, http.MethodPut, path, `{"revision":1,"items":[{"slug":"cli"}],"newCategories":[`+category+`]}`, "admin", 200)
	result := request(t, router, http.MethodGet, "/api/v1/public/discovery-tabs", "", "", 200)
	if got := jsonBody(t, result); got != `{"items":[{"name":"终端工具","slug":"cli"}]}` {
		t.Fatalf("公开标签应立即使用新分类: %s", got)
	}
}

func TestCategoryMappingChangesKeepDiscoverySelectionIndependent(t *testing.T) {
	router, _ := collectionRouter(t)
	request(t, router, http.MethodPost, "/api/v1/setup/initialize", "", "admin", 200)
	const categoryPath = "/api/v1/system/categories"
	const tabsPath = "/api/v1/public/discovery-tabs"
	before := request(t, router, http.MethodGet, tabsPath, "", "", 200)
	categories := request(t, router, http.MethodGet, categoryPath, "", "admin", 200)
	categories["items"] = append(categories["items"].([]any), map[string]any{
		"slug": "cli", "name": "终端工具", "anyTopics": []string{"cli"},
	})
	request(t, router, http.MethodPut, categoryPath, jsonBody(t, categories), "admin", 200)
	after := request(t, router, http.MethodGet, tabsPath, "", "", 200)
	if jsonBody(t, after) != jsonBody(t, before) {
		t.Fatalf("新增分类不能自动加入发现页: %+v", after)
	}
	admin := request(t, router, http.MethodGet, "/api/v1/system/discovery-tabs", "", "admin", 200)
	if !strings.Contains(jsonBody(t, admin["options"]), `"slug":"cli"`) {
		t.Fatalf("新分类应出现在可选列表: %+v", admin)
	}
}

func TestDiscoveryRuleReorderKeepsScheduleAndLease(t *testing.T) {
	router, db := collectionRouter(t)
	request(t, router, http.MethodPost, "/api/v1/setup/initialize", "", "admin", 200)
	const path = "/api/v1/system/discovery-rules"
	response := request(t, router, http.MethodGet, path, "", "admin", 200)
	items := response["items"].([]any)
	first := items[0].(map[string]any)
	var rule storage.DiscoveryRule
	if err := db.Take(&rule, "source_type = ? AND source_key = ?", first["sourceType"], first["sourceKey"]).Error; err != nil {
		t.Fatal(err)
	}
	leaseToken := "rule-lease"
	job := storage.Job{Type: "discover_search", DedupKey: "order-test", Status: "running", LeaseToken: &leaseToken,
		PayloadJSON: fmt.Sprintf(`{"ruleId":%d}`, rule.ID)}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	items[0], items[1] = items[1], items[0]
	request(t, router, http.MethodPut, path, jsonBody(t, response), "admin", 200)
	got := request(t, router, http.MethodGet, path, "", "admin", 200)
	if jsonBody(t, got["items"]) != jsonBody(t, items) {
		t.Fatalf("重新读取时应保留提交的展示顺序: %+v", got)
	}
	var persisted storage.DiscoveryRule
	if err := db.Take(&persisted, rule.ID).Error; err != nil || persisted.NextRunAt != rule.NextRunAt || persisted.Priority != rule.Priority {
		t.Fatalf("排序不应改变任务计划与优先级: %+v, %v", persisted, err)
	}
	if err := db.Take(&job, job.ID).Error; err != nil || job.Status != "running" || job.LeaseToken == nil || *job.LeaseToken != leaseToken {
		t.Fatalf("排序不应撤销运行中的任务租约: %+v, %v", job, err)
	}
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
