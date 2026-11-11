package summary

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/storage"
)

func retryAt(job storage.Job, now time.Time) int64 {
	return now.Add(time.Duration(1<<min(job.Attempts, 8))*time.Second + time.Duration(job.ID%17)*time.Second).UnixMilli()
}

func (e *Engine) sourceFailed(ctx context.Context, job storage.Job, err error) error {
	if errors.Is(err, ErrRepoUnavailable) {
		return e.Queue.settle(ctx, job, outcome{status: consts.StatusDead, code: consts.CodeSummaryRepoUnavailable, attempts: job.Attempts,
			apply: withdrawRepository(err, *job.RepoID)})
	}
	var upstream *gh.Failure
	if errors.As(err, &upstream) {
		return e.fail(ctx, job, upstream.Code, true, upstream.Until, upstream.Status == 0 && upstream.Until > 0)
	}
	return err
}

type visibilityFailure struct {
	observed int64
	gone     bool
}

func (*visibilityFailure) Error() string { return ErrRepoUnavailable.Error() }
func (*visibilityFailure) Unwrap() error { return ErrRepoUnavailable }

// 使用已有的仓库可见状态撤下已确认不可访问的内容，并保留管理员的历史记录。
func withdrawRepository(err error, repoID int64) func(*gorm.DB) error {
	var observation *visibilityFailure
	if !errors.As(err, &observation) {
		return nil
	}
	return func(tx *gorm.DB) error {
		at := observation.observed
		values := map[string]any{storage.RepositoryColumns.LastCheckedAt: at, storage.RepositoryColumns.UpdatedAt: gorm.Expr("MAX(updated_at, ?)", at),
			storage.RepositoryColumns.FirstNotFoundAt: gorm.Expr("COALESCE("+storage.RepositoryColumns.FirstNotFoundAt+", ?)", at)}
		if observation.gone {
			values[storage.RepositoryColumns.State] = consts.StatusUnavailable
		}
		return tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.RepoID+"=? AND COALESCE("+storage.RepositoryColumns.LastCheckedAt+",0)<=? AND COALESCE("+storage.RepositoryColumns.LastSuccessAt+",0)<=?", repoID, at, at).
			Updates(values).Error
	}
}

func (e *Engine) fail(ctx context.Context, job storage.Job, code string, retry bool, until int64, deferOnly bool) error {
	attempts := job.Attempts
	if deferOnly {
		attempts--
	}
	status := consts.StatusFailed
	if !retry || attempts >= job.MaxAttempts {
		status = consts.StatusDead
	}
	return e.Queue.settle(ctx, job, outcome{status: status, code: code, attempts: attempts, next: max(until, retryAt(job, time.Now()))})
}

func (e *Engine) finishFailure(ctx context.Context, job storage.Job, call storage.AICall, result ModelResult, duration time.Duration,
	old storage.AISummary, check storage.SummaryCheck, topicsFingerprint string) error {
	return settleCall(ctx, func(final context.Context) error {
		return e.recordCallFailure(final, job, call, result, duration, old, check, topicsFingerprint)
	})
}

func (e *Engine) recordCallFailure(ctx context.Context, job storage.Job, call storage.AICall, result ModelResult, duration time.Duration,
	old storage.AISummary, check storage.SummaryCheck, topicsFingerprint string) error {
	retry := result.Retry
	if result.Code == consts.CodeInvalidOutput && retry && e.Queue.AI.Summary.RepairAttempts < job.MaxAttempts-1 {
		var count int64
		if err := e.Queue.DB.WithContext(ctx).Model(&storage.AICall{}).
			Where(storage.AICallColumns.JobID+"=? AND "+storage.AICallColumns.Status+"='"+consts.StatusFailed+"' AND "+storage.AICallColumns.ErrorCode+"='"+consts.CodeInvalidOutput+"'", job.ID).Count(&count).Error; err != nil {
			return err
		}
		retry = count < int64(e.Queue.AI.Summary.RepairAttempts)
	}
	status := consts.StatusFailed
	if !retry || job.Attempts >= job.MaxAttempts {
		status = consts.StatusDead
	}
	if result.Unknown {
		status = consts.StatusUnknown
	}
	return e.Queue.settle(ctx, job, outcome{status: status, code: result.Code, next: retryAt(job, time.Now()),
		attempts: job.Attempts, call: &call, model: result, duration: duration, apply: func(tx *gorm.DB) error {
			return saveObserved(tx, old, check, topicsFingerprint)
		}})
}

// settleAfterCall 退出时给已发出请求一次短暂的台账提交机会，仍须通过租约校验。
func (e *Engine) settleAfterCall(ctx context.Context, job storage.Job, o outcome) error {
	return settleCall(ctx, func(final context.Context) error { return e.Queue.settle(final, job, o) })
}

// settleCall 在模型结果已返回后，用排空期限保护已确认的结果与用量。
func settleCall(ctx context.Context, write func(context.Context) error) error {
	if final, release, draining := lifecycle.Settlement(ctx); draining {
		defer release()
		return write(final)
	}
	if ctx.Err() != nil {
		final, release := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer release()
		return write(final)
	}
	err := write(ctx)
	if err != nil {
		if final, release, draining := lifecycle.Settlement(ctx); draining {
			defer release()
			return write(final)
		}
	}
	return err
}
