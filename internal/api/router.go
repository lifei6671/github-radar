package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/backup"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/summary"
)

// NewRouter 分别注册公开只读接口、登录接口和需要管理员会话的业务接口。
func NewRouter(db *gorm.DB, pool *sql.DB, logger *slog.Logger, cfg config.Config, readmes *summary.Engine, backups ...*backup.Manager) *gin.Engine {
	var manager *backup.Manager
	if len(backups) > 0 {
		manager = backups[0]
	}
	router := gin.New()
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		logger.Error("HTTP 请求处理异常", "method", c.Request.Method, "route", c.FullPath())
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": consts.CodeInternalError})
	}))
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		if manager != nil {
			release, err := manager.Enter(false)
			if err != nil {
				backupFailure(c, err)
				return
			}
			defer release()
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := pool.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": consts.StatusUnavailable})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": consts.StatusReady})
	})

	auth := newAdminAuth(cfg)
	admin := router.Group(consts.APIPath, adminRequest(cfg.Server.SiteURL))
	auth.register(admin.Group(consts.AuthGroupPath))
	v1 := admin.Group("", auth.require)
	v1.GET("/setup/status", func(c *gin.Context) {
		if manager != nil {
			c.JSON(http.StatusOK, gin.H{"initialized": manager.Initialized()})
			return
		}
		initialized, err := storage.Initialized(c.Request.Context(), db)
		if err != nil {
			logger.Error("查询安装状态失败", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": consts.CodeInternalError})
			return
		}
		c.JSON(http.StatusOK, gin.H{"initialized": initialized})
	})
	v1.POST("/setup/initialize", func(c *gin.Context) {
		if manager != nil {
			if err := manager.Initialize(c.Request.Context()); err != nil {
				backupFailure(c, err)
				return
			}
			c.JSON(http.StatusOK, gin.H{"initialized": true})
			return
		}
		err := storage.InitializeEmpty(c.Request.Context(), db)
		if errors.Is(err, storage.ErrAlreadyInitialized) {
			c.JSON(http.StatusConflict, gin.H{"code": consts.CodeAlreadyInitialized})
			return
		}
		if err != nil {
			logger.Error("初始化空库失败", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": consts.CodeInternalError})
			return
		}
		c.JSON(http.StatusOK, gin.H{"initialized": true})
	})
	collection := &collectionAPI{db: db, cfg: cfg, logger: logger, readmes: readmes}
	requireBusiness := func(c *gin.Context) {
		if manager != nil {
			release, err := manager.Enter(settingsWrite(c))
			if err != nil {
				backupFailure(c, err)
				return
			}
			defer release()
			if !manager.Initialized() {
				backupFailure(c, backup.ErrSetupRequired)
				return
			}
			c.Next()
			return
		}
		initialized, err := storage.Initialized(c.Request.Context(), db)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": consts.CodeInternalError})
			return
		}
		if !initialized {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": consts.CodeSetupRequired})
			return
		}
		c.Next()
	}
	business := v1.Group("", requireBusiness)
	// 两套读取接口共用安装与恢复门禁，公开接口只登记允许发布的读取操作。
	collection.registerPublic(router.Group("/api/v1/public", markPublicRead, requireBusiness))
	collection.register(business)
	collection.registerSettings(business.Group("/system"))
	if manager != nil {
		registerBackup(v1, business, manager)
	}
	return router
}
