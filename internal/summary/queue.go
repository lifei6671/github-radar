package summary

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

const JobType = "refresh_readme_summary"
const unresolved = "('pending','running','failed','unknown')"

var ErrUnavailable = errors.New("摘要服务未配置")
var ErrRepoUnavailable = errors.New("仓库当前不可分析")
var ErrNotUnknown = errors.New("只有未决摘要任务可确认重试")
var errLease = errors.New("摘要任务租约已失效")

type Queue struct {
	DB           *gorm.DB
	AI           config.AI
	LeaseSeconds int
}

type payload struct {
	Manual bool `json:"manual,omitempty"`
	Force  bool `json:"force,omitempty"`
}

type Decision struct {
	Job              storage.Job
	Cached, Conflict bool
}

func manualKey(repoID int64, requestID string) string {
	return fmt.Sprintf("manual:summary:%d:%s", repoID, requestID)
}

func (q Queue) matches(summary storage.AISummary, fingerprint string) bool {
	return summary.RepoID != 0 && fingerprint != "" && summary.SourceFingerprint == fingerprint &&
		summary.ModelProvider == ProviderKey(q.AI) && summary.ModelName == q.AI.Model && summary.PromptVersion == q.AI.Summary.PromptVersion
}

// Manual 先处理请求幂等与未决任务，再判断当前来源缓存是否仍有效。
func (q Queue) Manual(ctx context.Context, id int64, requestID string, force, available bool, now time.Time) (Decision, error) {
	var d Decision
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		key := manualKey(id, requestID)
		if err := tx.Find(&d.Job, "dedup_key=?", key).Error; err != nil || d.Job.ID != 0 {
			return err
		}
		var repo storage.Repository
		if err := tx.Take(&repo, "repo_id=?", id).Error; err != nil {
			return err
		}
		if err := tx.Find(&d.Job, "type=? AND repo_id=? AND status IN "+unresolved, JobType, id).Error; err != nil {
			return err
		}
		if d.Job.ID != 0 {
			d.Conflict = true
			return nil
		}
		if !force {
			var check storage.SummaryCheck
			if err := tx.Find(&check, "repo_id=?", id).Error; err != nil {
				return err
			}
			var cached storage.AISummary
			if err := tx.Find(&cached, "repo_id=?", id).Error; err != nil {
				return err
			}
			fingerprint := check.SourceFingerprint
			// 新采集的元数据可以使无 README 摘要失效，包括简介被删除。
			if cached.ReadmeSHA == nil && repo.LastSuccessAt != nil && *repo.LastSuccessAt >= check.LastCheckedAt {
				description := ""
				if repo.Description != nil {
					description = *repo.Description
				}
				fingerprint = descriptionKey(description)
			}
			if check.NextCheckAt > now.UnixMilli() && cached.SummaryStatus == "ready" &&
				check.SourceFingerprint == fingerprint && q.matches(cached, fingerprint) {
				d.Cached = true
				return nil
			}
		}
		if repo.Disabled || !repo.NameIsCurrent || repo.State == "unavailable" {
			return ErrRepoUnavailable
		}
		if !available {
			return ErrUnavailable
		}
		job, err := q.insert(tx, id, key, payload{Manual: true, Force: force}, 200, now.UnixMilli())
		d.Job = job
		return err
	})
	return d, err
}

// RetryUnknown 只在确认可能再次计费后关闭旧台账并安排新的手动任务。
func (q Queue) RetryUnknown(ctx context.Context, id int64, requestID string, available bool, now time.Time) (storage.Job, error) {
	var next storage.Job
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old storage.Job
		if err := tx.Take(&old, "id=? AND type=?", id, JobType).Error; err != nil {
			return err
		}
		if old.RepoID == nil {
			return ErrRepoUnavailable
		}
		key := manualKey(*old.RepoID, requestID)
		if err := tx.Find(&next, "dedup_key=?", key).Error; err != nil {
			return err
		}
		if next.ID != 0 {
			if next.ID == old.ID {
				return ErrNotUnknown
			}
			return nil
		}
		if old.Status != "unknown" {
			return ErrNotUnknown
		}
		if !available {
			return ErrUnavailable
		}
		var repo storage.Repository
		if err := tx.Take(&repo, "repo_id=?", *old.RepoID).Error; err != nil {
			return err
		}
		if repo.Disabled || !repo.NameIsCurrent || repo.State == "unavailable" {
			return ErrRepoUnavailable
		}
		ms := now.UnixMilli()
		if err := tx.Model(&storage.AICall{}).Where("job_id=? AND status='unknown'", id).
			Updates(map[string]any{"status": "failed", "error_code": "unknown_acknowledged", "updated_at": ms}).Error; err != nil {
			return err
		}
		if err := tx.Model(&old).Updates(map[string]any{"status": "canceled", "last_error_code": "unknown_acknowledged", "updated_at": ms}).Error; err != nil {
			return err
		}
		var err error
		next, err = q.insert(tx, *old.RepoID, key, payload{Manual: true, Force: true}, 200, ms)
		return err
	})
	return next, err
}

func (q Queue) insert(tx *gorm.DB, id int64, key string, p payload, priority int, now int64) (storage.Job, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return storage.Job{}, err
	}
	job := storage.Job{Type: JobType, RepoID: &id, DedupKey: key, PayloadJSON: string(data), Priority: priority,
		Status: "pending", AvailableAt: now, MaxAttempts: q.AI.Summary.MaxAttempts, CreatedAt: now, UpdatedAt: now}
	err = tx.Create(&job).Error
	return job, err
}

// Recover 收口过期租约。sending 表示请求可能已经计费，不能重新领取。
func (q Queue) Recover(ctx context.Context, now time.Time) error {
	ms := now.UnixMilli()
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
UPDATE ai_calls SET status='unknown',error_code='ai_delivery_unknown',updated_at=?
WHERE status='sending' AND job_id IN (
 SELECT id FROM jobs WHERE type=? AND status='running' AND lease_until<=?
)`, ms, JobType, ms).Error; err != nil {
			return err
		}
		unknown := tx.Model(&storage.Job{}).Where("type=? AND status='running' AND lease_until<=? AND EXISTS (SELECT 1 FROM ai_calls a WHERE a.job_id=jobs.id AND a.status='unknown')", JobType, ms)
		var affected []storage.Job
		if err := unknown.Find(&affected).Error; err != nil {
			return err
		}
		for _, job := range affected {
			if err := tx.Model(&job).Updates(jobFields("unknown", "ai_delivery_unknown", ms, ms, job.Attempts)).Error; err != nil {
				return err
			}
			if err := q.recordFailure(tx, *job.RepoID, "ai_delivery_unknown", now); err != nil {
				return err
			}
		}
		// 未发送的预留可由后续领取复用。重试耗尽时关闭保留记录。
		if err := tx.Exec(`
UPDATE ai_calls SET status='failed',error_code='lease_expired',updated_at=?
WHERE status='reserved' AND job_id IN (
 SELECT id FROM jobs WHERE type=? AND status='running' AND lease_until<=? AND attempts>=max_attempts
)`, ms, JobType, ms).Error; err != nil {
			return err
		}
		var expired []storage.Job
		if err := tx.Where("type=? AND status='running' AND lease_until<=? AND attempts>=max_attempts", JobType, ms).Find(&expired).Error; err != nil {
			return err
		}
		for _, job := range expired {
			if err := tx.Model(&job).Updates(jobFields("dead", "lease_expired", ms, ms, job.Attempts)).Error; err != nil {
				return err
			}
			if err := q.recordFailure(tx, *job.RepoID, "lease_expired", now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (q Queue) claim(ctx context.Context, now time.Time) (*storage.Job, error) {
	var job storage.Job
	err := q.DB.WithContext(ctx).Raw(`
UPDATE jobs SET status='running',attempts=attempts+1,lease_token=?,lease_until=?,updated_at=?
WHERE id=(SELECT id FROM jobs WHERE type=? AND attempts<max_attempts AND available_at<=?
AND (status IN ('pending','failed') OR (status='running' AND lease_until<=?))
AND (json_extract(jobs.payload_json,'$.manual')=1 OR NOT EXISTS(
 SELECT 1 FROM system_settings s WHERE s.key=? AND json_extract(s.value_json,'$')=1))
AND NOT EXISTS(SELECT 1 FROM ai_calls a WHERE a.job_id=jobs.id AND a.status IN ('sending','unknown'))
ORDER BY priority DESC,available_at,id LIMIT 1) RETURNING *`,
		rand.Text(), now.Add(time.Duration(q.LeaseSeconds)*time.Second).UnixMilli(), now.UnixMilli(), JobType, now.UnixMilli(), now.UnixMilli(), storage.AutoAIResumeKey).Scan(&job).Error
	if err != nil || job.ID == 0 {
		return nil, err
	}
	return &job, nil
}

func fence(tx *gorm.DB, job storage.Job, now int64) *gorm.DB {
	return tx.Model(&storage.Job{}).Where("id=? AND status='running' AND lease_token=? AND lease_until>?", job.ID, job.LeaseToken, now)
}

func checked(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errLease
	}
	return nil
}

func owns(tx *gorm.DB, job storage.Job) error {
	var n int64
	err := fence(tx, job, time.Now().UnixMilli()).Count(&n).Error
	if err != nil {
		return err
	}
	if n != 1 {
		return errLease
	}
	return nil
}

func jobFields(status, code string, now, next int64, attempts int) map[string]any {
	return map[string]any{"status": status, "last_error_code": optional(code), "lease_token": nil, "lease_until": nil,
		"updated_at": now, "available_at": next, "attempts": attempts}
}

func (q Queue) recordFailure(tx *gorm.DB, repoID int64, code string, now time.Time) error {
	if err := tx.Model(&storage.AISummary{}).Where("repo_id=?", repoID).Update("summary_status", "stale").Error; err != nil {
		return err
	}
	next := now.Add(time.Duration(q.AI.Summary.HighCheckHours) * time.Hour).UnixMilli()
	row := storage.SummaryCheck{RepoID: repoID, LastErrorCode: &code, NextCheckAt: next}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repo_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_error_code", "next_check_at"}),
	}).Create(&row).Error
}
