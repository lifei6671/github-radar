package summary

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

var errBudget = errors.New("当日模型调用额度已用完")

// reserve 在 UTC 日预算内预留一次调用；复用尚未发送且在当前日的预留。
func (q Queue) reserve(ctx context.Context, job storage.Job, manual bool, now time.Time) (storage.AICall, error) {
	var call storage.AICall
	ms := now.UnixMilli()
	start := now.UTC().Truncate(24 * time.Hour)
	origin, limit := "auto", q.AI.Summary.AutoDailyCalls
	if manual {
		origin, limit = "manual", q.AI.Summary.ManualDailyCalls
	}
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owns(tx, job); err != nil {
			return err
		}
		if err := tx.Find(&call, "job_id=? AND status='reserved'", job.ID).Error; err != nil {
			return err
		}
		provider := ProviderKey(q.AI)
		if call.CallKey != "" {
			if call.ReservedAt >= start.UnixMilli() && call.Provider == provider {
				return nil
			}
			if err := tx.Model(&call).Updates(map[string]any{"status": "failed", "error_code": "reservation_expired", "updated_at": ms}).Error; err != nil {
				return err
			}
		}
		var count int64
		if err := tx.Model(&storage.AICall{}).Where("origin=? AND reserved_at>=? AND reserved_at<?",
			origin, start.UnixMilli(), start.Add(24*time.Hour).UnixMilli()).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit) {
			// 保留已关闭的旧预留，事务成功后由调用方延期。
			call = storage.AICall{}
			return nil
		}
		call = storage.AICall{CallKey: rand.Text(), JobID: job.ID, Provider: provider, Origin: origin, Status: "reserved", ReservedAt: ms, UpdatedAt: ms}
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
		if err := checked(fence(tx, job, now.UnixMilli()).Update("lease_until", now.Add(time.Duration(q.LeaseSeconds)*time.Second).UnixMilli())); err != nil {
			return err
		}
		return checked(tx.Model(&call).Where("status='reserved'").Updates(map[string]any{"status": "sending", "updated_at": time.Now().UnixMilli()}))
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
			callStatus := "succeeded"
			if o.model.Code != "" {
				callStatus = "failed"
			}
			if o.model.Unknown {
				callStatus = "unknown"
			}
			var cost *float64
			if o.model.InputTokens != nil && o.model.OutputTokens != nil &&
				(q.AI.Summary.InputCostPerM > 0 || q.AI.Summary.OutputCostPerM > 0) {
				value := (float64(*o.model.InputTokens)*q.AI.Summary.InputCostPerM + float64(*o.model.OutputTokens)*q.AI.Summary.OutputCostPerM) / 1e6
				cost = &value
			}
			if err := checked(tx.Model(o.call).Where("status='sending'").Updates(map[string]any{
				"status": callStatus, "error_code": optional(o.model.Code), "updated_at": ms,
				"provider_request_id": optional(o.model.RequestID), "input_tokens": o.model.InputTokens,
				"output_tokens": o.model.OutputTokens, "estimated_cost": cost, "duration_ms": o.duration.Milliseconds(),
			})); err != nil {
				return err
			}
		} else if o.status == "succeeded" || o.status == "dead" || o.status == "canceled" {
			if err := tx.Model(&storage.AICall{}).Where("job_id=? AND status='reserved'", job.ID).
				Updates(map[string]any{"status": "failed", "error_code": "reservation_unused", "updated_at": ms}).Error; err != nil {
				return err
			}
		}
		if o.apply != nil {
			if err := o.apply(tx); err != nil {
				return err
			}
		}
		if (o.status == "failed" || o.status == "dead" || o.status == "unknown") && o.attempts == job.Attempts {
			if err := q.recordFailure(tx, *job.RepoID, o.code, now); err != nil {
				return err
			}
		}
		return checked(fence(tx, job, time.Now().UnixMilli()).Updates(jobFields(o.status, o.code, ms, max(o.next, ms), o.attempts)))
	})
}
