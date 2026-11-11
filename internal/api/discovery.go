package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/collector"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

type discoveryCursor struct {
	Categories []string `json:"categories"`
	Theme      string   `json:"theme,omitempty"`
	At         int64    `json:"at"`
	Score      float64  `json:"score"`
	RepoID     int64    `json:"repoId"`
}

func (a *collectionAPI) discovery(c *gin.Context) {
	limit, ok := pageLimit(c)
	if !ok {
		return
	}
	categories, err := categoryFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidFilter})
		return
	}
	var cursor discoveryCursor
	theme := c.Query("theme")
	hot := c.Query("sort") == consts.SortHotness
	if raw, exists := c.GetQuery("cursor"); exists {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if hot || err != nil || json.Unmarshal(data, &cursor) != nil || cursor.RepoID <= 0 || cursor.At <= 0 ||
			cursor.Theme != theme || !slices.Equal(cursor.Categories, categories) {
			c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidCursor})
			return
		}
	}

	var page gin.H
	err = a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		page, err = a.discoveryPage(c, tx, categories, theme, cursor, limit, hot, time.Now())
		return err
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (a *collectionAPI) discoveryPage(c *gin.Context, tx *gorm.DB, categories []string, theme string, cursor discoveryCursor, limit int, hot bool, now time.Time) (gin.H, error) {
	snapshot, err := (settings.Store{DB: tx}).Get(c.Request.Context())
	if err != nil {
		return nil, err
	}
	rules := snapshot.DiscoverySelection
	asOf := now.UnixMilli()
	since := func(hours int) int64 { return asOf - (time.Duration(hours) * time.Hour).Milliseconds() }
	// 使用指标对应的 24h 窗口终点，避免一次新采样让旧增长重新入选。
	growthAt := metricWindowSQL(consts.Window24h, "endAt")
	// 最新成功批次即使是空榜也会覆盖旧榜，已移除的榜单来源不参与入选。
	batch := tx.Model(&storage.MaintenanceState{}).
		Select("json_extract("+storage.MaintenanceStateColumns.Value+",'$.observedSlot') AS observed_slot, json_extract("+storage.MaintenanceStateColumns.Value+",'$.observedAt') AS observed_at").
		Where(storage.MaintenanceStateColumns.Key+"=? AND EXISTS (SELECT 1 FROM "+storage.DiscoveryRuleTable+" WHERE "+storage.DiscoveryRuleColumns.SourceType+"='"+consts.SourceTrending+"' AND "+storage.DiscoveryRuleColumns.SourceKey+"='"+consts.TrendingAllDaily+"')", collector.BatchKey(consts.TrendingAll, consts.PeriodDaily))
	// 三个信号取最大时间。批次观测以仓库和来源为键，每个仓库只生成一行。
	matches := publicRepos(c, tx.Model(&storage.Repository{})).
		Select(fmt.Sprintf(`%[1]s.*, coalesce(m.%[2]s,-1) AS discovery_score,
MAX(
  CASE WHEN %[1]s.%[3]s IN ('%[4]s','%[5]s') AND %[1]s.%[6]s BETWEEN ? AND ?
    AND %[1]s.%[7]s BETWEEN ? AND ? THEN %[1]s.%[6]s ELSE 0 END,
  CASE WHEN %[8]s='%[9]s' AND m.%[10]s>=?
    AND `,
			storage.RepositoryTable, storage.LatestMetricColumns.HotnessScore, storage.RepositoryColumns.Grade, consts.GradeS, consts.GradeA, storage.RepositoryColumns.DiscoveredAt, storage.RepositoryColumns.LastSuccessAt, metricWindowSQL(consts.Window24h, "quality"), consts.QualityExact, storage.LatestMetricColumns.Delta24h)+growthAt+` BETWEEN ? AND ? THEN `+growthAt+` ELSE 0 END,
  CASE WHEN t.`+storage.TrendingObservationColumns.RepoID+` IS NOT NULL THEN batch.observed_at ELSE 0 END
) AS discovery_at`, since(rules.RecentDays*24), asOf, since(rules.RecentSampleHours), asOf,
			a.cfg.Metrics.HotGrowth, since(rules.GrowthSampleHours), asOf).
		Joins("LEFT JOIN "+storage.LatestMetricTable+" m ON m."+storage.LatestMetricColumns.RepoID+"="+storage.RepositoryTable+"."+storage.RepositoryColumns.RepoID).
		Joins("LEFT JOIN (?) batch ON batch.observed_at BETWEEN ? AND ?", batch, since(rules.TrendingHours), asOf).
		Joins(fmt.Sprintf("LEFT JOIN %[1]s t ON t.%[2]s=%[3]s.%[4]s AND t.%[5]s='%[6]s' AND t.%[7]s='%[8]s' AND t.%[9]s=batch.observed_slot AND t.%[10]s BETWEEN 1 AND ?",
			storage.TrendingObservationTable, storage.TrendingObservationColumns.RepoID, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.TrendingObservationColumns.SourceKey, consts.TrendingAll, storage.TrendingObservationColumns.Window, consts.PeriodDaily, storage.TrendingObservationColumns.ObservedSlot, storage.TrendingObservationColumns.Rank), rules.TrendingTop).
		Where(storage.RepositoryTable + "." + storage.RepositoryColumns.State + "='" + consts.StatusActive + "'")
	matches = a.filterCategories(c, matches, categories)
	matches, err = filterProjectTheme(tx, matches, theme)
	if err != nil {
		return nil, err
	}

	// 日期按北京时间分组，缺少热度分的仓库排在同一天有分的仓库之后。
	const day = "((discovery_at + 28800000) / 86400000)"
	page := tx.Table("(?) AS selected", matches).Where("discovery_at > 0")
	if hot {
		// 当前热点与时间线共用入选范围，只取其中热度最高的五项。
		page = page.Where("discovery_score >= 0").Order("discovery_score DESC, " + storage.RepositoryColumns.RepoID + " ASC").Limit(min(limit, 5))
	} else {
		if cursor.RepoID != 0 {
			cursorDay := (cursor.At + (8 * time.Hour).Milliseconds()) / (24 * time.Hour).Milliseconds()
			page = page.Where("("+day+", discovery_score, discovery_at, -"+storage.RepositoryColumns.RepoID+") < (?, ?, ?, ?)",
				cursorDay, cursor.Score, cursor.At, -cursor.RepoID)
		}
		page = page.Order(day + " DESC, discovery_score DESC, discovery_at DESC, " + storage.RepositoryColumns.RepoID + " ASC").Limit(limit + 1)
	}
	var rows []struct {
		storage.Repository `gorm:"embedded"`
		At                 int64   `gorm:"column:discovery_at"`
		Score              float64 `gorm:"column:discovery_score"`
	}
	if err := page.Scan(&rows).Error; err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		data, err := json.Marshal(discoveryCursor{Categories: categories, Theme: theme, At: last.At, Score: last.Score, RepoID: last.RepoID})
		if err != nil {
			return nil, err
		}
		value := base64.RawURLEncoding.EncodeToString(data)
		next = &value
	}
	repos := make([]storage.Repository, 0, len(rows))
	for _, row := range rows {
		repos = append(repos, row.Repository)
	}
	items, err := a.repoCards(c, tx, repos)
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		items[i]["discoveryAt"] = iso(row.At)
	}
	updatedAt := optionalISO(&asOf)
	if hot {
		var marker storage.MaintenanceState
		if err := tx.Find(&marker, storage.MaintenanceStateColumns.Key+"=?", metrics.ComputeKey).Error; err != nil {
			return nil, err
		}
		updatedAt = optionalISO(&marker.UpdatedAt)
		if marker.Key == "" {
			updatedAt = nil
		}
	}
	return gin.H{"items": items, "nextCursor": next, "updatedAt": updatedAt}, nil
}
