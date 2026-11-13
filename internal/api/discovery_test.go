package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/discoverystore"
)

func TestDiscoveryRotationBalancesExposureWithoutChangingRanks(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	local := now.In(time.FixedZone("Beijing", 8*60*60))
	start := time.Date(local.Year(), local.Month(), local.Day()-3, 8, 0, 0, 0, local.Location())
	old := start.AddDate(0, 0, -30).UnixMilli()
	for _, repo := range []storage.Repository{
		{RepoID: 1, Name: "old", DiscoveredAt: old},
		{RepoID: 2, Name: "new", DiscoveredAt: start.Add(-time.Minute).UnixMilli()},
	} {
		repo.Owner, repo.FullName = "rotation", "rotation/"+repo.Name
		repo.NameIsCurrent, repo.TopicsJSON, repo.State, repo.Grade = true, "[]", "active", "A"
		repo.GitHubCreatedAt = &old
		repo.LastSuccessAt = new(start.UnixMilli())
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		summary := readySummaryFixture(repo.RepoID, start.UnixMilli())
		if err := db.Create(&summary).Error; err != nil {
			t.Fatal(err)
		}
	}
	updateSignals := func(at time.Time) {
		t.Helper()
		ms := at.UnixMilli()
		if err := db.Model(&storage.Repository{}).Where("repo_id IN (1,2)").Update("last_success_at", ms).Error; err != nil {
			t.Fatal(err)
		}
		for _, metric := range []storage.LatestMetric{
			{RepoID: 1, HotnessScore: new(90.0), Delta24h: new(int64(600))},
			{RepoID: 2, HotnessScore: new(70.0), Delta24h: new(int64(220))},
		} {
			metric.CalculatedAt = ms
			metric.WindowDetailsJSON = fmt.Sprintf(`{"24h":{"quality":"exact","endAt":%d}}`, ms)
			metric.DataQuality = "exact"
			if err := db.Save(&metric).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	type match struct {
		RepoID         int64
		DiscoveryScore float64
	}
	discoveryAt := func(at time.Time) []match {
		t.Helper()
		matches, _, err := discoverystore.Matches(db.WithContext(t.Context()), at)
		if err != nil {
			t.Fatal(err)
		}
		var result []match
		if err := db.Table("(?) AS eligible", matches).Select("repo_id, discovery_score").
			Where(discoverystore.TimeSQL + " > 0").Order("discovery_score DESC").Scan(&result).Error; err != nil {
			t.Fatal(err)
		}
		return result
	}
	record := func(at time.Time) {
		t.Helper()
		if err := discoverystore.Record(t.Context(), db, at); err != nil {
			t.Fatal(err)
		}
	}
	assertStreak := func(at time.Time, streak int) {
		t.Helper()
		var row struct {
			LastDay int64
			Streak  int
		}
		if err := db.Table("discovery_rotation").Take(&row, "repo_id=1").Error; err != nil ||
			row.LastDay != discoverystore.BeijingDay(at.UnixMilli()) || row.Streak != streak {
			t.Fatalf("连续入选计数错误: %+v %v", row, err)
		}
	}
	for i := range 3 {
		at := start.AddDate(0, 0, i)
		updateSignals(at)
		before := discoveryAt(at)
		if len(before) != 2 || before[0].RepoID != 2 || math.Abs(before[1].DiscoveryScore-(63-10*float64(i))) > 1e-9 {
			t.Fatalf("新项目加权或连续入选扣分错误，第 %d 天: %+v", i+1, before)
		}
		record(at)
		record(at.Add(time.Second))
		assertStreak(at, i+1)
		if got := discoveryAt(at); !slices.Equal(got, before) {
			t.Fatalf("同日记录不应改变展示分: 记录前=%+v 记录后=%+v", before, got)
		}
	}

	updateSignals(now)
	// 已有高热度项目即使再次登上 Trending，仍遵守发现页的连续曝光冷却。
	batchAt := now.UnixMilli()
	if err := db.Create(&storage.MaintenanceState{Key: "trending:all:daily",
		Value: fmt.Sprintf(`{"observedSlot":%d,"observedAt":%d,"size":1}`, batchAt, batchAt), UpdatedAt: batchAt}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&storage.TrendingObservation{RepoID: 1, SourceKey: "all", Window: "daily", ObservedSlot: batchAt, Rank: 1}).Error; err != nil {
		t.Fatal(err)
	}
	const root = "/api/v1/public"
	for _, path := range []string{"/discovery", "/discovery?sort=hotness"} {
		items := request(t, router, "GET", root+path, "", "", 200)["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["id"] != float64(2) {
			t.Fatalf("发现页应留出旧项目的展示位: %s %+v", path, items)
		}
	}
	for _, sort := range []string{"growth24h", "hotness"} {
		items := request(t, router, "GET", root+"/repos?sort="+sort, "", "", 200)["items"].([]any)
		first := items[0].(map[string]any)
		if first["id"] != float64(1) || first["selected"] != false || first["hotnessScore"] != float64(90) {
			t.Fatalf("热点榜应保留真实排序和热度，精选标记随冷却撤下: %s %+v", sort, first)
		}
	}
	const settingsPath = "/api/v1/system/discovery-tabs"
	ranking := request(t, router, "GET", settingsPath, "", "admin", 200)
	saveRanking := func(heat, fresh, streak, cooldown int) {
		t.Helper()
		body := map[string]any{"items": ranking["items"], "revision": ranking["revision"],
			"ranking": settings.DiscoveryRanking{HeatWeight: heat, FreshnessWeight: fresh, ConsecutiveDays: streak, CooldownDays: cooldown}}
		ranking = request(t, router, "PUT", settingsPath, jsonBody(t, body), "admin", 200)
	}
	saveRanking(100, 0, 3, 0)
	weighted := request(t, router, "GET", root+"/discovery?limit=1", "", "", 200)
	if weighted["items"].([]any)[0].(map[string]any)["id"] != float64(2) {
		t.Fatalf("日期应优先于展示分: %+v", weighted)
	}
	oldCursor := url.QueryEscape(weighted["nextCursor"].(string))
	olderPage := request(t, router, "GET", root+"/discovery?limit=1&cursor="+oldCursor, "", "", 200)
	if items := olderPage["items"].([]any); len(items) != 1 || items[0].(map[string]any)["id"] != float64(1) {
		t.Fatalf("关闭冷却后，旧项目应在首次发现当天重新出现: %+v", olderPage)
	}
	saveRanking(0, 100, 5, 1)
	weighted = request(t, router, "GET", root+"/discovery", "", "", 200)
	if items := weighted["items"].([]any); len(items) != 2 || items[0].(map[string]any)["id"] != float64(2) {
		t.Fatalf("新权重和上榜天数应在同一进程立即生效: %+v", weighted)
	}
	request(t, router, "GET", root+"/discovery?cursor="+oldCursor, "", "", 400)
	for _, sort := range []string{"growth24h", "hotness"} {
		items := request(t, router, "GET", root+"/repos?sort="+sort, "", "", 200)["items"].([]any)
		if first := items[0].(map[string]any); first["id"] != float64(1) || first["hotnessScore"] != float64(90) {
			t.Fatalf("保存展示权重不能改变热点榜原始排名: %s %+v", sort, first)
		}
	}
	recordedDay := start.AddDate(0, 0, 2)
	updateSignals(recordedDay)
	isOld := func(item match) bool { return item.RepoID == 1 }
	if !slices.ContainsFunc(discoveryAt(recordedDay), isOld) {
		t.Fatal("较高门槛应允许已记入历史的第三天继续展示")
	}
	saveRanking(70, 30, 2, 2)
	if slices.ContainsFunc(discoveryAt(recordedDay), isOld) {
		t.Fatal("降低连续上榜天数时，当天已记入历史的老项目也应立即进入冷却")
	}
	saveRanking(70, 30, 3, 2)
	for i := range 2 {
		at := now.AddDate(0, 0, i)
		updateSignals(at)
		record(at)
		assertStreak(start.AddDate(0, 0, 2), 3)
		if got := discoveryAt(at); len(got) != 1 || got[0].RepoID != 2 {
			t.Fatalf("冷却第 %d 天应只展示新项目: %+v", i+1, got)
		}
	}
	for _, days := range []int{2, 4} {
		at := now.AddDate(0, 0, days)
		updateSignals(at)
		record(at)
		assertStreak(at, 1)
		if got := discoveryAt(at); !slices.ContainsFunc(got, func(item match) bool { return item.RepoID == 1 }) {
			t.Fatalf("冷却结束后应按当前信号重新入选: %+v", got)
		}
	}
}

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
		{grade: "S", discovered: anchor + 25_000, sampled: recentSample, hotness: score(50)},                                                               // 1 最近发现
		{grade: "B", discovered: older, sampled: recentSample, quality: "exact", endAt: anchor, delta: 200, hotness: score(85)},                            // 2 精确增长
		{grade: "C", discovered: older, sampled: recentSample, rank: 2},                                                                                    // 3 老项目再次上榜
		{grade: "A", discovered: anchor + 5_000, sampled: recentSample, quality: "exact", endAt: anchor + 10_000, delta: 220, hotness: score(85), rank: 1}, // 4 三重命中
		{grade: "B", discovered: anchor, sampled: recentSample, quality: "estimated", endAt: anchor, delta: 500, hotness: score(100)},                      // 5 估算增长
		{grade: "S", discovered: older, sampled: recentSample},                                                                                             // 6 等级高但发现时间过期
		{grade: "C", discovered: older, sampled: recentSample, quality: "exact", endAt: stale, delta: 500},                                                 // 7 过期的增长
		{grade: "S", state: "candidate", discovered: anchor, sampled: recentSample, rank: 3},                                                               // 8 尚未监控
		{grade: "S", discovered: anchor, sampled: recentSample, rank: 4, blocked: true},                                                                    // 9 已屏蔽
		{grade: "C", discovered: older, sampled: recentSample, rank: 11},                                                                                   // 10 未达榜单名次门槛
		{grade: "C", discovered: older, sampled: recentSample, rank: 1, oldBatch: true},                                                                    // 11 只出现在旧榜
		{grade: "A", discovered: anchor, sampled: stale},                                                                                                   // 12 采样已过期
		{grade: "C", discovered: older, sampled: recentSample, quality: "exact", endAt: anchor, delta: 199},                                                // 13 未达增长门槛
		{grade: "C", discovered: older, sampled: recentSample, rank: 10},                                                                                   // 14 上榜名次边界
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
	reasons := map[int]string{
		1: "应用开发者可借鉴它的统一接口来复用工具调用流程。",
		4: "终端工具作者可参考它用交互式选择器组织脚本输入的方式。",
		5: "维护旧代码的团队可以用它定位未使用的导入和变量。",
	}
	for i := range cases {
		id := i + 1
		row := readySummaryFixture(int64(id), anchor)
		if reason, exists := reasons[id]; exists {
			row.RecommendationReason = new(reason)
			row.ValueTagSlugsJSON = `["tool-calling","workflow"]`
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	const discovery = "/api/v1/public/discovery"
	for _, id := range []int64{1, 2, 4, 5, 14, 15, 16} {
		seedPurposeTopics(t, db, id, `["mcp-server"]`)
	}
	ids := func(body map[string]any) []int {
		t.Helper()
		var out []int
		for _, raw := range body["items"].([]any) {
			out = append(out, int(raw.(map[string]any)["id"].(float64)))
		}
		return out
	}
	all := request(t, router, "GET", discovery, "", "", 200)
	want := []int{4, 1, 15, 2, 3, 14, 16}
	if got := ids(all); !slices.Equal(got, want) {
		t.Fatalf("精选筛选或排序错误: %v，期望 %v", got, want)
	}
	multi := all["items"].([]any)[0].(map[string]any)
	if multi["discoveryAt"] != iso(batchAt) || multi["discoveredAt"] != iso(anchor+5_000) {
		t.Fatalf("多条信号应选最新时间，保留首次发现时间: %v", multi)
	}
	categorized := request(t, router, "GET", discovery+"?category=mcp", "", "", 200)
	if got := ids(categorized); !slices.Equal(got, []int{4, 1, 15, 2, 14, 16}) {
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
		if path == discovery+"?limit=2" {
			raw, err := base64.RawURLEncoding.DecodeString(page["nextCursor"].(string))
			var previousDay discoveryCursor
			if err != nil || json.Unmarshal(raw, &previousDay) != nil {
				t.Fatalf("无法读取发现页游标: %v", err)
			}
			previousDay.Day--
			data, err := json.Marshal(previousDay)
			if err != nil {
				t.Fatal(err)
			}
			request(t, router, "GET", discovery+"?cursor="+base64.RawURLEncoding.EncodeToString(data), "", "", 400)
			previousDay.Day++
			previousDay.Version = 2 // 旧游标的 At 使用最新命中时间。
			data, err = json.Marshal(previousDay)
			if err != nil {
				t.Fatal(err)
			}
			request(t, router, "GET", discovery+"?cursor="+base64.RawURLEncoding.EncodeToString(data), "", "", 400)
		}
		path = discovery + "?limit=2&cursor=" + cursor
	}
	if !slices.Equal(paged, want) {
		t.Fatalf("分页应保留顺序且不重复: %v", paged)
	}
	complete := request(t, router, "GET", "/api/v1/public/repos", "", "", 200)
	if got := ids(complete); !slices.Contains(got, 5) || !slices.Contains(got, 11) || !slices.Contains(got, 13) {
		t.Fatalf("全部项目不应使用精选门槛: %v", got)
	}
	checkSelection := func(result map[string]any, expected []int) {
		t.Helper()
		for _, raw := range result["items"].([]any) {
			item := raw.(map[string]any)
			id := int(item["id"].(float64))
			shouldSelect := slices.Contains(expected, id)
			var wantReason any
			if reason, ok := reasons[id]; shouldSelect && ok {
				wantReason = reason
			}
			_, hasRuleReason := item["selectionReason"]
			if item["selected"] != shouldSelect || item["recommendationReason"] != wantReason || hasRuleReason {
				t.Fatalf("项目 %d 的精选状态或 AI 推荐理由不匹配: %v", id, item)
			}
		}
	}
	checkSelection(all, want)
	checkSelection(complete, want)
	tagged := request(t, router, "GET", "/api/v1/public/repos?tag="+url.QueryEscape("工具调用"), "", "", 200)
	gotTagged := ids(tagged)
	slices.Sort(gotTagged)
	if !slices.Equal(gotTagged, []int{1, 4, 5}) {
		t.Fatalf("AI 标签应能从公开列表筛选: %v", gotTagged)
	}
	for _, raw := range tagged["items"].([]any) {
		item := raw.(map[string]any)
		if tags := item["valueTags"].([]any); !slices.Equal(tags, []any{"模型工具调用", "工作流自动化"}) {
			t.Fatalf("公开列表应返回可用于筛选的 AI 标签: %v", item)
		}
	}
	search := request(t, router, "GET", "/api/v1/public/repos?q=discovery&sort=relevance", "", "", 200)
	checkSelection(search, want)

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
	if got := ids(updated); !slices.Equal(got, []int{4, 1, 2}) {
		t.Fatalf("后台筛选门槛未立即生效: %v", got)
	}
	checkSelection(request(t, router, "GET", "/api/v1/public/repos?category=mcp", "", "", 200), []int{4, 2, 1})
	selection["trendingHours"] = 0
	body["revision"] = settings["revision"].(float64) + 1
	encoded, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request(t, router, "PUT", adminPath, string(encoded), "admin", 400)
}
