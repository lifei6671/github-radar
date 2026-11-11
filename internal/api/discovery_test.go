package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/storage"
)

func TestDiscoverySelectsSortsAndPagesOnServer(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// 同一分钟内的信号落在同一个北京时间日期，便于验证先比分再比时间。
	anchor := now.Add(-15 * time.Minute).Truncate(time.Minute).UnixMilli()
	older := now.AddDate(0, 0, -14).UnixMilli()
	stale := now.Add(-50 * time.Hour).UnixMilli()
	recentSample := anchor + 30_000
	score := func(value float64) *float64 { return &value }
	cases := []struct {
		grade, state, quality string
		discovered, sampled   int64
		endAt, delta          int64
		hotness               *float64
		rank                  int
		blocked, oldBatch     bool
	}{
		{grade: "S", discovered: anchor + 25_000, sampled: recentSample, hotness: score(50)},                                    // 1 最近发现
		{grade: "B", discovered: older, sampled: recentSample, quality: "exact", endAt: anchor, delta: 200, hotness: score(85)}, // 2 精确增长
		{grade: "C", discovered: older, rank: 2}, // 3 上榜，缺少采样
		{grade: "A", discovered: anchor + 5_000, sampled: recentSample, quality: "exact", endAt: anchor + 10_000, delta: 220, hotness: score(85), rank: 1}, // 4 三重命中
		{grade: "B", discovered: anchor, sampled: recentSample, quality: "estimated", endAt: anchor, delta: 500, hotness: score(100)},                      // 5 估算增长
		{grade: "S", discovered: older, sampled: recentSample},                                                                                             // 6 等级高但发现时间过期
		{grade: "C", discovered: older, sampled: recentSample, quality: "exact", endAt: stale, delta: 500},                                                 // 7 过期的增长
		{grade: "S", state: "candidate", discovered: anchor, sampled: recentSample, rank: 3},                                                               // 8 尚未监控
		{grade: "S", discovered: anchor, sampled: recentSample, rank: 4, blocked: true},                                                                    // 9 已屏蔽
		{grade: "C", discovered: older, rank: 11},                                                                                                          // 10 未达榜单名次门槛
		{grade: "C", discovered: older, rank: 1, oldBatch: true},                                                                                           // 11 只出现在旧榜
		{grade: "A", discovered: anchor, sampled: stale},                                                                                                   // 12 采样已过期
		{grade: "C", discovered: older, sampled: recentSample, quality: "exact", endAt: anchor, delta: 199},                                                // 13 未达增长门槛
		{grade: "C", discovered: older, rank: 10},                                                                                                          // 14 上榜名次边界
		{grade: "A", discovered: now.Add(-52 * time.Hour).UnixMilli(), sampled: recentSample, hotness: score(99)},                                          // 15 日期优先于热度
		{grade: "C", discovered: older, sampled: recentSample, quality: "exact", endAt: now.Add(-2 * time.Hour).UnixMilli(), delta: 400},                   // 16 缩短增长窗口后不入选
	}
	batchAt := anchor + 20_000
	oldSlot := batchAt - int64(time.Hour/time.Millisecond)
	for i, fixture := range cases {
		id := int64(i + 1)
		name := fmt.Sprint(id)
		state := fixture.state
		if state == "" {
			state = "active"
		}
		repo := storage.Repository{RepoID: id, Owner: "discovery", Name: name, FullName: "discovery/" + name,
			HTMLURL: "https://github.com/discovery/" + name, NameIsCurrent: true, TopicsJSON: `["mcp"]`,
			State: state, Blocked: fixture.blocked, Grade: fixture.grade, DiscoveredAt: fixture.discovered,
			LastDiscoveredAt: fixture.discovered, UpdatedAt: anchor, NextFetchAt: anchor}
		if id == 3 {
			repo.TopicsJSON = "[]"
		}
		if fixture.sampled != 0 {
			repo.LastSuccessAt = &fixture.sampled
		}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		if fixture.quality != "" || fixture.hotness != nil {
			details := fmt.Sprintf(`{"24h":{"quality":%q,"endAt":%d}}`, fixture.quality, fixture.endAt)
			row := storage.LatestMetric{RepoID: id, Delta24h: &fixture.delta, HotnessScore: fixture.hotness,
				WindowDetailsJSON: details, DataQuality: "exact", CalculatedAt: anchor}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
		if fixture.rank != 0 {
			slot := batchAt
			if fixture.oldBatch {
				slot = oldSlot
			}
			row := storage.TrendingObservation{RepoID: id, SourceKey: "all", Window: "daily", ObservedSlot: slot, Rank: fixture.rank}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	marker := storage.MaintenanceState{Key: "trending:all:daily",
		Value: fmt.Sprintf(`{"observedSlot":%d,"observedAt":%d,"size":6}`, batchAt, batchAt), UpdatedAt: batchAt}
	if err := db.Create(&marker).Error; err != nil {
		t.Fatal(err)
	}

	const discovery = "/api/v1/public/discovery"
	ids := func(body map[string]any) []int {
		t.Helper()
		var out []int
		for _, raw := range body["items"].([]any) {
			out = append(out, int(raw.(map[string]any)["id"].(float64)))
		}
		return out
	}
	all := request(t, router, "GET", discovery, "", "", 200)
	want := []int{4, 2, 1, 3, 14, 16, 15}
	if got := ids(all); !slices.Equal(got, want) {
		t.Fatalf("精选筛选或排序错误: %v，期望 %v", got, want)
	}
	multi := all["items"].([]any)[0].(map[string]any)
	if multi["discoveryAt"] != iso(batchAt) || multi["discoveredAt"] != iso(anchor+5_000) {
		t.Fatalf("多条信号应选最新时间，保留首次发现时间: %v", multi)
	}
	categorized := request(t, router, "GET", discovery+"?category=mcp", "", "", 200)
	if got := ids(categorized); !slices.Equal(got, []int{4, 2, 1, 14, 16, 15}) {
		t.Fatalf("精选分类筛选错误: %v", got)
	}
	hot := request(t, router, "GET", discovery+"?sort=hotness", "", "", 200)
	if got := ids(hot); !slices.Equal(got, []int{15, 2, 4, 1}) {
		t.Fatalf("当前热点应从入选项目按热度排序: %v", got)
	}
	var paged []int
	path := discovery + "?limit=2"
	for range 4 {
		page := request(t, router, "GET", path, "", "", 200)
		paged = append(paged, ids(page)...)
		if page["nextCursor"] == nil {
			break
		}
		cursor := url.QueryEscape(page["nextCursor"].(string))
		request(t, router, "GET", discovery+"?category=mcp&cursor="+cursor, "", "", 400)
		path = discovery + "?limit=2&cursor=" + cursor
	}
	if !slices.Equal(paged, want) {
		t.Fatalf("分页应保留顺序且不重复: %v", paged)
	}
	complete := request(t, router, "GET", "/api/v1/public/repos", "", "", 200)
	if got := ids(complete); !slices.Contains(got, 5) || !slices.Contains(got, 11) || !slices.Contains(got, 13) {
		t.Fatalf("全部项目不应使用精选门槛: %v", got)
	}

	// 调整后台门槛后下一次请求立即使用新设置。
	const adminPath = "/api/v1/system/discovery-tabs"
	settings := request(t, router, "GET", adminPath, "", "admin", 200)
	selection := settings["selection"].(map[string]any)
	selection["trendingTop"] = 1
	selection["recentDays"] = 1
	selection["growthSampleHours"] = 1
	body := map[string]any{"revision": settings["revision"], "items": settings["items"], "selection": selection}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, "PUT", adminPath, string(encoded), "admin", 200)
	updated := request(t, router, "GET", discovery, "", "", 200)
	if got := ids(updated); !slices.Equal(got, []int{4, 2, 1}) {
		t.Fatalf("后台筛选门槛未立即生效: %v", got)
	}
	selection["trendingHours"] = 0
	body["revision"] = settings["revision"].(float64) + 1
	encoded, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, "PUT", adminPath, string(encoded), "admin", 400)
}
