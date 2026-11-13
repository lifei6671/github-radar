package summary

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/storage"
)

// ErrBudgetExhausted 供手动请求同步报告额度耗尽。
var ErrBudgetExhausted = aibudget.ErrExhausted
var errBudget = ErrBudgetExhausted
var errAdmissionChanged = errors.New("模型发送前的准入资格已变化")

// reserve 在 UTC 日预算内预留一次调用；复用尚未发送且在当前日的预留。
func (q Queue) reserve(ctx context.Context, job storage.Job, manual bool, now time.Time) (storage.AICall, error) {
	var call storage.AICall
	ms := now.UnixMilli()
	start := now.UTC().Truncate(24 * time.Hour)
	origin, limit := consts.OriginAuto, q.AI.Summary.AutoDailyCalls
	if manual {
		origin, limit = consts.OriginManual, q.AI.Summary.ManualDailyCalls
	}
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owns(tx, job); err != nil {
			return err
		}
		if err := tx.Find(&call, storage.AICallColumns.JobID+"=? AND "+storage.AICallColumns.Status+"='"+consts.StatusReserved+"'", job.ID).Error; err != nil {
			return err
		}
		provider := ProviderKey(q.AI)
		if call.CallKey != "" {
			if call.ReservedAt >= start.UnixMilli() && call.Provider == provider {
				return nil
			}
			if err := tx.Model(&call).Updates(map[string]any{storage.AICallColumns.Status: consts.StatusFailed, storage.AICallColumns.ErrorCode: consts.CodeReservationExpired, storage.AICallColumns.UpdatedAt: ms}).Error; err != nil {
				return err
			}
		}
		lane := ""
		if manual {
			lane = admission.Manual
		}
		remaining, err := aibudget.ForConfig(q.DB, q.AI.Summary).Remaining(tx, lane, now)
		if err != nil {
			return err
		}
		if limit == 0 || remaining == 0 {
			// 保留已关闭的旧预留，事务成功后由调用方延期。
			call = storage.AICall{}
			return nil
		}
		call = storage.AICall{CallKey: rand.Text(), JobID: job.ID, Provider: provider, Origin: origin, Status: consts.StatusReserved, ReservedAt: ms, UpdatedAt: ms}
		return tx.Create(&call).Error
	})
	if err == nil && call.CallKey == "" {
		err = errBudget
	}
	return call, err
}

// sending 在同一事务内围栏任务、复核最新资格并向统一账本登记发送。
func (q Queue) sending(ctx context.Context, job storage.Job, call storage.AICall, requestKeys ...string) (context.Context, error) {
	var key, operation string
	if len(requestKeys) > 0 {
		key = requestKeys[0]
	}
	if len(requestKeys) > 1 {
		operation = requestKeys[1]
	}
	return q.startSending(ctx, job, call, key, operation, nil)
}

// startSending 同时校验报告的公开引用及仓库任务的当前资格。
func (q Queue) startSending(ctx context.Context, job storage.Job, call storage.AICall, sourceKey, operation string, reportIDs []int64) (context.Context, error) {
	now := time.Now()
	cfg := q.AI.Summary
	req := aibudget.Request{Key: call.CallKey, Manual: call.Origin == consts.OriginManual, Operation: jobOperation(job),
		Lane: admission.Maintenance, Provider: call.Provider, Model: q.AI.Model,
		HeldCost: (float64(cfg.InputTokens)*cfg.InputCostPerM + float64(cfg.OutputTokens)*cfg.OutputCostPerM) / 1e6}
	if operation != "" {
		req.Operation = operation
	}
	if sourceKey != "" {
		var id int64
		if job.RepoID != nil {
			id = *job.RepoID
		}
		logicalOperation := req.Operation
		if logicalOperation == "summary_first" {
			logicalOperation = "summary"
		}
		req.LogicalKey = fmt.Sprintf("%s:%d:%x", logicalOperation, id,
			sha256.Sum256([]byte(req.Provider+"\n"+q.AI.Model+"\n"+sourceKey)))
	}
	permitted, _, err := aibudget.ForConfig(q.DB, cfg).Start(ctx, req, now, func(tx *gorm.DB, request *aibudget.Request) error {
		if err := checked(fence(tx, job, now.UnixMilli()).Update(storage.JobColumns.LeaseUntil, now.Add(time.Duration(q.LeaseSeconds)*time.Second).UnixMilli())); err != nil {
			return err
		}
		if !request.Manual {
			paused, err := storage.BooleanSetting(ctx, tx, storage.AutoAIResumeKey)
			if err != nil {
				return err
			}
			if paused {
				return errAdmissionChanged
			}
		}
		if job.Type != ReportJob {
			d, err := q.checkAdmission(tx, job, now)
			if err != nil {
				return err
			}
			if d.State != admission.Pass {
				return errAdmissionChanged
			}
			request.Lane = d.Lane
		} else if len(reportIDs) != 0 {
			var count int64
			if err := tx.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).
				Where("repositories.repo_id IN ?", reportIDs).Count(&count).Error; err != nil {
				return err
			}
			if count != int64(len(reportIDs)) {
				return errAdmissionChanged
			}
		}
		return checked(tx.Model(&call).Where(storage.AICallColumns.Status + "='" + consts.StatusReserved + "'").Updates(map[string]any{storage.AICallColumns.Status: consts.StatusSending, storage.AICallColumns.UpdatedAt: time.Now().UnixMilli()}))
	})
	return permitted, err
}

// sendingFailure 把竞争耗尽的通道延期，已失去资格的任务关闭且不发送。
func (q Queue) sendingFailure(ctx context.Context, job storage.Job, call storage.AICall, err error) error {
	now := time.Now()
	switch {
	case errors.Is(err, errBudget):
		next := budgetRetryAt(job, now)
		if !job.IsManual() && now.UTC().Hour() < 12 {
			next = admission.DayStart(now).Add(12 * time.Hour).UnixMilli()
		}
		return q.settle(ctx, job, outcome{status: consts.StatusFailed, code: consts.CodeAIBudgetExhausted, next: next, attempts: job.Attempts - 1,
			apply: func(tx *gorm.DB) error {
				return tx.Model(&call).Where("status = ?", consts.StatusReserved).
					Updates(map[string]any{"status": consts.StatusFailed, "error_code": consts.CodeReservationUnused, "updated_at": now.UnixMilli()}).Error
			}})
	case errors.Is(err, errAdmissionChanged):
		if job.Type == ReportJob {
			return q.settle(ctx, job, outcome{status: consts.StatusCanceled, code: codeReportEvidenceChanged, attempts: max(0, job.Attempts-1)})
		}
		d := admission.Decision{State: admission.Defer, Reason: "ai_admission_changed", RetryAt: now.Add(time.Hour).UnixMilli()}
		if job.RepoID != nil {
			var e error
			d, e = q.checkAdmission(q.DB.WithContext(ctx), job, now)
			if e != nil {
				return e
			}
			if d.State == admission.Pass {
				d = admission.Decision{State: admission.Defer, Reason: "ai_admission_changed", RetryAt: now.Add(time.Hour).UnixMilli()}
			}
		}
		return q.stopAdmission(ctx, job, d)
	case errors.Is(err, aibudget.ErrUncertain):
		return q.stopAdmission(ctx, job, admission.Decision{State: admission.Defer, Reason: consts.CodeAIDeliveryUnknown,
			RetryAt: now.Add(7 * 24 * time.Hour).UnixMilli()})
	default:
		return err
	}
}

type outcome struct {
	status, code string
	next         int64
	attempts     int
	call         *storage.AICall
	model        ModelResult
	duration     time.Duration
	apply        func(*gorm.DB) error
	// applyResult 允许一次已计费的写入在来源已更换时结束为取消。
	applyResult func(*gorm.DB) (bool, error)
}

// settle 将模型台账、摘要和任务终态一次提交，旧租约的写入整体回滚。
func (q Queue) settle(ctx context.Context, job storage.Job, o outcome) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owns(tx, job); err != nil {
			return err
		}
		now := time.Now()
		ms := now.UnixMilli()
		if o.call != nil {
			callStatus := consts.StatusSucceeded
			if o.model.Code != "" {
				callStatus = consts.StatusFailed
			}
			if o.model.Unknown {
				callStatus = consts.StatusUnknown
			}
			var cost *float64
			if o.model.InputTokens != nil && o.model.OutputTokens != nil &&
				(q.AI.Summary.InputCostPerM > 0 || q.AI.Summary.OutputCostPerM > 0) {
				value := (float64(*o.model.InputTokens)*q.AI.Summary.InputCostPerM + float64(*o.model.OutputTokens)*q.AI.Summary.OutputCostPerM) / 1e6
				cost = &value
			}
			if err := checked(tx.Model(o.call).Where(storage.AICallColumns.Status + "='" + consts.StatusSending + "'").Updates(map[string]any{
				storage.AICallColumns.Status: callStatus, storage.AICallColumns.ErrorCode: optional(o.model.Code), storage.AICallColumns.UpdatedAt: ms,
				storage.AICallColumns.ProviderRequestID: optional(o.model.RequestID), storage.AICallColumns.InputTokens: o.model.InputTokens,
				storage.AICallColumns.OutputTokens: o.model.OutputTokens, storage.AICallColumns.EstimatedCost: cost, storage.AICallColumns.DurationMS: o.duration.Milliseconds(),
				storage.AICallColumns.OutputIssue: optional(o.model.OutputIssue),
			})); err != nil {
				return err
			}
			if o.model.Unsent {
				if err := aibudget.Unused(tx, o.call.CallKey, now); err != nil {
					return err
				}
			} else if err := aibudget.Settle(tx, o.call.CallKey, callStatus, o.model.InputTokens, o.model.OutputTokens, cost, o.duration, now); err != nil {
				return err
			}
		} else if o.status == consts.StatusSucceeded || o.status == consts.StatusDead || o.status == consts.StatusCanceled {
			if err := tx.Model(&storage.AICall{}).Where(storage.AICallColumns.JobID+"=? AND "+storage.AICallColumns.Status+"='"+consts.StatusReserved+"'", job.ID).
				Updates(map[string]any{storage.AICallColumns.Status: consts.StatusFailed, storage.AICallColumns.ErrorCode: consts.CodeReservationUnused, storage.AICallColumns.UpdatedAt: ms}).Error; err != nil {
				return err
			}
		}
		if o.apply != nil {
			if err := o.apply(tx); err != nil {
				return err
			}
		}
		if o.applyResult != nil {
			applied, err := o.applyResult(tx)
			if err != nil {
				return err
			}
			if !applied {
				o.status, o.code = consts.StatusCanceled, consts.CodeSummarySourceChanged
			}
		}
		if job.Type == JobType && (o.status == consts.StatusFailed || o.status == consts.StatusDead || o.status == consts.StatusUnknown) && o.attempts == job.Attempts {
			if err := q.recordFailure(tx, *job.RepoID, o.code, now, !jobTopicsOnly(job)); err != nil {
				return err
			}
		}
		return checked(fence(tx, job, time.Now().UnixMilli()).Updates(jobFields(o.status, o.code, ms, max(o.next, ms), o.attempts)))
	})
}
