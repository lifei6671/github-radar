package metricstore

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	domain "github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

type Store struct{ DB *gorm.DB }

type transaction struct{ db *gorm.DB }

func (s Store) Transaction(ctx context.Context, work func(metrics.Transaction) error) error {
	return s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		return work(transaction{db: db})
	})
}

func (s Store) ReadTransaction(ctx context.Context, work func(metrics.Transaction) error) error {
	return s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		return work(transaction{db: db})
	}, &sql.TxOptions{ReadOnly: true})
}

func (r transaction) ComputeMarker() (string, int64, bool, error) {
	var marker storage.MaintenanceState
	err := r.db.Find(&marker, storage.MaintenanceStateColumns.Key+" = ?", metrics.ComputeKey).Error
	return marker.Value, marker.UpdatedAt, marker.Key != "", err
}

// observationSubjects 复用已有公开身份约束。只有已有本地样本或已人工重点的非活跃仓库才参与指标计算。
func observationSubjects(observation bool) string {
	if !observation {
		return "repositories.state = 'active'"
	}
	// 活跃项目即使正在复核 404，也需要占用原容量直至确认下线。
	return "(repositories.state = 'active' OR ((" + storage.PublicRepositoryCondition + ") AND (repositories.last_success_at IS NOT NULL OR repositories.starred_by_user = 1)))"
}

func (r transaction) Repositories(asOf int64, observation bool) ([]domain.Repository, error) {
	c := storage.RepositoryColumns
	var rows []storage.Repository
	err := r.db.Select(c.RepoID, c.Grade, c.BelowGradeSince, c.FirstNotFoundAt, c.NextFetchAt,
		c.State, c.Stars, c.PrimaryLanguage, c.StarredByUser, c.LastSuccessAt).
		Where(observationSubjects(observation)).Order(c.RepoID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	var signals map[int64]int64
	paused := make(map[int64]int64)
	if observation {
		signals, err = r.observationSignals(asOf)
		if err != nil {
			return nil, err
		}
		var history []struct{ RepoID, At int64 }
		ids := r.db.Model(&storage.Repository{}).Select("repo_id").Where("state = ?", consts.StatusPaused)
		if err := r.db.Model(&storage.ObservationTransition{}).Select("repo_id, MAX(occurred_at) AS at").
			Where("repo_id IN (?) AND to_state = ?", ids, consts.StatusPaused).Group("repo_id").Scan(&history).Error; err != nil {
			return nil, err
		}
		for _, row := range history {
			paused[row.RepoID] = row.At
		}
	}
	repos := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repo := domain.Repository{ID: row.RepoID, Grade: row.Grade, State: row.State, Stars: row.Stars,
			Followed: row.StarredByUser, LastSuccessAt: row.LastSuccessAt, LastSignalAt: signals[row.RepoID], PausedAt: paused[row.RepoID], BelowGradeSince: row.BelowGradeSince,
			FirstNotFoundAt: row.FirstNotFoundAt, NextFetchAt: row.NextFetchAt}
		if row.PrimaryLanguage != nil {
			repo.Language = *row.PrimaryLanguage
		}
		repos = append(repos, repo)
	}
	return repos, nil
}

// observationSignals 合并近期榜单和人工记录的传播链接，重复 Search 命中不刷新传播时间。
func (r transaction) observationSignals(asOf int64) (map[int64]int64, error) {
	result := make(map[int64]int64)
	for _, source := range []struct{ table, field, condition string }{
		{storage.TrendingObservationTable, "observed_slot", "rank > 0"},
		{"repo_discovery_events", "first_observed_at", "source_url <> ''"},
	} {
		var rows []struct{ RepoID, At int64 }
		if err := r.db.Table(source.table).Select("repo_id, MAX("+source.field+") AS at").
			Where(source.condition).Where(source.field+" BETWEEN ? AND ?", asOf-14*domain.Day, asOf).
			Group("repo_id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			result[row.RepoID] = max(result[row.RepoID], row.At)
		}
	}
	return result, nil
}

// LoadObservationPoints 读取原始采样及保留的小时锚点。相同观测只参与一次连续性判定。
func (r transaction) LoadObservationPoints(from, to int64) (map[int64][]domain.Point, error) {
	type row struct{ RepoID, At, Stars, Slot int64 }
	var raw, hourly []row
	active := r.db.Model(&storage.Repository{}).Select("repo_id").Where(observationSubjects(true)).
		Where("state = ? OR last_success_at >= ?", consts.StatusActive, to-domain.Day)
	if err := r.db.Model(&storage.RepoSnapshot{}).Select("repo_id, observed_at AS at, stars, sample_slot AS slot").
		Where("observed_at BETWEEN ? AND ? AND repo_id IN (?)", from, to, active).Scan(&raw).Error; err != nil {
		return nil, err
	}
	for _, prefix := range []string{"open", "close"} {
		var rows []row
		if err := r.db.Model(&storage.HourlyMetric{}).
			Select("repo_id, "+prefix+"_observed_at AS at, stars_"+prefix+" AS stars, "+prefix+"_slot AS slot").
			Where("hour_start BETWEEN ? AND ? AND "+prefix+"_observed_at BETWEEN ? AND ? AND repo_id IN (?)",
				time.UnixMilli(from).Truncate(time.Hour).UnixMilli(), to, from, to, active).Scan(&rows).Error; err != nil {
			return nil, err
		}
		hourly = append(hourly, rows...)
	}
	points := make(map[int64][]domain.Point)
	for _, group := range [][]row{raw, hourly} {
		for _, r := range group {
			points[r.RepoID] = append(points[r.RepoID], domain.Point{At: r.At, Stars: r.Stars, Slot: r.Slot})
		}
	}
	for id, series := range points {
		slices.SortStableFunc(series, func(a, b domain.Point) int {
			if n := cmp.Compare(b.At, a.At); n != 0 {
				return n
			}
			return cmp.Compare(a.Slot, b.Slot)
		})
		points[id] = slices.CompactFunc(series, func(a, b domain.Point) bool { return a.At == b.At })
	}
	return points, nil
}

func (r transaction) Publish(slot string, asOf int64, calculations []domain.Calculation, grades []domain.GradeChange) error {
	rows := make([]storage.LatestMetric, 0, len(calculations))
	for _, result := range calculations {
		windows, err := json.Marshal(result.Windows)
		if err != nil {
			return err
		}
		breakout := ""
		signal := result.Breakout
		if signal == nil {
			signal = result.ObservationBreakout
		}
		if signal != nil {
			encoded, err := json.Marshal(signal)
			if err != nil {
				return err
			}
			breakout = string(encoded)
		}
		rows = append(rows, storage.LatestMetric{
			RepoID: result.RepoID, CalculatedAt: result.CalculatedAt, WindowDetailsJSON: string(windows),
			Delta1h: result.Delta1h, Delta24h: result.Delta24h, Delta7d: result.Delta7d, Delta30d: result.Delta30d,
			RelativeGrowth: result.RelativeGrowth, Acceleration: result.Acceleration, PersistenceScore: result.PersistenceScore,
			DailyConcentration: result.DailyConcentration, HotnessScore: result.HotnessScore, CandidateBurst: result.CandidateBurst,
			TrendingFrequency: result.TrendingFrequency, TrendingStreak: result.TrendingStreak, TrendingRank: result.TrendingRank,
			TrendingScore: result.TrendingScore, DataQuality: result.DataQuality, BreakoutJSON: breakout,
		})
	}
	if len(rows) > 0 {
		if err := r.db.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(&rows, 100).Error; err != nil {
			return err
		}
	}
	for _, row := range calculations {
		signal := row.Breakout
		if signal == nil {
			signal = row.ObservationBreakout
		}
		if signal == nil {
			continue
		}
		// 同一官方事件经本地爆发确认后不再退回待确认；新官方自然日仍可独立判定。
		if err := r.db.Model(&storage.RepoStarHistoryStatus{}).Where("repo_id = ? AND breakout_at > ? AND breakout_at < ?",
			row.RepoID, signal.StartAt, signal.At+domain.Day).
			Update("confirmed_at", gorm.Expr("breakout_at")).Error; err != nil {
			return err
		}
	}
	c := storage.RepositoryColumns
	for _, grade := range grades {
		updates := map[string]any{c.Grade: grade.Grade, c.BelowGradeSince: grade.BelowGradeSince, c.UpdatedAt: asOf}
		if grade.NextFetchAt != nil {
			updates[c.NextFetchAt] = *grade.NextFetchAt
		}
		if grade.Breakout != nil {
			// 保留实际进入 S 档时的证据，后续一轮信号消失不擦除入档依据。
			encoded, err := json.Marshal(grade.Breakout)
			if err != nil {
				return err
			}
			updates[c.BreakoutGradeJSON] = string(encoded)
		}
		if grade.State != "" {
			var repo storage.Repository
			if err := r.db.Take(&repo, c.RepoID+" = ?", grade.RepoID).Error; err != nil {
				return err
			}
			if err := storage.RecordObservation(r.db, repo, grade.State, grade.Grade, grade.Reason, asOf, grade.Evidence); err != nil {
				return err
			}
			updates[c.State], updates["observation_probe_due_at"] = grade.State, 0
			if grade.State == consts.StatusPaused && repo.FirstNotFoundAt != nil {
				updates["observation_probe_due_at"] = max(asOf, *repo.FirstNotFoundAt+domain.Hour)
			}
			if err := storage.UpdateObservationJobs(r.db, grade.RepoID, asOf,
				grade.State != consts.StatusActive || grade.NextFetchAt != nil && *grade.NextFetchAt > asOf,
				storage.SamplingPriority(grade.Grade)); err != nil {
				return err
			}
		}
		if err := r.db.Model(&storage.Repository{}).Where(c.RepoID+" = ?", grade.RepoID).Updates(updates).Error; err != nil {
			return err
		}
	}
	inactive := r.db.Model(&storage.Repository{}).Select(c.RepoID).Where(c.State+" <> ?", consts.StatusActive)
	if err := r.db.Where(storage.LatestMetricColumns.RepoID+" IN (?)", inactive).Delete(&storage.LatestMetric{}).Error; err != nil {
		return err
	}
	marker := storage.MaintenanceState{Key: metrics.ComputeKey, Value: slot, UpdatedAt: asOf}
	return r.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
}
