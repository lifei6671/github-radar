package api

import (
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/storage"
)

// 验收后台权限、主扫描与补扫记录、唯一项目数和连续失败提示。
func TestDiscoveryRunsReportCoverageAndAlerts(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	rule := storage.DiscoveryRule{SourceType: "search", SourceKey: "early-test", Query: "archived:false",
		AgeWindow: "0-3", Enabled: true, IntervalMinutes: 120, NextRunAt: now}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 3; i++ {
		run := storage.DiscoveryRuleRun{RuleID: rule.ID, SourceType: rule.SourceType, SourceKey: rule.SourceKey, Pass: "primary",
			PlannedAt: now - i*600_000, StartedAt: now - i*600_000 + 2000, FinishedAt: now - i*600_000 + 3000,
			Status: "dead", UpstreamTotal: new(1040), PlannedShards: 2, CompletedShards: 1, ErrorCode: new("github_incomplete_search")}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			for _, hit := range []storage.DiscoveryRuleRunRepo{
				{RunID: run.ID, RepoID: 3, NewlyDiscovered: true}, {RunID: run.ID, RepoID: 8},
			} {
				if err := db.Create(&hit).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	path := "/api/v1/system/discovery-runs?sourceType=search&sourceKey=early-test"
	request(t, router, "GET", path, "", "", 401)
	view := request(t, router, "GET", path, "", "admin", 200)
	items := view["items"].([]any)
	first := items[0].(map[string]any)
	alerts := view["alerts"].([]any)
	if len(items) != 3 || first["uniqueRepos"] != float64(2) || first["newRepos"] != float64(1) ||
		first["complete"] != false || first["delayMs"] != float64(2000) || first["upstreamTotal"] != float64(1040) ||
		len(alerts) != 1 || alerts[0].(map[string]any)["reason"] != "consecutive_failures" {
		t.Fatalf("后台运行记录或异常错误: %+v", view)
	}
}
