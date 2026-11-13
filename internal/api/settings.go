package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (a *collectionAPI) registerSettings(system *gin.RouterGroup) {
	system.GET("/settings", a.getRuntime)
	system.GET("/observation-replay", a.observationReplay)
	system.PUT("/settings", a.putRuntime)
	system.GET("/categories", a.getCategorySettings)
	system.PUT("/categories", a.putCategorySettings)
	system.GET("/technical-tags", a.getTechnicalTags)
	system.PUT("/technical-tags", a.putTechnicalTags)
	system.GET("/discovery-rules", a.getDiscoveryRules)
	system.PUT("/discovery-rules", a.putDiscoveryRules)
	system.GET("/discovery-runs", a.discoveryRuns)
	system.GET("/discovery-tabs", a.getDiscoveryTabSettings)
	system.PUT("/discovery-tabs", a.putDiscoveryTabSettings)
	system.GET("/project-themes", a.getProjectThemeSettings)
	system.PUT("/project-themes", a.putProjectThemeSettings)
	system.GET("/project-theme-groups", a.getProjectThemeGroups)
	system.PUT("/project-theme-groups", a.putProjectThemeGroups)
}

// getTechnicalTags 只向管理员返回可编辑的技术词表及修订号。
func (a *collectionAPI) getTechnicalTags(c *gin.Context) {
	revision, items, err := (settings.Store{DB: a.db}).TechnicalTags(c.Request.Context())
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	a.technicalTagsResponse(c, revision, items)
}

// putTechnicalTags 校验整表后原子保存，成功即供后续模型调用和展示读取。
func (a *collectionAPI) putTechnicalTags(c *gin.Context) {
	var input struct {
		Revision int64                  `json:"revision"`
		Items    []storage.TechnicalTag `json:"items"`
	}
	// 有界请求同时覆盖 512 个词条和每条允许的别名。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	if !decodeSettings(c, &input) {
		return
	}
	revision, items, err := (settings.Store{DB: a.db}).SaveTechnicalTags(c.Request.Context(), input.Revision, input.Items)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	a.technicalTagsResponse(c, revision, items)
}

// technicalTagsResponse 返回可编辑能力与用于选择的内置领域。
func (a *collectionAPI) technicalTagsResponse(c *gin.Context, revision int64, items []storage.TechnicalTag) {
	version, domains, _, err := storage.TaxonomyMetadata()
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revision": revision, "items": items, "taxonomyVersion": version, "domains": domains})
}

// decodeSettings 避免拼错字段时悄悄丢弃管理员设置。
func decodeSettings(c *gin.Context, dest any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(dest); err != nil || decoder.Decode(&extra) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettings, "message": "配置字段或数据格式无效，请重新加载后保存"})
		return false
	}
	return true
}

func (a *collectionAPI) settingsFailure(c *gin.Context, err error) {
	var invalid *settings.Invalid
	switch {
	case errors.As(err, &invalid):
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettings, "message": invalid.Error()})
	case errors.Is(err, settings.ErrRevision):
		c.JSON(http.StatusConflict, gin.H{"code": consts.CodeSettingsConflict})
	case errors.Is(err, settings.ErrCapacity):
		c.JSON(http.StatusConflict, gin.H{"code": consts.CodeMonitoringCapacityBelowFollowed})
	case storage.IsBusy(err):
		// 让表单保留当前编辑，并区分临时写锁与配置字段无效。
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "settings_database_busy"})
	default:
		a.fail(c, err)
	}
}

func (a *collectionAPI) runtimeResponse(c *gin.Context, snapshot settings.Snapshot) {
	cfg := runtimeFor(c).Config
	restartRequired := cfg.NeedsRestart(snapshot.Settings)
	if a.live == nil {
		restartRequired = !reflect.DeepEqual(snapshot.Settings, cfg.RuntimeSettings())
	}
	c.JSON(http.StatusOK, gin.H{
		"revision": snapshot.Revision, "settings": snapshot.Settings,
		"restartRequired": restartRequired,
	})
}

func (a *collectionAPI) getRuntime(c *gin.Context) {
	snapshot, err := (settings.Store{DB: a.db}).Get(c.Request.Context())
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	a.runtimeResponse(c, snapshot)
}

func (a *collectionAPI) putRuntime(c *gin.Context) {
	var input struct {
		Revision int64          `json:"revision"`
		Settings config.Runtime `json:"settings"`
	}
	if !decodeSettings(c, &input) {
		return
	}
	if input.Revision < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettingsRevision})
		return
	}
	save := (settings.Store{DB: a.db}).SaveRuntime
	if a.live != nil {
		save = a.live.SaveRuntime
	}
	snapshot, err := save(c.Request.Context(), input.Revision, input.Settings)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	a.runtimeResponse(c, snapshot)
}

func (a *collectionAPI) getCategorySettings(c *gin.Context) {
	revision, items, err := (settings.Store{DB: a.db}).CategorySettings(c.Request.Context())
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revision": revision, "items": items})
}

func (a *collectionAPI) putCategorySettings(c *gin.Context) {
	var input struct {
		Revision int64             `json:"revision"`
		Items    []config.Category `json:"items"`
	}
	if !decodeSettings(c, &input) {
		return
	}
	if input.Revision < 1 || input.Items == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettings})
		return
	}
	revision, items, err := (settings.Store{DB: a.db}).SaveCategories(c.Request.Context(), input.Revision, input.Items)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revision": revision, "items": items})
}

func (a *collectionAPI) getDiscoveryRules(c *gin.Context) {
	revision, items, err := (settings.Store{DB: a.db}).DiscoveryRules(c.Request.Context())
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revision": revision, "items": items})
}

func (a *collectionAPI) putDiscoveryRules(c *gin.Context) {
	var input struct {
		Revision int64           `json:"revision"`
		Items    []settings.Rule `json:"items"`
	}
	if !decodeSettings(c, &input) {
		return
	}
	if input.Revision < 1 || input.Items == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettings})
		return
	}
	revision, items, err := (settings.Store{DB: a.db}).SaveDiscoveryRules(c.Request.Context(), input.Revision, input.Items)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"revision": revision, "items": items})
}

func (a *collectionAPI) getDiscoveryTabSettings(c *gin.Context) {
	result, err := (settings.Store{DB: a.db}).DiscoveryTabSettings(c.Request.Context())
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (a *collectionAPI) putDiscoveryTabSettings(c *gin.Context) {
	var input struct {
		Revision      int64                        `json:"revision"`
		Items         []settings.DiscoveryTab      `json:"items"`
		NewCategories []config.Category            `json:"newCategories"`
		Selection     *settings.DiscoverySelection `json:"selection"`
		Ranking       *settings.DiscoveryRanking   `json:"ranking"`
	}
	if !decodeSettings(c, &input) {
		return
	}
	if input.Revision < 1 || input.Items == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidSettings})
		return
	}
	result, err := (settings.Store{DB: a.db}).SaveDiscoveryTabs(c.Request.Context(), input.Revision, input.Items, input.NewCategories, input.Selection, input.Ranking)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
