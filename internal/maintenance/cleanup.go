package maintenance

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/storage"
)

const CleanupKey = "maintenance.cleanup"

// Cleanup 只删除已落下一级真实锚点的历史数据，并在每批提交后释放写锁。
func Cleanup(ctx context.Context, db *gorm.DB, cfg config.Maintenance, now time.Time) error {
	db = db.WithContext(ctx)
	hourly, err := watermark(db, HourlyKey)
	if err != nil {
		return err
	}
	daily, err := watermark(db, DailyKey)
	if err != nil {
		return err
	}
	age := func(days int) int64 { return now.UTC().AddDate(0, 0, -days).UnixMilli() }
	dayAge := func(days int) time.Time { return now.UTC().AddDate(0, 0, -days).Truncate(24 * time.Hour) }
	discovery := age(cfg.DiscoveryDays)
	rules := []struct {
		name, query string
		args        []any
	}{
		{"原始快照", fmt.Sprintf(`DELETE FROM %[1]s WHERE (%[2]s,%[3]s) IN (
SELECT s.%[2]s,s.%[3]s FROM %[1]s s
JOIN %[4]s h ON h.%[5]s=s.%[2]s AND h.%[6]s=s.%[7]s/3600000*3600000
WHERE s.%[7]s<? AND (s.%[7]s,s.%[3]s) BETWEEN (h.%[13]s,h.%[14]s) AND (h.%[15]s,h.%[16]s)
AND NOT EXISTS(SELECT 1 FROM %[8]s p WHERE p.%[9]s=s.%[2]s AND p.%[10]s='%[11]s' AND p.%[12]s=h.%[6]s)
ORDER BY s.%[7]s LIMIT ?)`,
			storage.RepoSnapshotTable, storage.RepoSnapshotColumns.RepoID, storage.RepoSnapshotColumns.SampleSlot,
			storage.HourlyMetricTable, storage.HourlyMetricColumns.RepoID, storage.HourlyMetricColumns.HourStart, storage.RepoSnapshotColumns.ObservedAt,
			storage.PendingMetricTable, storage.PendingMetricColumns.RepoID, storage.PendingMetricColumns.Resolution, consts.ResolutionHourly, storage.PendingMetricColumns.BucketStart,
			storage.HourlyMetricColumns.OpenObservedAt, storage.HourlyMetricColumns.OpenSlot,
			storage.HourlyMetricColumns.CloseObservedAt, storage.HourlyMetricColumns.CloseSlot), []any{min(age(cfg.RawDays), hourly)}},
		{"小时聚合", fmt.Sprintf(`DELETE FROM %[1]s WHERE (%[2]s,%[3]s) IN (
SELECT h.%[2]s,h.%[3]s FROM %[1]s h
JOIN %[4]s d ON d.%[5]s=h.%[2]s AND d.%[6]s=date(h.%[3]s/1000,'unixepoch')
WHERE h.%[3]s<? AND (h.%[15]s,h.%[16]s)>=(d.%[17]s,d.%[18]s)
AND (h.%[19]s,h.%[20]s)<=(d.%[21]s,d.%[22]s)
AND NOT EXISTS(SELECT 1 FROM %[7]s p WHERE p.%[8]s=h.%[2]s AND p.%[9]s='%[10]s' AND p.%[11]s=h.%[3]s/86400000*86400000)
AND NOT EXISTS(SELECT 1 FROM %[12]s s WHERE s.%[13]s=h.%[2]s AND s.%[14]s>=h.%[3]s AND s.%[14]s<h.%[3]s+3600000)
ORDER BY h.%[3]s LIMIT ?)`,
			storage.HourlyMetricTable, storage.HourlyMetricColumns.RepoID, storage.HourlyMetricColumns.HourStart,
			storage.DailyMetricTable, storage.DailyMetricColumns.RepoID, storage.DailyMetricColumns.Day,
			storage.PendingMetricTable, storage.PendingMetricColumns.RepoID, storage.PendingMetricColumns.Resolution, consts.ResolutionDaily, storage.PendingMetricColumns.BucketStart,
			storage.RepoSnapshotTable, storage.RepoSnapshotColumns.RepoID, storage.RepoSnapshotColumns.ObservedAt,
			storage.HourlyMetricColumns.OpenObservedAt, storage.HourlyMetricColumns.OpenSlot, storage.DailyMetricColumns.OpenObservedAt, storage.DailyMetricColumns.OpenSlot,
			storage.HourlyMetricColumns.CloseObservedAt, storage.HourlyMetricColumns.CloseSlot, storage.DailyMetricColumns.CloseObservedAt, storage.DailyMetricColumns.CloseSlot), []any{min(dayAge(cfg.HourlyDays).UnixMilli(), daily)}},
		{"日聚合", fmt.Sprintf(`DELETE FROM %[1]s WHERE (%[2]s,%[3]s) IN (
SELECT d.%[2]s,d.%[3]s FROM %[1]s d WHERE d.%[3]s<?
AND NOT EXISTS(SELECT 1 FROM %[4]s p WHERE p.%[5]s=d.%[2]s AND p.%[6]s='%[7]s' AND p.%[8]s=unixepoch(d.%[3]s)*1000)
AND NOT EXISTS(SELECT 1 FROM %[4]s p WHERE p.%[5]s=d.%[2]s AND p.%[6]s='%[9]s' AND p.%[8]s>=unixepoch(d.%[3]s)*1000 AND p.%[8]s<(unixepoch(d.%[3]s)+86400)*1000)
AND NOT EXISTS(SELECT 1 FROM %[10]s h WHERE h.%[11]s=d.%[2]s AND h.%[12]s>=unixepoch(d.%[3]s)*1000 AND h.%[12]s<(unixepoch(d.%[3]s)+86400)*1000)
ORDER BY d.%[3]s LIMIT ?)`,
			storage.DailyMetricTable, storage.DailyMetricColumns.RepoID, storage.DailyMetricColumns.Day, storage.PendingMetricTable, storage.PendingMetricColumns.RepoID, storage.PendingMetricColumns.Resolution, consts.ResolutionDaily, storage.PendingMetricColumns.BucketStart, consts.ResolutionHourly, storage.HourlyMetricTable, storage.HourlyMetricColumns.RepoID, storage.HourlyMetricColumns.HourStart), []any{dayAge(cfg.DailyDays).Format(time.DateOnly)}},
		{"历史 Trending", fmt.Sprintf(`DELETE FROM %[1]s WHERE (%[2]s,%[3]s,%[4]s,%[5]s) IN (
SELECT o.%[2]s,o.%[3]s,o.%[4]s,o.%[5]s FROM %[1]s o
WHERE o.%[5]s<? AND NOT EXISTS(SELECT 1 FROM %[6]s b WHERE b.%[7]s=o.%[3]s AND b.%[8]s=o.%[4]s AND b.%[9]s=o.%[5]s)
ORDER BY o.%[5]s LIMIT ?)`,
			storage.TrendingObservationTable, storage.TrendingObservationColumns.RepoID, storage.TrendingObservationColumns.SourceKey, storage.TrendingObservationColumns.Window, storage.TrendingObservationColumns.ObservedSlot, storage.TrendingBatchRecordTable, storage.TrendingBatchRecordColumns.SourceKey, storage.TrendingBatchRecordColumns.Window, storage.TrendingBatchRecordColumns.ObservedSlot), []any{discovery}},
		{"Search 发现记录", fmt.Sprintf(`DELETE FROM %[1]s WHERE (%[2]s,%[3]s,%[4]s) IN (
SELECT %[2]s,%[3]s,%[4]s FROM %[1]s WHERE %[5]s<? ORDER BY %[5]s LIMIT ?)`,
			storage.SearchObservationTable, storage.SearchObservationColumns.RepoID, storage.SearchObservationColumns.SourceKey, storage.SearchObservationColumns.ObservedSlot, storage.SearchObservationColumns.ObservedAt), []any{discovery}},
	}
	if err := cleanTrending(db, discovery, min(cfg.BatchRows, 100)); err != nil {
		return fmt.Errorf("清理 Trending 批次: %w", err)
	}
	for _, rule := range rules {
		for {
			res := db.Exec(rule.query, append(rule.args, cfg.BatchRows)...)
			if res.Error != nil {
				return fmt.Errorf("清理%s: %w", rule.name, res.Error)
			}
			if res.RowsAffected < int64(cfg.BatchRows) {
				break
			}
		}
	}
	if err := cleanJobs(db, age(cfg.FinishedJobDays), age(cfg.DeadJobDays), cfg.BatchRows); err != nil {
		return err
	}
	// 独立保留 90 天可结算用量；未决状态保留，直到有确定结论。
	if err := cleanBudget(db, age(90), cfg.BatchRows); err != nil {
		return err
	}
	if err := db.Where("day < ?", dayAge(45).Format(time.DateOnly)).Delete(&storage.AdmissionDaily{}).Error; err != nil {
		return err
	}
	for {
		ids := db.Model(&storage.DiscoveryRuleRun{}).Select("id").
			Where("finished_at > 0 AND finished_at < ?", discovery).Order("finished_at").Limit(cfg.BatchRows)
		result := db.Where("id IN (?)", ids).Delete(&storage.DiscoveryRuleRun{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected < int64(cfg.BatchRows) {
			break
		}
	}
	return cleanCandidates(db, age(cfg.CandidateDays), min(cfg.BatchRows, 100))
}

// cleanBudget 仅清理结算完成或明确未发送的历史条目。
func cleanBudget(db *gorm.DB, cutoff int64, limit int) error {
	for {
		keys := db.Model(&storage.AIBudgetReservation{}).Select("reservation_key").
			Where("reserved_at < ? AND status IN ?", cutoff, []string{consts.StatusSucceeded, consts.StatusFailed, "unused"}).
			Order("reserved_at").Limit(limit)
		result := db.Where("reservation_key IN (?)", keys).Delete(&storage.AIBudgetReservation{})
		if result.Error != nil || result.RowsAffected < int64(limit) {
			return result.Error
		}
	}
}

// cleanJobs 先删终态台账，再删关联任务。未决调用保留到人工确认。
func cleanJobs(db *gorm.DB, finished, dead int64, limit int) error {
	for {
		var ids []int64
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&storage.Job{}).Where(fmt.Sprintf(`
((%[1]s IN ('%[2]s','%[3]s') AND %[4]s<?) OR (%[1]s='%[5]s' AND %[4]s<?))
AND NOT EXISTS(SELECT 1 FROM %[6]s a WHERE a.%[7]s=%[8]s.%[9]s AND a.%[10]s IN ('%[11]s','%[12]s','%[13]s'))`,
				storage.JobColumns.Status, consts.StatusSucceeded, consts.StatusCanceled, storage.JobColumns.UpdatedAt, consts.StatusDead, storage.AICallTable, storage.AICallColumns.JobID, storage.JobTable, storage.JobColumns.ID, storage.AICallColumns.Status, consts.StatusUnknown, consts.StatusSending, consts.StatusReserved), finished, dead).
				Where("NOT EXISTS (?)", tx.Model(&storage.DiscoveryRuleRun{}).Select("1").
					Where("discovery_rule_runs.id = jobs.discovery_run_id AND finished_at = 0")).
				Order(storage.JobColumns.UpdatedAt+","+storage.JobColumns.ID).Limit(limit).Pluck(storage.JobColumns.ID, &ids).Error; err != nil || len(ids) == 0 {
				return err
			}
			if err := tx.Where(storage.AICallColumns.JobID+" IN ?", ids).Delete(&storage.AICall{}).Error; err != nil {
				return err
			}
			return tx.Where(storage.JobColumns.ID+" IN ?", ids).Delete(&storage.Job{}).Error
		})
		if err != nil || len(ids) < limit {
			return err
		}
	}
}

func cleanTopics(db *gorm.DB, cutoff int64, limit int) error {
	t := storage.TopicCandidateColumns
	for {
		ids := db.Model(&storage.TopicCandidate{}).Select(t.ID).
			Where(t.Status+"=? AND "+t.ReviewedAt+" IS NULL AND "+t.UpdatedAt+"<?", consts.StatusCandidate, cutoff).
			Where("NOT EXISTS (?)", db.Model(&storage.TopicReviewHistory{}).Select("1").Where("topic_review_history.topic_id = topic_candidates.id")).
			Order(t.UpdatedAt).Order(t.ID).Limit(limit)
		res := db.Where(t.ID+" IN (?)", ids).Delete(&storage.TopicCandidate{})
		if res.Error != nil || res.RowsAffected < int64(limit) {
			return res.Error
		}
	}
}

func cleanTrending(db *gorm.DB, cutoff int64, limit int) error {
	for {
		count := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			var batches []storage.TrendingBatchRecord
			if err := tx.Where(storage.TrendingBatchRecordColumns.ObservedAt+"<?", cutoff).Order(storage.TrendingBatchRecordColumns.ObservedAt).Limit(limit).Find(&batches).Error; err != nil {
				return err
			}
			count = len(batches)
			for _, batch := range batches {
				if err := tx.Where(storage.TrendingObservationColumns.SourceKey+"=? AND "+storage.TrendingObservationColumns.Window+"=? AND "+storage.TrendingObservationColumns.ObservedSlot+"=?", batch.SourceKey, batch.Window, batch.ObservedSlot).
					Delete(&storage.TrendingObservation{}).Error; err != nil {
					return err
				}
				if err := tx.Where(storage.MaintenanceStateColumns.Key+"=? AND json_extract("+storage.MaintenanceStateColumns.Value+",'$.observedSlot')=?", collection.BatchKey(batch.SourceKey, batch.Window), batch.ObservedSlot).
					Delete(&storage.MaintenanceState{}).Error; err != nil {
					return err
				}
				if err := tx.Delete(&batch).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil || count < limit {
			return err
		}
	}
}

func cleanCandidates(db *gorm.DB, cutoff int64, limit int) error {
	for {
		count := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			var ids []int64
			// 保留管理员提交待复核的候选，以及有活跃任务或近期观测的候选。
			if err := tx.Model(&storage.Repository{}).Where(fmt.Sprintf(`%[1]s='%[2]s' AND %[3]s=0 AND %[4]s=0 AND %[5]s<?
AND (%[6]s IS NULL OR %[6]s<?)
AND NOT EXISTS(SELECT 1 FROM %[7]s j WHERE j.%[8]s=%[9]s.%[10]s AND j.%[11]s NOT IN ('%[12]s','%[13]s','%[14]s'))
AND NOT EXISTS(SELECT 1 FROM %[15]s s WHERE s.%[16]s=%[9]s.%[10]s)
AND NOT EXISTS(SELECT 1 FROM %[17]s t WHERE t.%[18]s=%[9]s.%[10]s)
AND NOT EXISTS(SELECT 1 FROM repo_discovery_events d WHERE d.repo_id=%[9]s.%[10]s)
AND NOT EXISTS(SELECT 1 FROM %[7]s j JOIN %[19]s a ON a.%[20]s=j.%[21]s
  WHERE j.%[8]s=%[9]s.%[10]s AND a.%[22]s IN ('%[23]s','%[24]s','%[25]s'))
AND NOT EXISTS(SELECT 1 FROM %[26]s o JOIN %[27]s m ON m.%[28]s='%[29]s'||o.%[30]s||':'||o.%[31]s
 WHERE o.%[32]s=%[9]s.%[10]s AND json_extract(m.%[33]s,'$.observedSlot')=o.%[34]s)`,
				storage.RepositoryColumns.State, consts.StatusCandidate, storage.RepositoryColumns.Blocked, storage.RepositoryColumns.StarredByUser, storage.RepositoryColumns.LastDiscoveredAt, storage.RepositoryColumns.LastSuccessAt, storage.JobTable, storage.JobColumns.RepoID, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.JobColumns.Status, consts.StatusSucceeded, consts.StatusCanceled, consts.StatusDead, storage.AISummaryTable, storage.AISummaryColumns.RepoID, storage.TopicCandidateTable, storage.TopicCandidateColumns.RepoID, storage.AICallTable, storage.AICallColumns.JobID, storage.JobColumns.ID, storage.AICallColumns.Status, consts.StatusUnknown, consts.StatusSending, consts.StatusReserved, storage.TrendingObservationTable, storage.MaintenanceStateTable, storage.MaintenanceStateColumns.Key, consts.TrendingStatePrefix, storage.TrendingObservationColumns.SourceKey, storage.TrendingObservationColumns.Window, storage.TrendingObservationColumns.RepoID, storage.MaintenanceStateColumns.Value, storage.TrendingObservationColumns.ObservedSlot), cutoff, cutoff).
				Where("NOT EXISTS (?)", tx.Table("daily_report_items").Select("1").Where("repo_id = repositories.repo_id")).
				Where("NOT EXISTS (?)", tx.Table("discovery_rotation").Select("1").Where("repo_id = repositories.repo_id")).
				Order(storage.RepositoryColumns.LastDiscoveredAt+","+storage.RepositoryColumns.RepoID).Limit(limit).Pluck(storage.RepositoryColumns.RepoID, &ids).Error; err != nil {
				return err
			}
			count = len(ids)
			if count == 0 {
				return nil
			}
			children := []struct {
				model  any
				repoID string
			}{
				{&storage.PendingMetric{}, storage.PendingMetricColumns.RepoID},
				{&storage.RepoSnapshot{}, storage.RepoSnapshotColumns.RepoID},
				{&storage.HourlyMetric{}, storage.HourlyMetricColumns.RepoID},
				{&storage.DailyMetric{}, storage.DailyMetricColumns.RepoID},
				{&storage.LatestMetric{}, storage.LatestMetricColumns.RepoID},
				{&storage.TrendingObservation{}, storage.TrendingObservationColumns.RepoID},
				{&storage.SearchObservation{}, storage.SearchObservationColumns.RepoID},
				{&storage.SummaryCheck{}, storage.SummaryCheckColumns.RepoID},
			}
			for _, child := range children {
				if err := tx.Where(child.repoID+" IN ?", ids).Delete(child.model).Error; err != nil {
					return err
				}
			}
			// 任务记录按各自期限保留，仓库外键可空。
			if err := tx.Model(&storage.Job{}).Where(storage.JobColumns.RepoID+" IN ?", ids).Update(storage.JobColumns.RepoID, nil).Error; err != nil {
				return err
			}
			return tx.Where(storage.RepositoryColumns.RepoID+" IN ?", ids).Delete(&storage.Repository{}).Error
		})
		if err != nil || count < limit {
			return err
		}
	}
}
