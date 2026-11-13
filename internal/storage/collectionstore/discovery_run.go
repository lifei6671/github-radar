package collectionstore

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/storage"
)

// enqueueRuleRun 固化原始查询和边界，子任务只继承计划，不根据执行日期重算。
func (q Queue) enqueueRuleRun(tx *gorm.DB, rule storage.DiscoveryRule, p collection.Payload, now time.Time, pass string) (storage.Job, error) {
	p.AgeWindow, p.RunRoot = rule.AgeWindow, true
	actualSlot := collection.Slot(now.UnixMilli())
	p.PlannedAt = collection.Slot(rule.NextRunAt)
	if p.PlannedAt <= 0 || pass == collection.DiscoveryBackfill {
		// 补扫由当天首次调度触发，独立于主扫描的逾期计划。
		p.PlannedAt = actualSlot
	}
	p.CreatedFrom, p.CreatedUntil, _ = collection.CreationWindow(rule.AgeWindow, time.UnixMilli(p.PlannedAt), pass)
	key := fmt.Sprintf(consts.DiscoveryAutoDedupFormat, rule.SourceType, rule.ID, actualSlot)
	if pass == collection.DiscoveryBackfill {
		key += ":backfill"
	}
	var existing storage.Job
	if err := tx.Find(&existing, "dedup_key = ?", key).Error; err != nil || existing.ID != 0 {
		return existing, err
	}
	var previous storage.DiscoveryRuleRun
	if err := tx.Select("id").Find(&previous, "rule_id = ? AND planned_at = ? AND pass = ?", rule.ID, p.PlannedAt, pass).Error; err != nil {
		return storage.Job{}, err
	}
	if previous.ID != 0 {
		// 恢复或规则改动可能重用旧的计划槽，移到下一个调度槽。
		return storage.Job{Status: consts.StatusCanceled}, nil
	}
	run := storage.DiscoveryRuleRun{RuleID: rule.ID, SourceType: rule.SourceType, SourceKey: rule.SourceKey,
		PlannedAt: p.PlannedAt, Pass: pass, CreatedFrom: p.CreatedFrom, CreatedUntil: p.CreatedUntil,
		Query: p.Query, MinStars: p.StarFloor, PlannedShards: 1, Status: consts.StatusPending}
	if err := tx.Create(&run).Error; err != nil {
		return storage.Job{}, err
	}
	p.RunID = &run.ID
	priority := rule.Priority
	if pass == collection.DiscoveryBackfill {
		priority--
	}
	return q.enqueue(tx, "discover_"+rule.SourceType, nil, key, p, priority, now.UnixMilli())
}

// enqueueEarlyBackfill 每条滚动规则每天补扫一次，低于本轮主任务优先级。
func (q Queue) enqueueEarlyBackfill(tx *gorm.DB, rule storage.DiscoveryRule, p collection.Payload, now time.Time) error {
	var count int64
	day := now.UTC().Truncate(24 * time.Hour).UnixMilli()
	if err := tx.Model(&storage.DiscoveryRuleRun{}).
		Where("rule_id = ? AND pass = ? AND status <> ? AND (planned_at >= ? OR finished_at = 0)",
			rule.ID, collection.DiscoveryBackfill, consts.StatusCanceled, day).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	_, err := q.enqueueRuleRun(tx, rule, p, now, collection.DiscoveryBackfill)
	return err
}

// discoveryRunHit 在发现前记录仓库是否已存在，写入后同一轮重复命中保留首次结论。
func discoveryRunHit(tx *gorm.DB, runID *int64, repoID int64) (storage.DiscoveryRuleRunRepo, error) {
	if runID == nil {
		return storage.DiscoveryRuleRunRepo{}, nil
	}
	var count int64
	err := tx.Model(&storage.Repository{}).Where("repo_id = ?", repoID).Count(&count).Error
	return storage.DiscoveryRuleRunRepo{RunID: *runID, RepoID: repoID, NewlyDiscovered: count == 0}, err
}

func saveDiscoveryRunHit(tx *gorm.DB, hit storage.DiscoveryRuleRunRepo) error {
	if hit.RunID == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&hit).Error
}

// updateDiscoveryJobs 将批量取消或租约耗尽同步到受影响的运行记录。
func updateDiscoveryJobs(tx, jobs *gorm.DB, status, code string, now int64) error {
	var runs []int64
	if err := jobs.Session(&gorm.Session{}).Where("discovery_run_id IS NOT NULL").Distinct().Pluck("discovery_run_id", &runs).Error; err != nil {
		return err
	}
	if err := jobs.Updates(map[string]any{"status": status, "last_error_code": code, "updated_at": now, "lease_until": nil, "lease_token": nil}).Error; err != nil {
		return err
	}
	for _, id := range runs {
		if err := storage.SettleDiscoveryRun(tx, &id, now); err != nil {
			return err
		}
	}
	return nil
}
