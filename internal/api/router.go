package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

// NewRouter 注册公开健康检查与需要管理员令牌的安装状态接口。
func NewRouter(db *gorm.DB, pool *sql.DB, adminToken string, logger *slog.Logger, cfg config.Config) *gin.Engine {
	router := gin.New()
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		logger.Error("HTTP 请求处理异常", "method", c.Request.Method, "route", c.FullPath())
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": "internal_error"})
	}))
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := pool.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	v1 := router.Group("/api/v1", requireAdmin(adminToken))
	v1.GET("/setup/status", func(c *gin.Context) {
		initialized, err := storage.Initialized(c.Request.Context(), db)
		if err != nil {
			logger.Error("查询安装状态失败", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": "internal_error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"initialized": initialized})
	})
	v1.POST("/setup/initialize", func(c *gin.Context) {
		err := storage.InitializeEmpty(c.Request.Context(), db)
		if errors.Is(err, storage.ErrAlreadyInitialized) {
			c.JSON(http.StatusConflict, gin.H{"code": "already_initialized"})
			return
		}
		if err != nil {
			logger.Error("初始化空库失败", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"code": "internal_error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"initialized": true})
	})
	collection := &collectionAPI{db: db, cfg: cfg, logger: logger}
	business := v1.Group("", func(c *gin.Context) {
		initialized, err := storage.Initialized(c.Request.Context(), db)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"code": "internal_error"})
			return
		}
		if !initialized {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"code": "setup_required"})
			return
		}
		c.Next()
	})
	collection.register(business)
	collection.registerSettings(business.Group("/system"))
	return router
}

// requireAdmin 在业务处理前统一校验 Bearer 令牌。
func requireAdmin(token string) gin.HandlerFunc {
	expected := []byte(token)
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") ||
			subtle.ConstantTimeCompare([]byte(parts[1]), expected) != 1 {
			c.Header("WWW-Authenticate", "Bearer")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "unauthorized"})
			return
		}
		c.Next()
	}
}
