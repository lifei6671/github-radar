package summary

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

func retryAt(job storage.Job, now time.Time) int64 {
	return now.Add(time.Duration(1<<min(job.Attempts, 8))*time.Second + time.Duration(job.ID%17)*time.Second).UnixMilli()
}

func (e *Engine) sourceFailed(ctx context.Context, job storage.Job, err error) error {
	if errors.Is(err, ErrRepoUnavailable) {
		return e.fail(ctx, job, "summary_repo_unavailable", false, 0, false)
	}
	var upstream *gh.Failure
	if errors.As(err, &upstream) {
		return e.fail(ctx, job, upstream.Code, true, upstream.Until, upstream.Status == 0 && upstream.Until > 0)
	}
	return err
}

func (e *Engine) fail(ctx context.Context, job storage.Job, code string, retry bool, until int64, deferOnly bool) error {
	attempts := job.Attempts
	if deferOnly {
		attempts--
	}
	status := "failed"
	if !retry || attempts >= job.MaxAttempts {
		status = "dead"
	}
	return e.Queue.settle(ctx, job, outcome{status: status, code: code, attempts: attempts, next: max(until, retryAt(job, time.Now()))})
}

func (e *Engine) finishFailure(ctx context.Context, job storage.Job, call storage.AICall, result ModelResult, duration time.Duration,
	old storage.AISummary, check storage.SummaryCheck) error {
	retry := result.Retry
	if result.Code == "invalid_output" && retry && e.Queue.AI.Summary.RepairAttempts < job.MaxAttempts-1 {
		var count int64
		if err := e.Queue.DB.WithContext(ctx).Model(&storage.AICall{}).
			Where("job_id=? AND status='failed' AND error_code='invalid_output'", job.ID).Count(&count).Error; err != nil {
			return err
		}
		retry = count < int64(e.Queue.AI.Summary.RepairAttempts)
	}
	status := "failed"
	if !retry || job.Attempts >= job.MaxAttempts {
		status = "dead"
	}
	if result.Unknown {
		status = "unknown"
	}
	return e.settleAfterCall(ctx, job, outcome{status: status, code: result.Code, next: retryAt(job, time.Now()),
		attempts: job.Attempts, call: &call, model: result, duration: duration, apply: func(tx *gorm.DB) error {
			return saveObserved(tx, old, check)
		}})
}

// settleAfterCall 退出时给已发出请求一次短暂的台账提交机会，仍须通过租约校验。
func (e *Engine) settleAfterCall(ctx context.Context, job storage.Job, o outcome) error {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
	}
	return e.Queue.settle(ctx, job, o)
}
