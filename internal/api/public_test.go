package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

func readySummaryFixture(id, at int64) storage.AISummary {
	return storage.AISummary{RepoID: id, SourceFingerprint: "readme:test", SourceQuality: "sufficient", SourceLanguage: "en",
		SummaryZh: "用于维护开发工作流。", FeaturesJSON: "[]", AudienceJSON: "[]", ValueTagsJSON: "[]",
		ModelProvider: "fixture", ModelName: "internal-model", PromptVersion: 1, SummaryStatus: "ready", GeneratedAt: at}
}

func seedReadySummary(t *testing.T, db *gorm.DB, id, at int64) {
	t.Helper()
	summary := readySummaryFixture(id, at)
	if err := db.Create(&summary).Error; err != nil {
		t.Fatal(err)
	}
}

func TestPublicSearchStarFloorUsesDurableDiscoverySources(t *testing.T) {
	router, db := collectionRouter(t)
	request(t, router, "POST", "/api/v1/setup/initialize", "", "admin", 200)
	const rulesPath = "/api/v1/system/discovery-rules"
	settings := request(t, router, "GET", rulesPath, "", "admin", 200)
	strict := map[string]any{"sourceType": "search", "sourceKey": "strict", "query": "topic:mcp", "minStars": 200, "intervalMinutes": 60, "enabled": true}
	settings["items"] = []any{strict, map[string]any{
		"sourceType": "search", "sourceKey": "lenient", "query": "topic:agent", "minStars": 100, "intervalMinutes": 60,
	}}
	settings["revision"] = request(t, router, "PUT", rulesPath, jsonBody(t, settings), "admin", 200)["revision"]
	now := time.Now().Add(-time.Minute).UnixMilli()
	for i, fixture := range []struct {
		stars    int
		sources  []string
		trending bool
	}{
		{182, []string{"strict"}, false},
		{182, []string{"strict"}, true},
		{200, []string{"strict"}, false},
		{125, []string{"strict", "lenient"}, false},
		{75, []string{"deleted-rule"}, false},
		{25, nil, false},
	} {
		id := int64(i + 1)
		repo := storage.Repository{RepoID: id, Owner: "floor", Name: fmt.Sprint(id), FullName: fmt.Sprintf("floor/%d", id),
			NameIsCurrent: true, Stars: fixture.stars, State: "active", Grade: "A", TopicsJSON: `["mcp"]`, DiscoveredAt: now, LastSuccessAt: &now}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		seedReadySummary(t, db, id, now)
		for _, source := range fixture.sources {
			observation := storage.SearchObservation{RepoID: id, SourceKey: source, ObservedAt: now}
			if err := db.Create(&observation).Error; err != nil {
				t.Fatal(err)
			}
		}
		if fixture.trending {
			observation := storage.TrendingObservation{RepoID: id, SourceKey: "go", Window: "weekly", Rank: 1}
			if err := db.Create(&observation).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	// 清理过期观测明细后，来源及曾经上榜的事实仍用于发布判定。
	if err := db.Exec("DELETE FROM search_observations").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM trending_observations").Error; err != nil {
		t.Fatal(err)
	}
	const public = "/api/v1/public"
	for _, path := range []string{"/repos", "/repos?q=floor&sort=relevance", "/discovery"} {
		items := request(t, router, "GET", public+path, "", "", 200)["items"].([]any)
		var ids []int
		for _, raw := range items {
			ids = append(ids, int(raw.(map[string]any)["id"].(float64)))
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []int{2, 3, 4, 5, 6}) {
			t.Fatalf("%s 筛选结果错误: %v", path, ids)
		}
	}
	const detail = public + "/repos/by-name/floor/1"
	request(t, router, "GET", detail, "", "", 404)
	for _, child := range []string{"readme", "trend", "similar"} {
		request(t, router, "GET", public+"/repos/1/"+child, "", "", 404)
	}
	if items := request(t, router, "GET", public+"/repos/published?id=1&id=2", "", "", 200)["items"].([]any); !slices.Equal(items, []any{float64(2)}) {
		t.Fatalf("收藏应同步使用同一公开门槛: %v", items)
	}
	request(t, router, "GET", "/api/v1/repos/1", "", "admin", 200)
	strict["minStars"] = 180
	settings["revision"] = request(t, router, "PUT", rulesPath, jsonBody(t, settings), "admin", 200)["revision"]
	request(t, router, "GET", detail, "", "", 200)
	strict["minStars"] = 200
	request(t, router, "PUT", rulesPath, jsonBody(t, settings), "admin", 200)
	request(t, router, "GET", detail, "", "", 404)
	observation := storage.TrendingObservation{RepoID: 1, SourceKey: "all", Window: "daily", ObservedSlot: now, Rank: 1}
	if err := db.Create(&observation).Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, "GET", detail, "", "", 200)
}

func TestPublicReadDoesNotExposeAdminData(t *testing.T) {
	router, db := collectionRouter(t)
	const public = "/api/v1/public"

	before := request(t, router, "GET", public+"/repos", "", "", 503)
	if before["code"] != "setup_required" {
		t.Fatalf("公开读取应遵守安装门禁: %v", before)
	}
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	slot := now / 300000 * 300000
	for id := int64(1); id <= 8; id++ {
		repo := storage.Repository{RepoID: id, Owner: "example", Name: fmt.Sprint(id), FullName: fmt.Sprintf("example/%d", id),
			NameIsCurrent: true, HTMLURL: fmt.Sprintf("https://github.com/example/%d", id), TopicsJSON: `["mcp"]`,
			Description: new("GitHub 简介"), State: "active", Grade: "A", DiscoveredAt: now + id, LastDiscoveredAt: now, LastSuccessAt: &now, NextFetchAt: now, UpdatedAt: now}
		switch id {
		case 1:
			repo.StarredByUser, repo.FollowedAt = true, &now
		case 2:
			repo.State = "unavailable"
		case 3:
			repo.FirstNotFoundAt = &now
		case 4:
			repo.State = "candidate"
		case 6:
			repo.LastSuccessAt = nil
		}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		seedPurposeTopics(t, db, id, `["mcp-server"]`)
		if id != 4 && id != 5 {
			summary := readySummaryFixture(id, now)
			switch id {
			case 7:
				summary.SummaryStatus = "stale"
			case 8:
				summary.SummaryZh = " "
			}
			if err := db.Create(&summary).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Create(&storage.TrendingObservation{RepoID: id, SourceKey: "all", Window: "daily", ObservedSlot: slot, Rank: int(id)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	marker := storage.MaintenanceState{Key: "trending:all:daily",
		Value: fmt.Sprintf(`{"observedSlot":%d,"observedAt":%d,"size":8}`, slot, now), UpdatedAt: now}
	if err := db.Create(&marker).Error; err != nil {
		t.Fatal(err)
	}
	summary := readySummaryFixture(1, now)
	assertPublicCard := func(card map[string]any) {
		t.Helper()
		for _, key := range []string{"followed", "followedAt", "summaryJobId", "summaryModel", "summaryAvailable"} {
			if _, exists := card[key]; exists {
				t.Fatalf("公开响应包含管理字段 %s", key)
			}
		}
	}
	list := request(t, router, "GET", public+"/repos", "", "", 200)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != float64(1) {
		t.Fatalf("公开列表包含未发布仓库: %v", list)
	}
	assertPublicCard(list[0].(map[string]any))
	detail := request(t, router, "GET", public+"/repos/by-name/example/1", "", "admin", 200)
	assertPublicCard(detail)
	byName := request(t, router, "GET", public+"/repos/by-name/EXAMPLE/1", "", "", 200)
	assertPublicCard(byName)
	if byName["id"] != detail["id"] || byName["fullName"] != "example/1" {
		t.Fatalf("名称查找应保留同一仓库身份和原始大小写: %+v", byName)
	}
	bookmarked := request(t, router, "GET", public+"/repos/by-name/example/4?repoId=1", "", "", 200)
	if bookmarked["id"] != detail["id"] || bookmarked["fullName"] != "example/1" {
		t.Fatalf("收藏应解析已保存仓库的当前名称: %+v", bookmarked)
	}
	request(t, router, "GET", public+"/repos/by-name/example/1?repoId=2", "", "", 404)
	request(t, router, "GET", public+"/repos/by-name/example/1?repoId=invalid", "", "", 400)
	if detail["summaryZh"] != summary.SummaryZh || detail["summaryStatus"] != "ready" {
		t.Fatalf("公开摘要应保留正文与真实有效状态: %v", detail)
	}
	request(t, router, "GET", public+"/repos/1/trend", "", "", 200)
	legacy := httptest.NewRecorder()
	router.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, public+"/repos/1", nil))
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("数字公开详情路由应返回 404，实际 %d", legacy.Code)
	}
	request(t, router, "GET", public+"/repos/by-name/example/2", "", "", 404)
	request(t, router, "GET", public+"/repos/by-name/example/3", "", "", 404)
	request(t, router, "GET", public+"/repos/3/trend", "", "", 404)
	for id := 5; id <= 8; id++ {
		request(t, router, "GET", fmt.Sprintf("%s/repos/by-name/example/%d", public, id), "", "", 404)
		for _, subresource := range []string{"trend", "readme", "similar"} {
			request(t, router, "GET", fmt.Sprintf("%s/repos/%d/%s", public, id, subresource), "", "", 404)
		}
		request(t, router, "GET", fmt.Sprintf("/api/v1/repos/%d", id), "", "admin", 200)
	}
	request(t, router, "GET", "/api/v1/repos/5/readme", "", "admin", 200)
	if got := request(t, router, "GET", public+"/repos/published?id=1&id=5&id=6&id=7&id=8", "", "", 200)["items"].([]any); !slices.Equal(got, []any{float64(1)}) {
		t.Fatalf("收藏校验返回未发布仓库: %v", got)
	}
	request(t, router, "GET", public+"/repos/published?id=0", "", "", 400)
	request(t, router, "GET", public+"/repos?followed=true", "", "", 400)
	admin := request(t, router, "GET", "/api/v1/repos/1", "", "admin", 200)
	if admin["followed"] != true || admin["followedAt"] == nil {
		t.Fatal("管理接口必须保留原有关注数据")
	}
	request(t, router, "GET", "/api/v1/repos/by-name/example/1", "", "", 401)
	request(t, router, "GET", "/api/v1/repos/by-name/example/2", "", "admin", 200)
	repoID := int64(1)
	for _, job := range []storage.Job{
		{Type: "refresh_repo", RepoID: &repoID, DedupKey: "named-job", PayloadJSON: "{}", Status: "pending"},
		{Type: "discover_search", DedupKey: "unlinked-job", PayloadJSON: "{}", Status: "pending"},
	} {
		if err := db.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
	}
	jobs := request(t, router, "GET", "/api/v1/system/jobs", "", "admin", 200)["items"].([]any)
	if len(jobs) != 2 || jobs[0].(map[string]any)["repoFullName"] != nil || jobs[1].(map[string]any)["repoFullName"] != "example/1" {
		t.Fatalf("任务入口应使用关联仓库的完整名称: %+v", jobs)
	}
	// 榜单保留可公开仓库的原始名次，详情门槛和导读展示独立判断。
	assertTrending := func(withNewSummary bool) {
		t.Helper()
		trending := request(t, router, "GET", public+"/trending", "", "", 200)["items"].([]any)
		wantRanks := []int{1, 4, 5, 6, 7, 8}
		if len(trending) != len(wantRanks) {
			t.Fatalf("公开榜单应保留可公开的批次成员: %v", trending)
		}
		for i, rank := range wantRanks {
			card := trending[i].(map[string]any)
			assertPublicCard(card)
			entry := card["trendingEntries"].([]any)[0].(map[string]any)
			if card["id"] != float64(rank) || entry["rank"] != float64(rank) {
				t.Fatalf("公开榜单改变了原始名次 %d: %v", rank, card)
			}
			hasDetail := rank == 1 || (rank == 5 && withNewSummary)
			if card["detailAvailable"] != hasDetail {
				t.Fatalf("第 %d 名的详情可用状态错误: %v", rank, card)
			}
			if hasDetail || rank == 6 {
				if card["summaryPreview"] != summary.SummaryZh {
					t.Fatalf("第 %d 名应展示已就绪的导读: %v", rank, card)
				}
			} else if card["summaryPreview"] != nil || card["titleZh"] != nil || card["description"] != "GitHub 简介" {
				t.Fatalf("第 %d 名应回退到 GitHub 简介: %v", rank, card)
			}
		}
	}
	assertTrending(false)
	categories := request(t, router, "GET", public+"/categories", "", "", 200)["items"].([]any)
	for _, item := range categories {
		category := item.(map[string]any)
		if category["slug"] == "mcp" && category["count"] != float64(1) {
			t.Fatalf("分类计数包含未发布仓库: %v", category)
		}
	}
	sources := request(t, router, "GET", public+"/trending/sources", "", "", 200)["items"].([]any)
	if len(sources) == 0 || len(sources[0].(map[string]any)) != 1 || sources[0].(map[string]any)["sourceKey"] == nil {
		t.Fatalf("公开榜单来源只能返回标识: %v", sources)
	}
	for _, path := range []string{"/setup/status", "/system/settings", "/system/discovery-rules", "/system/jobs", "/topics", "/jobs/1", "/repos/1"} {
		request(t, router, "GET", "/api/v1"+path, "", "", 401)
	}
	seedReadySummary(t, db, 5, now)
	request(t, router, "GET", public+"/repos/by-name/example/5", "", "", 200)
	request(t, router, "GET", public+"/repos/5/trend", "", "", 200)
	for _, path := range []string{"/repos", "/discovery"} {
		page := request(t, router, "GET", public+path, "", "", 200)["items"].([]any)
		if len(page) != 2 || !slices.ContainsFunc(page, func(raw any) bool { return raw.(map[string]any)["id"] == float64(5) }) {
			t.Fatalf("导读准备完成后应进入 %s: %v", path, page)
		}
	}
	assertTrending(true)
	for _, action := range []string{"refresh", "summary", "follow", "topic"} {
		request(t, router, "POST", "/api/v1/repos/1/"+action, "{}", "", 401)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("POST", public+"/repos/1/"+action, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("公开接口注册了写操作 %s: %d", action, rec.Code)
		}
	}
}

// seedPurposeTopics 为 API 用例显式保存经过新协议审核的技术用途词。
func seedPurposeTopics(t *testing.T, db *gorm.DB, id int64, topics string) {
	t.Helper()
	row := storage.RepoAITopics{RepoID: id, TopicsJSON: topics, PurposeVersion: storage.TechnicalPurposeVersion}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}
