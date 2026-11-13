package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// 演算只读，变更记录只出现在已认证的仓库详情。
func TestObservationReplayAndPrivateHistory(t *testing.T) {
	router, db := collectionRouter(t)
	request(t, router, http.MethodPost, "/api/v1/setup/initialize", "", "admin", 200)
	now := time.Now().UnixMilli()
	repo := storage.Repository{RepoID: 99, Owner: "owner", Name: "observation", FullName: "owner/observation",
		State: consts.StatusActive, Grade: consts.GradeA, NameIsCurrent: true, TopicsJSON: "[]",
		HTMLURL: "https://github.com/owner/observation", LastSuccessAt: &now, DiscoveredAt: now}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	summary := storage.AISummary{RepoID: repo.RepoID, SummaryStatus: consts.StatusReady, SummaryZh: "项目导读",
		SourceQuality: "sufficient", SourceLanguage: "zh", FeaturesJSON: "[]", AudienceJSON: "[]", ValueTagsJSON: "[]"}
	if err := db.Create(&summary).Error; err != nil {
		t.Fatal(err)
	}
	if err := storage.RecordObservation(db, repo, consts.StatusActive, consts.GradeB, "slowed_48h", now,
		metrics.ObservationEvidence{Exact24h: new(int64(12))}); err != nil {
		t.Fatal(err)
	}
	const replay = "/api/v1/system/observation-replay"
	request(t, router, http.MethodGet, replay, "", "", 401)
	result := request(t, router, http.MethodGet, replay, "", "admin", 200)
	if scheduler := result["scheduler"].(map[string]any); scheduler["observationPool"] != true || scheduler["maxCooling"] != float64(1000) {
		t.Fatalf("演算返回参数错误: %+v", scheduler)
	}
	detail := request(t, router, http.MethodGet, "/api/v1/repos/99", "", "admin", 200)
	records := detail["observationHistory"].([]any)
	if detail["grade"] != "A" || len(records) != 1 || records[0].(map[string]any)["reason"] != "slowed_48h" {
		t.Fatalf("演算不应写档位，历史必须可读: %+v", detail)
	}
	public := request(t, router, http.MethodGet, "/api/v1/public/repos/by-name/owner/observation", "", "", 200)
	if _, ok := public["observationHistory"]; ok {
		t.Fatal("公开详情泄漏了后台监控记录")
	}
}
