package summary

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/reports"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/reportstore"
)

func TestReadmeQueueSpreadsAutomaticStartsWithoutBlockingReportsOrManualJobs(t *testing.T) {
	e, _, _, _ := fixture(t)
	q := e.Queue
	q.Pacing = storage.JobPacing{
		{Key: "readme", Types: []string{JobType, TranslationJob}, Gap: 2 * time.Second,
			Resource: gh.ReadmeCheckResource, Window: time.Hour, Budget: 120},
		{Key: "reports", Types: []string{ReportJob}, Gap: 2 * time.Second},
	}
	ctx, db := t.Context(), q.DB
	now := time.Now()
	ms := now.UnixMilli()
	rate := storage.RateLimit{Resource: gh.ReadmeCheckResource, LocalWindow: ms, LocalRequests: 4}
	if err := db.Create(&rate).Error; err != nil {
		t.Fatal(err)
	}
	auto, err := q.insert(db, JobType, 1, "paced-auto", payload{}, 100, ms)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := q.insert(db, JobType, 2, "paced-manual", payload{Manual: true}, 200, ms)
	if err != nil {
		t.Fatal(err)
	}
	report, err := q.reportJob(reportPayload{}, "paced-paper", 150, ms)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&report).Error; err != nil {
		t.Fatal(err)
	}
	claim := func(at time.Time, want int64) {
		t.Helper()
		job, err := q.claim(ctx, at, true, true)
		if err != nil || (job == nil && want != 0) || (job != nil && job.ID != want) {
			t.Fatalf("领取结果 %+v，期望 %d: %v", job, want, err)
		}
		if job != nil {
			if err := db.Model(&storage.Job{}).Where("id=?", job.ID).Update("status", consts.StatusSucceeded).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	claim(now, manual.ID)
	claim(now, report.ID)
	claim(now.Add(119*time.Second), 0)
	claim(now.Add(120*time.Second), auto.ID)
}

func TestFirstGuideForSampledRepoPrecedesRoutineBackfill(t *testing.T) {
	e, _, _, _ := fixture(t)
	q, now := e.Queue, time.Now()
	if err := q.DB.Model(&storage.Repository{}).Where("repo_id = ?", 1).Update("last_success_at", now.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := q.insert(q.DB, JobType, 2, "theme-backfill", payload{}, 120, now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	first, err := q.insert(q.DB, JobType, 1, "first-guide", payload{}, 100, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := q.claim(t.Context(), now, true, true)
	if err != nil || claimed == nil || claimed.ID != first.ID {
		t.Fatalf("已采样且缺导读的仓库应优先领取: got=%+v err=%v", claimed, err)
	}
}

func TestReportJobsUseSavedEvidenceAndAccountForAI(t *testing.T) {
	e, upstream, _, _ := fixture(t)
	e.Provider = modelFixture(func(context.Context, string) ModelResult {
		t.Fatal("报刊任务不得调用项目导读提示词")
		return ModelResult{}
	})
	const guide = "项目通过可复用脚本减少重复操作，并支持在本地验证每个步骤。"
	const answer = `{"headline":"把重复的项目操作变成可复用流程","lead":"两套工具分别处理操作编排和本地验证，适合需要反复执行相同任务的团队。","stories":[{"title":"通过可复用脚本减少操作重复","body":"两个项目分别覆盖本地脚本执行和团队工作流编排，适用于需要反复执行并检查结果的场景。","repoIds":[1,2]}]}`
	calls := 0
	e.WriteReport = func(_ context.Context, prompt string) ModelResult {
		calls++
		if !strings.Contains(prompt, guide) || !strings.Contains(prompt, "owner/2") || !strings.Contains(prompt, "覆盖 1 个观测日") {
			t.Fatalf("报刊没有使用已保存的导读证据: %s", prompt)
		}
		if calls == 1 {
			if err := e.Queue.DB.Model(&storage.Repository{}).Where("repo_id=2").Update("blocked", true).Error; err != nil {
				t.Fatal(err)
			}
		}
		return ModelResult{Content: answer}
	}
	q, db := e.Queue, e.Queue.DB
	q.AI.Summary.AutoDailyCalls = 10
	e.Queue = q
	at := time.Date(2020, 11, 12, 10, 0, 0, 0, time.UTC)
	for id := int64(1); id <= 2; id++ {
		if err := db.Model(&storage.Repository{}).Where("repo_id=?", id).
			Updates(map[string]any{"discovered_at": at.UnixMilli(), "last_success_at": at.UnixMilli()}).Error; err != nil {
			t.Fatal(err)
		}
		r := storage.AISummary{RepoID: id, SummaryStatus: "ready", SummaryZh: guide, FeaturesJSON: "[]",
			AudienceJSON: "[]", ValueTagsJSON: "[]", SourceQuality: "sufficient", SourceLanguage: "zh"}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := reportstore.Store{DB: db}
	if err := store.Publish(t.Context(), reports.Window{Date: "2020-11-12", Start: at.Add(-time.Hour), End: at.Add(time.Hour), GeneratedAt: at}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := q.ScheduleReports(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	step(t, e)
	canceled := row[storage.Job](t, db, "type=? AND status='canceled'", ReportJob)
	if canceled.LastErrorCode == nil || *canceled.LastErrorCode != codeReportEvidenceChanged {
		t.Fatalf("调用期间撤回证据必须取消本轮发布: %+v", canceled)
	}
	if err := db.Model(&storage.Repository{}).Where("repo_id=2").Update("blocked", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.ScheduleReports(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		step(t, e)
	}
	if done, err := e.Step(t.Context()); done || err != nil || calls != 4 || len(upstream.etags) != 0 {
		t.Fatalf("每种报刊只用一次已计费的 AI 请求和本地证据: done=%v calls=%d reads=%v err=%v", done, calls, upstream.etags, err)
	}
	var billed int64
	if err := db.Model(&storage.AICall{}).Where("origin='auto' AND status='succeeded'").Count(&billed).Error; err != nil || billed != 4 {
		t.Fatalf("报刊调用需要复用 AI 台账: %d %v", billed, err)
	}
	week, err := store.ReadEditorials(t.Context(), reports.Weekly, "", "", 25, time.Now())
	if err != nil || week.Edition == nil || len(week.Citations) != 2 || len(week.Calendar) != 0 ||
		week.Edition.PublicationStatus != reports.Preview || week.Edition.IssueNumber != nil ||
		week.Edition.Headline != "把重复的项目操作变成可复用流程" {
		t.Fatalf("专题必须由内容组织并绑定多个真实引用: %+v %v", week, err)
	}
	if err := q.ScheduleReports(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if done, err := e.Step(t.Context()); done || err != nil {
		t.Fatalf("证据相同不得重新计费: %v %v", done, err)
	}

	// 模拟请求发送后退出，恢复将调用记为未决，只有确认才安排手动任务。
	plan := reports.Plan{Kind: reports.Weekly, Key: week.Edition.IssueKey, Fingerprint: week.Edition.Fingerprint, Status: reports.Preview}
	now := time.Now()
	job, err := q.reportJob(reportPayload{Plan: plan}, "crashed-report", 180, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	running, err := q.claim(t.Context(), now, true, true)
	if err != nil || running == nil || running.ID != job.ID {
		t.Fatalf("领取报刊任务: %+v %v", running, err)
	}
	another, err := q.reportJob(reportPayload{Plan: plan}, "same-period-different-task", 150, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&another).Error; err != nil {
		t.Fatal(err)
	}
	if duplicate, err := q.claim(t.Context(), now, true, true); err != nil || duplicate != nil {
		t.Fatalf("同一报刊周期必须串行: %+v %v", duplicate, err)
	}
	call, err := q.reserve(t.Context(), *running, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.sending(t.Context(), *running, call); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&job).Update("lease_until", now.Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.Recover(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if got := row[storage.Job](t, db, "id=?", job.ID); got.Status != "unknown" {
		t.Fatalf("请求必须进入未决: %+v", got)
	}
	if duplicate, err := q.claim(t.Context(), now, true, true); err != nil || duplicate != nil {
		t.Fatalf("相同证据的未决调用必须等待确认: %+v %v", duplicate, err)
	}
	next, err := q.RetryUnknown(t.Context(), job.ID, "ack-report", true, now)
	var p reportPayload
	if err != nil || json.Unmarshal([]byte(next.PayloadJSON), &p) != nil || !p.Manual || p.Plan != plan {
		t.Fatalf("重试保留周期和证据身份: %+v %v", next, err)
	}
	step(t, e)
	if calls != 4 {
		t.Fatalf("已写好的报刊在重试时仍应避免重复计费: %d", calls)
	}
}
