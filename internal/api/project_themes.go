package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

// 专题目录、精选和全部项目共用匹配条件，GitHub Topics 非空时只匹配原生主题。
var projectThemeMatch = `EXISTS (SELECT 1 FROM json_each(` + effectiveTopicsSQL + `) topic
WHERE lower(topic.value) IN (SELECT value FROM json_each(` + storage.ProjectThemeTable + `.` + storage.ProjectThemeColumns.TopicsJSON + `)))`

func filterProjectTheme(tx, query *gorm.DB, slug string) (*gorm.DB, error) {
	if slug == "" {
		return query, nil
	}
	var theme storage.ProjectTheme
	if err := tx.Select(storage.ProjectThemeColumns.Slug).Take(&theme, storage.ProjectThemeColumns.Slug+"=?", slug).Error; err != nil {
		return nil, err
	}
	return query.Where("EXISTS (SELECT 1 FROM "+storage.ProjectThemeTable+" WHERE "+storage.ProjectThemeTable+"."+storage.ProjectThemeColumns.Slug+"=? AND "+projectThemeMatch+")", theme.Slug), nil
}

func (a *collectionAPI) projectThemes(c *gin.Context) {
	var result gin.H
	err := a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = readProjectThemes(c, tx, c.Param("slug"))
		return err
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// readProjectThemes 从同一读快照加载专题及方向，避免并发编辑导致归属错位。
func readProjectThemes(c *gin.Context, db *gorm.DB, slug string) (gin.H, error) {
	now := time.Now()
	// 目录按专题聚合公开仓库，最近一次发现时间用于预览卡片。
	members := publicRepos(c, db.Model(&storage.Repository{})).
		Select(fmt.Sprintf(`%[1]s.%[2]s AS theme_slug, %[3]s.%[4]s, %[3]s.%[5]s, %[3]s.%[6]s,
COUNT(*) OVER (PARTITION BY %[1]s.%[2]s) AS total,
SUM(CASE WHEN %[3]s.%[6]s BETWEEN ? AND ? THEN 1 ELSE 0 END) OVER (PARTITION BY %[1]s.%[2]s) AS recent,
ROW_NUMBER() OVER (PARTITION BY %[1]s.%[2]s ORDER BY %[3]s.%[6]s DESC, %[3]s.%[4]s) AS position`,
			storage.ProjectThemeTable, storage.ProjectThemeColumns.Slug, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.RepositoryColumns.FullName, storage.RepositoryColumns.DiscoveredAt),
			now.Add(-30*24*time.Hour).UnixMilli(), now.UnixMilli()).
		Joins("JOIN " + storage.ProjectThemeTable + " ON " + projectThemeMatch).
		Where(storage.RepositoryTable + "." + storage.RepositoryColumns.State + "='" + consts.StatusActive + "'")
	if slug != "" {
		members = members.Where(storage.ProjectThemeTable+"."+storage.ProjectThemeColumns.Slug+"=?", slug)
	}
	query := db.Model(&storage.ProjectTheme{}).
		Select(storage.ProjectThemeTable+`.`+storage.ProjectThemeColumns.Slug+`, `+storage.ProjectThemeTable+`.`+storage.ProjectThemeColumns.Name+`, `+storage.ProjectThemeTable+`.`+storage.ProjectThemeColumns.Description+`, `+
			storage.ProjectThemeTable+"."+storage.ProjectThemeColumns.Icon+", "+storage.ProjectThemeTable+"."+storage.ProjectThemeColumns.IconColor+", "+
			storage.ProjectThemeTable+"."+storage.ProjectThemeColumns.GroupSlug+`,
COALESCE(m.total,0) AS total, COALESCE(m.recent,0) AS recent, m.`+storage.RepositoryColumns.RepoID+`, m.`+storage.RepositoryColumns.FullName+`, m.`+storage.RepositoryColumns.DiscoveredAt).
		Joins("LEFT JOIN (?) m ON m.theme_slug="+storage.ProjectThemeTable+"."+storage.ProjectThemeColumns.Slug+" AND m.position=1", members).
		Order(storage.ProjectThemeTable + "." + storage.ProjectThemeColumns.Position + ", " + storage.ProjectThemeTable + "." + storage.ProjectThemeColumns.Slug)
	if slug != "" {
		query = query.Where(storage.ProjectThemeTable+"."+storage.ProjectThemeColumns.Slug+"=?", slug)
	}
	var rows []struct {
		Slug         string `gorm:"column:slug"`
		Name         string `gorm:"column:name"`
		Description  string `gorm:"column:description"`
		Icon         string `gorm:"column:icon"`
		IconColor    string `gorm:"column:icon_color"`
		GroupSlug    string `gorm:"column:group_slug"`
		Total        int    `gorm:"column:total"`
		Recent       int    `gorm:"column:recent"`
		RepoID       *int64 `gorm:"column:repo_id"`
		FullName     string `gorm:"column:full_name"`
		DiscoveredAt int64  `gorm:"column:discovered_at"`
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		var latest gin.H
		if row.RepoID != nil {
			latest = gin.H{"id": *row.RepoID, "fullName": row.FullName, "discoveredAt": iso(row.DiscoveredAt)}
		}
		items = append(items, gin.H{"slug": row.Slug, "name": row.Name, "description": row.Description,
			"icon": row.Icon, "iconColor": row.IconColor, "groupSlug": row.GroupSlug, "count": row.Total, "recentCount": row.Recent, "latestRepo": latest})
	}
	if slug != "" {
		if len(items) == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		milestones, err := themeMilestones(c, db, slug, now)
		if err != nil {
			return nil, err
		}
		items[0]["milestones"] = milestones
		return items[0], nil
	}
	groups, err := settings.ReadProjectThemeGroups(db)
	return gin.H{"items": items, "groups": groups}, err
}

func (a *collectionAPI) getProjectThemeGroups(c *gin.Context) {
	result, err := (settings.Store{DB: a.db}).ProjectThemeGroups(c.Request.Context())
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (a *collectionAPI) putProjectThemeGroups(c *gin.Context) {
	var input settings.ProjectThemeGroupSettings
	if !decodeSettings(c, &input) {
		return
	}
	if input.Revision < 1 || input.Items == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettings})
		return
	}
	result, err := (settings.Store{DB: a.db}).SaveProjectThemeGroups(c.Request.Context(), input.Revision, input.Items)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (a *collectionAPI) getProjectThemeSettings(c *gin.Context) {
	result, err := (settings.Store{DB: a.db}).ProjectThemeSettings(c.Request.Context())
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (a *collectionAPI) putProjectThemeSettings(c *gin.Context) {
	var input settings.ProjectThemeSettings
	if !decodeSettings(c, &input) {
		return
	}
	if input.Revision < 1 || input.Items == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettings})
		return
	}
	result, err := (settings.Store{DB: a.db}).SaveProjectThemes(c.Request.Context(), input.Revision, input.Items)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
