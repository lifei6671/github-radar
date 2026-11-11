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
	"github.com/lifei6671/github-radar/internal/consts"
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
		Priority: priority, Status: consts.StatusPending, AvailableAt: now, MaxAttempts: 5,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: storage.JobColumns.DedupKey}}, DoNothing: true}).Create(&job).Error; err != nil {
		return storage.Job{}, err
	}
	if job.ID == 0 {
		err = tx.Take(&job, storage.JobColumns.DedupKey+" = ?", key).Error
	}
	return job, err
}

func cancelDisabledRules(tx *gorm.DB, now int64) error {
	return tx.Model(&storage.Job{}).
		Where(storage.JobColumns.Type+" IN ('"+consts.JobDiscoverSearch+"','"+consts.JobDiscoverTrending+"')").
		Where("("+storage.JobColumns.Status+" IN ('"+consts.StatusPending+"','"+consts.StatusFailed+"') OR ("+storage.JobColumns.Status+"='"+consts.StatusRunning+"' AND "+storage.JobColumns.LeaseUntil+"<=?))", now).
		Where("CAST(json_extract(" + storage.JobColumns.PayloadJSON + ",'$.ruleId') AS INTEGER) IN (SELECT " + storage.DiscoveryRuleColumns.ID + " FROM " + storage.DiscoveryRuleTable + " WHERE " + storage.DiscoveryRuleColumns.Enabled + "=0)").
		Updates(map[string]any{storage.JobColumns.Status: consts.StatusCanceled, storage.JobColumns.LastErrorCode: consts.CodeSourceDisabled, storage.JobColumns.UpdatedAt: now}).Error
}

// Schedule 在短事务内建立到期任务；运行中的任务不会被另一轮扫描重复投递。
func (q Queue) Schedule(ctx context.Context, now time.Time) error {
	ms := now.UnixMilli()
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := cancelDisabledRules(tx, ms); err != nil {
			return err
		}
		for _, grade := range []string{consts.GradeS, consts.GradeA, consts.GradeB, consts.GradeC} {
			if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.Grade+"=? AND "+storage.RepositoryColumns.State+"='"+consts.StatusActive+"'", grade).
				Where(fmt.Sprintf("%[1]s IN (SELECT %[2]s FROM %[3]s WHERE %[4]s='%[5]s' AND %[6]s='%[7]s' AND %[8]s<=? AND %[9]s>=%[10]s)",
					storage.RepositoryColumns.RepoID, storage.JobColumns.RepoID, storage.JobTable, storage.JobColumns.Type, consts.JobRefreshRepo, storage.JobColumns.Status, consts.StatusRunning, storage.JobColumns.LeaseUntil, storage.JobColumns.Attempts, storage.JobColumns.MaxAttempts), ms).
				Update(storage.RepositoryColumns.NextFetchAt, now.Add(q.Cfg.Interval(grade)).UnixMilli()).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&storage.Job{}).Where(storage.JobColumns.Type+" IN ? AND "+storage.JobColumns.Status+" = '"+consts.StatusRunning+"' AND "+storage.JobColumns.LeaseUntil+" <= ? AND "+storage.JobColumns.Attempts+" >= "+storage.JobColumns.MaxAttempts,
			[]string{consts.JobRefreshRepo, consts.JobDiscoverTrending, consts.JobDiscoverSearch}, ms).
			Updates(map[string]any{storage.JobColumns.Status: consts.StatusDead, storage.JobColumns.LastErrorCode: consts.CodeLeaseExpired, storage.JobColumns.UpdatedAt: ms}).Error; err != nil {
			return err
		}
		for _, grade := range []string{consts.GradeS, consts.GradeA, consts.GradeB, consts.GradeC} {
			// 积压任务跨过当前采样周期时，取消旧槽，按当前扫描重建。
			query := tx.Model(&storage.Job{}).Where(storage.JobColumns.Type+" = '"+consts.JobRefreshRepo+"' AND "+storage.JobColumns.DedupKey+" LIKE '"+consts.AutoJobPattern+"'").
				Where("("+storage.JobColumns.Status+" IN ('"+consts.StatusPending+"','"+consts.StatusFailed+"') OR ("+storage.JobColumns.Status+" = '"+consts.StatusRunning+"' AND "+storage.JobColumns.LeaseUntil+" <= ?))", ms).
				Where(storage.JobColumns.RepoID+" IN (SELECT "+storage.RepositoryColumns.RepoID+" FROM "+storage.RepositoryTable+" WHERE "+storage.RepositoryColumns.Grade+" = ?)", grade).
				Where("CAST(json_extract("+storage.JobColumns.PayloadJSON+",'$.plannedAt') AS INTEGER) < ?", now.Add(-q.Cfg.Interval(grade)).UnixMilli())
			if err := query.Updates(map[string]any{storage.JobColumns.Status: consts.StatusCanceled, storage.JobColumns.LastErrorCode: consts.CodeStalePlan, storage.JobColumns.UpdatedAt: ms}).Error; err != nil {
				return err
			}
		}
		var repos []storage.Repository
		if err := tx.Where(storage.RepositoryColumns.State+" = '"+consts.StatusActive+"' AND "+storage.RepositoryColumns.Blocked+"=0 AND "+storage.RepositoryColumns.NextFetchAt+" <= ?", ms).
			Where(fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %[1]s WHERE %[1]s.%[2]s = %[3]s.%[4]s AND %[1]s.%[5]s = '%[6]s' AND %[1]s.%[7]s IN ('%[8]s','%[9]s','%[10]s'))",
				storage.JobTable, storage.JobColumns.RepoID, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.JobColumns.Type, consts.JobRefreshRepo, storage.JobColumns.Status, consts.StatusPending, consts.StatusRunning, consts.StatusFailed)).
			Order("CASE " + storage.RepositoryColumns.Grade + " WHEN '" + consts.GradeS + "' THEN 0 WHEN '" + consts.GradeA + "' THEN 1 WHEN '" + consts.GradeB + "' THEN 2 ELSE 3 END, " + storage.RepositoryColumns.NextFetchAt + ", " + storage.RepositoryColumns.RepoID).
			Limit(500).Find(&repos).Error; err != nil {
			return err
		}
		for _, repo := range repos {
			key := fmt.Sprintf(consts.RefreshAutoDedupFormat, repo.RepoID, Slot(ms))
			if _, err := q.enqueue(tx, consts.JobRefreshRepo, &repo.RepoID, key, Payload{PlannedAt: ms}, gradePriority(repo.Grade), ms); err != nil {
				return err
			}
		}
		var rules []storage.DiscoveryRule
		if err := tx.Where(storage.DiscoveryRuleColumns.Enabled+" = 1 AND "+storage.DiscoveryRuleColumns.NextRunAt+" <= ?", ms).
			Where(fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %[1]s WHERE %[2]s IN ('%[3]s','%[4]s') AND CAST(json_extract(%[5]s,'$.ruleId') AS INTEGER) = %[6]s.%[7]s AND %[8]s IN ('%[9]s','%[10]s','%[11]s'))",
				storage.JobTable, storage.JobColumns.Type, consts.JobDiscoverSearch, consts.JobDiscoverTrending, storage.JobColumns.PayloadJSON, storage.DiscoveryRuleTable, storage.DiscoveryRuleColumns.ID, storage.JobColumns.Status, consts.StatusPending, consts.StatusRunning, consts.StatusFailed)).
			Order(storage.DiscoveryRuleColumns.NextRunAt + ", " + storage.DiscoveryRuleColumns.ID).Find(&rules).Error; err != nil {
			return err
		}
		for _, rule := range rules {
			payload := Payload{PlannedAt: ms, RuleID: rule.ID, SourceKey: rule.SourceKey, Query: rule.Query, Page: 1}
			if rule.SourceType == consts.SourceTrending {
				source, language, period, err := config.TrendingSource(rule.SourceKey)
				if err != nil {
					return err
				}
				payload = Payload{RuleID: rule.ID, PlannedAt: ms, SourceKey: source, Language: language, Period: period}
			}
			key := fmt.Sprintf(consts.DiscoveryAutoDedupFormat, rule.SourceType, rule.ID, Slot(ms))
			job, err := q.enqueue(tx, "discover_"+rule.SourceType, nil, key, payload, rule.Priority, ms)
			if err != nil {
				return err
			}
			nextRun := now.Add(time.Duration(rule.IntervalMinutes) * time.Minute).UnixMilli()
			if job.Status != consts.StatusPending {
				// 同槽旧规则任务可能已被撤销，下一槽重新安排新定义。
				nextRun = Slot(ms) + 300000
			}
			if err := tx.Model(&rule).Update(storage.DiscoveryRuleColumns.NextRunAt, nextRun).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func gradePriority(grade string) int {
	return map[string]int{consts.GradeS: 100, consts.GradeA: 80, consts.GradeB: 40, consts.GradeC: 20}[grade]
}

// Claim 单条 UPDATE RETURNING 原子领取任务，过期租约可由新 token 接管。
func (q Queue) Claim(ctx context.Context, now time.Time) (*storage.Job, error) {
	db := q.DB.WithContext(ctx)
	j, r, d := storage.JobColumns, storage.RepositoryColumns, storage.DiscoveryRuleColumns
	jobRepo := db.Model(&storage.Repository{}).Select("1").
		Where(r.RepoID+"="+storage.JobTable+"."+j.RepoID).
		Where(r.Blocked+"=?", false)
	jobRule := db.Model(&storage.DiscoveryRule{}).Select("1").
		Where(d.ID+"=CAST(json_extract("+storage.JobTable+"."+j.PayloadJSON+",'$.ruleId') AS INTEGER)").
		Where(d.Enabled+"=?", true)
	ms := now.UnixMilli()
	next := db.Model(&storage.Job{}).Select(j.ID).
		Where(j.Type+" IN ?", []string{consts.JobRefreshRepo, consts.JobDiscoverTrending, consts.JobDiscoverSearch}).
		Where("(("+j.Type+"=? AND EXISTS (?)) OR ("+j.Type+"<>? AND EXISTS (?)))", consts.JobRefreshRepo, jobRepo, consts.JobRefreshRepo, jobRule).
		Where(j.Attempts+"<"+j.MaxAttempts).Where(j.AvailableAt+"<=?", ms).
		Where("("+j.Status+" IN ? OR ("+j.Status+"=? AND "+j.LeaseUntil+"<=?))", []string{consts.StatusPending, consts.StatusFailed}, consts.StatusRunning, ms).
		Order(j.Priority + " DESC").Order(j.AvailableAt).Order(j.ID).Limit(1)
	var job storage.Job
	err := db.Raw("UPDATE "+storage.JobTable+" SET "+j.Status+"=?, "+j.Attempts+"="+j.Attempts+"+1, "+
		j.LeaseToken+"=?, "+j.LeaseUntil+"=?, "+j.UpdatedAt+"=? WHERE "+j.ID+"=(?) RETURNING *",
		consts.StatusRunning, rand.Text(), now.Add(time.Duration(q.Cfg.LeaseSeconds)*time.Second).UnixMilli(), ms, next).Scan(&job).Error
	if err != nil || job.ID == 0 {
		return nil, err
	}
	return &job, nil
}

func (q Queue) Renew(ctx context.Context, job storage.Job, now time.Time) error {
	result := q.fenced(q.DB.WithContext(ctx), job, now.UnixMilli()).
		Update(storage.JobColumns.LeaseUntil, now.Add(time.Duration(q.Cfg.LeaseSeconds)*time.Second).UnixMilli())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (q Queue) fenced(tx *gorm.DB, job storage.Job, now int64) *gorm.DB {
	return tx.Model(&storage.Job{}).Where(storage.JobColumns.ID+" = ? AND "+storage.JobColumns.Status+" = '"+consts.StatusRunning+"' AND "+storage.JobColumns.LeaseToken+" = ? AND "+storage.JobColumns.LeaseUntil+" > ?",
		job.ID, job.LeaseToken, now)
}

// Complete 同事务校验租约、写业务数据并结束任务，旧执行者的结果会整体回滚。
func (q Queue) Complete(ctx context.Context, job storage.Job, apply func(*gorm.DB) error) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := q.fenced(tx, job, time.Now().UnixMilli()).Update(storage.JobColumns.UpdatedAt, time.Now().UnixMilli())
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
			storage.JobColumns.Status: consts.StatusSucceeded, storage.JobColumns.LeaseToken: nil, storage.JobColumns.LeaseUntil: nil, storage.JobColumns.LastErrorCode: nil, storage.JobColumns.UpdatedAt: time.Now().UnixMilli(),
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
	code, next := consts.CodeCollectionFailed, now.Add(time.Duration(1<<min(job.Attempts, 8))*time.Second+time.Duration(job.ID%17)*time.Second).UnixMilli()
	attempts, status := job.Attempts, consts.StatusFailed
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
		status = consts.StatusDead
	}
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := q.fenced(tx, job, now.UnixMilli()).Updates(map[string]any{
			storage.JobColumns.Status: status, storage.JobColumns.Attempts: attempts, storage.JobColumns.AvailableAt: next, storage.JobColumns.LastErrorCode: code,
			storage.JobColumns.LeaseToken: nil, storage.JobColumns.LeaseUntil: nil, storage.JobColumns.UpdatedAt: now.UnixMilli(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrLeaseLost
		}
		if status != consts.StatusDead || job.RepoID == nil {
			return nil
		}
		var repo storage.Repository
		if err := tx.Take(&repo, storage.RepositoryColumns.RepoID+" = ?", *job.RepoID).Error; err != nil {
			return err
		}
		return tx.Model(&repo).Where(storage.RepositoryColumns.State+"='"+consts.StatusActive+"'").Update(storage.RepositoryColumns.NextFetchAt, now.Add(q.Cfg.Interval(repo.Grade)).UnixMilli()).Error
	})
}
