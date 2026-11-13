package collectionstore

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// SetBlocked 按 GitHub 数字 ID 持久化屏蔽，并撤销尚未完成任务的写入资格。
func (q Queue) SetBlocked(ctx context.Context, id int64, blocked bool, now time.Time) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Take(&repo, storage.RepositoryColumns.RepoID+"=?", id).Error; err != nil {
			return err
		}
		if repo.Blocked == blocked {
			return nil
		}
		at := now.UnixMilli()
		values := map[string]any{storage.RepositoryColumns.Blocked: blocked, storage.RepositoryColumns.UpdatedAt: at}
		if blocked && repo.State == consts.StatusActive {
			values[storage.RepositoryColumns.State] = consts.StatusPaused
			if q.Cfg.ObservationPool {
				if err := storage.RecordObservation(tx, repo, consts.StatusPaused, repo.Grade, "blocked", at,
					metrics.ObservationEvidence{Stars: repo.Stars}); err != nil {
					return err
				}
			}
		}
		if blocked {
			values["observation_probe_due_at"] = 0
		}
		if !blocked {
			values[storage.RepositoryColumns.NextFetchAt] = at
			if q.Cfg.ObservationPool && repo.FirstNotFoundAt != nil {
				// 解除屏蔽后恢复尚未完成的公开身份复核。
				values["observation_probe_due_at"] = max(at, *repo.FirstNotFoundAt+metrics.Hour)
			}
		}
		if err := tx.Model(&repo).Updates(values).Error; err != nil {
			return err
		}
		if !blocked {
			repo.Blocked = false
			return q.admit(tx, &repo, at)
		}

		unfinished := tx.Model(&storage.Job{}).Select(storage.JobColumns.ID).Where(
			storage.JobColumns.RepoID+"=? AND "+storage.JobColumns.Type+" IN ? AND "+storage.JobColumns.Status+" IN ('"+consts.StatusPending+"','"+consts.StatusRunning+"','"+consts.StatusFailed+"')", id,
			[]string{consts.JobRefreshRepo, consts.JobRefreshSummary, consts.JobTranslateReadme, consts.JobStarHistory})
		// 请求已经发出的 AI 台账保留为待确认，解除屏蔽后仍需人工确认可能的计费。
		if err := storage.MarkBudgetUnknown(tx, tx.Model(&storage.AICall{}).Select("call_key").
			Where("job_id IN (?) AND status = ?", unfinished, consts.StatusSending), at); err != nil {
			return err
		}
		if err := tx.Model(&storage.AICall{}).Where(storage.AICallColumns.JobID+" IN (?) AND "+storage.AICallColumns.Status+"='"+consts.StatusSending+"'", unfinished).
			Updates(map[string]any{storage.AICallColumns.Status: consts.StatusUnknown, storage.AICallColumns.ErrorCode: consts.CodeAIDeliveryUnknown, storage.AICallColumns.UpdatedAt: at}).Error; err != nil {
			return err
		}
		if err := tx.Model(&storage.AICall{}).Where(storage.AICallColumns.JobID+" IN (?) AND "+storage.AICallColumns.Status+"='"+consts.StatusReserved+"'", unfinished).
			Updates(map[string]any{storage.AICallColumns.Status: consts.StatusFailed, storage.AICallColumns.ErrorCode: consts.CodeRepoBlocked, storage.AICallColumns.UpdatedAt: at}).Error; err != nil {
			return err
		}
		sent := tx.Model(&storage.AICall{}).Select("1").
			Where(storage.AICallColumns.JobID+"="+storage.JobTable+"."+storage.JobColumns.ID).
			Where(storage.AICallColumns.Status+"=?", consts.StatusUnknown)
		return tx.Model(&storage.Job{}).Where(storage.JobColumns.ID+" IN (?)", unfinished).Updates(map[string]any{
			storage.JobColumns.Status:        gorm.Expr("CASE WHEN EXISTS (?) THEN ? ELSE ? END", sent, consts.StatusUnknown, consts.StatusCanceled),
			storage.JobColumns.LastErrorCode: gorm.Expr("CASE WHEN EXISTS (?) THEN ? ELSE ? END", sent, consts.CodeAIDeliveryUnknown, consts.CodeRepoBlocked),
			storage.JobColumns.LeaseToken:    nil, storage.JobColumns.LeaseUntil: nil, storage.JobColumns.UpdatedAt: at,
		}).Error
	})
}
