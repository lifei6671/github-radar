package summary

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

var errBudget = errors.New("当日模型调用额度已用完")

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
		var count int64
		if err := tx.Model(&storage.AICall{}).Where(storage.AICallColumns.Origin+"=? AND "+storage.AICallColumns.ReservedAt+">=? AND "+storage.AICallColumns.ReservedAt+"<?",
			origin, start.UnixMilli(), start.Add(24*time.Hour).UnixMilli()).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit) {
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

func (q Queue) sending(ctx context.Context, job storage.Job, call storage.AICall) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := checked(fence(tx, job, now.UnixMilli()).Update(storage.JobColumns.LeaseUntil, now.Add(time.Duration(q.LeaseSeconds)*time.Second).UnixMilli())); err != nil {
			return err
		}
		return checked(tx.Model(&call).Where(storage.AICallColumns.Status + "='" + consts.StatusReserved + "'").Updates(map[string]any{storage.AICallColumns.Status: consts.StatusSending, storage.AICallColumns.UpdatedAt: time.Now().UnixMilli()}))
	})
}

type outcome struct {
	status, code string
	next         int64
	attempts     int
	call         *storage.AICall
	model        ModelResult
	duration     time.Duration
	apply        func(*gorm.DB) error
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
			})); err != nil {
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
		if job.Type == JobType && (o.status == consts.StatusFailed || o.status == consts.StatusDead || o.status == consts.StatusUnknown) && o.attempts == job.Attempts {
			if err := q.recordFailure(tx, *job.RepoID, o.code, now); err != nil {
				return err
			}
		}
		return checked(fence(tx, job, time.Now().UnixMilli()).Updates(jobFields(o.status, o.code, ms, max(o.next, ms), o.attempts)))
	})
}
