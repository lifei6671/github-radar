package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

const MaintenanceInterrupted = "maintenance_interrupted"

// InterruptJob 仅由仍拥有租约的执行者收尾，发送中的调用和任务原子进入 unknown。
func InterruptJob(ctx context.Context, db *gorm.DB, job Job) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		at := now.UnixMilli()
		var call AICall
		if job.Type == "refresh_readme_summary" || job.Type == "translate_readme" {
			if err := tx.Find(&call, "job_id=? AND status IN ('reserved','sending','unknown')", job.ID).Error; err != nil {
				return err
			}
		}
		status, code := "failed", MaintenanceInterrupted
		if job.Attempts >= job.MaxAttempts {
			status = "dead"
		}
		if call.Status == "sending" || call.Status == "unknown" {
			status, code = "unknown", "ai_delivery_unknown"
			if err := tx.Model(&call).Updates(map[string]any{"status": "unknown", "error_code": code, "updated_at": at}).Error; err != nil {
				return err
			}
		}
		if status == "dead" && call.Status == "reserved" {
			if err := tx.Model(&call).Updates(map[string]any{"status": "failed", "error_code": code, "updated_at": at}).Error; err != nil {
				return err
			}
		}
		backoff := time.Duration(1<<min(job.Attempts, 8)+int(job.ID%17)) * time.Second
		next := max(job.AvailableAt, now.Add(backoff).UnixMilli())
		result := tx.Model(&Job{}).Where("id=? AND status='running' AND lease_token=? AND lease_until>?", job.ID, job.LeaseToken, time.Now().UnixMilli()).
			Updates(map[string]any{"status": status, "last_error_code": code, "lease_token": nil, "lease_until": nil, "available_at": next, "updated_at": at})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("排空时任务租约已失效")
		}
		return nil
	})
}
