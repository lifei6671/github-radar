package maintenance

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/collector"
	"github.com/lifei6671/github-radar/internal/config"
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
		{"原始快照", `DELETE FROM repo_snapshots WHERE (repo_id,sample_slot) IN (
SELECT s.repo_id,s.sample_slot FROM repo_snapshots s
JOIN metrics_hourly h ON h.repo_id=s.repo_id AND h.hour_start=s.observed_at/3600000*3600000
WHERE s.observed_at<? AND (s.observed_at,s.sample_slot) BETWEEN (h.open_observed_at,h.open_slot) AND (h.close_observed_at,h.close_slot)
AND NOT EXISTS(SELECT 1 FROM metric_rollup_pending p WHERE p.repo_id=s.repo_id AND p.resolution='hourly' AND p.bucket_start=h.hour_start)
ORDER BY s.observed_at LIMIT ?)`, []any{min(age(cfg.RawDays), hourly)}},
		{"小时聚合", `DELETE FROM metrics_hourly WHERE (repo_id,hour_start) IN (
SELECT h.repo_id,h.hour_start FROM metrics_hourly h
JOIN metrics_daily d ON d.repo_id=h.repo_id AND d.day=date(h.hour_start/1000,'unixepoch')
WHERE h.hour_start<? AND (h.open_observed_at,h.open_slot)>=(d.open_observed_at,d.open_slot)
AND (h.close_observed_at,h.close_slot)<=(d.close_observed_at,d.close_slot)
AND NOT EXISTS(SELECT 1 FROM metric_rollup_pending p WHERE p.repo_id=h.repo_id AND p.resolution='daily' AND p.bucket_start=h.hour_start/86400000*86400000)
AND NOT EXISTS(SELECT 1 FROM repo_snapshots s WHERE s.repo_id=h.repo_id AND s.observed_at>=h.hour_start AND s.observed_at<h.hour_start+3600000)
ORDER BY h.hour_start LIMIT ?)`, []any{min(dayAge(cfg.HourlyDays).UnixMilli(), daily)}},
		{"日聚合", `DELETE FROM metrics_daily WHERE (repo_id,day) IN (
SELECT d.repo_id,d.day FROM metrics_daily d WHERE d.day<?
AND NOT EXISTS(SELECT 1 FROM metric_rollup_pending p WHERE p.repo_id=d.repo_id AND p.resolution='daily' AND p.bucket_start=unixepoch(d.day)*1000)
AND NOT EXISTS(SELECT 1 FROM metric_rollup_pending p WHERE p.repo_id=d.repo_id AND p.resolution='hourly' AND p.bucket_start>=unixepoch(d.day)*1000 AND p.bucket_start<(unixepoch(d.day)+86400)*1000)
AND NOT EXISTS(SELECT 1 FROM metrics_hourly h WHERE h.repo_id=d.repo_id AND h.hour_start>=unixepoch(d.day)*1000 AND h.hour_start<(unixepoch(d.day)+86400)*1000)
ORDER BY d.day LIMIT ?)`, []any{dayAge(cfg.DailyDays).Format(time.DateOnly)}},
		{"历史 Trending", `DELETE FROM trending_observations WHERE (repo_id,source_key,window,observed_slot) IN (
SELECT o.repo_id,o.source_key,o.window,o.observed_slot FROM trending_observations o
WHERE o.observed_slot<? AND NOT EXISTS(SELECT 1 FROM trending_batches b WHERE b.source_key=o.source_key AND b.window=o.window AND b.observed_slot=o.observed_slot)
ORDER BY o.observed_slot LIMIT ?)`, []any{discovery}},
		{"Search 发现记录", `DELETE FROM search_observations WHERE (repo_id,source_key,observed_slot) IN (
SELECT repo_id,source_key,observed_slot FROM search_observations WHERE observed_at<? ORDER BY observed_at LIMIT ?)`, []any{discovery}},
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
	return cleanCandidates(db, age(cfg.CandidateDays), min(cfg.BatchRows, 100))
}

// cleanJobs 先删终态台账，再删关联任务。未决调用保留到人工确认。
func cleanJobs(db *gorm.DB, finished, dead int64, limit int) error {
	for {
		var ids []int64
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&storage.Job{}).Where(`
((status IN ('succeeded','canceled') AND updated_at<?) OR (status='dead' AND updated_at<?))
AND NOT EXISTS(SELECT 1 FROM ai_calls a WHERE a.job_id=jobs.id AND a.status IN ('unknown','sending','reserved'))`, finished, dead).
				Order("updated_at,id").Limit(limit).Pluck("id", &ids).Error; err != nil || len(ids) == 0 {
				return err
			}
			if err := tx.Where("job_id IN ?", ids).Delete(&storage.AICall{}).Error; err != nil {
				return err
			}
			return tx.Where("id IN ?", ids).Delete(&storage.Job{}).Error
		})
		if err != nil || len(ids) < limit {
			return err
		}
	}
}

func cleanTopics(db *gorm.DB, cutoff int64, limit int) error {
	for {
		res := db.Exec(`DELETE FROM topic_candidates WHERE id IN (
SELECT id FROM topic_candidates WHERE status='candidate' AND reviewed_at IS NULL AND updated_at<?
ORDER BY updated_at,id LIMIT ?)`, cutoff, limit)
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
			if err := tx.Where("observed_at<?", cutoff).Order("observed_at").Limit(limit).Find(&batches).Error; err != nil {
				return err
			}
			count = len(batches)
			for _, batch := range batches {
				if err := tx.Where("source_key=? AND window=? AND observed_slot=?", batch.SourceKey, batch.Window, batch.ObservedSlot).
					Delete(&storage.TrendingObservation{}).Error; err != nil {
					return err
				}
				if err := tx.Where("key=? AND json_extract(value,'$.observedSlot')=?", collector.BatchKey(batch.SourceKey, batch.Window), batch.ObservedSlot).
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
			// 有未终结任务、近期采样或仍在最新完整榜单里的候选继续保留。
			if err := tx.Model(&storage.Repository{}).Where(`state='candidate' AND starred_by_user=0 AND last_discovered_at<?
AND (last_success_at IS NULL OR last_success_at<?)
AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.repo_id=repositories.repo_id AND j.status NOT IN ('succeeded','canceled','dead'))
AND NOT EXISTS(SELECT 1 FROM ai_summaries s WHERE s.repo_id=repositories.repo_id)
AND NOT EXISTS(SELECT 1 FROM topic_candidates t WHERE t.repo_id=repositories.repo_id)
AND NOT EXISTS(SELECT 1 FROM jobs j JOIN ai_calls a ON a.job_id=j.id
  WHERE j.repo_id=repositories.repo_id AND a.status IN ('unknown','sending','reserved'))
AND NOT EXISTS(SELECT 1 FROM trending_observations o JOIN maintenance_state m ON m.key='trending:'||o.source_key||':'||o.window
 WHERE o.repo_id=repositories.repo_id AND json_extract(m.value,'$.observedSlot')=o.observed_slot)`, cutoff, cutoff).
				Order("last_discovered_at,repo_id").Limit(limit).Pluck("repo_id", &ids).Error; err != nil {
				return err
			}
			count = len(ids)
			if count == 0 {
				return nil
			}
			for _, model := range []any{&storage.PendingMetric{}, &storage.RepoSnapshot{}, &storage.HourlyMetric{}, &storage.DailyMetric{},
				&storage.LatestMetric{}, &storage.TrendingObservation{}, &storage.SearchObservation{}, &storage.SummaryCheck{}} {
				if err := tx.Where("repo_id IN ?", ids).Delete(model).Error; err != nil {
					return err
				}
			}
			// 任务记录按各自期限保留，仓库外键可空。
			if err := tx.Model(&storage.Job{}).Where("repo_id IN ?", ids).Update("repo_id", nil).Error; err != nil {
				return err
			}
			return tx.Where("repo_id IN ?", ids).Delete(&storage.Repository{}).Error
		})
		if err != nil || count < limit {
			return err
		}
	}
}
