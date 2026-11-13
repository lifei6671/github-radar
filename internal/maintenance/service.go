package maintenance

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/reports"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/discoverystore"
	"github.com/lifei6671/github-radar/internal/storage/metricstore"
	"github.com/lifei6671/github-radar/internal/storage/reportstore"
	"github.com/lifei6671/github-radar/internal/storage/topicstore"
	"github.com/lifei6671/github-radar/internal/topics"
)

const requestKey = "maintenance.request"
const optimizeKey = "maintenance.optimize"

type Service struct {
	DB          *gorm.DB
	Cfg         config.Config
	Logger      *slog.Logger
	PlanReports func(context.Context, time.Time) error
}

// Request 持久化一轮维护请求，服务重启后仍会执行。
func Request(ctx context.Context, db *gorm.DB, now time.Time) error {
	row := storage.MaintenanceState{Key: requestKey, Value: rand.Text(), UpdatedAt: now.UnixMilli()}
	return db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

func (s Service) RunMetrics(ctx context.Context) error {
	return s.loop(ctx, time.Duration(s.Cfg.Scheduler.MetricsIntervalMinutes)*time.Minute, func(now time.Time) error {
		return s.compute(ctx, now, false)
	})
}

func (s Service) compute(ctx context.Context, now time.Time, force bool) error {
	if err := metrics.Compute(ctx, metricstore.Store{DB: s.DB}, s.Cfg, now, force); err != nil {
		return err
	}
	return discoverystore.Record(ctx, s.DB, now)
}

func (s Service) RunMaintenance(ctx context.Context) error {
	return s.loop(ctx, time.Duration(s.Cfg.Scheduler.TickMinutes)*time.Minute, func(now time.Time) error {
		return s.maintain(ctx, now, false)
	})
}

// Once 对已完成安装的数据库执行一轮本地维护，支持离线使用。
func (s Service) Once(ctx context.Context, now time.Time) error {
	ready, err := storage.Initialized(ctx, s.DB)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("请先完成系统初始化")
	}
	return s.maintain(ctx, now, true)
}

func (s Service) loop(ctx context.Context, interval time.Duration, work func(time.Time) error) error {
	for !lifecycle.Stopping(ctx) {
		now := time.Now().UTC()
		ready, err := storage.Initialized(ctx, s.DB)
		if err == nil && ready && !lifecycle.Stopping(ctx) {
			err = work(now)
		}
		if err != nil && ctx.Err() == nil {
			s.Logger.Warn("数据库后台任务未完成，将在下次扫描重试", "error", err)
		}
		wait := time.Until(time.Now().UTC().Truncate(interval).Add(interval))
		if !ready {
			wait = min(wait, time.Second)
		}
		if !lifecycle.Wait(ctx, wait) {
			break
		}
	}
	return ctx.Err()
}

func (s Service) maintain(ctx context.Context, now time.Time, force bool) error {
	db := s.DB.WithContext(ctx)
	var request storage.MaintenanceState
	if err := db.Find(&request, storage.MaintenanceStateColumns.Key+"=?", requestKey).Error; err != nil {
		return err
	}
	manual := force || request.Key != ""
	if err := Rollup(ctx, s.DB, s.Cfg, now); err != nil {
		return err
	}
	if manual {
		if err := s.compute(ctx, now, true); err != nil {
			return err
		}
	}
	cleanupDay := dayBoundary(now, s.Cfg.Maintenance.CleanupHour)
	last, err := watermark(db, CleanupKey)
	if err != nil {
		return err
	}
	if manual || last < cleanupDay.UnixMilli() {
		if err := Cleanup(ctx, s.DB, s.Cfg.Maintenance, now); err != nil {
			return err
		}
		if err := cleanTopics(db, now.AddDate(0, 0, -s.Cfg.Recommendations.RetentionDays).UnixMilli(), s.Cfg.Maintenance.BatchRows); err != nil {
			return err
		}
		if err := mark(db, CleanupKey, cleanupDay.UnixMilli(), now); err != nil {
			return err
		}
	}
	last, err = watermark(db, optimizeKey)
	if err != nil {
		return err
	}
	// 每周一 UTC 清理窗口之后优化，停机恢复时最多补做一次。
	monday := cleanupDay.AddDate(0, 0, -(int(cleanupDay.Weekday())+6)%7).UnixMilli()
	if last < monday {
		if err := db.Exec("PRAGMA optimize").Error; err != nil {
			return err
		}
		if err := mark(db, optimizeKey, monday, now); err != nil {
			return err
		}
	}
	if request.Key != "" {
		if err := db.Where(storage.MaintenanceStateColumns.Key+"=? AND "+storage.MaintenanceStateColumns.Value+"=?", request.Key, request.Value).Delete(&storage.MaintenanceState{}).Error; err != nil {
			return err
		}
	}
	reportErr := reports.Generate(ctx, reportstore.Store{DB: s.DB}, now)
	if reportErr == nil && s.PlanReports != nil {
		reportErr = s.PlanReports(ctx, now)
	}
	return errors.Join(topics.Generate(ctx, topicstore.Store{DB: s.DB}, s.Cfg.Recommendations, now), reportErr)
}
