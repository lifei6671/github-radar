package maintenance

import (
	"context"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

const (
	HourlyKey = "metrics.hourly"
	DailyKey  = "metrics.daily"
)

// dayBoundary 给出到计划时刻为止最近一次可处理的 UTC 日边界。
func dayBoundary(now time.Time, hour int) time.Time {
	return now.UTC().Add(-time.Duration(hour) * time.Hour).Truncate(24 * time.Hour)
}

func watermark(tx *gorm.DB, key string) (int64, error) {
	var row storage.MaintenanceState
	if err := tx.Find(&row, "key=?", key).Error; err != nil || row.Key == "" {
		return 0, err
	}
	return strconv.ParseInt(row.Value, 10, 64)
}

func mark(tx *gorm.DB, key string, value int64, now time.Time) error {
	row := storage.MaintenanceState{Key: key, Value: strconv.FormatInt(value, 10), UpdatedAt: now.UnixMilli()}
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

// Rollup 合并已结束的 UTC 桶。迟到增量可补进历史桶，已删除的源样本不会被重新读取。
func Rollup(ctx context.Context, db *gorm.DB, cfg config.Config, now time.Time) error {
	for _, layer := range []struct {
		resolution, key string
		cutoff          int64
	}{
		{"hourly", HourlyKey, now.UTC().Truncate(time.Hour).UnixMilli()},
		{"daily", DailyKey, dayBoundary(now, cfg.Maintenance.DailyRollupHour).UnixMilli()},
	} {
		for {
			done := false
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var pending []storage.PendingMetric
				if err := tx.Where("resolution=? AND bucket_start<?", layer.resolution, layer.cutoff).
					Order("bucket_start,repo_id").Limit(min(cfg.Maintenance.BatchRows, 200)).Find(&pending).Error; err != nil {
					return err
				}
				if len(pending) == 0 {
					done = true
					previous, err := watermark(tx, layer.key)
					if err != nil || previous >= layer.cutoff {
						return err
					}
					return mark(tx, layer.key, layer.cutoff, now)
				}
				for _, part := range pending {
					var err error
					if layer.resolution == "hourly" {
						err = rollHour(tx, cfg.Metrics.Hour, part)
					} else {
						err = rollDay(tx, cfg.Metrics.Day, part)
					}
					if err != nil {
						return err
					}
					if err := tx.Delete(&part).Error; err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			if done {
				break
			}
		}
	}
	return nil
}

func merge(dest *storage.MetricAnchors, part storage.MetricAnchors) {
	if dest.SampleCount == 0 || part.OpenObservedAt < dest.OpenObservedAt ||
		(part.OpenObservedAt == dest.OpenObservedAt && part.OpenSlot < dest.OpenSlot) {
		dest.OpenObservedAt, dest.OpenSlot, dest.StarsOpen = part.OpenObservedAt, part.OpenSlot, part.StarsOpen
	}
	if dest.SampleCount == 0 || part.CloseObservedAt > dest.CloseObservedAt ||
		(part.CloseObservedAt == dest.CloseObservedAt && part.CloseSlot > dest.CloseSlot) {
		dest.CloseObservedAt, dest.CloseSlot, dest.StarsClose = part.CloseObservedAt, part.CloseSlot, part.StarsClose
	}
	dest.SampleCount += part.SampleCount
}

func rollHour(tx *gorm.DB, tolerance config.BoundaryToleranceMinutes, part storage.PendingMetric) error {
	hour := storage.HourlyMetric{RepoID: part.RepoID, HourStart: part.BucketStart}
	if err := tx.Find(&hour, "repo_id=? AND hour_start=?", part.RepoID, part.BucketStart).Error; err != nil {
		return err
	}
	merge(&hour.MetricAnchors, part.MetricAnchors)
	window := metrics.Bucket(hour.MetricAnchors, hour.HourStart, metrics.Hour, tolerance)
	hour.Delta, hour.Quality = window.Delta, window.Quality
	if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&hour).Error; err != nil {
		return err
	}

	// 向日层传递本次增量，历史小时桶重跑时不会重复统计旧样本。
	dayStart := time.UnixMilli(part.BucketStart).UTC().Truncate(24 * time.Hour).UnixMilli()
	day := storage.PendingMetric{RepoID: part.RepoID, Resolution: "daily", BucketStart: dayStart}
	if err := tx.Find(&day, "repo_id=? AND resolution='daily' AND bucket_start=?", part.RepoID, dayStart).Error; err != nil {
		return err
	}
	merge(&day.MetricAnchors, part.MetricAnchors)
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&day).Error
}

func rollDay(tx *gorm.DB, tolerance config.BoundaryToleranceMinutes, part storage.PendingMetric) error {
	date := time.UnixMilli(part.BucketStart).UTC().Format(time.DateOnly)
	day := storage.DailyMetric{RepoID: part.RepoID, Day: date}
	if err := tx.Find(&day, "repo_id=? AND day=?", part.RepoID, date).Error; err != nil {
		return err
	}
	merge(&day.MetricAnchors, part.MetricAnchors)
	window := metrics.Bucket(day.MetricAnchors, part.BucketStart, metrics.Day, tolerance)
	day.Delta, day.Quality = window.Delta, window.Quality
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&day).Error
}
