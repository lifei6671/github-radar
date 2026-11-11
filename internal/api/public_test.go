package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/storage"
)

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
	for id := int64(1); id <= 4; id++ {
		repo := storage.Repository{RepoID: id, Owner: "example", Name: fmt.Sprint(id), FullName: fmt.Sprintf("example/%d", id),
			NameIsCurrent: true, HTMLURL: fmt.Sprintf("https://github.com/example/%d", id), TopicsJSON: `["mcp"]`,
			State: "active", Grade: "A", DiscoveredAt: now + id, LastDiscoveredAt: now, NextFetchAt: now, UpdatedAt: now}
		switch id {
		case 1:
			repo.StarredByUser, repo.FollowedAt = true, &now
		case 2:
			repo.State = "unavailable"
		case 3:
			repo.FirstNotFoundAt = &now
		case 4:
			repo.State = "candidate"
		}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&storage.TrendingObservation{RepoID: id, SourceKey: "all", Window: "daily", ObservedSlot: slot, Rank: int(id)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	marker := storage.MaintenanceState{Key: "trending:all:daily",
		Value: fmt.Sprintf(`{"observedSlot":%d,"observedAt":%d,"size":4}`, slot, now), UpdatedAt: now}
	if err := db.Create(&marker).Error; err != nil {
		t.Fatal(err)
	}
	summary := storage.AISummary{RepoID: 1, SourceFingerprint: "readme:test", SourceQuality: "sufficient", SourceLanguage: "en",
		SummaryZh: "用于维护开发工作流。", FeaturesJSON: "[]", AudienceJSON: "[]", ValueTagsJSON: "[]",
		ModelProvider: "fixture", ModelName: "internal-model", PromptVersion: 1, SummaryStatus: "stale", GeneratedAt: now}
	if err := db.Create(&summary).Error; err != nil {
		t.Fatal(err)
	}
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
	if detail["summaryZh"] != summary.SummaryZh || detail["summaryStatus"] != "stale" {
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
	trending := request(t, router, "GET", public+"/trending", "", "", 200)["items"].([]any)
	if len(trending) != 2 || trending[1].(map[string]any)["id"] != float64(4) {
		t.Fatalf("公开榜单未正确过滤批次成员: %v", trending)
	}
	assertPublicCard(trending[0].(map[string]any))
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
	for _, action := range []string{"refresh", "summary", "follow", "topic"} {
		request(t, router, "POST", "/api/v1/repos/1/"+action, "{}", "", 401)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("POST", public+"/repos/1/"+action, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("公开接口注册了写操作 %s: %d", action, rec.Code)
		}
	}
}
