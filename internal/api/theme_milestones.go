package api

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

const themeMonthLimit = 5
const themeTrendingTop = 3

var themeStarThresholds = []int64{100_000, 50_000, 10_000, 5_000, 1_000}
var themeCalendar = time.FixedZone("CST", 8*3600)

type themeMilestone struct {
	Kind     string `json:"kind"`
	FullName string `json:"fullName"`
	At       string `json:"at"`
	Value    int64  `json:"value"`
}

// 大事记只用已采集的榜单和真实日聚合锚点，按北京时间归月。
func themeMilestones(c *gin.Context, db *gorm.DB, slug string, now time.Time) ([]themeMilestone, error) {
	today := now.In(themeCalendar)
	since := time.Date(today.Year(), today.Month()-11, 1, 0, 0, 0, 0, themeCalendar)
	r, theme := storage.RepositoryColumns, storage.ProjectThemeColumns
	members := func() *gorm.DB {
		return publicRepos(c, db.Model(&storage.Repository{})).
			Select(storage.RepositoryTable+"."+r.RepoID+", "+storage.RepositoryTable+"."+r.FullName).
			Joins("JOIN "+storage.ProjectThemeTable+" ON "+projectThemeMatch).
			Where(storage.ProjectThemeTable+"."+theme.Slug+"=?", slug)
	}

	type observed struct {
		Kind       string `gorm:"-"`
		FullName   string `gorm:"column:full_name"`
		Value      int64  `gorm:"column:value"`
		ObservedAt int64  `gorm:"column:observed_at"`
	}
	o, b := storage.TrendingObservationColumns, storage.TrendingBatchRecordColumns
	var trending []observed
	err := db.Table(storage.TrendingBatchRecordTable+" b").
		Select("m."+r.FullName+" AS full_name, o."+o.Rank+" AS value, b."+b.ObservedAt+" AS observed_at").
		Joins("JOIN "+storage.TrendingObservationTable+" o ON o."+o.SourceKey+"=b."+b.SourceKey+
			" AND o."+o.Window+"=b."+b.Window+" AND o."+o.ObservedSlot+"=b."+b.ObservedSlot).
		Joins("JOIN (?) m ON m."+r.RepoID+"=o."+o.RepoID, members()).
		Where("b."+b.SourceKey+"=? AND b."+b.Window+"=?", consts.TrendingAll, consts.PeriodDaily).
		Where("b."+b.ObservedAt+" BETWEEN ? AND ?", since.UnixMilli(), now.UnixMilli()).
		Where("o."+o.Rank+" BETWEEN 1 AND ?", themeTrendingTop).
		Order("o." + o.Rank).Order("b." + b.ObservedAt + " DESC").Order("m." + r.RepoID).Scan(&trending).Error
	if err != nil {
		return nil, err
	}

	d := storage.DailyMetricColumns
	var daily []struct {
		FullName   string `gorm:"column:full_name"`
		ObservedAt int64  `gorm:"column:observed_at"`
		StarsOpen  int64  `gorm:"column:stars_open"`
		StarsClose int64  `gorm:"column:stars_close"`
	}
	var ranges []string
	var thresholds []any
	for _, threshold := range themeStarThresholds {
		ranges = append(ranges, "(d."+d.StarsOpen+"<? AND d."+d.StarsClose+">=?)")
		thresholds = append(thresholds, threshold, threshold)
	}
	err = db.Table(storage.DailyMetricTable+" d").
		Select("m."+r.FullName+" AS full_name, d."+d.CloseObservedAt+" AS observed_at, d."+d.StarsOpen+
			" AS stars_open, d."+d.StarsClose+" AS stars_close").
		Joins("JOIN (?) m ON m."+r.RepoID+"=d."+d.RepoID, members()).
		Where("d."+d.Day+">=? AND d."+d.CloseObservedAt+" BETWEEN ? AND ?", since.UTC().Format(time.DateOnly), since.UnixMilli(), now.UnixMilli()).
		Where("("+strings.Join(ranges, " OR ")+")", thresholds...).
		Order("d." + d.CloseObservedAt).Order("m." + r.RepoID).Scan(&daily).Error
	if err != nil {
		return nil, err
	}

	monthOf := func(at int64) string { return time.UnixMilli(at).In(themeCalendar).Format("2006-01") }
	months := make(map[string][]observed)
	seenTrending := make(map[[2]string]bool)
	for _, row := range trending {
		month := monthOf(row.ObservedAt)
		key := [2]string{month, row.FullName}
		if !seenTrending[key] {
			seenTrending[key] = true
			row.Kind = consts.MilestoneTrending
			months[month] = append(months[month], row)
		}
	}
	type crossing struct {
		repo      string
		threshold int64
	}
	seenStars := make(map[crossing]bool)
	for _, row := range daily {
		var best int64
		for _, threshold := range themeStarThresholds {
			key := crossing{row.FullName, threshold}
			if !seenStars[key] && row.StarsOpen < threshold && row.StarsClose >= threshold {
				seenStars[key] = true
				best = max(best, threshold)
			}
		}
		if best != 0 {
			month := monthOf(row.ObservedAt)
			months[month] = append(months[month], observed{
				Kind: consts.MilestoneStars, FullName: row.FullName, Value: best, ObservedAt: row.ObservedAt,
			})
		}
	}
	// 每月按名次或跨越的 Star 档位取舍，最终统一按时间倒序呈现。
	newest := func(a, b observed) int {
		return cmp.Or(cmp.Compare(b.ObservedAt, a.ObservedAt), cmp.Compare(a.FullName, b.FullName), cmp.Compare(a.Kind, b.Kind))
	}
	var chosen []observed
	for _, rows := range months {
		slices.SortFunc(rows, func(a, b observed) int {
			if a.Kind != b.Kind {
				if a.Kind == consts.MilestoneTrending {
					return -1
				}
				return 1
			}
			order := cmp.Compare(a.Value, b.Value)
			if a.Kind == consts.MilestoneStars {
				order = -order
			}
			return cmp.Or(order, newest(a, b))
		})
		chosen = append(chosen, rows[:min(themeMonthLimit, len(rows))]...)
	}
	slices.SortFunc(chosen, newest)
	result := make([]themeMilestone, 0, len(chosen))
	for _, row := range chosen {
		result = append(result, themeMilestone{Kind: row.Kind, FullName: row.FullName, Value: row.Value, At: iso(row.ObservedAt)})
	}
	return result, nil
}
