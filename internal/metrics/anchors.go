package metrics

import (
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
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
			err := tx.Raw(`
SELECT r.repo_id,s.observed_at AS at,s.stars,s.sample_slot AS slot
FROM repositories r JOIN repo_snapshots s ON s.repo_id=r.repo_id AND s.sample_slot=(
  SELECT x.sample_slot FROM repo_snapshots x WHERE x.repo_id=r.repo_id AND x.observed_at BETWEEN ? AND ?
  ORDER BY x.observed_at `+seek.order+`,x.sample_slot ASC LIMIT 1
) WHERE r.state='active'`, seek.min, seek.max).Scan(&rows).Error
			if err != nil {
				return a, err
			}
			for _, row := range rows {
				row.Resolution = "raw"
				a.raw[row.RepoID] = append(a.raw[row.RepoID], row.Point)
			}
		}
	}
	var hours []storage.HourlyMetric
	if err := tx.Table("metrics_hourly AS h").Select("h.*").
		Joins("JOIN repositories r ON r.repo_id=h.repo_id AND r.state='active'").
		Where("h.hour_start BETWEEN ? AND ?", time.UnixMilli(target-limit).UTC().Truncate(time.Hour).UnixMilli(), min(asOf, target+limit)).
		Find(&hours).Error; err != nil {
		return a, err
	}
	for _, h := range hours {
		if h.OpenObservedAt <= asOf {
			a.hourly[h.RepoID] = append(a.hourly[h.RepoID], Point{h.OpenObservedAt, h.StarsOpen, h.OpenSlot, "hourly"})
		}
		if h.CloseObservedAt <= asOf && h.CloseSlot != h.OpenSlot {
			a.hourly[h.RepoID] = append(a.hourly[h.RepoID], Point{h.CloseObservedAt, h.StarsClose, h.CloseSlot, "hourly"})
		}
	}
	return a, nil
}
