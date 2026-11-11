package metrics

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

type anchors struct {
	raw, hourly map[int64][]Point
}

func (a anchors) pick(id, target int64, tolerance config.BoundaryToleranceMinutes, hourlyOnly bool) *Point {
	limit := int64(tolerance.Max) * minute
	if !hourlyOnly {
		if p := nearest(a.raw[id], target, limit); p != nil {
			return p
		}
	}
	return nearest(a.hourly[id], target, limit)
}

// loadAnchors 每个仓库对目标时刻做两次索引查找；小时层只读取可能命中的桶。
func loadAnchors(tx *gorm.DB, target, limit, asOf int64, hourlyOnly bool) (anchors, error) {
	a := anchors{make(map[int64][]Point), make(map[int64][]Point)}
	if !hourlyOnly {
		type row struct {
			RepoID int64
			Point  `gorm:"embedded"`
		}
		for _, seek := range []struct {
			min, max int64
			order    string
		}{
			{target - limit, target, "DESC"}, {target, min(asOf, target+limit), "ASC"},
		} {
			var rows []row
			err := tx.Raw(fmt.Sprintf(`
SELECT r.%[1]s AS repo_id,s.%[2]s AS at,s.%[3]s AS stars,s.%[4]s AS slot
FROM %[5]s r JOIN %[6]s s ON s.%[7]s=r.%[1]s AND s.%[4]s=(
  SELECT x.%[4]s FROM %[6]s x WHERE x.%[7]s=r.%[1]s AND x.%[2]s BETWEEN ? AND ?
  ORDER BY x.%[2]s `,
				storage.RepositoryColumns.RepoID, storage.RepoSnapshotColumns.ObservedAt, storage.RepoSnapshotColumns.Stars, storage.RepoSnapshotColumns.SampleSlot, storage.RepositoryTable, storage.RepoSnapshotTable, storage.RepoSnapshotColumns.RepoID)+seek.order+`,x.`+storage.RepoSnapshotColumns.SampleSlot+` ASC LIMIT 1
) WHERE r.`+storage.RepositoryColumns.State+`='`+consts.StatusActive+`'`, seek.min, seek.max).Scan(&rows).Error
			if err != nil {
				return a, err
			}
			for _, row := range rows {
				row.Resolution = consts.ResolutionRaw
				a.raw[row.RepoID] = append(a.raw[row.RepoID], row.Point)
			}
		}
	}
	var hours []storage.HourlyMetric
	if err := tx.Table(storage.HourlyMetricTable+" AS h").Select("h.*").
		Joins("JOIN "+storage.RepositoryTable+" r ON r."+storage.RepositoryColumns.RepoID+"=h."+storage.HourlyMetricColumns.RepoID+" AND r."+storage.RepositoryColumns.State+"='"+consts.StatusActive+"'").
		Where("h."+storage.HourlyMetricColumns.HourStart+" BETWEEN ? AND ?", time.UnixMilli(target-limit).UTC().Truncate(time.Hour).UnixMilli(), min(asOf, target+limit)).
		Find(&hours).Error; err != nil {
		return a, err
	}
	for _, h := range hours {
		if h.OpenObservedAt <= asOf {
			a.hourly[h.RepoID] = append(a.hourly[h.RepoID], Point{h.OpenObservedAt, h.StarsOpen, h.OpenSlot, consts.ResolutionHourly})
		}
		if h.CloseObservedAt <= asOf && h.CloseSlot != h.OpenSlot {
			a.hourly[h.RepoID] = append(a.hourly[h.RepoID], Point{h.CloseObservedAt, h.StarsClose, h.CloseSlot, consts.ResolutionHourly})
		}
	}
	return a, nil
}
