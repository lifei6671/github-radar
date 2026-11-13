package collectionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

const starHistoryManualPriority = 160

// scheduleStarHistory 优先安排近期收录且没有探测记录的仓库；失败或已有结果的仓库隔日再探测。
func (q Queue) scheduleStarHistory(tx *gorm.DB, now time.Time) error {
	busy := activeHistoryJobs(tx)
	var queued int64
	eligible := tx.Model(&storage.Repository{}).Select("repo_id").Where(storage.PublicRepositoryCondition)
	if err := busy.Session(&gorm.Session{}).Where("repo_id IN (?) AND priority < ?", eligible, starHistoryManualPriority).Count(&queued).Error; err != nil {
		return err
	}
	slots := 40 - int(queued)
	if slots <= 0 {
		return nil
	}
	var ids []int64
	if err := tx.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).
		Joins("LEFT JOIN repo_star_history_status h ON h.repo_id = repositories.repo_id").
		Where("(h.repo_id IS NULL OR h.checked_at <= ?)", now.Add(-24*time.Hour).UnixMilli()).
		Where("NOT EXISTS (?)", busy.Session(&gorm.Session{}).Select("1").Where("jobs.repo_id = repositories.repo_id")).
		Order("h.checked_at, repositories.discovered_at DESC, repositories.repo_id").Limit(slots).
		Pluck("repositories.repo_id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		key := fmt.Sprintf("auto:star_history:%d:%s", id, now.UTC().Format(time.DateOnly))
		if _, err := q.enqueue(tx, consts.JobStarHistory, &id, key,
			collection.Payload{PlannedAt: now.UnixMilli(), Page: 1}, 25, now.UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

func activeHistoryJobs(db *gorm.DB) *gorm.DB {
	return db.Model(&storage.Job{}).Where("type = ? AND status IN ?", consts.JobStarHistory,
		[]string{consts.StatusPending, consts.StatusRunning, consts.StatusFailed})
}

// RequestStarHistory 去重同仓库未决任务。管理员可在有限分页之外重复刷新当前页。
func (q Queue) RequestStarHistory(ctx context.Context, id int64, requestID string, now time.Time) (storage.Job, error) {
	var job storage.Job
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Where(storage.PublicRepositoryCondition).Take(&repo, "repo_id = ?", id).Error; err != nil {
			return err
		}
		key := fmt.Sprintf("manual:star_history:%d:%s", id, requestID)
		if err := tx.Find(&job, "dedup_key = ?", key).Error; err != nil || job.ID != 0 {
			return err
		}
		if err := activeHistoryJobs(tx).Where("repo_id = ?", id).Order("priority DESC, id").Limit(1).Find(&job).Error; err != nil {
			return err
		}
		if job.ID != 0 {
			if job.IsManual() {
				return nil
			}
			// 只有尚未运行的自动探测可让位给手动回补；保留其任务记录。
			if job.Status != consts.StatusRunning {
				if err := tx.Model(&job).Updates(map[string]any{"status": consts.StatusCanceled, "updated_at": now.UnixMilli()}).Error; err != nil {
					return err
				}
			}
		}
		var err error
		job, err = q.enqueue(tx, consts.JobStarHistory, &id, key,
			collection.Payload{PlannedAt: now.UnixMilli(), Manual: true, Page: 1}, starHistoryManualPriority, now.UnixMilli())
		return err
	})
	return job, err
}

// SaveStarHistory 在领取租约的事务中更新每周快照，并且只安排一个后续分页。
func (w writer) SaveStarHistory(job storage.Job, payload collection.Payload, change port.StarHistory) error {
	if change.Unavailable {
		id, at := *job.RepoID, change.FetchedAt
		// 与采样共用可见性字段；请求发出后若已有更晚的公开观测，则不能覆盖。
		query := w.db.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).
			Where("repo_id = ? AND (last_checked_at IS NULL OR last_checked_at <= ?) AND (last_success_at IS NULL OR last_success_at <= ?)", id, at, at)
		if w.queue.Cfg.ObservationPool {
			var repo storage.Repository
			if err := query.Session(&gorm.Session{}).Take(&repo).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if err := storage.RecordObservation(w.db, repo, consts.StatusUnavailable, repo.Grade, "github_unavailable", at,
				metrics.ObservationEvidence{Stars: repo.Stars}); err != nil {
				return err
			}
			if err := storage.UpdateObservationJobs(w.db, id, at, true, 0); err != nil {
				return err
			}
		}
		result := query.Updates(map[string]any{"state": consts.StatusUnavailable, "first_not_found_at": at, "last_checked_at": at,
			"updated_at": at, "observation_probe_due_at": 0})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		return recordStarHistoryFailure(w.db, job, consts.CodeGitHubGone, at)
	}
	if payload.Manual {
		// 已完成的管理员页覆盖自动探测需求；不再重试已失败或租约已过期的自动探测。
		if err := activeHistoryJobs(w.db).Where("repo_id = ? AND priority < ?", *job.RepoID, starHistoryManualPriority).
			Where("status <> ? OR lease_until <= ?", consts.StatusRunning, change.FetchedAt).
			Updates(map[string]any{"status": consts.StatusCanceled, "updated_at": change.FetchedAt, "lease_token": nil, "lease_until": nil}).Error; err != nil {
			return err
		}
	}
	var repo storage.Repository
	if err := w.db.Where(storage.PublicRepositoryCondition).Take(&repo, "repo_id = ?", *job.RepoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return storage.ErrRepositoryBlocked
		}
		return err
	}
	for _, week := range change.Weeks {
		days, err := json.Marshal(week.Days)
		if err != nil {
			return err
		}
		row := storage.RepoStarHistoryWeek{RepoID: repo.RepoID, WeekTimestamp: week.Week, DaysJSON: string(days), FetchedAt: week.FetchedAt}
		if err := w.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
			return err
		}
	}
	state := storage.RepoStarHistoryStatus{RepoID: repo.RepoID, CheckedAt: change.FetchedAt, FetchedAt: change.FetchedAt}
	fields := []string{"checked_at", "fetched_at", "last_error_code"}
	if payload.Page == 1 {
		fields = append(fields, "breakout_at", "breakout_json")
		if signal := metrics.OfficialBreakout(change.Weeks, change.FetchedAt, repo.GitHubCreatedAt); signal != nil {
			encoded, err := json.Marshal(signal)
			if err != nil {
				return err
			}
			state.BreakoutAt, state.BreakoutJSON = &signal.At, string(encoded)
			var local storage.LatestMetric
			if err := w.db.Select("breakout_json").Find(&local, "repo_id = ?", repo.RepoID).Error; err != nil {
				return err
			}
			if local.BreakoutJSON != "" {
				var observed metrics.Breakout
				if err := json.Unmarshal([]byte(local.BreakoutJSON), &observed); err != nil {
					return err
				}
				if observed.OverlapsOfficialDay(signal.At) {
					fields = append(fields, "confirmed_at")
					state.ConfirmedAt = &signal.At
				}
			}
		}
	}
	if err := w.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "repo_id"}},
		DoUpdates: clause.AssignmentColumns(fields)}).Create(&state).Error; err != nil {
		return err
	}
	if change.NextPage == 0 {
		return nil
	}
	payload.Page = change.NextPage
	payload.HistoryBefore = change.Weeks[len(change.Weeks)-1].Week
	key := fmt.Sprintf("%s:page:%d", job.DedupKey, payload.Page)
	_, err := w.queue.enqueue(w.db, consts.JobStarHistory, job.RepoID, key, payload, job.Priority, change.FetchedAt)
	return err
}

func recordStarHistoryFailure(tx *gorm.DB, job storage.Job, code string, now int64) error {
	row := storage.RepoStarHistoryStatus{RepoID: *job.RepoID, CheckedAt: now, LastErrorCode: &code}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "repo_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"checked_at", "last_error_code"})}).Create(&row).Error
}
