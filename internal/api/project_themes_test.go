package api

import (
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestProjectThemeMatchesConfiguredOwnersExactly(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UnixMilli()
	for i, owner := range []string{"Microsoft", "dev-user", "microsoft-samples", "microsoft"} {
		id := int64(i + 1)
		name := fmt.Sprint(id)
		repo := storage.Repository{RepoID: id, Owner: owner, Name: name, FullName: owner + "/" + name,
			NameIsCurrent: true, State: "active", Grade: "A", TopicsJSON: `["company-projects"]`,
			DiscoveredAt: at, LastDiscoveredAt: at, LastSuccessAt: &at, Blocked: id == 4}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		seedReadySummary(t, db, id, at)
	}
	const settingsPath = "/api/v1/system/project-themes"
	settings := request(t, router, "GET", settingsPath, "", "admin", 200)
	revision := settings["revision"]
	request(t, router, "PUT", settingsPath,
		fmt.Sprintf(`{"revision":%v,"items":[{"slug":"company-projects","name":"公司开源","description":"按账号匹配","owners":["microsoft/repo"]}]}`, revision), "admin", 400)
	request(t, router, "PUT", settingsPath,
		fmt.Sprintf(`{"revision":%v,"items":[{"slug":"company-projects","name":"公司开源","description":"按账号匹配","anyTopics":["company-projects"],"owners":[" @Microsoft ","microsoft","dev-user"]}]}`, revision), "admin", 200)
	item := request(t, router, "GET", settingsPath, "", "admin", 200)["items"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(item["owners"], []any{"microsoft", "dev-user"}) {
		t.Fatalf("所有者应归一化并去重: %v", item)
	}
	catalog, err := storage.ReadThemeCatalog(db.WithContext(t.Context()))
	if err != nil || len(catalog.Terms) != 0 {
		t.Fatalf("所有者专题不应由 AI 分配: %+v %v", catalog, err)
	}
	const public = "/api/v1/public"
	detail := request(t, router, "GET", public+"/project-themes/company-projects", "", "", 200)
	if detail["count"] != float64(2) || detail["recentCount"] != float64(2) {
		t.Fatalf("专题只应统计已公开的指定账号仓库: %v", detail)
	}
	for _, path := range []string{"/repos", "/discovery"} {
		body := request(t, router, "GET", public+path+"?theme=company-projects", "", "", 200)
		var ids []int
		for _, raw := range body["items"].([]any) {
			ids = append(ids, int(raw.(map[string]any)["id"].(float64)))
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []int{1, 2}) {
			t.Fatalf("%s 应只匹配确切的所有者: %v", path, ids)
		}
	}
}

func TestProjectThemeUsesTopicsAndKeepsDiscoveryAndCompleteViews(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	const public = "/api/v1/public"
	const admin = "/api/v1/system/project-themes"
	now := time.Now()
	sampledAt := now.Add(-time.Minute).UnixMilli()
	cases := []struct {
		native, ai string
		age        time.Duration
		grade      string
		blocked    bool
	}{
		{`["mcp","mcp-server"]`, `["unrelated"]`, time.Hour, "A", false},
		{`["mcp"]`, `["mcp-server"]`, 2 * time.Hour, "S", false},  // 原生标签没有命中词表，使用已有 AI Topics。
		{`[]`, `["mcp-server"]`, 40 * 24 * time.Hour, "C", false}, // 只出现在专题全部项目。
		{`["mcp-server"]`, "", 2 * time.Hour, "A", true},
		{`["mcp-client"]`, "", 2 * time.Hour, "A", false},
		{`[]`, `["mcp-server"]`, 3 * time.Hour, "S", false},
	}
	for i, fixture := range cases {
		id := int64(i + 1)
		name := fmt.Sprint(id)
		discoveredAt := now.Add(-fixture.age).UnixMilli()
		repo := storage.Repository{RepoID: id, Owner: "themes", Name: name, FullName: "themes/" + name,
			HTMLURL: "https://github.com/themes/" + name, NameIsCurrent: true, TopicsJSON: fixture.native,
			State: "active", Grade: fixture.grade, Blocked: fixture.blocked, DiscoveredAt: discoveredAt,
			LastDiscoveredAt: discoveredAt, LastSuccessAt: &sampledAt, NextFetchAt: sampledAt, UpdatedAt: sampledAt}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		seedReadySummary(t, db, id, sampledAt)
		if fixture.ai != "" {
			topics := storage.RepoAITopics{RepoID: id, TopicsJSON: fixture.ai, SourceFingerprint: "fixture",
				ModelProvider: "fixture", ModelName: "fixture", PromptVersion: 1, GeneratedAt: sampledAt}
			if err := db.Create(&topics).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	request(t, router, "GET", admin, "", "", 401)
	request(t, router, "PUT", admin, "{}", "", 401)
	initial := request(t, router, "GET", admin, "", "admin", 200)
	if len(initial["items"].([]any)) == 0 {
		t.Fatal("迁移应提供可修改的初始专题")
	}
	categoryPath := public + "/categories"
	before := request(t, router, "GET", categoryPath, "", "", 200)
	payload := fmt.Sprintf(`{"revision":%v,"items":[{"slug":"mcp-tools","name":" MCP 工具 ","description":" MCP 服务端与工具集 ","anyTopics":["MCP-SERVER","mcp-server"],"aliases":[" Model Context Protocol ","model context protocol"],"icon":"plug","iconColor":"green"}]}`, initial["revision"])
	saved := request(t, router, "PUT", admin, payload, "admin", 200)
	item := saved["items"].([]any)[0].(map[string]any)
	if item["name"] != "MCP 工具" || item["description"] != "MCP 服务端与工具集" ||
		!reflect.DeepEqual(item["anyTopics"], []any{"mcp-server"}) || !reflect.DeepEqual(item["aliases"], []any{"model context protocol"}) {
		t.Fatalf("专题写入后未正确归一化: %v", item)
	}
	request(t, router, "PUT", admin, payload, "admin", 409)
	request(t, router, "PUT", admin, fmt.Sprintf(`{"revision":%v,"items":[{"slug":"empty","name":"空","description":"缺少匹配词","anyTopics":[]}]}`, saved["revision"]), "admin", 400)
	request(t, router, "PUT", admin, fmt.Sprintf(`{"revision":%v,"items":[{"slug":"mcp-tools","name":"工具","description":"工具","anyTopics":["mcp-server"],"icon":"custom-svg"}]}`, saved["revision"]), "admin", 400)
	if after := request(t, router, "GET", categoryPath, "", "", 200); !reflect.DeepEqual(before, after) {
		t.Fatalf("专题管理改变了分类映射: %v", after)
	}

	const summaryPath = public + "/project-themes"
	directory := request(t, router, "GET", summaryPath, "", "", 200)["items"].([]any)
	if len(directory) != 1 {
		t.Fatalf("目录应使用后台专题定义: %v", directory)
	}
	detail := request(t, router, "GET", summaryPath+"/mcp-tools", "", "", 200)
	if got := detail["milestones"].([]any); len(got) != 0 {
		t.Fatalf("没有观测记录时不应有大事记: %v", got)
	}
	delete(detail, "milestones")
	delete(detail, "groupName")
	if !reflect.DeepEqual(directory[0], detail) || detail["count"] != float64(4) || detail["recentCount"] != float64(3) ||
		detail["icon"] != consts.ThemeIconPlug || detail["iconColor"] != consts.ThemeColorGreen ||
		detail["latestRepo"].(map[string]any)["id"] != float64(1) {
		t.Fatalf("目录和详情计数应使用相同的公开专题归属: %v", detail)
	}
	// 大事记使用同一主题归属并过滤被屏蔽的仓库。
	monthStart := time.Date(now.In(themeCalendar).Year(), now.In(themeCalendar).Month(), 1, 0, 0, 0, 0, themeCalendar)
	at := monthStart.Add(-24 * time.Hour)
	for i, rank := range []int{2, 1} {
		observed := at.Add(-time.Duration(i) * time.Hour).UnixMilli()
		batch := storage.TrendingBatchRecord{SourceKey: consts.TrendingAll, Window: consts.PeriodDaily, ObservedSlot: observed, ObservedAt: observed}
		if err := db.Create(&batch).Error; err != nil {
			t.Fatal(err)
		}
		for _, id := range []int64{1, 2, 4} {
			entry := storage.TrendingObservation{RepoID: id, SourceKey: batch.SourceKey, Window: batch.Window, ObservedSlot: observed, Rank: rank}
			if err := db.Create(&entry).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	daily := storage.DailyMetric{RepoID: 3, Day: at.UTC().Format(time.DateOnly), Quality: consts.QualityExact,
		MetricAnchors: storage.MetricAnchors{StarsOpen: 999, StarsClose: 1002, OpenObservedAt: at.Add(-time.Hour).UnixMilli(),
			CloseObservedAt: at.UnixMilli(), SampleCount: 2}}
	if err := db.Create(&daily).Error; err != nil {
		t.Fatal(err)
	}
	chronicle := request(t, router, "GET", summaryPath+"/mcp-tools", "", "", 200)["milestones"]
	want := []any{
		map[string]any{"kind": consts.MilestoneStars, "at": iso(at.UnixMilli()), "fullName": "themes/3", "value": float64(1000)},
		map[string]any{"kind": consts.MilestoneTrending, "at": iso(at.Add(-time.Hour).UnixMilli()), "fullName": "themes/1", "value": float64(1)},
		map[string]any{"kind": consts.MilestoneTrending, "at": iso(at.Add(-time.Hour).UnixMilli()), "fullName": "themes/2", "value": float64(1)},
	}
	if !reflect.DeepEqual(chronicle, want) {
		t.Fatalf("大事记未使用真实 Star 观测和当月最佳上榜名次: %v", chronicle)
	}
	assertIDs := func(result map[string]any, want ...int) {
		t.Helper()
		var got []int
		for _, raw := range result["items"].([]any) {
			got = append(got, int(raw.(map[string]any)["id"].(float64)))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("专题仓库不匹配: %v，期望 %v", got, want)
		}
	}
	for _, scenario := range []struct {
		endpoint string
		ids      []int
	}{
		{public + "/discovery", []int{1, 2, 6}},
		{public + "/repos", []int{1, 2, 6, 3}},
	} {
		path := scenario.endpoint + "?theme=mcp-tools"
		assertIDs(request(t, router, "GET", path, "", "", 200), scenario.ids...)
		first := request(t, router, "GET", path+"&limit=1", "", "", 200)
		assertIDs(first, 1)
		cursor := url.QueryEscape(first["nextCursor"].(string))
		assertIDs(request(t, router, "GET", path+"&cursor="+cursor, "", "", 200), scenario.ids[1:]...)
		request(t, router, "GET", scenario.endpoint+"?cursor="+cursor, "", "", 400)
	}
	request(t, router, "PUT", admin, fmt.Sprintf(`{"revision":%v,"items":[]}`, saved["revision"]), "admin", 200)
	request(t, router, "GET", summaryPath+"/mcp-tools", "", "", 404)
	request(t, router, "GET", public+"/discovery?theme=mcp-tools", "", "", 404)
	request(t, router, "GET", public+"/repos?theme=mcp-tools", "", "", 404)
}

func TestThemeCatalogPrefersNativeMatchesAndUsesAIForUnknownTopics(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	repo := storage.Repository{RepoID: 1, Owner: "themes", Name: "project", FullName: "themes/project",
		State: "active", Grade: "A", NameIsCurrent: true, TopicsJSON: `["mcp-server"]`, LastSuccessAt: &now}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	seedReadySummary(t, db, 1, now)
	if err := db.Create(&storage.RepoAITopics{RepoID: 1, TopicsJSON: `["tui"]`, ThemeSlugsJSON: `["terminal-tools"]`, ThemeCatalogKey: "initial"}).Error; err != nil {
		t.Fatal(err)
	}
	const themes = "/api/v1/public/project-themes/"
	count := func(slug string) float64 {
		t.Helper()
		return request(t, router, "GET", themes+slug, "", "", 200)["count"].(float64)
	}
	if count("mcp-servers") != 1 || count("terminal-tools") != 0 {
		t.Fatal("原生匹配必须优先于 AI 归类")
	}
	if err := db.Model(&repo).Update("topics_json", `["unmapped-topic"]`).Error; err != nil {
		t.Fatal(err)
	}
	if count("mcp-servers") != 0 || count("terminal-tools") != 1 {
		t.Fatal("原生主题未命中时应显示词表内的 AI 归类")
	}
}

func TestProjectThemeGroupAssignmentAndRemoval(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	const groupsPath = "/api/v1/system/project-theme-groups"
	const themesPath = "/api/v1/system/project-themes"
	initial := request(t, router, "GET", groupsPath, "", "admin", 200)
	// 仍有专题归属时不能删方向，也不能消耗设置修订号。
	request(t, router, "PUT", groupsPath, fmt.Sprintf(`{"revision":%v,"items":[]}`, initial["revision"]), "admin", 400)
	updated := request(t, router, "PUT", groupsPath, fmt.Sprintf(`{"revision":%v,"items":[
		{"slug":"content","name":"项目用途","description":"按用途查看工具。"},
		{"slug":"technology","name":"技术方向","description":"跟踪技术领域。"}]}`, initial["revision"]), "admin", 200)
	request(t, router, "PUT", themesPath, fmt.Sprintf(`{"revision":%v,"items":[
		{"slug":"sample","name":"项目","description":"介绍","anyTopics":["cli"],"groupSlug":"missing"}]}`, updated["revision"]), "admin", 400)
	request(t, router, "PUT", themesPath, fmt.Sprintf(`{"revision":%v,"items":[
		{"slug":"sample","name":"项目","description":"介绍","anyTopics":["cli"],"groupSlug":"content"}]}`, updated["revision"]), "admin", 200)
	directory := request(t, router, "GET", "/api/v1/public/project-themes", "", "", 200)
	first := directory["groups"].([]any)[0].(map[string]any)
	item := directory["items"].([]any)[0].(map[string]any)
	if first["slug"] != "content" || first["name"] != "项目用途" || first["description"] != "按用途查看工具。" || item["groupSlug"] != first["slug"] {
		t.Fatalf("公开目录未返回更新后的方向及专题归属: %+v", directory)
	}
	detail := request(t, router, "GET", "/api/v1/public/project-themes/sample", "", "", 200)
	if detail["groupName"] != first["name"] {
		t.Fatalf("公开详情未返回所属方向的名称: %+v", detail)
	}
}
