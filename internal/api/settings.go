package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
)

func (a *collectionAPI) registerSettings(system *gin.RouterGroup) {
	system.GET("/settings", a.getRuntime)
	system.PUT("/settings", a.putRuntime)
	system.GET("/categories", a.getCategorySettings)
	system.PUT("/categories", a.putCategorySettings)
	system.GET("/discovery-rules", a.getDiscoveryRules)
	system.PUT("/discovery-rules", a.putDiscoveryRules)
	system.GET("/discovery-tabs", a.getDiscoveryTabSettings)
	system.PUT("/discovery-tabs", a.putDiscoveryTabSettings)
}

// decodeSettings 避免拼错字段时悄悄丢弃管理员设置。
func decodeSettings(c *gin.Context, dest any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(dest); err != nil || decoder.Decode(&extra) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_settings"})
		return false
	}
	return true
}

func (a *collectionAPI) settingsFailure(c *gin.Context, err error) {
	var invalid *settings.Invalid
	switch {
	case errors.As(err, &invalid):
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_settings", "message": invalid.Error()})
	case errors.Is(err, settings.ErrRevision):
		c.JSON(http.StatusConflict, gin.H{"code": "settings_conflict"})
	case errors.Is(err, settings.ErrCapacity):
		c.JSON(http.StatusConflict, gin.H{"code": "monitoring_capacity_below_followed"})
	default:
		a.fail(c, err)
	}
}

func (a *collectionAPI) runtimeResponse(c *gin.Context, snapshot settings.Snapshot) {
	c.JSON(http.StatusOK, gin.H{
		"revision": snapshot.Revision, "settings": snapshot.Settings,
		"restartRequired": !reflect.DeepEqual(snapshot.Settings, a.cfg.RuntimeSettings()),
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
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_settings_revision"})
		return
	}
	snapshot, err := (settings.Store{DB: a.db}).SaveRuntime(c.Request.Context(), input.Revision, input.Settings)
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
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_settings"})
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
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_settings"})
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
		Revision int64                   `json:"revision"`
		Items    []settings.DiscoveryTab `json:"items"`
	}
	if !decodeSettings(c, &input) {
		return
	}
	if input.Revision < 1 || input.Items == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_settings"})
		return
	}
	result, err := (settings.Store{DB: a.db}).SaveDiscoveryTabs(c.Request.Context(), input.Revision, input.Items)
	if err != nil {
		a.settingsFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
