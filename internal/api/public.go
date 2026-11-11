package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

const publicReadKey = "publicRead"

var publicRepoCondition = storage.PublicRepositoryCondition

func markPublicRead(c *gin.Context) {
	c.Set(publicReadKey, true)
	c.Header("Cache-Control", "no-store")
	c.Next()
}

func publicRepos(c *gin.Context, db *gorm.DB) *gorm.DB {
	if c.GetBool(publicReadKey) {
		return db.Where(publicRepoCondition)
	}
	return db
}

func (a *collectionAPI) registerPublic(public *gin.RouterGroup) {
	public.GET("/repos", a.loadCategories, a.repos)
	public.GET("/repos/by-name/:owner/:name", a.loadCategories, a.repo)
	public.GET("/repos/:id/trend", a.trend)
	public.GET("/repos/:id/readme", a.readme)
	public.GET("/repos/:id/similar", a.similar)
	public.GET("/categories", a.loadCategories, a.categoryList)
	public.GET("/discovery-tabs", a.publicDiscoveryTabs)
	public.GET("/discovery", a.loadCategories, a.discovery)
	public.GET("/project-themes", a.projectThemes)
	public.GET("/project-themes/:slug", a.projectThemes)
	public.GET("/trending", a.loadCategories, a.trending)
	public.GET("/trending/sources", a.trendingSources)
}

// publicDiscoveryTabs 只返回前台需要的分类标识和显示名称。
func (a *collectionAPI) publicDiscoveryTabs(c *gin.Context) {
	result, err := (settings.Store{DB: a.db}).DiscoveryTabSettings(c.Request.Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	names := make(map[string]string, len(result.Options))
	for _, option := range result.Options {
		names[option.Slug] = option.Name
	}
	items := make([]settings.DiscoveryTabOption, 0, len(result.Items))
	for _, tab := range result.Items {
		name := names[tab.Slug]
		if tab.Label != "" {
			name = tab.Label
		}
		items = append(items, settings.DiscoveryTabOption{Slug: tab.Slug, Name: name})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// trendingSources 只提供榜单选择器所需的标识。
func (a *collectionAPI) trendingSources(c *gin.Context) {
	items := make([]struct {
		SourceKey string `json:"sourceKey"`
	}, 0)
	err := a.db.WithContext(c.Request.Context()).Model(&storage.DiscoveryRule{}).
		Select(storage.DiscoveryRuleColumns.SourceKey).Where(storage.DiscoveryRuleColumns.SourceType+" = ?", consts.SourceTrending).Order(storage.DiscoveryRuleColumns.ID).Find(&items).Error
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}
