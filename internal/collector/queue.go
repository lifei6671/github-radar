package collector

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
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

var ErrLeaseLost = errors.New("任务租约已失效")
var ErrCapacity = errors.New("监控容量已满")

// Payload 固化计划槽，跨窗口重试也不会重算槽。
type Payload struct {
	PlannedAt    int64  `json:"plannedAt"`
	RuleID       int64  `json:"ruleId,omitempty"`
	SourceKey    string `json:"sourceKey,omitempty"`
	Query        string `json:"query,omitempty"`
	Language     string `json:"language,omitempty"`
	Period       string `json:"period,omitempty"`
	Manual       bool   `json:"manual,omitempty"`
	Page         int    `json:"page,omitempty"`
	MinStars     int    `json:"minStars,omitempty"`
	MaxStars     int    `json:"maxStars,omitempty"`
	StarRange    bool   `json:"starRange,omitempty"`
	CreatedFrom  int64  `json:"createdFrom,omitempty"`
	CreatedUntil int64  `json:"createdUntil,omitempty"`
}

func Slot(ms int64) int64 { return ms / 300000 * 300000 }

type Queue struct {
	DB  *gorm.DB
	Cfg config.Scheduler
}

func (q Queue) enqueue(tx *gorm.DB, kind string, repoID *int64, key string, payload Payload, priority int, now int64) (storage.Job, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return storage.Job{}, err
	}
	job := storage.Job{
		Type: kind, RepoID: repoID, DedupKey: key, PayloadJSON: string(data),
		Priority: priority, Status: "pending", AvailableAt: now, MaxAttempts: 5,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).Create(&job).Error; err != nil {
		return storage.Job{}, err
	}
	if job.ID == 0 {
		err = tx.Take(&job, "dedup_key = ?", key).Error
	}
	return job, err
}

func cancelDisabledRules(tx *gorm.DB, now int64) error {
	return tx.Model(&storage.Job{}).
		Where("type IN ('discover_search','discover_trending')").
		Where("(status IN ('pending','failed') OR (status='running' AND lease_until<=?))", now).
		Where("CAST(json_extract(payload_json,'$.ruleId') AS INTEGER) IN (SELECT id FROM discovery_rules WHERE enabled=0)").
		Updates(map[string]any{"status": "canceled", "last_error_code": "source_disabled", "updated_at": now}).Error
}

// Schedule 在短事务内建立到期任务；运行中的任务不会被另一轮扫描重复投递。
func (q Queue) Schedule(ctx context.Context, now time.Time) error {
	ms := now.UnixMilli()
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := cancelDisabledRules(tx, ms); err != nil {
			return err
		}
		for _, grade := range []string{"S", "A", "B", "C"} {
			if err := tx.Model(&storage.Repository{}).Where("grade=? AND state='active'", grade).
				Where("repo_id IN (SELECT repo_id FROM jobs WHERE type='refresh_repo' AND status='running' AND lease_until<=? AND attempts>=max_attempts)", ms).
				Update("next_fetch_at", now.Add(q.Cfg.Interval(grade)).UnixMilli()).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&storage.Job{}).Where("type IN ? AND status = 'running' AND lease_until <= ? AND attempts >= max_attempts",
			[]string{"refresh_repo", "discover_trending", "discover_search"}, ms).
			Updates(map[string]any{"status": "dead", "last_error_code": "lease_expired", "updated_at": ms}).Error; err != nil {
			return err
		}
		for _, grade := range []string{"S", "A", "B", "C"} {
			// 积压任务跨过当前采样周期时，取消旧槽，按当前扫描重建。
			query := tx.Model(&storage.Job{}).Where("type = 'refresh_repo' AND dedup_key LIKE 'auto:%'").
				Where("(status IN ('pending','failed') OR (status = 'running' AND lease_until <= ?))", ms).
				Where("repo_id IN (SELECT repo_id FROM repositories WHERE grade = ?)", grade).
				Where("CAST(json_extract(payload_json,'$.plannedAt') AS INTEGER) < ?", now.Add(-q.Cfg.Interval(grade)).UnixMilli())
			if err := query.Updates(map[string]any{"status": "canceled", "last_error_code": "stale_plan", "updated_at": ms}).Error; err != nil {
				return err
			}
		}
		var repos []storage.Repository
		if err := tx.Where("state = 'active' AND blocked=0 AND next_fetch_at <= ?", ms).
			Where("NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.repo_id = repositories.repo_id AND jobs.type = 'refresh_repo' AND jobs.status IN ('pending','running','failed'))").
			Order("CASE grade WHEN 'S' THEN 0 WHEN 'A' THEN 1 WHEN 'B' THEN 2 ELSE 3 END, next_fetch_at, repo_id").
			Limit(500).Find(&repos).Error; err != nil {
			return err
		}
		for _, repo := range repos {
			key := fmt.Sprintf("auto:refresh_repo:%d:%d", repo.RepoID, Slot(ms))
			if _, err := q.enqueue(tx, "refresh_repo", &repo.RepoID, key, Payload{PlannedAt: ms}, gradePriority(repo.Grade), ms); err != nil {
				return err
			}
		}
		var rules []storage.DiscoveryRule
		if err := tx.Where("enabled = 1 AND next_run_at <= ?", ms).
			Where("NOT EXISTS (SELECT 1 FROM jobs WHERE type IN ('discover_search','discover_trending') AND CAST(json_extract(payload_json,'$.ruleId') AS INTEGER) = discovery_rules.id AND status IN ('pending','running','failed'))").
			Order("next_run_at, id").Find(&rules).Error; err != nil {
			return err
		}
		for _, rule := range rules {
			payload := Payload{PlannedAt: ms, RuleID: rule.ID, SourceKey: rule.SourceKey, Query: rule.Query, Page: 1}
			if rule.SourceType == "trending" {
				source, language, period, err := config.TrendingSource(rule.SourceKey)
				if err != nil {
					return err
				}
				payload = Payload{RuleID: rule.ID, PlannedAt: ms, SourceKey: source, Language: language, Period: period}
			}
			key := fmt.Sprintf("auto:discover_%s:%d:%d", rule.SourceType, rule.ID, Slot(ms))
			job, err := q.enqueue(tx, "discover_"+rule.SourceType, nil, key, payload, rule.Priority, ms)
			if err != nil {
				return err
			}
			nextRun := now.Add(time.Duration(rule.IntervalMinutes) * time.Minute).UnixMilli()
			if job.Status != "pending" {
				// 同槽旧规则任务可能已被撤销，下一槽重新安排新定义。
				nextRun = Slot(ms) + 300000
			}
			if err := tx.Model(&rule).Update("next_run_at", nextRun).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func gradePriority(grade string) int {
	return map[string]int{"S": 100, "A": 80, "B": 40, "C": 20}[grade]
}

// Claim 单条 UPDATE RETURNING 原子领取任务，过期租约可由新 token 接管。
func (q Queue) Claim(ctx context.Context, now time.Time) (*storage.Job, error) {
	var job storage.Job
	err := q.DB.WithContext(ctx).Raw(`
UPDATE jobs SET status='running', attempts=attempts+1, lease_token=?, lease_until=?, updated_at=?
WHERE id=(SELECT id FROM jobs
  WHERE type IN ('refresh_repo','discover_trending','discover_search')
    AND ((type='refresh_repo' AND EXISTS (SELECT 1 FROM repositories r WHERE r.repo_id=jobs.repo_id AND r.blocked=0))
      OR (type<>'refresh_repo' AND EXISTS (
        SELECT 1 FROM discovery_rules r WHERE r.id=CAST(json_extract(jobs.payload_json,'$.ruleId') AS INTEGER) AND r.enabled=1)))
    AND attempts < max_attempts AND available_at <= ?
    AND (status IN ('pending','failed') OR (status='running' AND lease_until <= ?))
  ORDER BY priority DESC, available_at, id LIMIT 1)
RETURNING *`, rand.Text(), now.Add(time.Duration(q.Cfg.LeaseSeconds)*time.Second).UnixMilli(),
		now.UnixMilli(), now.UnixMilli(), now.UnixMilli()).Scan(&job).Error
	if err != nil || job.ID == 0 {
		return nil, err
	}
	return &job, nil
}

func (q Queue) Renew(ctx context.Context, job storage.Job, now time.Time) error {
	result := q.fenced(q.DB.WithContext(ctx), job, now.UnixMilli()).
		Update("lease_until", now.Add(time.Duration(q.Cfg.LeaseSeconds)*time.Second).UnixMilli())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (q Queue) fenced(tx *gorm.DB, job storage.Job, now int64) *gorm.DB {
	return tx.Model(&storage.Job{}).Where("id = ? AND status = 'running' AND lease_token = ? AND lease_until > ?",
		job.ID, job.LeaseToken, now)
}

// Complete 同事务校验租约、写业务数据并结束任务，旧执行者的结果会整体回滚。
func (q Queue) Complete(ctx context.Context, job storage.Job, apply func(*gorm.DB) error) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := q.fenced(tx, job, time.Now().UnixMilli()).Update("updated_at", time.Now().UnixMilli())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrLeaseLost
		}
		if err := apply(tx); err != nil {
			return err
		}
		result = q.fenced(tx, job, time.Now().UnixMilli()).Updates(map[string]any{
			"status": "succeeded", "lease_token": nil, "lease_until": nil, "last_error_code": nil, "updated_at": time.Now().UnixMilli(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

// Fail 把恢复时间交给持久队列，不在 Worker 中长时间睡眠。
func (q Queue) Fail(ctx context.Context, job storage.Job, cause error, now time.Time) error {
	code, next := "collection_failed", now.Add(time.Duration(1<<min(job.Attempts, 8))*time.Second+time.Duration(job.ID%17)*time.Second).UnixMilli()
	attempts, status := job.Attempts, "failed"
	var upstream *gh.Failure
	if errors.As(cause, &upstream) {
		code = upstream.Code
		if upstream.Until > 0 {
			next = max(next, upstream.Until)
			// 本地等待没有发出请求，不占重试次数；真实限流响应仍受上限约束。
			if upstream.Status == 0 {
				attempts--
			}
		}
	}
	if attempts >= job.MaxAttempts {
		status = "dead"
	}
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := q.fenced(tx, job, now.UnixMilli()).Updates(map[string]any{
			"status": status, "attempts": attempts, "available_at": next, "last_error_code": code,
			"lease_token": nil, "lease_until": nil, "updated_at": now.UnixMilli(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrLeaseLost
		}
		if status != "dead" || job.RepoID == nil {
			return nil
		}
		var repo storage.Repository
		if err := tx.Take(&repo, "repo_id = ?", *job.RepoID).Error; err != nil {
			return err
		}
		return tx.Model(&repo).Where("state='active'").Update("next_fetch_at", now.Add(q.Cfg.Interval(repo.Grade)).UnixMilli()).Error
	})
}
