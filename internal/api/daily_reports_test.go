package api

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/reports"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/reportstore"
	"gorm.io/gorm"
)

func TestReportEvidenceAndEditorialPublication(t *testing.T) {
	router, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/public/daily-reports"
	empty := request(t, router, "GET", path, "", "", 200)
	if empty["report"] != nil || len(empty["archive"].([]any)) != 0 {
		t.Fatalf("空库不能显示日报: %v", empty)
	}
	for _, kind := range []string{"weekly", "monthly"} {
		empty = request(t, router, "GET", path+"?kind="+kind, "", "", 200)
		if empty["report"] != nil || empty["total"] != float64(0) {
			t.Fatalf("空库不能显示周期报告: %v", empty)
		}
	}
	now := time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)
	addRepo := func(id int64, at time.Time, ready bool) {
		name := fmt.Sprint(id)
		observed := at.UnixMilli()
		repo := storage.Repository{RepoID: id, Owner: "report", Name: name, FullName: "report/" + name,
			HTMLURL: "https://github.com/report/" + name, NameIsCurrent: true,
			State: "active", Grade: "A", TopicsJSON: "[]", DiscoveredAt: observed, LastSuccessAt: &observed}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		if ready {
			summary := readySummaryFixture(id, observed)
			summary.SummaryZh = "保存的 AI 导读。\n第二段用于验证完整展示。"
			if err := db.Create(&summary).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	addRepo(1, now.Add(-4*time.Hour), true)
	addRepo(2, now.Add(-3*time.Hour), true)
	addRepo(3, now.Add(-2*time.Hour), false)
	addBatch := func(at time.Time, source string, entries ...storage.TrendingObservation) {
		batch := storage.TrendingBatchRecord{SourceKey: source, Window: "daily", ObservedAt: at.UnixMilli(), ObservedSlot: at.Truncate(5 * time.Minute).UnixMilli()}
		if err := db.Create(&batch).Error; err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			entry.SourceKey, entry.Window, entry.ObservedSlot = source, batch.Window, batch.ObservedSlot
			if err := db.Create(&entry).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	addBatch(now.Add(-25*time.Hour), "all", storage.TrendingObservation{RepoID: 1, Rank: 5})
	addBatch(now.Add(-time.Hour+15*time.Second), "all", storage.TrendingObservation{RepoID: 1, Rank: 2}, storage.TrendingObservation{RepoID: 3, Rank: 1})
	addBatch(now.Add(-time.Minute), "language:go", storage.TrendingObservation{RepoID: 2, Rank: 1})
	searchAt := now.Add(-5 * time.Hour).UnixMilli()
	if err := db.Create(&storage.SearchObservation{RepoID: 2, SourceKey: "fixtures", ObservedAt: searchAt, ObservedSlot: searchAt}).Error; err != nil {
		t.Fatal(err)
	}
	store := reportstore.Store{DB: db}
	generate := func(at time.Time) {
		if err := reports.Generate(t.Context(), store, at); err != nil {
			t.Fatal(err)
		}
	}
	generate(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	readDaily := func(date string) (*reportstore.Edition, []reportstore.PublicItem) {
		t.Helper()
		edition, items, _, _, err := store.Read(t.Context(), date, "", 25)
		if err != nil || edition == nil {
			t.Fatalf("读取日报证据: %+v %v", edition, err)
		}
		return edition, items
	}
	first, items := readDaily("")
	if first.IssueDate != "2026-10-07" || first.ObservedFrom != searchAt ||
		first.ObservedTo != now.Add(-time.Hour+15*time.Second).UnixMilli() || len(items) != 2 {
		t.Fatalf("首期证据应记录实际观测并跳过未就绪仓库: %+v %+v", first, items)
	}
	// 模拟旧版已归档的 08:00 截稿窗口。重建时应复用刊号，并排除次日观测。
	legacyEnd := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC).UnixMilli()
	if err := db.Model(first).Update("window_end", legacyEnd).Error; err != nil {
		t.Fatal(err)
	}
	addRepo(4, now.Add(4*time.Hour), true)
	generate(time.UnixMilli(legacyEnd).Add(time.Minute))
	corrected, correctedItems := readDaily(first.IssueDate)
	_, expectedEnd, _ := reports.Daily.Bounds(first.IssueDate)
	if corrected.IssueNumber != first.IssueNumber || corrected.WindowEnd != expectedEnd.UnixMilli() ||
		len(correctedItems) != 2 || corrected.ObservedTo >= expectedEnd.UnixMilli() {
		t.Fatalf("重建旧日报时不得混入次日证据或改变期号: %+v %+v", corrected, correctedItems)
	}
	trend, newProject := items[0], items[1]
	if trend.RepoID != 1 || trend.Rank == nil || *trend.Rank != 2 || trend.PreviousRank == nil ||
		*trend.PreviousRank != 5 || trend.Section != "trending" || newProject.RepoID != 2 || newProject.Section != "discoveries" {
		t.Fatalf("Trending 与新发现应分开去重: %+v", items)
	}
	if unpublished := request(t, router, "GET", path, "", "", 200); unpublished["report"] != nil || unpublished["total"] != float64(0) {
		t.Fatalf("证据就绪但未组稿时不能公开: %v", unpublished)
	}
	if err := db.Model(&storage.AISummary{}).Where("repo_id=1").Update("summary_zh", "之后更新的摘要").Error; err != nil {
		t.Fatal(err)
	}
	seedReadySummary(t, db, 3, now.UnixMilli())
	generate(now.Add(5 * time.Hour))
	generate(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))
	latest, latestItems := readDaily("")
	if latest.IssueDate != "2026-10-08" || len(latestItems) != 1 || latestItems[0].RepoID != 4 || latest.IssueNumber != 3 {
		t.Fatalf("次日不能重复采纳首期观测: %+v %+v", latest, latestItems)
	}
	_, saved := readDaily("2026-10-07")
	if saved[0].SummaryZh != trend.SummaryZh {
		t.Fatal("重跑应保持首期导读证据快照")
	}
	prior, _ := readDaily("2026-10-06")
	after, _ := readDaily("")
	if prior.IssueNumber != 1 || after.IssueNumber != 3 {
		t.Fatalf("补刊不能重排期号: %+v %+v", prior, after)
	}
	const headline = "把项目发现组织成读者关心的问题"
	evidenceNow := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	plans, err := store.Plans(t.Context(), evidenceNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		source, err := store.Evidence(t.Context(), plan, evidenceNow)
		evidence := source.Repositories
		if err != nil || len(evidence) == 0 {
			t.Fatalf("无法读取报刊证据: %+v %v", plan, err)
		}
		if plan.Kind != reports.Daily && (source.SourceIssues != 3 || source.CoverageDays != 3 ||
			len(evidence) != 3 || evidence[0].ID != 1 || evidence[0].Rank == nil || *evidence[0].Rank != 2 ||
			evidence[0].Guide != trend.SummaryZh || evidence[0].Date != "2026-10-07") {
			t.Fatalf("周期证据应按观测日计数，保留去重和最好名次: %+v", source)
		}
		ids := []int64{evidence[0].ID}
		content := reports.Editorial{Headline: headline, Lead: "用实测数据和保存的导读说明项目解决的问题。",
			Stories: []reports.Story{{Title: "项目如何减少维护工作", Body: "此处根据项目导读说明适用的实际场景与实现方式。", RepoIDs: ids}}}
		if err := db.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
			return (reportstore.Store{DB: tx}).SaveEditorial(t.Context(), plan, source, content, evidence, evidenceNow)
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, period := range []struct{ kind, key, from, to string }{
		{"weekly", "2026-W41", "2026-10-05", "2026-10-11"}, {"monthly", "2026-10", "2026-10-01", "2026-10-31"},
	} {
		query := path + "?kind=" + period.kind
		result := request(t, router, "GET", query+"&date="+period.key, "", "", 200)
		report := result["report"].(map[string]any)
		if report["headline"] != headline || report["periodFrom"] != period.from || report["periodTo"] != period.to ||
			report["sourceIssues"] != float64(3) || report["coverageDays"] != float64(3) || report["publicationStatus"] != "preview" ||
			report["number"] != nil || len(report["stories"].([]any)) != 1 || len(report["items"].([]any)) != 1 ||
			len(result["calendar"].([]any)) != 0 || len(result["archive"].([]any)) != 0 {
			t.Fatalf("多份日报可能来自同一观测日，预览不能出现在归档和出刊日历: %v", result)
		}
		older := request(t, router, "GET", query+"&before="+period.key, "", "", 200)
		if len(older["archive"].([]any)) != 0 || older["total"] != float64(0) {
			t.Fatalf("预览不得占用正式期数: %v", older)
		}
	}
	daily := request(t, router, "GET", path+"?date=2026-10-07", "", "", 200)
	if daily["archive"].([]any)[0].(map[string]any)["headline"] != headline || len(daily["calendar"].([]any)) != 3 {
		t.Fatalf("往期列表使用编辑标题，出刊日历包含本月全部期次: %v", daily)
	}
	// 第 2 个仓库参与写作但未被模型列为引用，也必须触发撤回。
	if err := db.Model(&storage.Repository{}).Where("repo_id=2").Update("blocked", true).Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, "GET", path+"?date=2026-10-07", "", "", 404)
	request(t, router, "GET", path+"?kind=weekly&date=2026-W41", "", "", 404)
	if err := db.Model(&storage.Repository{}).Where("repo_id=1").Update("blocked", true).Error; err != nil {
		t.Fatal(err)
	}
	if archive := request(t, router, "GET", path, "", "", 200)["archive"].([]any); len(archive) != 1 ||
		archive[0].(map[string]any)["date"] != "2026-10-08" {
		t.Fatalf("引用被撤回后隐藏原刊，其他期次仍可读: %v", archive)
	}
	request(t, router, "GET", path+"?date=2026-02-30", "", "", 400)
	request(t, router, "GET", path+"?kind=weekly&date=2025-W53", "", "", 400)
	request(t, router, "GET", path+"?kind=weekly&date=2026-W40", "", "", 404)
	request(t, router, "GET", path+"?kind=monthly&date=2026-13", "", "", 400)

	for i, day := range []string{"2026-12-31", "2027-01-01"} {
		start, end, valid := reports.Daily.Bounds(day)
		if !valid {
			t.Fatal(err)
		}
		addRepo(int64(10+i), end.Add(-time.Hour), true)
		if err := store.Publish(t.Context(), reports.Window{Date: day, Start: start, End: end, GeneratedAt: end}); err != nil {
			t.Fatal(err)
		}
	}
	crossYear := time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)
	plans, err = store.Plans(t.Context(), crossYear)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		if plan.Kind == reports.Weekly && plan.Key == "2026-W53" {
			week, err := store.Evidence(t.Context(), plan, crossYear)
			if err != nil || week.SourceIssues != 2 || week.CoverageDays != 2 {
				t.Fatalf("跨年观测必须归入同一 ISO 周: %+v %v", week, err)
			}
			return
		}
	}
	t.Fatal("未生成跨年周报计划")
}

func TestWeeklyPreviewOnlyGetsNumberAfterFormalPublication(t *testing.T) {
	router, db := collectionRouter(t)
	ctx := t.Context()
	if err := storage.InitializeEmpty(ctx, db); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/public/daily-reports?kind=weekly&date=2026-W45"
	const key = "2026-W45"
	cutoff, _ := reports.Weekly.PublicationPolicy(key, config.DefaultReportPublication())
	id := int64(50)
	repo := storage.Repository{RepoID: id, Owner: "report", Name: "50", FullName: "report/50", NameIsCurrent: true,
		HTMLURL: "https://github.com/report/50", State: "active", Grade: "A", TopicsJSON: "[]", LastSuccessAt: new(cutoff.UnixMilli())}
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	seedReadySummary(t, db, id, cutoff.UnixMilli())
	for index, at := range []time.Time{
		cutoff.AddDate(0, 0, -5).Add(-time.Minute), cutoff.AddDate(0, 0, -4).Add(-time.Minute),
		cutoff.AddDate(0, 0, -3).Add(-time.Minute), cutoff.AddDate(0, 0, -2).Add(-time.Minute), cutoff.Add(-9 * time.Hour),
	} {
		// 周日 23:00 的观测归属周日，周一 08:00 起可以组稿。
		issueDate := reports.ObservationDate(at.UnixMilli())
		_, end, _ := reports.Daily.Bounds(issueDate)
		issue := reportstore.Edition{IssueDate: issueDate, IssueNumber: index + 1, ObservedFrom: at.UnixMilli(),
			ObservedTo: at.UnixMilli(), GeneratedAt: at.UnixMilli(), WindowEnd: end.UnixMilli()}
		if err := db.Create(&issue).Error; err != nil {
			t.Fatal(err)
		}
		item := reportstore.Item{IssueDate: issueDate, Position: 1, RepoID: id, Section: "discoveries",
			ObservedAt: at.UnixMilli(), SummaryZh: "项目用本地脚本组织日常开发工作。"}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := reportstore.Store{DB: db}
	planAt := func(now time.Time, want string) (reports.Plan, reportstore.ManuscriptSource) {
		t.Helper()
		plans, err := store.Plans(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, plan := range plans {
			if plan.Kind != reports.Weekly || plan.Key != key {
				continue
			}
			if plan.Status != want {
				t.Fatalf("周期状态: %+v", plan)
			}
			source, err := store.Evidence(ctx, plan, now)
			if err != nil || source.CoverageDays != 5 || source.ObservedTo != cutoff.Add(-9*time.Hour).UnixMilli() {
				t.Fatalf("按真实观测日期划分周期: %+v %v", source, err)
			}
			return plan, source
		}
		t.Fatal("找不到周报计划")
		return reports.Plan{}, reportstore.ManuscriptSource{}
	}
	write := func(now time.Time, plan reports.Plan, source reportstore.ManuscriptSource) error {
		content := reports.Editorial{Headline: "用本地脚本组织开发工作", Lead: "本地工具可用于执行日常开发任务。",
			Stories: []reports.Story{{Title: "用脚本组织工作", Body: "项目将重复操作集中在本地工作流中，便于按需执行。", RepoIDs: []int64{id}}}}
		return db.Transaction(func(tx *gorm.DB) error {
			return (reportstore.Store{DB: tx}).SaveEditorial(ctx, plan, source, content, source.Repositories, now)
		})
	}
	beforeCutoff := cutoff.Add(-time.Second)
	early, err := store.Plans(ctx, beforeCutoff)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range early {
		if plan.Kind == reports.Weekly && plan.Key == key {
			t.Fatalf("周一 08:00 之前不得组稿: %+v", plan)
		}
	}
	currentDate := reports.Weekly.Key(time.Now())
	futurePreview := reportstore.Editorial{Kind: reports.Weekly, IssueKey: currentDate, PublicationStatus: reports.Preview,
		CoverageDays: 1, Fingerprint: "old-current-period", StoriesJSON: "[]", RepoIDsJSON: "[]"}
	if err := db.Create(&futurePreview).Error; err != nil {
		t.Fatal(err)
	}
	request(t, router, "GET", "/api/v1/public/daily-reports?kind=weekly&date="+currentDate, "", "", 404)
	oldPlan, oldSource := planAt(cutoff, reports.Published)
	options := settings.Store{DB: db}
	update := func(days int) {
		t.Helper()
		saved, err := options.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		saved.Settings.ReportPublication.WeeklyMinDays = days
		if _, err := options.SaveRuntime(ctx, saved.Revision, saved.Settings); err != nil {
			t.Fatal(err)
		}
	}
	update(6)
	if err := write(cutoff, oldPlan, oldSource); !errors.Is(err, reportstore.ErrEvidenceChanged) {
		t.Fatalf("配置提交后，正在执行的旧规则任务不得正式出刊: %v", err)
	}
	plan, source := planAt(cutoff, reports.Preview)
	if err := write(cutoff, plan, source); err != nil {
		t.Fatal(err)
	}
	preview := request(t, router, "GET", path, "", "", 200)
	if preview["report"].(map[string]any)["number"] != nil || preview["total"] != float64(0) {
		t.Fatalf("覆盖不足只能生成未编号的预览: %v", preview)
	}
	update(4)
	plan, source = planAt(cutoff, reports.Published)
	if err := write(cutoff, plan, source); err != nil {
		t.Fatal(err)
	}
	issue := request(t, router, "GET", path, "", "", 200)
	if issue["report"].(map[string]any)["number"] != float64(1) || issue["total"] != float64(1) ||
		issue["calendar"].([]any)[0] != key || issue["report"].(map[string]any)["publicationStatus"] != "published" {
		t.Fatalf("正式出刊时才编号并列入归档与日历: %v", issue)
	}
	// 后续校正仍应使旧周稿失效，即使该日报已经超过自动补刊期限。
	date := reports.ObservationDate(cutoff.Add(-9 * time.Hour).UnixMilli())
	if err := db.Model(&reportstore.Edition{}).Where("issue_date = ?", date).
		Updates(map[string]any{"generated_at": cutoff.Add(time.Minute).UnixMilli(), "observed_from": 0, "observed_to": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&reportstore.Item{}, "issue_date = ?", date).Error; err != nil {
		t.Fatal(err)
	}
	archive, err := store.ReadEditorials(ctx, reports.Weekly, "", "", 25, cutoff.AddDate(0, 0, 14))
	if err != nil || archive.Edition != nil || archive.Total != 0 {
		t.Fatalf("校正后旧稿不得在补刊期限外重新可见: %+v %v", archive, err)
	}
}
