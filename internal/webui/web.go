package webui

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/consts"
)

// Register 将浏览器路由交给 Vite 入口，API 与缺失静态文件保留各自的 404。
func Register(router *gin.Engine, dist fs.FS) {
	router.NoRoute(func(c *gin.Context) {
		request := c.Request
		name := strings.TrimPrefix(request.URL.Path, "/")
		if name == "api" || strings.HasPrefix(name, "api/") {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusNotFound, gin.H{"code": consts.CodeNotFound})
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
			if adminPage(strings.TrimSuffix(name, "/")) {
				cache = "no-store"
				c.Header("X-Robots-Tag", "noindex, nofollow")
			}
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
	if adminPage(name) {
		return true
	}
	switch name {
	case "", "trending", "repos", "categories", consts.PublicTopicPathSegment, "starred":
		return true
	}
	prefix, value, found := strings.Cut(name, "/")
	if !found || value == "" {
		return false
	}
	if prefix == "categories" || prefix == consts.PublicTopicPathSegment {
		return !strings.ContainsAny(value, "/.")
	}
	if prefix == "repos" {
		return repoPath(value)
	}
	return false
}

// 后台深链接由 React 校验会话，API 数据仍由 Gin 独立鉴权。
func adminPage(name string) bool {
	switch name {
	case "admin", "admin/login", "admin/runs", "admin/repos", "admin/topics", "admin/watchlist", "admin/setup",
		"watchlist", "system", "setup":
		return true
	}
	id, found := strings.CutPrefix(name, "admin/repos/")
	return found && repoPath(id)
}

var repoNamePath = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

// repoPath 仅接受 owner/name 两段，其他路径交给普通 404 处理。
func repoPath(value string) bool {
	_, name, found := strings.Cut(value, "/")
	return found && name != "." && name != ".." && repoNamePath.MatchString(value)
}
