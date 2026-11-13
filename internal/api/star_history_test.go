package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// 两种口径从 API、列表筛选到撤回边界保持独立。
func TestStarHistoryIsSeparateFromLocalGrowth(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	yesterday := time.Now().UTC().Truncate(24*time.Hour).UnixMilli() - metrics.Day
	repo := storage.Repository{RepoID: 81, Owner: "owner", Name: "history", FullName: "owner/history",
		NameIsCurrent: true, TopicsJSON: "[]", Grade: "C", State: consts.StatusActive, LastSuccessAt: &now}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	seedReadySummary(t, db, repo.RepoID, now)
	for _, row := range []any{
		&storage.RepoStarHistoryWeek{RepoID: repo.RepoID, WeekTimestamp: yesterday, DaysJSON: `[120,0,0,0,0,0,0]`, FetchedAt: now},
		&storage.RepoStarHistoryStatus{RepoID: repo.RepoID, FetchedAt: now, BreakoutAt: new(yesterday + metrics.Day),
			BreakoutJSON: `{"source":"github_daily_stars","value":120}`, LastErrorCode: new("github_secondary_limit")},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	public := "/api/v1/public/repos"
	history := request(t, router, "GET", public+"/81/star-history?days=2", "", "", 200)
	days := history["days"].([]any)
	if days[0].(map[string]any)["state"] != "complete" || days[1].(map[string]any)["state"] != "partial" ||
		history["breakout"].(map[string]any)["source"] != metrics.BreakoutGitHub || history["errorCode"] != nil {
		t.Fatalf("官方日计数或公开边界错误: %+v", history)
	}
	admin := request(t, router, "GET", "/api/v1/repos/81/star-history?days=2", "", "admin", 200)
	if admin["errorCode"] != "github_secondary_limit" {
		t.Fatalf("后台未保留历史采集错误: %+v", admin)
	}
	trend := request(t, router, "GET", public+"/81/trend?range=24h", "", "", 200)
	detail := request(t, router, "GET", public+"/by-name/owner/history", "", "", 200)
	if len(trend["points"].([]any)) != 0 || detail["growth24h"].(map[string]any)["quality"] != metrics.Insufficient {
		t.Fatalf("官方日计数不能变成本地快照或精确净增: %+v %+v", trend, detail["growth24h"])
	}
	if items := request(t, router, "GET", public+"?breakout=github", "", "", 200)["items"].([]any); len(items) != 1 {
		t.Fatalf("应命中 GitHub 待确认筛选: %+v", items)
	}
	windows, _ := json.Marshal(map[string]metrics.Window{consts.Window24h: {Quality: metrics.Exact,
		Delta: new(int64(510)), StartAt: new(now - metrics.Day), EndAt: &now}})
	local := storage.LatestMetric{RepoID: repo.RepoID, WindowDetailsJSON: string(windows), CalculatedAt: now, DataQuality: metrics.Exact,
		BreakoutJSON: `{"source":"local_net_24h","value":510}`}
	if err := db.Create(&local).Error; err != nil {
		t.Fatal(err)
	}
	if items := request(t, router, "GET", public+"?breakout=github", "", "", 200)["items"].([]any); len(items) != 0 {
		t.Fatalf("已由本地确认的仓库不能保留待确认标记: %+v", items)
	}
	if items := request(t, router, "GET", public+"?breakout=local", "", "", 200)["items"].([]any); len(items) != 1 {
		t.Fatalf("本地爆发筛选未命中: %+v", items)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, public+"/81/star-history", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("公开端不能提交回补: %d", response.Code)
	}
	request(t, router, "PATCH", "/api/v1/repos/81/block", `{"blocked":true}`, "admin", 200)
	request(t, router, "GET", public+"/81/star-history", "", "", 404)
}
