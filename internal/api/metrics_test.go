package api

import (
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/maintenance"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestMetricSortingKeepsGlobalRankAndNullCursor(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, sort := range []string{"hotness", "growth24h", "growth7d"} {
		body := request(t, router, "GET", "/api/v1/repos?sort="+sort, "", "admin", 200)
		if body["updatedAt"] != nil {
			t.Fatalf("首次成功计算前没有指标发布时间: %+v", body)
		}
	}
	now := time.Now().UTC()
	marker := storage.MaintenanceState{Key: metrics.ComputeKey, Value: "1", UpdatedAt: now.UnixMilli()}
	if err := db.Create(&marker).Error; err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 4; id++ {
		name := fmt.Sprintf("repo-%d", id)
		repo := storage.Repository{RepoID: id, Owner: "owner", Name: name, FullName: "owner/" + name, NameIsCurrent: true,
			TopicsJSON: "[]", Grade: "A", State: "active", DiscoveredAt: now.UnixMilli()}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 1 和 2 热度相同。估算增量 900 不能排在精确的负增量前。
	for _, item := range []struct {
		id, delta int64
		quality   string
		heat      *float64
	}{
		{1, 900, "estimated", number(81.125)}, {2, -15, "exact", number(81.125)}, {3, 0, "insufficient", nil},
	} {
		row := storage.LatestMetric{RepoID: item.id, Delta24h: &item.delta, Delta7d: &item.delta, HotnessScore: item.heat, DataQuality: item.quality,
			CalculatedAt: now.UnixMilli(), WindowDetailsJSON: fmt.Sprintf(`{"24h":{"delta":%d,"quality":%q,"resolution":"raw"},"7d":{"delta":%d,"quality":%q,"resolution":"raw"}}`, item.delta, item.quality, item.delta, item.quality)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for sort, want := range map[string][]int{"hotness": {1, 2, 3, 4}, "growth24h": {2, 1, 3, 4}, "growth7d": {2, 1, 3, 4}} {
		cursor := ""
		for i, id := range want {
			path := "/api/v1/repos?limit=1&sort=" + sort + cursor
			body := request(t, router, "GET", path, "", "admin", 200)
			items := body["items"].([]any)
			if len(items) != 1 || items[0].(map[string]any)["id"] != float64(id) {
				t.Fatalf("%s 第 %d 页顺序错误: %+v", sort, i, body)
			}
			if i+1 < len(want) {
				cursor = "&cursor=" + url.QueryEscape(body["nextCursor"].(string))
			} else if body["nextCursor"] != nil {
				t.Fatal("末页不应继续分页")
			}
		}
	}
	body := request(t, router, "GET", "/api/v1/repos?sort=hotness&q=repo-2", "", "admin", 200)
	card := body["items"].([]any)[0].(map[string]any)
	if card["hotnessRank"] != float64(2) || card["metricsUpdatedAt"] != iso(now.UnixMilli()) ||
		card["growth24h"].(map[string]any)["delta"] != float64(-15) || len(card["sparkline"].([]any)) != 24 {
		t.Fatalf("筛选改变全局排名或破坏指标契约: %+v", card)
	}
	if body["updatedAt"] != iso(now.UnixMilli()) {
		t.Fatalf("列表应使用指标成功发布时间: %+v", body)
	}

	// 在排序与卡片读取之间发布新指标，第一轮响应仍须使用旧读快照。
	var published atomic.Bool
	next := now.Add(time.Minute).UnixMilli()
	callback := "test:publish_metrics"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "repositories" || len(tx.Statement.Joins) == 0 || !published.CompareAndSwap(false, true) {
			return
		}
		if err := db.Transaction(func(writer *gorm.DB) error {
			if err := writer.Model(&storage.LatestMetric{}).Where("repo_id=2").
				Updates(map[string]any{"hotness_score": 99.125, "calculated_at": next}).Error; err != nil {
				return err
			}
			return writer.Model(&marker).Update("updated_at", next).Error
		}); err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(callback)
	old := request(t, router, "GET", "/api/v1/repos?limit=1&sort=hotness", "", "admin", 200)
	first := old["items"].([]any)[0].(map[string]any)
	if !published.Load() || old["updatedAt"] != iso(now.UnixMilli()) || first["id"] != float64(1) ||
		first["hotnessRank"] != float64(1) || first["hotnessScore"] != 81.125 {
		t.Fatalf("同一页读取到了两轮指标: %+v", old)
	}
	current := request(t, router, "GET", "/api/v1/repos?limit=1&sort=hotness", "", "admin", 200)
	first = current["items"].([]any)[0].(map[string]any)
	if current["updatedAt"] != iso(next) || first["id"] != float64(2) || first["hotnessRank"] != float64(1) {
		t.Fatalf("新请求未读到已提交指标: %+v", current)
	}
}

func number(n float64) *float64 { return &n }

func TestTrendRetainsObservedTimesAfterRawCleanup(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	hour := now.Truncate(time.Hour).Add(-16 * 24 * time.Hour)
	repo := storage.Repository{RepoID: 1, Owner: "owner", Name: "repo", FullName: "owner/repo", NameIsCurrent: true,
		TopicsJSON: "[]", Grade: "A", State: "active", DiscoveredAt: hour.UnixMilli()}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	for i, minute := range []int{5, 55} {
		at := hour.Add(time.Duration(minute) * time.Minute).UnixMilli()
		s := storage.RepoSnapshot{RepoID: 1, SampleSlot: at, ObservedAt: at, Stars: 100 - i*20}
		if err := db.Create(&s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := maintenance.Rollup(t.Context(), db, cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Cleanup(t.Context(), db, cfg.Maintenance, now); err != nil {
		t.Fatal(err)
	}
	body := request(t, router, "GET", "/api/v1/repos/1/trend?range=30d", "", "admin", 200)
	points := body["points"].([]any)
	if body["resolution"] != "hourly" || len(points) != 30*24 || body["updatedAt"] != iso(hour.Add(55*time.Minute).UnixMilli()) {
		t.Fatal("小时趋势返回错误的粒度、网格或观测时间")
	}
	idx := int(hour.Sub(now.Truncate(time.Hour).Add(-30*24*time.Hour)) / time.Hour)
	hit, gap := points[idx].(map[string]any), points[idx+1].(map[string]any)
	if hit["stars"] != float64(80) || hit["delta"] != float64(-20) || hit["quality"] != "exact" ||
		hit["startAt"] != iso(hour.Add(5*time.Minute).UnixMilli()) || hit["endAt"] != iso(hour.Add(55*time.Minute).UnixMilli()) ||
		gap["stars"] != nil || gap["delta"] != nil || gap["quality"] != "insufficient" {
		t.Fatalf("点位或缺口被篡改: %+v %+v", hit, gap)
	}
	daily := request(t, router, "GET", "/api/v1/repos/1/trend?range=180d", "", "admin", 200)
	if daily["resolution"] != "daily" || len(daily["points"].([]any)) != 180 {
		t.Fatal("日趋势网格错误")
	}
	status := request(t, router, "GET", "/api/v1/system/storage", "", "admin", 200)
	counts := status["counts"].(map[string]any)
	if counts["raw"] != float64(0) || counts["hourly"] != float64(1) || counts["daily"] != float64(1) ||
		status["watermarks"].(map[string]any)["daily"] == nil {
		t.Fatalf("存储统计错误: %+v", status)
	}
	request(t, router, "POST", "/api/v1/system/maintenance/run", "", "", 401)
	request(t, router, "POST", "/api/v1/system/maintenance/run", "", "admin", 202)
	var queued storage.MaintenanceState
	if err := db.Take(&queued, "key='maintenance.request'").Error; err != nil {
		t.Fatal(err)
	}
}
