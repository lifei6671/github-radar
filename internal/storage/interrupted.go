package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
)

const MaintenanceInterrupted = consts.CodeMaintenanceInterrupted

// InterruptJob 仅由仍拥有租约的执行者收尾，发送中的调用和任务原子进入 unknown。
func InterruptJob(ctx context.Context, db *gorm.DB, job Job) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		at := now.UnixMilli()
		var call AICall
		if job.Type == consts.JobRefreshSummary || job.Type == consts.JobTranslateReadme {
			if err := tx.Find(&call, AICallColumns.JobID+"=? AND "+AICallColumns.Status+" IN ('"+consts.StatusReserved+"','"+consts.StatusSending+"','"+consts.StatusUnknown+"')", job.ID).Error; err != nil {
				return err
			}
		}
		status, code := consts.StatusFailed, MaintenanceInterrupted
		if job.Attempts >= job.MaxAttempts {
			status = consts.StatusDead
		}
		if call.Status == consts.StatusSending || call.Status == consts.StatusUnknown {
			status, code = consts.StatusUnknown, consts.CodeAIDeliveryUnknown
			if err := tx.Model(&call).Updates(map[string]any{AICallColumns.Status: consts.StatusUnknown, AICallColumns.ErrorCode: code, AICallColumns.UpdatedAt: at}).Error; err != nil {
				return err
			}
		}
		if status == consts.StatusDead && call.Status == consts.StatusReserved {
			if err := tx.Model(&call).Updates(map[string]any{AICallColumns.Status: consts.StatusFailed, AICallColumns.ErrorCode: code, AICallColumns.UpdatedAt: at}).Error; err != nil {
				return err
			}
		}
		backoff := time.Duration(1<<min(job.Attempts, 8)+int(job.ID%17)) * time.Second
		next := max(job.AvailableAt, now.Add(backoff).UnixMilli())
		result := tx.Model(&Job{}).Where(JobColumns.ID+"=? AND "+JobColumns.Status+"='"+consts.StatusRunning+"' AND "+JobColumns.LeaseToken+"=? AND "+JobColumns.LeaseUntil+">?", job.ID, job.LeaseToken, time.Now().UnixMilli()).
			Updates(map[string]any{JobColumns.Status: status, JobColumns.LastErrorCode: code, JobColumns.LeaseToken: nil, JobColumns.LeaseUntil: nil, JobColumns.AvailableAt: next, JobColumns.UpdatedAt: at})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("排空时任务租约已失效")
		}
		return nil
	})
}
