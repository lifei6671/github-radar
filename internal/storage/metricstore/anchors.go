package metricstore

import (
	"fmt"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// LoadAnchors 每个仓库对目标时刻做两次索引查找；小时层只读取可能命中的桶。
func (r transaction) LoadAnchors(target, limit, asOf int64, hourlyOnly, observation bool) (metrics.Anchors, error) {
	tx := r.db
	a := metrics.Anchors{Raw: make(map[int64][]metrics.Point), Hourly: make(map[int64][]metrics.Point)}
	if !hourlyOnly {
		type row struct {
			RepoID        int64
			metrics.Point `gorm:"embedded"`
		}
		for _, seek := range []struct {
			min, max int64
			order    string
		}{
			{target - limit, target, "DESC"}, {target, min(asOf, target+limit), "ASC"},
		} {
			var rows []row
			err := tx.Raw(fmt.Sprintf(`
SELECT %[5]s.%[1]s AS repo_id,s.%[2]s AS at,s.%[3]s AS stars,s.%[4]s AS slot
FROM %[5]s JOIN %[6]s s ON s.%[7]s=%[5]s.%[1]s AND s.%[4]s=(
  SELECT x.%[4]s FROM %[6]s x WHERE x.%[7]s=%[5]s.%[1]s AND x.%[2]s BETWEEN ? AND ?
  ORDER BY x.%[2]s `,
				storage.RepositoryColumns.RepoID, storage.RepoSnapshotColumns.ObservedAt, storage.RepoSnapshotColumns.Stars, storage.RepoSnapshotColumns.SampleSlot, storage.RepositoryTable, storage.RepoSnapshotTable, storage.RepoSnapshotColumns.RepoID)+seek.order+`,x.`+storage.RepoSnapshotColumns.SampleSlot+` ASC LIMIT 1
) WHERE `+observationSubjects(observation), seek.min, seek.max).Scan(&rows).Error
			if err != nil {
				return a, err
			}
			for _, row := range rows {
				row.Resolution = consts.ResolutionRaw
				a.Raw[row.RepoID] = append(a.Raw[row.RepoID], row.Point)
			}
		}
	}
	var hours []storage.HourlyMetric
	if err := tx.Table(storage.HourlyMetricTable+" AS h").Select("h.*").
		Joins("JOIN "+storage.RepositoryTable+" ON "+storage.RepositoryTable+"."+storage.RepositoryColumns.RepoID+"=h."+storage.HourlyMetricColumns.RepoID).
		Where(observationSubjects(observation)).
		Where("h."+storage.HourlyMetricColumns.HourStart+" BETWEEN ? AND ?", time.UnixMilli(target-limit).UTC().Truncate(time.Hour).UnixMilli(), min(asOf, target+limit)).
		Find(&hours).Error; err != nil {
		return a, err
	}
	for _, h := range hours {
		if h.OpenObservedAt <= asOf {
			a.Hourly[h.RepoID] = append(a.Hourly[h.RepoID], metrics.Point{At: h.OpenObservedAt, Stars: h.StarsOpen, Slot: h.OpenSlot, Resolution: consts.ResolutionHourly})
		}
		if h.CloseObservedAt <= asOf && h.CloseSlot != h.OpenSlot {
			a.Hourly[h.RepoID] = append(a.Hourly[h.RepoID], metrics.Point{At: h.CloseObservedAt, Stars: h.StarsClose, Slot: h.CloseSlot, Resolution: consts.ResolutionHourly})
		}
	}
	return a, nil
}
