package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/backup"
)

const backupPath = "/system/backup"

func backupFailure(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch err {
	case backup.ErrAlreadyInitialized, backup.ErrBusy, backup.ErrRestoring, backup.ErrReload, backup.ErrSession, backup.ErrNotConfigured:
		status = http.StatusConflict
	case backup.ErrMaintenance, backup.ErrSetupRequired:
		status = http.StatusServiceUnavailable
	case backup.ErrInvalidSettings, backup.ErrInvalidObject:
		status = http.StatusBadRequest
	case backup.ErrOperation:
		status = http.StatusNotFound
	case backup.ErrR2, backup.ErrUploadUnknown:
		status = http.StatusBadGateway
	}
	c.AbortWithStatusJSON(status, gin.H{"code": backup.ErrorCode(err)})
}

func accepted(c *gin.Context, path string, op backup.Operation, err error) {
	if err != nil {
		backupFailure(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"operationId": op.ID, "statusUrl": "/api/v1" + path + "/operations/" + op.ID})
}

// registerBackup 将无 SQL 的状态路由留在维护门禁外，所有路由均沿用管理员鉴权。
func registerBackup(v1, business *gin.RouterGroup, m *backup.Manager) {
	system := business.Group(backupPath)
	system.GET("/settings", func(c *gin.Context) { c.JSON(http.StatusOK, m.Settings()) })
	system.PUT("/settings", func(c *gin.Context) {
		var input backup.SettingsUpdate
		if !decodeSettings(c, &input) {
			return
		}
		value, err := m.Save(c.Request.Context(), input)
		if err != nil {
			backupFailure(c, err)
			return
		}
		c.JSON(http.StatusOK, value)
	})
	test := func(setup bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			var input backup.Connection
			if !decodeSettings(c, &input) {
				return
			}
			if err := m.Test(c.Request.Context(), input, setup); err != nil {
				backupFailure(c, err)
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true})
		}
	}
	system.POST("/test", test(false))
	list := func(setup bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			limit := 20
			if raw := c.Query("limit"); raw != "" {
				var err error
				limit, err = strconv.Atoi(raw)
				if err != nil || limit < 1 || limit > 30 {
					backupFailure(c, backup.ErrInvalidObject)
					return
				}
			}
			page, err := m.List(c.Request.Context(), c.GetHeader("X-Setup-Session"), setup, limit, c.Query("cursor"))
			if err != nil {
				backupFailure(c, err)
				return
			}
			c.JSON(http.StatusOK, page)
		}
	}
	system.GET("/list", list(false))
	system.POST("/run", func(c *gin.Context) {
		op, err := m.StartBackup()
		accepted(c, backupPath, op, err)
	})
	system.POST("/restore", func(c *gin.Context) {
		var input struct {
			ObjectKey string             `json:"objectKey"`
			R2        *backup.Connection `json:"r2"`
		}
		if !decodeSettings(c, &input) {
			return
		}
		op, err := m.StartRestore(input.ObjectKey, input.R2, "", false)
		accepted(c, backupPath, op, err)
	})
	business.POST("/system/ai/resume", func(c *gin.Context) {
		var input struct {
			AcceptIncompleteUsage bool `json:"acceptIncompleteUsage"`
		}
		if !decodeSettings(c, &input) {
			return
		}
		if !input.AcceptIncompleteUsage {
			c.JSON(http.StatusBadRequest, gin.H{"code": "incomplete_usage_not_accepted"})
			return
		}
		if err := m.ResumeAI(c.Request.Context()); err != nil {
			backupFailure(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"autoAIResumeRequired": false})
	})

	operation := func(c *gin.Context) {
		value, err := m.Operation(c.Param("id"))
		if err != nil {
			backupFailure(c, err)
			return
		}
		c.JSON(http.StatusOK, value)
	}
	v1.GET(backupPath+"/status", func(c *gin.Context) { c.JSON(http.StatusOK, m.Status()) })
	v1.GET(backupPath+"/operations/:id", operation)
	v1.POST(backupPath+"/reload", func(c *gin.Context) {
		op, err := m.Reload()
		accepted(c, backupPath, op, err)
	})

	v1.GET("/setup/operations/:id", operation)
	setup := v1.Group("/setup", func(c *gin.Context) {
		if m.Initialized() {
			backupFailure(c, backup.ErrAlreadyInitialized)
			return
		}
		release, err := m.Enter(c.Request.Method == http.MethodPost)
		if err != nil {
			backupFailure(c, err)
			return
		}
		defer release()
		c.Next()
	})
	setup.POST("/r2/test", test(true))
	setup.POST("/r2/session", func(c *gin.Context) {
		var input backup.Connection
		if !decodeSettings(c, &input) {
			return
		}
		id, expires, err := m.CreateSession(c.Request.Context(), input)
		if err != nil {
			backupFailure(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"sessionId": id, "expiresAt": expires})
	})
	setup.GET("/backups", list(true))
	setup.POST("/restore", func(c *gin.Context) {
		var input struct {
			SessionID string `json:"sessionId"`
			ObjectKey string `json:"objectKey"`
		}
		if !decodeSettings(c, &input) {
			return
		}
		op, err := m.StartRestore(input.ObjectKey, nil, input.SessionID, true)
		accepted(c, "/setup", op, err)
	})
}

func settingsWrite(c *gin.Context) bool {
	path := c.FullPath()
	return c.Request.Method == http.MethodPut || c.Request.Method == http.MethodPost &&
		(path == "/api/v1"+backupPath+"/run" || path == "/api/v1"+backupPath+"/restore" || path == "/api/v1/system/ai/resume")
}
