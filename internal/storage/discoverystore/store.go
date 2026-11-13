package discoverystore

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

const (
	TimeSQL       = "MAX(recent_at, growth_at, trending_at)"
	millisPerDay  = int64(24 * time.Hour / time.Millisecond)
	beijingOffset = int64(8 * time.Hour / time.Millisecond)
	streakPenalty = 10
	penaltyDays   = 2
)

func BeijingDay(at int64) int64 { return (at + beijingOffset) / millisPerDay }

func BeijingDaySQL(column string) string {
	return fmt.Sprintf("((%s + %d) / %d)", column, beijingOffset, millisPerDay)
}

// Matches 统一用于发现页、精选标记和后台按日记录，热度原值单独保留。
func Matches(tx *gorm.DB, now time.Time) (*gorm.DB, int64, error) {
	snapshot, err := (settings.Store{DB: tx}).Get(tx.Statement.Context)
	if err != nil {
		return nil, 0, err
	}
	rules, ranking := snapshot.DiscoverySelection, snapshot.DiscoveryRanking
	asOf := now.UnixMilli()
	day := BeijingDay(asOf)
	since := func(hours int) int64 { return asOf - (time.Duration(hours) * time.Hour).Milliseconds() }
	repoID := storage.RepositoryTable + "." + storage.RepositoryColumns.RepoID
	discoveredAt := storage.RepositoryTable + "." + storage.RepositoryColumns.DiscoveredAt
	discoveredDay := BeijingDaySQL(discoveredAt)
	old := day - int64(rules.RecentDays)
	hotness := "m." + storage.LatestMetricColumns.HotnessScore
	growthAt := storage.MetricWindowSQL(consts.Window24h, "endAt")
	batch := tx.Model(&storage.MaintenanceState{}).
		Select("json_extract("+storage.MaintenanceStateColumns.Value+",'$.observedSlot') AS observed_slot, json_extract("+storage.MaintenanceStateColumns.Value+",'$.observedAt') AS observed_at").
		Where(storage.MaintenanceStateColumns.Key+"=? AND EXISTS (SELECT 1 FROM "+storage.DiscoveryRuleTable+" WHERE "+storage.DiscoveryRuleColumns.SourceType+"='"+consts.SourceTrending+"' AND "+storage.DiscoveryRuleColumns.SourceKey+"='"+consts.TrendingAllDaily+"')", collection.BatchKey(consts.TrendingAll, consts.PeriodDaily))

	// 当天被记录前后均只扣前几天的分，避免后台执行时间改变当天排序。
	score := fmt.Sprintf(`? * COALESCE(%s,0) + ? * MAX(0.0, 1.0 - MAX(0, ? - %s) * 1.0 / ?)
 - CASE WHEN %s <= ? THEN ? * MIN(?, CASE
     WHEN rotation.last_day = ? THEN rotation.streak - 1
     WHEN rotation.last_day = ? THEN rotation.streak ELSE 0 END)
   ELSE 0 END`, hotness, discoveredDay, discoveredDay)
	return tx.Model(&storage.Repository{}).Where(storage.PublishableRepositoryCondition).
		Select(fmt.Sprintf(`%[1]s.*, %[2]s AS raw_hotness, %[3]s AS discovery_score, rotation.last_day AS rotation_day,
  CASE WHEN %[1]s.%[4]s IN ('%[5]s','%[6]s') AND %[7]s BETWEEN ? AND ?
    AND %[1]s.%[8]s BETWEEN ? AND ? THEN %[7]s ELSE 0 END AS recent_at,
  CASE WHEN %[9]s='%[10]s' AND m.%[11]s>=?
    AND %[12]s BETWEEN ? AND ? THEN %[12]s ELSE 0 END AS growth_at,
  CASE WHEN t.%[13]s IS NOT NULL THEN batch.observed_at ELSE 0 END AS trending_at`,
			storage.RepositoryTable, hotness, score, storage.RepositoryColumns.Grade, consts.GradeS, consts.GradeA, discoveredAt,
			storage.RepositoryColumns.LastSuccessAt, storage.MetricWindowSQL(consts.Window24h, "quality"), consts.QualityExact,
			storage.LatestMetricColumns.Delta24h, growthAt, storage.TrendingObservationColumns.RepoID),
			float64(ranking.HeatWeight)/100, ranking.FreshnessWeight, day, rules.RecentDays, old, streakPenalty, penaltyDays, day, day-1,
			since(rules.RecentDays*24), asOf, since(rules.RecentSampleHours), asOf,
			snapshot.Settings.Metrics.HotGrowth, since(rules.GrowthSampleHours), asOf).
		Joins("LEFT JOIN "+storage.LatestMetricTable+" m ON m."+storage.LatestMetricColumns.RepoID+"="+repoID).
		Joins("LEFT JOIN discovery_rotation rotation ON rotation.repo_id="+repoID).
		Joins("LEFT JOIN (?) batch ON batch.observed_at BETWEEN ? AND ?", batch, since(rules.TrendingHours), asOf).
		Joins(fmt.Sprintf("LEFT JOIN %[1]s t ON t.%[2]s=%[3]s AND t.%[4]s='%[5]s' AND t.%[6]s='%[7]s' AND t.%[8]s=batch.observed_slot AND t.%[9]s BETWEEN 1 AND ?",
			storage.TrendingObservationTable, storage.TrendingObservationColumns.RepoID, repoID, storage.TrendingObservationColumns.SourceKey,
			consts.TrendingAll, storage.TrendingObservationColumns.Window, consts.PeriodDaily, storage.TrendingObservationColumns.ObservedSlot, storage.TrendingObservationColumns.Rank), rules.TrendingTop).
		Where(storage.RepositoryTable+"."+storage.RepositoryColumns.State+" = ?", consts.StatusActive).
		// 保存新门槛后以今天以前的连续入选记录判定，避免当天已记录的项目绕过新规则。
		Where(`(`+discoveredDay+` > ? OR rotation.last_day IS NULL
  OR rotation.streak - CASE WHEN rotation.last_day = ? THEN 1 ELSE 0 END < ?
  OR ? - rotation.last_day + CASE WHEN rotation.last_day = ? THEN 1 ELSE 0 END NOT BETWEEN 1 AND ?)`,
			old, day, ranking.ConsecutiveDays, day, day, ranking.CooldownDays), snapshot.Revision, nil
}

// Record 每个北京时间自然日只记录一次有效入选。跳过冷却日和停机期间未观测到的日期。
func Record(ctx context.Context, db *gorm.DB, now time.Time) error {
	day := BeijingDay(now.UnixMilli())
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		matches, _, err := Matches(tx, now)
		if err != nil {
			return err
		}
		return tx.Exec(`
INSERT INTO discovery_rotation (repo_id, last_day, streak)
SELECT matches.repo_id, ?, 1 FROM (?) AS matches
WHERE `+TimeSQL+` > 0 AND (rotation_day IS NULL OR rotation_day < ?)
ON CONFLICT(repo_id) DO UPDATE SET
    streak = CASE WHEN discovery_rotation.last_day = excluded.last_day - 1
        THEN discovery_rotation.streak + 1 ELSE 1 END,
    last_day = excluded.last_day
WHERE discovery_rotation.last_day < excluded.last_day`, day, matches, day).Error
	})
}
