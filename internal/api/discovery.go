package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/discoverystore"
)

type discoveryCursor struct {
	Version    int      `json:"v"`
	Revision   int64    `json:"revision"`
	Day        int64    `json:"day"`
	Categories []string `json:"categories"`
	Theme      string   `json:"theme,omitempty"`
	At         int64    `json:"at"`
	Score      float64  `json:"score"`
	RepoID     int64    `json:"repoId"`
}

const discoveryCursorVersion = 3

func (a *collectionAPI) discovery(c *gin.Context) {
	now := time.Now()
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
		if hot || err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != discoveryCursorVersion ||
			cursor.Day != discoverystore.BeijingDay(now.UnixMilli()) || cursor.RepoID <= 0 || cursor.At <= 0 ||
			cursor.Theme != theme || !slices.Equal(cursor.Categories, categories) {
			c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidCursor})
			return
		}
	}

	var page gin.H
	err = a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		page, err = a.discoveryPage(c, tx, categories, theme, cursor, limit, hot, now)
		return err
	}, &sql.TxOptions{ReadOnly: true})
	if errors.Is(err, errInvalidCursor) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidCursor})
		return
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (a *collectionAPI) discoveryPage(c *gin.Context, tx *gorm.DB, categories []string, theme string, cursor discoveryCursor, limit int, hot bool, now time.Time) (gin.H, error) {
	matches, revision, err := discoverystore.Matches(tx, now)
	if err != nil {
		return nil, err
	}
	if cursor.RepoID != 0 && cursor.Revision != revision {
		return nil, errInvalidCursor
	}
	matches = a.filterCategories(c, matches, categories)
	matches, err = filterProjectTheme(tx, matches, theme)
	if err != nil {
		return nil, err
	}

	// 时间线按首次发现的北京时间日期分组，同一天按展示分排序。
	firstDiscovered := storage.RepositoryColumns.DiscoveredAt
	day := discoverystore.BeijingDaySQL(firstDiscovered)
	withTime := tx.Table("(?) AS matches", matches).Select("matches.*, " + discoverystore.TimeSQL + " AS discovery_at")
	page := tx.Table("(?) AS selected", withTime).Where("discovery_at > 0")
	if hot {
		// 当前热点与时间线共用入选范围，只取其中热度最高的五项。
		page = page.Where("raw_hotness >= 0").Order("raw_hotness DESC, " + storage.RepositoryColumns.RepoID + " ASC").Limit(min(limit, 5))
	} else {
		if cursor.RepoID != 0 {
			cursorDay := discoverystore.BeijingDay(cursor.At)
			page = page.Where("("+day+", discovery_score, "+firstDiscovered+", -"+storage.RepositoryColumns.RepoID+") < (?, ?, ?, ?)",
				cursorDay, cursor.Score, cursor.At, -cursor.RepoID)
		}
		page = page.Order(day + " DESC, discovery_score DESC, " + firstDiscovered + " DESC, " + storage.RepositoryColumns.RepoID + " ASC").Limit(limit + 1)
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
		data, err := json.Marshal(discoveryCursor{
			Version: discoveryCursorVersion, Revision: revision, Day: discoverystore.BeijingDay(now.UnixMilli()), Categories: categories,
			Theme: theme, At: last.DiscoveredAt, Score: last.Score, RepoID: last.RepoID,
		})
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
		items[i]["selected"] = true
	}
	asOf := now.UnixMilli()
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
