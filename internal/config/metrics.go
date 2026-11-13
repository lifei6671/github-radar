package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/lifei6671/github-radar/internal/domain/metrics"
)

type BoundaryToleranceMinutes = metrics.BoundaryToleranceMinutes
type Metrics = metrics.Policy

// Maintenance 保存数据保留天数和 UTC 维护计划。
type Maintenance struct {
	RawDays         int `json:"rawDays"`
	HourlyDays      int `json:"hourlyDays"`
	DailyDays       int `json:"dailyDays"`
	DiscoveryDays   int `json:"discoveryDays"`
	FinishedJobDays int `json:"finishedJobDays"`
	DeadJobDays     int `json:"deadJobDays"`
	CandidateDays   int `json:"candidateDays"`
	DailyRollupHour int `json:"dailyRollupHour"`
	CleanupHour     int `json:"cleanupHour"`
	BatchRows       int `json:"batchRows"`
}

func DefaultMetrics() Metrics {
	return Metrics{
		Hour: BoundaryToleranceMinutes{Exact: 10, Max: 30}, Day: BoundaryToleranceMinutes{Exact: 90, Max: 360},
		Week: BoundaryToleranceMinutes{Exact: 180, Max: 720}, Month: BoundaryToleranceMinutes{Exact: 360, Max: 1440},
		HotGrowth: 200, ActiveGrowth: 50, HotDemotionHours: 12, ActiveDemotionHours: 48, NormalDemotionHours: 168,
		TopGrowthRepos: 200, RelativeStarFloor: 100, Velocity24Cap: 1000, Velocity7Cap: 5000,
		AccelerationCap: 3, MinPersistenceDays: 3, Weights: [5]float64{.35, .20, .15, .15, .15},
	}
}

func DefaultMaintenance() Maintenance {
	return Maintenance{RawDays: 14, HourlyDays: 60, DailyDays: 180, DiscoveryDays: 30,
		FinishedJobDays: 14, DeadJobDays: 30, CandidateDays: 30, DailyRollupHour: 1, CleanupHour: 3, BatchRows: 5000}
}

// DefaultMetricsInterval 兼容已有采集扫描周期，默认选择不少于 15 分钟的最小整倍数。
func (r *Runtime) DefaultMetricsInterval() {
	if r.Scheduler.MetricsIntervalMinutes == 0 && r.Scheduler.TickMinutes > 0 {
		tick := r.Scheduler.TickMinutes
		r.Scheduler.MetricsIntervalMinutes = (14/tick + 1) * tick
	}
}

func (r Runtime) validateMetrics() error {
	s, m, p := r.Scheduler, r.Metrics, r.Maintenance
	if s.MetricsIntervalMinutes < 1 || s.MetricsIntervalMinutes%s.TickMinutes != 0 ||
		int64(s.MetricsIntervalMinutes) > DurationLimit(time.Minute) {
		return errors.New("指标计算周期必须是采集扫描周期的正整数倍")
	}
	for _, item := range []struct {
		tolerance BoundaryToleranceMinutes
		minutes   int
	}{
		{m.Hour, 60}, {m.Day, 1440}, {m.Week, 10080}, {m.Month, 43200},
	} {
		if item.tolerance.Exact < 0 || item.tolerance.Max < item.tolerance.Exact || item.tolerance.Max >= item.minutes {
			return fmt.Errorf("%d 分钟窗口的容差无效", item.minutes)
		}
	}
	if m.ActiveGrowth < 1 || m.HotGrowth < m.ActiveGrowth || m.TopGrowthRepos < 1 {
		return errors.New("采样升档的增长阈值无效")
	}
	if m.RelativeStarFloor < 1 || m.Velocity24Cap < 1 || m.Velocity7Cap < 1 || m.AccelerationCap < 1 ||
		m.MinPersistenceDays < 1 || m.MinPersistenceDays > 7 {
		return errors.New("热度特征参数无效")
	}
	for _, weight := range m.Weights {
		if !(weight > 0 && weight <= 1) {
			return errors.New("五项热度权重须大于零且不超过 1")
		}
	}
	for _, hours := range []int{m.HotDemotionHours, m.ActiveDemotionHours, m.NormalDemotionHours} {
		if hours < 1 || int64(hours) > DurationLimit(time.Hour) {
			return errors.New("采样降档观察时长无效")
		}
	}
	for _, days := range []int{p.RawDays, p.HourlyDays, p.DailyDays, p.DiscoveryDays, p.FinishedJobDays, p.DeadJobDays, p.CandidateDays} {
		if days < 1 || int64(days) > DurationLimit(24*time.Hour) {
			return errors.New("数据保留天数无效")
		}
	}
	if p.RawDays > p.HourlyDays || p.HourlyDays > p.DailyDays || p.DeadJobDays < p.FinishedJobDays ||
		p.DailyRollupHour < 0 || p.DailyRollupHour > 23 || p.CleanupHour < 0 || p.CleanupHour > 23 ||
		p.BatchRows < 1 || p.BatchRows > 10000 {
		return errors.New("数据保留顺序、维护时刻或清理批次大小无效")
	}
	return nil
}
