package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Register 将浏览器路由交给 Vite 入口，API 与缺失静态文件保留各自的 404。
func Register(router *gin.Engine, dist fs.FS) {
	router.NoRoute(func(c *gin.Context) {
		request := c.Request
		name := strings.TrimPrefix(request.URL.Path, "/")
		if name == "api" || strings.HasPrefix(name, "api/") {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusNotFound, gin.H{"code": "not_found"})
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			c.Status(http.StatusNotFound)
			return
		}

		cache := "no-cache"
		switch {
		case strings.HasPrefix(name, "assets/") && fs.ValidPath(name):
			// Vite 生成的资源文件名包含内容哈希。
			cache = "public, max-age=31536000, immutable"
		case name == "radar.svg", name == "third-party-licenses.txt":
		case pageRoute(strings.TrimSuffix(name, "/")):
			name = "index.html"
		default:
			c.Status(http.StatusNotFound)
			return
		}

		data, err := fs.ReadFile(dist, name)
		if err != nil {
			if name == "index.html" {
				c.Header("Cache-Control", "no-store")
				c.String(http.StatusServiceUnavailable, "前端构建产物未就绪，请先运行 npm --prefix web run build。")
				return
			}
			c.Status(http.StatusNotFound)
			return
		}
		c.Header("Cache-Control", cache)
		// NoRoute 进入时 Gin 已暂存 404，需要在交给 ServeContent 前恢复正常状态。
		c.Status(http.StatusOK)
		http.ServeContent(c.Writer, request, path.Base(name), time.Time{}, bytes.NewReader(data))
	})
}

func pageRoute(name string) bool {
	switch name {
	case "", "trending", "repos", "categories", "topics", "watchlist", "system", "setup":
		return true
	}
	prefix, value, found := strings.Cut(name, "/")
	if !found || value == "" || strings.Contains(value, "/") {
		return false
	}
	if prefix == "categories" {
		return !strings.Contains(value, ".")
	}
	if prefix == "repos" {
		id, err := strconv.ParseInt(value, 10, 64)
		return err == nil && id > 0
	}
	return false
}
