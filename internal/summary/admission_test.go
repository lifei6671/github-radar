package summary

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/storage"
)

// 验证低质 A 档的旧任务被清理，早期项目无需摘要或采样即可首次清洗。
func TestAutomaticAdmissionBeforePublication(t *testing.T) {
	e, _, _, _ := fixture(t)
	q := e.Queue
	q.AI.Summary.LegacyAdmission = false
	now := time.Now().UTC()
	created := now.Add(-48 * time.Hour).UnixMilli()
	for id, description := range map[int64]string{1: "TODO", 2: "Organize local development workflows"} {
		if err := q.DB.Model(&storage.Repository{}).Where("repo_id = ?", id).Updates(map[string]any{
			"description": description, "stars": 40, "github_created_at": created, "topics_json": "[]",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	stale, err := q.insert(q.DB, JobType, 1, "old-low-quality", payload{TopicsOnly: true}, 120, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := q.insert(q.DB, JobType, 3, "unknown-low-quality", payload{}, 120, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.DB.Model(&unknown).Update("status", consts.StatusUnknown).Error; err != nil {
		t.Fatal(err)
	}
	if err := q.Schedule(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var got storage.Job
	if err := q.DB.Take(&got, stale.ID).Error; err != nil || got.Status != consts.StatusCanceled ||
		got.LastErrorCode == nil || *got.LastErrorCode != "no_readable_source" {
		t.Fatalf("低价值待发主题任务: %+v %v", got, err)
	}
	if err := q.DB.Take(&unknown, unknown.ID).Error; err != nil || unknown.Status != consts.StatusUnknown {
		t.Fatalf("未决调用被更改: %+v %v", unknown, err)
	}
	job, err := q.claim(t.Context(), now, true, true)
	if err != nil || job == nil || job.RepoID == nil || *job.RepoID != 2 {
		t.Fatalf("早期项目首次清洗未准入: %+v %v", job, err)
	}
	e.Queue = q
	e.Provider = modelFixture(func(context.Context, string) ModelResult {
		return ModelResult{Content: modelAnswer(false)}
	})
	if err := e.runJob(t.Context(), *job, false); err != nil {
		t.Fatal(err)
	}
	guide := row[storage.AISummary](t, q.DB, "repo_id = ?", 2)
	if guide.SummaryStatus != consts.StatusReady {
		t.Fatalf("首次清洗未生成有效导读: %+v", guide)
	}
	var attempts int64
	if err := q.DB.Model(&storage.AIBudgetReservation{}).Where("operation = ? AND status = ?", "summary_first", "succeeded").
		Count(&attempts).Error; err != nil || attempts != 1 {
		t.Fatalf("首次清洗账本条目 %d: %v", attempts, err)
	}
	report, err := storage.ReadAdmissionAudit(t.Context(), q.DB, now)
	if err != nil || report.FirstGuides != 1 {
		t.Fatalf("首次有效清洗未计入日指标: %+v %v", report, err)
	}
}

// 手动任务可以接管已延期的自动任务，但同一请求 ID 仍幂等。
func TestManualSupersedesDeferredAuto(t *testing.T) {
	e, _, _, _ := fixture(t)
	q := e.Queue
	q.AI.Summary.AutoDailyCalls = 0
	now := time.Now()
	job, err := q.insert(q.DB, JobType, 1, "deferred-auto", payload{}, 100, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if err := q.DB.Model(&job).Updates(map[string]any{"status": "failed", "available_at": now.Add(time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	manual, err := q.Manual(t.Context(), 1, "manual-override", true, true, now)
	if err != nil || manual.Conflict || !manual.Job.IsManual() || manual.Job.ID == job.ID {
		t.Fatalf("未使用人工额度接管: %+v %v", manual, err)
	}
	repeat, err := q.Manual(t.Context(), 1, "manual-override", true, true, now)
	if err != nil || repeat.Job.ID != manual.Job.ID || row[storage.Job](t, q.DB, "id = ?", job.ID).Status != consts.StatusCanceled {
		t.Fatalf("接管未幂等: %+v %v", repeat, err)
	}
}

// 新精确增长能跳过仍未到期的摘要检查，普通元数据刷新不能解除空内容冷却。
func TestNewGrowthWakesDeferredAdmission(t *testing.T) {
	e, _, _, _ := fixture(t)
	q := e.Queue
	q.AI.Summary.LegacyAdmission = false
	now := time.Now().UTC()
	for id, text := range map[int64]string{1: "Process local development workflows", 2: "TODO", 3: "Manage automation across projects"} {
		if err := q.DB.Model(&storage.Repository{}).Where("repo_id = ?", id).
			Updates(map[string]any{"description": text, "updated_at": now.UnixMilli()}).Error; err != nil {
			t.Fatal(err)
		}
		reason := "no_value_signal"
		if id == 2 {
			reason = "verified_no_readable_source"
		}
		if id == 3 {
			created := now.AddDate(0, 0, -365).UnixMilli()
			if err := q.DB.Model(&storage.Repository{}).Where("repo_id = ?", id).Update("github_created_at", created).Error; err != nil {
				t.Fatal(err)
			}
			old := storage.AISummary{RepoID: id, SummaryStatus: consts.StatusReady, SummaryZh: "旧导读", SourceQuality: "sufficient",
				SourceLanguage: "en", ModelProvider: ProviderKey(q.AI), ModelName: q.AI.Model, PromptVersion: q.AI.Summary.PromptVersion,
				FeaturesJSON: "[]", AudienceJSON: "[]", ValueTagsJSON: "[]", EditorialBriefJSON: "{}"}
			if err := q.DB.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
		}
		previous := now.Add(-time.Hour)
		if err := storage.SaveAdmission(q.DB, id, "summary", admission.Decision{State: admission.Defer, Reason: reason, RetryAt: now.Add(24 * time.Hour).UnixMilli()}, admission.Evidence{}, previous); err != nil {
			t.Fatal(err)
		}
		if err := q.DB.Create(&storage.SummaryCheck{RepoID: id, LastCheckedAt: previous.UnixMilli(), NextCheckAt: now.Add(24 * time.Hour).UnixMilli()}).Error; err != nil {
			t.Fatal(err)
		}
		delta := int64(101)
		window := fmt.Sprintf(`{"24h":{"delta":101,"quality":"exact","startAt":%d,"endAt":%d}}`, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli())
		if err := q.DB.Create(&storage.LatestMetric{RepoID: id, Delta24h: &delta, DataQuality: consts.QualityExact,
			WindowDetailsJSON: window, CalculatedAt: now.UnixMilli()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Schedule(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var jobs []storage.Job
	if err := q.DB.Where("type = ?", JobType).Order("repo_id").Find(&jobs).Error; err != nil ||
		len(jobs) != 2 || *jobs[0].RepoID != 1 || *jobs[1].RepoID != 3 {
		t.Fatalf("新精确窗口/无关元数据复核错误: %+v %v", jobs, err)
	}
}
