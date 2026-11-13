package metricstore

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	domain "github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// ErrMonitoringChanged 表示计算期间监控或身份发生变更，应重新读取快照。
var ErrMonitoringChanged = errors.New("监控状态已更新，指标快照已过期")

type Store struct{ DB *gorm.DB }

type transaction struct{ db *gorm.DB }

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
	return "(repositories.state = 'active' OR ((" + storage.PublicRepositoryCondition + ") AND (repositories.last_success_at IS NOT NULL OR repositories.starred_by_user = 1 OR repositories.stars >= 200)))"
}

func (r transaction) Repositories(asOf int64, observation bool) ([]domain.Repository, error) {
	c := storage.RepositoryColumns
	var rows []storage.Repository
	err := r.db.Select(c.RepoID, c.Grade, c.BelowGradeSince, c.FirstNotFoundAt, c.NextFetchAt,
		c.State, c.Stars, c.PrimaryLanguage, c.StarredByUser, c.LastSuccessAt, c.GitHubCreatedAt, c.GitHubPushedAt,
		c.Blocked, c.Disabled, c.NameIsCurrent).
		Where(observationSubjects(observation)).Order(c.RepoID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	var signals map[int64]int64
	paused := make(map[int64]int64)
	pausedReason := make(map[int64]string)
	lastDowngrade := make(map[int64]int64)
	trending := make(map[int64]int64)
	official := make(map[int64]int64)
	protected := make(map[int64]bool)
	if observation {
		signals, err = r.observationSignals(asOf)
		if err != nil {
			return nil, err
		}
		ids := r.db.Model(&storage.Repository{}).Select("repo_id").Where("state = ?", consts.StatusPaused)
		var transitions []storage.ObservationTransition
		if err := r.db.Select("repo_id", "occurred_at", "reason").Where("repo_id IN (?) AND to_state = ?", ids, consts.StatusPaused).
			Order("occurred_at DESC, id DESC").Find(&transitions).Error; err != nil {
			return nil, err
		}
		for _, transition := range transitions {
			if paused[transition.RepoID] == 0 {
				paused[transition.RepoID] = transition.OccurredAt
				pausedReason[transition.RepoID] = transition.Reason
			}
		}
		var cooled []struct{ RepoID, At int64 }
		if err := r.db.Model(&storage.ObservationTransition{}).Select("repo_id, MAX(occurred_at) AS at").
			Where("to_grade = ? AND from_grade <> ? AND to_state = ? AND occurred_at >= ?",
				consts.GradeC, consts.GradeC, consts.StatusActive, asOf-3*domain.Day).
			Group("repo_id").Scan(&cooled).Error; err != nil {
			return nil, err
		}
		for _, row := range cooled {
			lastDowngrade[row.RepoID] = row.At
		}
		var trend []struct{ RepoID, At int64 }
		if err := storage.StrongTrending(r.db, time.UnixMilli(asOf)).Select("o.repo_id, MAX(b.observed_at) AS at").
			Group("o.repo_id").Scan(&trend).Error; err != nil {
			return nil, err
		}
		for _, row := range trend {
			trending[row.RepoID] = row.At
		}
		var bursts []storage.RepoStarHistoryStatus
		if err := r.db.Select("repo_id", "breakout_at").Where("breakout_at BETWEEN ? AND ?", asOf-2*domain.Day, asOf).
			Find(&bursts).Error; err != nil {
			return nil, err
		}
		for _, row := range bursts {
			official[row.RepoID] = *row.BreakoutAt
		}
		for _, query := range []*gorm.DB{
			r.db.Model(&storage.AISummary{}).Where("summary_status = ? AND summary_zh <> ''", consts.StatusReady),
			r.db.Model(&storage.TopicCandidate{}).Where("status IN ?", []string{consts.StatusCandidate, consts.StatusShortlisted}),
		} {
			var ids []int64
			if err := query.Distinct("repo_id").Pluck("repo_id", &ids).Error; err != nil {
				return nil, err
			}
			for _, id := range ids {
				protected[id] = true
			}
		}
	}
	repos := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repo := domain.Repository{ID: row.RepoID, Grade: row.Grade, State: row.State, Stars: row.Stars,
			Followed: row.StarredByUser, LastSuccessAt: row.LastSuccessAt, LastSignalAt: signals[row.RepoID], PausedAt: paused[row.RepoID], PausedReason: pausedReason[row.RepoID], BelowGradeSince: row.BelowGradeSince,
			FirstNotFoundAt: row.FirstNotFoundAt, NextFetchAt: row.NextFetchAt, CreatedAt: row.GitHubCreatedAt, PushedAt: row.GitHubPushedAt,
			Blocked: row.Blocked, Disabled: row.Disabled, NameIsCurrent: row.NameIsCurrent,
			TrustedTrendingAt: trending[row.RepoID], OfficialBurstAt: official[row.RepoID], PreserveAvailability: protected[row.RepoID],
			LastDowngradeAt: lastDowngrade[row.RepoID]}
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

// Publish 在短写事务中复核档位和身份，避免旧快照覆盖屏蔽、恢复和人工关注。
func (s Store) Publish(ctx context.Context, slot string, asOf int64, force bool, repos []domain.Repository, calculations []domain.Calculation, grades []domain.GradeChange) error {
	// 计算仍覆盖观察池候选，最终只持久化会留在活跃池的指标。
	active := make(map[int64]bool, len(repos))
	for _, repo := range repos {
		active[repo.ID] = repo.State == consts.StatusActive
	}
	for _, grade := range grades {
		if grade.State != "" {
			active[grade.RepoID] = grade.State == consts.StatusActive
		}
	}
	rows := make([]storage.LatestMetric, 0, len(calculations))
	for _, result := range calculations {
		if !active[result.RepoID] {
			continue
		}
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
	return s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		r := transaction{db: db}
		lastSlot, at, found, err := r.ComputeMarker()
		if err != nil || found && (at > asOf || !force && lastSlot == slot) {
			return err
		}
		current, err := r.verifyMonitoringSnapshot(repos, grades)
		if err != nil {
			return err
		}
		return r.publish(slot, asOf, rows, calculations, grades, current)
	})
}

// verifyMonitoringSnapshot 只比较席位、身份和恢复状态。并发采样可以继续落盘。
func (r transaction) verifyMonitoringSnapshot(repos []domain.Repository, grades []domain.GradeChange) (map[int64]storage.Repository, error) {
	expected := make(map[int64]domain.Repository, len(repos))
	for _, repo := range repos {
		expected[repo.ID] = repo
	}
	ids := make([]int64, 0, len(grades))
	samplingChanges := make(map[int64]bool, len(grades))
	for _, grade := range grades {
		ids = append(ids, grade.RepoID)
		samplingChanges[grade.RepoID] = grade.NextFetchAt != nil
	}
	var rows []storage.Repository
	if err := r.db.Select("repo_id", "state", "grade", "below_grade_since", "starred_by_user", "blocked", "disabled", "name_is_current",
		"first_not_found_at", "last_success_at", "next_fetch_at", "updated_at").
		Where("state = ? OR repo_id IN ?", consts.StatusActive, ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	current := make(map[int64]storage.Repository, len(rows))
	for _, row := range rows {
		before, exists := expected[row.RepoID]
		if !exists || before.State != row.State || before.Grade != row.Grade || before.Followed != row.StarredByUser ||
			before.Blocked != row.Blocked || before.Disabled != row.Disabled || before.NameIsCurrent != row.NameIsCurrent ||
			!sameTime(before.FirstNotFoundAt, row.FirstNotFoundAt) || !sameTime(before.BelowGradeSince, row.BelowGradeSince) ||
			samplingChanges[row.RepoID] && !sameTime(before.LastSuccessAt, row.LastSuccessAt) {
			return nil, ErrMonitoringChanged
		}
		current[row.RepoID] = row
	}
	for _, before := range repos {
		if before.State == consts.StatusActive && current[before.ID].RepoID == 0 {
			return nil, ErrMonitoringChanged
		}
	}
	for _, id := range ids {
		if current[id].RepoID == 0 {
			return nil, ErrMonitoringChanged
		}
	}
	return current, nil
}

func sameTime(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// publish 写入已算好的指标，事务内只做必要的状态更新和水位提交。
func (r transaction) publish(slot string, asOf int64, rows []storage.LatestMetric, calculations []domain.Calculation, grades []domain.GradeChange, current map[int64]storage.Repository) error {
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
		repo := current[grade.RepoID]
		updates := map[string]any{c.Grade: grade.Grade, c.BelowGradeSince: grade.BelowGradeSince, c.UpdatedAt: max(asOf, repo.UpdatedAt)}
		updateNext := grade.NextFetchAt != nil
		if updateNext {
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
			if err := storage.RecordObservation(r.db, repo, grade.State, grade.Grade, grade.Reason, asOf, grade.Evidence); err != nil {
				return err
			}
			decision := admission.Decision{State: "monitoring", Reason: grade.Reason}
			if grade.State == consts.StatusPaused {
				decision.State = "deferred"
			}
			evidence := admission.Evidence{Exact24h: grade.Evidence.Exact24h}
			if grade.Evidence.WindowStartAt != nil && grade.Evidence.WindowEndAt != nil {
				evidence.StartAt, evidence.EndAt = *grade.Evidence.WindowStartAt, *grade.Evidence.WindowEndAt
			}
			if grade.Evidence.Breakout != nil {
				evidence.BurstAt, evidence.Source = grade.Evidence.Breakout.At, grade.Evidence.Breakout.Source
			}
			if err := storage.SaveAdmission(r.db, repo.RepoID, "monitor", decision, evidence, time.UnixMilli(asOf)); err != nil {
				return err
			}
			updates[c.State], updates["observation_probe_due_at"] = grade.State, 0
			if grade.State == consts.StatusPaused && repo.FirstNotFoundAt != nil {
				updates["observation_probe_due_at"] = max(asOf, *repo.FirstNotFoundAt+domain.Hour)
			}
			if err := storage.UpdateObservationJobs(r.db, grade.RepoID, asOf,
				grade.State != consts.StatusActive || updateNext && *grade.NextFetchAt > asOf,
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
	if err := storage.RecordMonitoringTotal(r.db, time.UnixMilli(asOf)); err != nil {
		return err
	}
	marker := storage.MaintenanceState{Key: metrics.ComputeKey, Value: slot, UpdatedAt: asOf}
	return r.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
}
