package summary

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

const JobType = consts.JobRefreshSummary
const TranslationJob = consts.JobTranslateReadme

var unresolved = "('" + consts.StatusPending + "','" + consts.StatusRunning + "','" + consts.StatusFailed + "','" + consts.StatusUnknown + "')"

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

func manualKey(jobType string, repoID int64, requestID string) string {
	if jobType == TranslationJob {
		return fmt.Sprintf(consts.ReadmeManualDedupFormat, repoID, requestID)
	}
	return fmt.Sprintf(consts.SummaryManualDedupFormat, repoID, requestID)
}

func (q Queue) matches(summary storage.AISummary, fingerprint string) bool {
	return summary.RepoID != 0 && fingerprint != "" && summary.SourceFingerprint == fingerprint &&
		summary.ModelProvider == ProviderKey(q.AI) && summary.ModelName == q.AI.Model && summary.PromptVersion == q.AI.Summary.PromptVersion
}

func (q Queue) matchesTopicModel(topics storage.RepoAITopics) bool {
	return topics.RepoID != 0 &&
		topics.ModelProvider == ProviderKey(q.AI) && topics.ModelName == q.AI.Model && topics.PromptVersion == q.AI.Summary.PromptVersion
}

// Manual 先处理请求幂等与未决任务，再判断当前来源缓存是否仍有效。
func (q Queue) Manual(ctx context.Context, id int64, requestID string, force, available bool, now time.Time) (Decision, error) {
	return q.manual(ctx, JobType, id, requestID, force, available, now)
}

func (q Queue) ManualTranslation(ctx context.Context, id int64, requestID string, available bool, now time.Time) (Decision, error) {
	return q.manual(ctx, TranslationJob, id, requestID, false, available, now)
}

func (q Queue) manual(ctx context.Context, jobType string, id int64, requestID string, force, available bool, now time.Time) (Decision, error) {
	var d Decision
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		key := manualKey(jobType, id, requestID)
		if err := tx.Find(&d.Job, storage.JobColumns.DedupKey+"=?", key).Error; err != nil || d.Job.ID != 0 {
			return err
		}
		var repo storage.Repository
		if err := tx.Take(&repo, storage.RepositoryColumns.RepoID+"=?", id).Error; err != nil {
			return err
		}
		if repo.Blocked {
			return storage.ErrRepositoryBlocked
		}
		if err := tx.Find(&d.Job, storage.JobColumns.Type+"=? AND "+storage.JobColumns.RepoID+"=? AND "+storage.JobColumns.Status+" IN "+unresolved, jobType, id).Error; err != nil {
			return err
		}
		if d.Job.ID != 0 {
			d.Conflict = true
			return nil
		}
		if jobType == JobType && !force {
			var check storage.SummaryCheck
			if err := tx.Find(&check, storage.SummaryCheckColumns.RepoID+"=?", id).Error; err != nil {
				return err
			}
			var cached storage.AISummary
			if err := tx.Find(&cached, storage.AISummaryColumns.RepoID+"=?", id).Error; err != nil {
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
			if check.NextCheckAt > now.UnixMilli() && cached.SummaryStatus == consts.StatusReady &&
				check.SourceFingerprint == fingerprint && q.matches(cached, fingerprint) &&
				(!available || cached.RecommendationReason != nil) {
				d.Cached = !available || len(repo.GitHubTopics()) > 0
				if !d.Cached {
					var topics storage.RepoAITopics
					if err := tx.Find(&topics, storage.RepoAITopicsColumns.RepoID+"=?", id).Error; err != nil {
						return err
					}
					d.Cached = q.matchesTopicModel(topics) &&
						strings.HasPrefix(topics.SourceFingerprint, fingerprint+"|")
					if d.Cached && repo.LastSuccessAt != nil && *repo.LastSuccessAt >= check.LastCheckedAt {
						description := ""
						if repo.Description != nil {
							description = *repo.Description
						}
						d.Cached = topics.SourceFingerprint == topicsFingerprint(fingerprint, description)
					}
				}
				if d.Cached {
					return nil
				}
			}
		}
		if repo.Disabled || !repo.NameIsCurrent || repo.State == consts.StatusUnavailable {
			return ErrRepoUnavailable
		}
		if !available {
			return ErrUnavailable
		}
		job, err := q.insert(tx, jobType, id, key, payload{Manual: true, Force: force}, 200, now.UnixMilli())
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
		if err := tx.Take(&old, storage.JobColumns.ID+"=? AND "+storage.JobColumns.Type+" IN (?,?)", id, JobType, TranslationJob).Error; err != nil {
			return err
		}
		if old.RepoID == nil {
			return ErrRepoUnavailable
		}
		key := manualKey(old.Type, *old.RepoID, requestID)
		if err := tx.Find(&next, storage.JobColumns.DedupKey+"=?", key).Error; err != nil {
			return err
		}
		if next.ID != 0 {
			if next.ID == old.ID {
				return ErrNotUnknown
			}
			return nil
		}
		if old.Status != consts.StatusUnknown {
			return ErrNotUnknown
		}
		if !available {
			return ErrUnavailable
		}
		var repo storage.Repository
		if err := tx.Take(&repo, storage.RepositoryColumns.RepoID+"=?", *old.RepoID).Error; err != nil {
			return err
		}
		if repo.Blocked {
			return storage.ErrRepositoryBlocked
		}
		if repo.Disabled || !repo.NameIsCurrent || repo.State == consts.StatusUnavailable {
			return ErrRepoUnavailable
		}
		ms := now.UnixMilli()
		if err := tx.Model(&storage.AICall{}).Where(storage.AICallColumns.JobID+"=? AND "+storage.AICallColumns.Status+"='"+consts.StatusUnknown+"'", id).
			Updates(map[string]any{storage.AICallColumns.Status: consts.StatusFailed, storage.AICallColumns.ErrorCode: consts.CodeUnknownAcknowledged, storage.AICallColumns.UpdatedAt: ms}).Error; err != nil {
			return err
		}
		if err := tx.Model(&old).Updates(map[string]any{storage.JobColumns.Status: consts.StatusCanceled, storage.JobColumns.LastErrorCode: consts.CodeUnknownAcknowledged, storage.JobColumns.UpdatedAt: ms}).Error; err != nil {
			return err
		}
		var err error
		next, err = q.insert(tx, old.Type, *old.RepoID, key, payload{Manual: true, Force: true}, 200, ms)
		return err
	})
	return next, err
}

func (q Queue) insert(tx *gorm.DB, jobType string, id int64, key string, p payload, priority int, now int64) (storage.Job, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return storage.Job{}, err
	}
	job := storage.Job{Type: jobType, RepoID: &id, DedupKey: key, PayloadJSON: string(data), Priority: priority,
		Status: consts.StatusPending, AvailableAt: now, MaxAttempts: q.AI.Summary.MaxAttempts, CreatedAt: now, UpdatedAt: now}
	err = tx.Create(&job).Error
	return job, err
}

// Recover 收口过期租约。sending 表示请求可能已经计费，不能重新领取。
func (q Queue) Recover(ctx context.Context, now time.Time) error {
	ms := now.UnixMilli()
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf(`
UPDATE %[1]s SET %[2]s='%[3]s',%[4]s='%[5]s',%[6]s=?
WHERE %[2]s='%[7]s' AND %[8]s IN (
 SELECT %[9]s FROM %[10]s WHERE %[11]s IN (?,?) AND %[12]s='%[13]s' AND %[14]s<=?
)`,
			storage.AICallTable, storage.AICallColumns.Status, consts.StatusUnknown, storage.AICallColumns.ErrorCode, consts.CodeAIDeliveryUnknown, storage.AICallColumns.UpdatedAt, consts.StatusSending, storage.AICallColumns.JobID, storage.JobColumns.ID, storage.JobTable, storage.JobColumns.Type, storage.JobColumns.Status, consts.StatusRunning, storage.JobColumns.LeaseUntil), ms, JobType, TranslationJob, ms).Error; err != nil {
			return err
		}
		unknown := tx.Model(&storage.Job{}).Where(fmt.Sprintf("%[1]s IN (?,?) AND %[2]s='%[3]s' AND %[4]s<=? AND EXISTS (SELECT 1 FROM %[5]s a WHERE a.%[6]s=%[7]s.%[8]s AND a.%[9]s='%[10]s')",
			storage.JobColumns.Type, storage.JobColumns.Status, consts.StatusRunning, storage.JobColumns.LeaseUntil, storage.AICallTable, storage.AICallColumns.JobID, storage.JobTable, storage.JobColumns.ID, storage.AICallColumns.Status, consts.StatusUnknown), JobType, TranslationJob, ms)
		var affected []storage.Job
		if err := unknown.Find(&affected).Error; err != nil {
			return err
		}
		for _, job := range affected {
			if err := tx.Model(&job).Updates(jobFields(consts.StatusUnknown, consts.CodeAIDeliveryUnknown, ms, ms, job.Attempts)).Error; err != nil {
				return err
			}
			if job.Type == JobType {
				if err := q.recordFailure(tx, *job.RepoID, consts.CodeAIDeliveryUnknown, now); err != nil {
					return err
				}
			}
		}
		// 未发送的预留可由后续领取复用。重试耗尽时关闭保留记录。
		if err := tx.Exec(fmt.Sprintf(`
UPDATE %[1]s SET %[2]s='%[3]s',%[4]s='%[5]s',%[6]s=?
WHERE %[2]s='%[7]s' AND %[8]s IN (
 SELECT %[9]s FROM %[10]s WHERE %[11]s IN (?,?) AND %[12]s='%[13]s' AND %[14]s<=? AND %[15]s>=%[16]s
)`,
			storage.AICallTable, storage.AICallColumns.Status, consts.StatusFailed, storage.AICallColumns.ErrorCode, consts.CodeLeaseExpired, storage.AICallColumns.UpdatedAt, consts.StatusReserved, storage.AICallColumns.JobID, storage.JobColumns.ID, storage.JobTable, storage.JobColumns.Type, storage.JobColumns.Status, consts.StatusRunning, storage.JobColumns.LeaseUntil, storage.JobColumns.Attempts, storage.JobColumns.MaxAttempts), ms, JobType, TranslationJob, ms).Error; err != nil {
			return err
		}
		var expired []storage.Job
		if err := tx.Where(storage.JobColumns.Type+" IN (?,?) AND "+storage.JobColumns.Status+"='"+consts.StatusRunning+"' AND "+storage.JobColumns.LeaseUntil+"<=? AND "+storage.JobColumns.Attempts+">="+storage.JobColumns.MaxAttempts, JobType, TranslationJob, ms).Find(&expired).Error; err != nil {
			return err
		}
		for _, job := range expired {
			if err := tx.Model(&job).Updates(jobFields(consts.StatusDead, consts.CodeLeaseExpired, ms, ms, job.Attempts)).Error; err != nil {
				return err
			}
			if job.Type == JobType {
				if err := q.recordFailure(tx, *job.RepoID, consts.CodeLeaseExpired, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (q Queue) claim(ctx context.Context, now time.Time) (*storage.Job, error) {
	var job storage.Job
	err := q.DB.WithContext(ctx).Raw(fmt.Sprintf(`
UPDATE %[1]s SET %[2]s='%[3]s',%[4]s=%[4]s+1,%[5]s=?,%[6]s=?,%[7]s=?
WHERE %[8]s=(SELECT %[8]s FROM %[1]s WHERE %[9]s IN (?,?) AND %[4]s<%[10]s AND %[11]s<=?
AND (%[2]s IN ('%[12]s','%[13]s') OR (%[2]s='%[3]s' AND %[6]s<=?))
AND EXISTS(SELECT 1 FROM %[14]s r WHERE r.%[15]s=%[1]s.%[16]s AND r.%[17]s=0)
AND (json_extract(%[1]s.%[18]s,'$.manual')=1 OR NOT EXISTS(
 SELECT 1 FROM %[19]s s WHERE s.%[20]s=? AND json_extract(s.%[21]s,'$')=1))
AND NOT EXISTS(SELECT 1 FROM %[22]s a WHERE a.%[23]s=%[1]s.%[8]s AND a.%[24]s IN ('%[25]s','%[26]s'))
ORDER BY %[27]s DESC,%[11]s,%[8]s LIMIT 1) RETURNING *`,
		storage.JobTable, storage.JobColumns.Status, consts.StatusRunning, storage.JobColumns.Attempts, storage.JobColumns.LeaseToken, storage.JobColumns.LeaseUntil, storage.JobColumns.UpdatedAt, storage.JobColumns.ID, storage.JobColumns.Type, storage.JobColumns.MaxAttempts, storage.JobColumns.AvailableAt, consts.StatusPending, consts.StatusFailed, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.JobColumns.RepoID, storage.RepositoryColumns.Blocked, storage.JobColumns.PayloadJSON, storage.SystemSettingTable, storage.SystemSettingColumns.Key, storage.SystemSettingColumns.ValueJSON, storage.AICallTable, storage.AICallColumns.JobID, storage.AICallColumns.Status, consts.StatusSending, consts.StatusUnknown, storage.JobColumns.Priority),
		rand.Text(), now.Add(time.Duration(q.LeaseSeconds)*time.Second).UnixMilli(), now.UnixMilli(), JobType, TranslationJob, now.UnixMilli(), now.UnixMilli(), storage.AutoAIResumeKey).Scan(&job).Error
	if err != nil || job.ID == 0 {
		return nil, err
	}
	return &job, nil
}

func fence(tx *gorm.DB, job storage.Job, now int64) *gorm.DB {
	return tx.Model(&storage.Job{}).Where(storage.JobColumns.ID+"=? AND "+storage.JobColumns.Status+"='"+consts.StatusRunning+"' AND "+storage.JobColumns.LeaseToken+"=? AND "+storage.JobColumns.LeaseUntil+">?", job.ID, job.LeaseToken, now)
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
	return map[string]any{storage.JobColumns.Status: status, storage.JobColumns.LastErrorCode: optional(code), storage.JobColumns.LeaseToken: nil, storage.JobColumns.LeaseUntil: nil,
		storage.JobColumns.UpdatedAt: now, storage.JobColumns.AvailableAt: next, storage.JobColumns.Attempts: attempts}
}

func (q Queue) recordFailure(tx *gorm.DB, repoID int64, code string, now time.Time) error {
	if err := tx.Model(&storage.AISummary{}).Where(storage.AISummaryColumns.RepoID+"=?", repoID).Update(storage.AISummaryColumns.SummaryStatus, consts.StatusStale).Error; err != nil {
		return err
	}
	next := now.Add(time.Duration(q.AI.Summary.HighCheckHours) * time.Hour).UnixMilli()
	row := storage.SummaryCheck{RepoID: repoID, LastErrorCode: &code, NextCheckAt: next}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: storage.SummaryCheckColumns.RepoID}},
		DoUpdates: clause.AssignmentColumns([]string{storage.SummaryCheckColumns.LastErrorCode, storage.SummaryCheckColumns.NextCheckAt}),
	}).Create(&row).Error
}
