package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

const ComputeKey = "metrics.compute"

// Compute 原子发布同一 asOf 的全量指标和等级；恢复时只计算当前窗口。
func Compute(ctx context.Context, db *gorm.DB, cfg config.Config, now time.Time, force bool) error {
	asOf := now.UnixMilli()
	slot := now.UTC().Truncate(time.Duration(cfg.Scheduler.MetricsIntervalMinutes) * time.Minute).UnixMilli()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var marker storage.MaintenanceState
		if err := tx.Find(&marker, storage.MaintenanceStateColumns.Key+" = ?", ComputeKey).Error; err != nil {
			return err
		}
		if marker.Key != "" && (marker.UpdatedAt > asOf || (!force && marker.Value == strconv.FormatInt(slot, 10))) {
			return nil
		}
		var repos []storage.Repository
		if err := tx.Where(storage.RepositoryColumns.State + "='" + consts.StatusActive + "'").Order(storage.RepositoryColumns.RepoID).Find(&repos).Error; err != nil {
			return err
		}
		rows, exact, err := calculate(tx, cfg.Metrics, repos, now)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(&rows, 100).Error; err != nil {
				return err
			}
			if err := applyGrades(tx, cfg, repos, rows, exact, asOf); err != nil {
				return err
			}
		}
		if err := tx.Exec("DELETE FROM " + storage.LatestMetricTable + " WHERE " + storage.LatestMetricColumns.RepoID + " IN (SELECT " + storage.RepositoryColumns.RepoID + " FROM " + storage.RepositoryTable + " WHERE " + storage.RepositoryColumns.State + " <> '" + consts.StatusActive + "')").Error; err != nil {
			return err
		}
		marker = storage.MaintenanceState{Key: ComputeKey, Value: strconv.FormatInt(slot, 10), UpdatedAt: asOf}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
	})
}

type windowSpec struct {
	key        string
	span       int64
	tolerance  config.BoundaryToleranceMinutes
	hourlyOnly bool
}

func calculate(tx *gorm.DB, cfg config.Metrics, repos []storage.Repository, now time.Time) ([]storage.LatestMetric, []bool, error) {
	rows := make([]storage.LatestMetric, 0, len(repos))
	exact := make([]bool, 0, len(repos))
	if len(repos) == 0 {
		return rows, exact, nil
	}
	asOf := now.UnixMilli()
	windows := []windowSpec{
		{consts.Window1h, Hour, cfg.Hour, false}, {consts.Window24h, Day, cfg.Day, false}, {consts.Window7d, 7 * Day, cfg.Week, false},
		{consts.Window30d, 30 * Day, cfg.Month, true}, {consts.WindowPrevious24h, 2 * Day, cfg.Day, false},
	}
	load := func(target int64, tolerance config.BoundaryToleranceMinutes, hourlyOnly bool) (anchors, error) {
		return loadAnchors(tx, target, int64(tolerance.Max)*minute, asOf, hourlyOnly)
	}
	starts := make([]anchors, len(windows))
	for i, w := range windows {
		a, err := load(asOf-w.span, w.tolerance, w.hourlyOnly)
		if err != nil {
			return nil, nil, fmt.Errorf("读取 %s 采样边界: %w", w.key, err)
		}
		starts[i] = a
	}
	end, err := load(asOf, config.BoundaryToleranceMinutes{Max: max(cfg.Hour.Max, cfg.Day.Max, cfg.Week.Max, cfg.Month.Max)}, false)
	if err != nil {
		return nil, nil, err
	}
	midnight := now.UTC().Truncate(24 * time.Hour).UnixMilli()
	days := make([]anchors, 8)
	for i := range days {
		a, err := load(midnight-int64(i)*Day, cfg.Day, false)
		if err != nil {
			return nil, nil, err
		}
		days[i] = a
	}
	trending, fresh, err := trendingSignals(tx, asOf)
	if err != nil {
		return nil, nil, err
	}
	for _, repo := range repos {
		id := repo.RepoID
		w := make(map[string]Window, len(windows))
		var start7 *Point
		for i, spec := range windows {
			from, to := asOf-spec.span, asOf
			start := starts[i].pick(id, from, spec.tolerance, spec.hourlyOnly)
			finish := end.pick(id, to, spec.tolerance, false)
			if spec.key == consts.WindowPrevious24h {
				to -= Day
				finish = starts[1].pick(id, to, spec.tolerance, false)
			}
			w[spec.key] = Between(start, finish, from, to, spec.tolerance)
			if spec.key == consts.Window7d {
				start7 = start
			}
		}
		dayWindows := make([]Window, 7)
		for i := range dayWindows {
			to := midnight - int64(i)*Day
			dayWindows[i] = Between(days[i+1].pick(id, to-Day, cfg.Day, false), days[i].pick(id, to, cfg.Day, false), to-Day, to, cfg.Day)
		}
		row := summarize(cfg, w, dayWindows, trending[id], fresh, start7)
		row.RepoID, row.CalculatedAt = id, asOf
		row.CandidateBurst = (repo.Grade == consts.GradeB || repo.Grade == consts.GradeC) &&
			w[consts.Window24h].Quality == Estimated && *w[consts.Window24h].Delta >= int64(cfg.HotGrowth)
		details, err := json.Marshal(w)
		if err != nil {
			return nil, nil, err
		}
		row.WindowDetailsJSON = string(details)
		rows = append(rows, row)
		exact = append(exact, w[consts.Window24h].Quality == Exact)
	}
	return rows, exact, nil
}

func ptr(value float64) *float64 { return &value }

func scale(value, cap float64) float64 {
	return 100 * min(1, math.Log1p(max(value, 0))/math.Log1p(cap))
}

func summarize(cfg config.Metrics, w map[string]Window, days []Window, signal trendingSignal, fresh bool, start7 *Point) storage.LatestMetric {
	current, week, prev := w[consts.Window24h], w[consts.Window7d], w[consts.WindowPrevious24h]
	row := storage.LatestMetric{
		Delta1h: w[consts.Window1h].Delta, Delta24h: current.Delta, Delta7d: week.Delta, Delta30d: w[consts.Window30d].Delta,
		TrendingFrequency: signal.frequency, TrendingStreak: signal.streak, TrendingRank: signal.rank, TrendingScore: signal.score,
		DataQuality: Insufficient,
	}
	if fresh && row.TrendingScore == nil {
		row.TrendingScore = ptr(0)
	}
	if week.Delta != nil {
		row.RelativeGrowth = ptr(float64(*week.Delta) / float64(max(start7.Stars, int64(cfg.RelativeStarFloor))))
	}
	if current.Delta != nil && prev.Delta != nil {
		row.Acceleration = ptr((float64(max(*current.Delta, 0)) + 1) / (float64(max(*prev.Delta, 0)) + 1))
	}
	var valid, positive int
	var peak, total int64
	for _, day := range days {
		if day.Quality != Exact {
			continue
		}
		valid++
		if delta := *day.Delta; delta > 0 {
			positive++
			total += delta
			peak = max(peak, delta)
		}
	}
	if valid >= cfg.MinPersistenceDays {
		row.PersistenceScore = ptr(100 * float64(positive) / float64(valid))
	}
	if total > 0 {
		row.DailyConcentration = ptr(float64(peak) / float64(total))
	}
	if current.Quality != Exact {
		return row
	}
	features := [5]*float64{ptr(scale(float64(*current.Delta), float64(cfg.Velocity24Cap))), nil, nil, row.PersistenceScore, row.TrendingScore}
	if week.Quality == Exact {
		features[1] = ptr(scale(float64(*week.Delta), float64(cfg.Velocity7Cap)))
	}
	if prev.Quality == Exact {
		features[2] = ptr(scale(*row.Acceleration-1, float64(cfg.AccelerationCap)))
	}
	var score, knownWeight float64
	var known int
	for i, value := range features {
		if value != nil {
			score += *value * cfg.Weights[i]
			knownWeight += cfg.Weights[i]
			known++
		}
	}
	if known >= 3 {
		row.HotnessScore = ptr(score / knownWeight)
		row.DataQuality = Estimated
		if known == len(features) {
			row.DataQuality = Exact
		}
	}
	return row
}
