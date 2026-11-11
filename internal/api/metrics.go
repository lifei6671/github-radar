package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

type metricView struct {
	row       storage.LatestMetric
	rank      *int
	windows   map[string]metrics.Window
	sparkline []gin.H
}

func (v metricView) window(key string) gin.H {
	w := v.windows[key]
	resolution := consts.ResolutionRaw
	if key == consts.Window30d {
		resolution = consts.ResolutionMixed
	}
	if w.Resolution != nil {
		resolution = *w.Resolution
	}
	if w.Quality == "" {
		w.Quality = metrics.Insufficient
	}
	return gin.H{"delta": w.Delta, "quality": w.Quality, "startAt": optionalISO(w.StartAt), "endAt": optionalISO(w.EndAt), "resolution": resolution}
}

// metricViews 先在全部 active 仓库中计算排名，再取本次响应所需的仓库。
func (a *collectionAPI) metricViews(db *gorm.DB, repos []storage.Repository) (map[int64]metricView, error) {
	result := make(map[int64]metricView, len(repos))
	if len(repos) == 0 {
		return result, nil
	}
	ids := make([]int64, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.RepoID)
	}
	var rows []struct {
		storage.LatestMetric
		Rank *int
	}
	if err := db.Raw(fmt.Sprintf(`
WITH ranks AS (
 SELECT m.%[1]s,ROW_NUMBER() OVER(ORDER BY m.%[2]s DESC,m.%[1]s) AS rank
 FROM %[3]s m JOIN %[4]s r ON r.%[5]s=m.%[1]s
 WHERE r.%[6]s='%[7]s' AND m.%[2]s IS NOT NULL
)
SELECT m.*, ranks.rank FROM %[3]s m JOIN %[4]s r ON r.%[5]s=m.%[1]s AND r.%[6]s='%[7]s'
LEFT JOIN ranks ON ranks.%[1]s=m.%[1]s WHERE m.%[1]s IN ?`,
		storage.LatestMetricColumns.RepoID, storage.LatestMetricColumns.HotnessScore, storage.LatestMetricTable, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.RepositoryColumns.State, consts.StatusActive), ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		v := metricView{row: row.LatestMetric, rank: row.Rank}
		if err := json.Unmarshal([]byte(row.WindowDetailsJSON), &v.windows); err != nil {
			return nil, err
		}
		result[row.RepoID] = v
	}
	to := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	from := to - 24*metrics.Hour
	type key struct{ repo, bucket int64 }
	type spark struct{ RepoID, Bucket, Stars, Observed, Slot int64 }
	var closes []spark
	// 尚未合并的小时也能提供真实收盘样本，缺测小时保持空值。
	if err := db.Raw(fmt.Sprintf(`
SELECT %[1]s,%[2]s AS bucket,%[9]s AS stars,%[10]s AS observed,%[11]s AS slot
FROM %[3]s WHERE %[1]s IN ? AND %[2]s>=? AND %[2]s<?
UNION ALL
SELECT %[4]s,%[5]s,%[12]s,%[13]s,%[14]s FROM %[6]s
WHERE %[7]s='%[8]s' AND %[4]s IN ? AND %[5]s>=? AND %[5]s<?`,
		storage.HourlyMetricColumns.RepoID, storage.HourlyMetricColumns.HourStart, storage.HourlyMetricTable,
		storage.PendingMetricColumns.RepoID, storage.PendingMetricColumns.BucketStart, storage.PendingMetricTable, storage.PendingMetricColumns.Resolution, consts.ResolutionHourly,
		storage.HourlyMetricColumns.StarsClose, storage.HourlyMetricColumns.CloseObservedAt, storage.HourlyMetricColumns.CloseSlot,
		storage.PendingMetricColumns.StarsClose, storage.PendingMetricColumns.CloseObservedAt, storage.PendingMetricColumns.CloseSlot),
		ids, from, to, ids, from, to).
		Scan(&closes).Error; err != nil {
		return nil, err
	}
	points := make(map[key]spark, len(closes))
	for _, row := range closes {
		k := key{row.RepoID, row.Bucket}
		old, ok := points[k]
		if !ok || row.Observed > old.Observed || (row.Observed == old.Observed && row.Slot > old.Slot) {
			points[k] = row
		}
	}
	for _, id := range ids {
		v := result[id]
		v.sparkline = make([]gin.H, 0, 24)
		for at := from; at < to; at += metrics.Hour {
			point := gin.H{"at": iso(at), "stars": nil, "observedAt": nil}
			if close, ok := points[key{id, at}]; ok {
				point["stars"], point["observedAt"] = close.Stars, iso(close.Observed)
			}
			v.sparkline = append(v.sparkline, point)
		}
		result[id] = v
	}
	return result, nil
}

func (a *collectionAPI) trend(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	type interval struct {
		days       int
		step       int64
		resolution string
	}
	r, ok := map[string]interval{
		consts.Window24h: {1, 0, consts.ResolutionRaw}, consts.Window7d: {7, 0, consts.ResolutionRaw}, consts.Window30d: {30, metrics.Hour, consts.ResolutionHourly}, consts.Window180d: {180, metrics.Day, consts.ResolutionDaily},
	}[c.DefaultQuery("range", consts.Window7d)]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidRange})
		return
	}
	db := a.db.WithContext(c.Request.Context())
	var repo storage.Repository
	if err := publicRepos(c, db).Take(&repo, storage.RepositoryColumns.RepoID+"=?", id).Error; err != nil {
		a.fail(c, err)
		return
	}
	now := time.Now().UTC()
	to := now.UnixMilli()
	if r.step > 0 {
		to = now.Truncate(time.Duration(r.step) * time.Millisecond).UnixMilli()
	}
	from := to - int64(r.days)*metrics.Day
	updated := repo.DiscoveredAt
	if repo.LastSuccessAt != nil {
		updated = *repo.LastSuccessAt
	}
	points := make([]gin.H, 0)
	if r.resolution == consts.ResolutionRaw {
		var snapshots []storage.RepoSnapshot
		if err := db.Where(storage.RepoSnapshotColumns.RepoID+"=? AND "+storage.RepoSnapshotColumns.ObservedAt+" BETWEEN ? AND ?", id, from, to).
			Order(storage.RepoSnapshotColumns.ObservedAt + "," + storage.RepoSnapshotColumns.SampleSlot).Find(&snapshots).Error; err != nil {
			a.fail(c, err)
			return
		}
		var previous *storage.RepoSnapshot
		for i := range snapshots {
			s := &snapshots[i]
			w := metrics.Window{Quality: metrics.Insufficient}
			// 原始序列的增量覆盖相邻真实观测，时间轴直接保留观测间距。
			if previous != nil && previous.ObservedAt < s.ObservedAt {
				delta := int64(s.Stars) - int64(previous.Stars)
				w = metrics.Window{Delta: &delta, Quality: metrics.Exact, StartAt: &previous.ObservedAt, EndAt: &s.ObservedAt}
			}
			points = append(points, trendPoint(s.ObservedAt, s.Stars, w))
			previous = s
		}
		if previous != nil {
			updated = previous.ObservedAt
		}
	} else {
		type bucket struct {
			storage.MetricAnchors
			Delta   *int64
			Quality string
			At      int64
		}
		var buckets []bucket
		var err error
		if r.resolution == consts.ResolutionHourly {
			err = db.Table(storage.HourlyMetricTable).Select("*,"+storage.HourlyMetricColumns.HourStart+" AS at").
				Where(storage.HourlyMetricColumns.RepoID+"=? AND "+storage.HourlyMetricColumns.HourStart+">=? AND "+storage.HourlyMetricColumns.HourStart+"<?", id, from, to).Scan(&buckets).Error
		} else {
			err = db.Table(storage.DailyMetricTable).Select("*,unixepoch("+storage.DailyMetricColumns.Day+")*1000 AS at").
				Where(storage.DailyMetricColumns.RepoID+"=? AND "+storage.DailyMetricColumns.Day+">=? AND "+storage.DailyMetricColumns.Day+"<?", id, time.UnixMilli(from).UTC().Format(time.DateOnly),
					time.UnixMilli(to).UTC().Format(time.DateOnly)).Scan(&buckets).Error
		}
		if err != nil {
			a.fail(c, err)
			return
		}
		byTime := make(map[int64]bucket, len(buckets))
		var lastObservation int64
		for _, b := range buckets {
			byTime[b.At] = b
			lastObservation = max(lastObservation, b.CloseObservedAt)
		}
		if lastObservation > 0 {
			updated = lastObservation
		}
		for at := from; at < to; at += r.step {
			b, ok := byTime[at]
			if !ok {
				points = append(points, trendPoint(at, nil, metrics.Window{Quality: metrics.Insufficient}))
				continue
			}
			w := metrics.Window{Delta: b.Delta, Quality: b.Quality, EndAt: &b.CloseObservedAt}
			if b.OpenObservedAt < b.CloseObservedAt {
				w.StartAt = &b.OpenObservedAt
			}
			points = append(points, trendPoint(at, b.StarsClose, w))
		}
	}
	c.JSON(http.StatusOK, gin.H{"points": points, "resolution": r.resolution, "updatedAt": iso(updated)})
}

func trendPoint(at int64, stars any, w metrics.Window) gin.H {
	return gin.H{"at": iso(at), "stars": stars, "delta": w.Delta, "quality": w.Quality,
		"startAt": optionalISO(w.StartAt), "endAt": optionalISO(w.EndAt)}
}
