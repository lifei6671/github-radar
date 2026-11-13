package summary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/reports"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/reportstore"
)

type reportPayload struct {
	reports.Plan
	Manual bool `json:"manual,omitempty"`
}

const codeReportEvidenceChanged = "report_evidence_changed"

func (q Queue) reportJob(p reportPayload, key string, priority int, now int64) (storage.Job, error) {
	data, err := json.Marshal(p)
	return storage.Job{Type: ReportJob, DedupKey: key, PayloadJSON: string(data), Priority: priority,
		Status: consts.StatusPending, AvailableAt: now, MaxAttempts: q.AI.Summary.MaxAttempts, CreatedAt: now, UpdatedAt: now}, err
}

// ScheduleReports 为每个周期的证据版本安排一次后台写作，复用摘要的自动调用额度。
func (q Queue) ScheduleReports(ctx context.Context, now time.Time) error {
	if q.AI.Summary.AutoDailyCalls <= 0 {
		return nil
	}
	db := q.DB.WithContext(ctx)
	paused, err := storage.BooleanSetting(ctx, db, storage.AutoAIResumeKey)
	if err != nil || paused {
		return err
	}
	plans, err := (reportstore.Store{DB: db}).Plans(ctx, now)
	if err != nil || len(plans) == 0 {
		return err
	}
	var saved []reportstore.Editorial
	if err := db.Select("kind", "issue_key", "fingerprint", "generated_at", "publication_status").Find(&saved).Error; err != nil {
		return err
	}
	current := make(map[string]reportstore.Editorial, len(saved))
	for _, row := range saved {
		current[string(row.Kind)+":"+row.IssueKey] = row
	}
	var previous []storage.Job
	if err := db.Select("id", "dedup_key", "status", "last_error_code", "payload_json").Where("type=?", ReportJob).Find(&previous).Error; err != nil {
		return err
	}
	enqueued := make(map[string]storage.Job, len(previous))
	unknown := make(map[reports.Plan]bool)
	for _, job := range previous {
		enqueued[job.DedupKey] = job
		var p reportPayload
		if job.Status == consts.StatusUnknown && json.Unmarshal([]byte(job.PayloadJSON), &p) == nil {
			unknown[p.Plan] = true
		}
	}
	var jobs []storage.Job
	var resumed []int64
	for _, plan := range plans {
		period := string(plan.Kind) + ":" + plan.Key
		existing := current[period]
		if existing.Fingerprint == plan.Fingerprint || unknown[plan] ||
			(existing.PublicationStatus == reports.Published && plan.PublicationStatus() == reports.Preview) {
			continue
		}
		key := fmt.Sprintf("%s:%s:%s:%d", ReportJob, period, plan.Fingerprint, existing.GeneratedAt)
		if old, exists := enqueued[key]; exists {
			if old.Status == consts.StatusCanceled && old.LastErrorCode != nil && *old.LastErrorCode == codeReportEvidenceChanged {
				resumed = append(resumed, old.ID)
			}
		} else {
			job, err := q.reportJob(reportPayload{Plan: plan}, key, 150, now.UnixMilli())
			if err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		if len(jobs)+len(resumed) == 9 {
			break
		}
	}
	if len(jobs)+len(resumed) == 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if len(resumed) > 0 {
			ms := now.UnixMilli()
			if err := tx.Model(&storage.Job{}).Where("id IN ? AND status=? AND last_error_code=?",
				resumed, consts.StatusCanceled, codeReportEvidenceChanged).
				Updates(jobFields(consts.StatusPending, "", ms, ms, 0)).Error; err != nil {
				return err
			}
		}
		if len(jobs) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).Create(&jobs).Error
	})
}

func (e *Engine) runReport(ctx context.Context, job storage.Job) error {
	q := e.Queue
	var p reportPayload
	if json.Unmarshal([]byte(job.PayloadJSON), &p) != nil || e.WriteReport == nil {
		return e.fail(ctx, job, consts.CodeSummaryInvalidJob, false, 0, false)
	}
	store := reportstore.Store{DB: q.DB}
	var saved int64
	if err := q.DB.WithContext(ctx).Model(&reportstore.Editorial{}).
		Where("kind=? AND issue_key=? AND (fingerprint=? OR (publication_status=? AND ?=?))",
			p.Kind, p.Key, p.Fingerprint, reports.Published, p.PublicationStatus(), reports.Preview).Count(&saved).Error; err != nil {
		return err
	}
	if saved != 0 {
		return q.settle(ctx, job, outcome{status: consts.StatusSucceeded, attempts: job.Attempts})
	}
	source, err := store.Evidence(ctx, p.Plan, time.Now())
	if err != nil && !errors.Is(err, reportstore.ErrEvidenceChanged) {
		return err
	}
	if len(source.Repositories) == 0 {
		return q.settle(ctx, job, outcome{status: consts.StatusCanceled, code: codeReportEvidenceChanged, attempts: job.Attempts})
	}
	prompt, sent := reports.EditorPrompt(p.Plan, source.Repositories, source.CoverageDays, time.UnixMilli(source.ObservedFrom),
		time.UnixMilli(source.ObservedTo), q.AI.Summary.InputTokens)
	if len(sent) == 0 {
		return e.fail(ctx, job, consts.CodeSummaryInputBudgetTooSmall, false, 0, false)
	}
	start := time.Now()
	call, err := q.reserve(ctx, job, p.Manual, start)
	if errors.Is(err, errBudget) {
		return q.settle(ctx, job, outcome{status: consts.StatusFailed, code: consts.CodeAIBudgetExhausted, next: budgetRetryAt(job, start), attempts: job.Attempts - 1})
	}
	if err != nil {
		return err
	}
	if err := q.sending(ctx, job, call); err != nil {
		return err
	}
	model := e.WriteReport(ctx, prompt)
	finished := time.Now()
	edition, _, valid := reports.ParseEditorial(model.Content, sent)
	if model.Code == "" && (!valid || (p.PublicationStatus() == reports.Preview && !edition.SafePreview())) {
		model.Code, model.Retry = consts.CodeInvalidOutput, true
	}
	if model.Code != "" {
		return e.finishFailure(ctx, job, call, model, finished.Sub(start), storage.AISummary{}, storage.SummaryCheck{}, "")
	}
	result := outcome{status: consts.StatusSucceeded, attempts: job.Attempts, call: &call, model: model,
		duration: finished.Sub(start), apply: func(tx *gorm.DB) error {
			return (reportstore.Store{DB: tx}).SaveEditorial(tx.Statement.Context, p.Plan, source, edition, sent, finished)
		}}
	if err := e.settleAfterCall(ctx, job, result); !errors.Is(err, reportstore.ErrEvidenceChanged) {
		return err
	}
	result.status, result.code, result.apply = consts.StatusCanceled, codeReportEvidenceChanged, nil
	return e.settleAfterCall(ctx, job, result)
}
