package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func TestSPAAndStaticRouteBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"initialized": true})
	})
	Register(router, fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>GitHub 雷达</title>")},
		"assets/index-TEST.js": {Data: []byte("console.log('radar')")},
		"radar.svg":            {Data: []byte("<svg/>")},
	})
	for _, tc := range []struct {
		method, url, body, cache string
		status                   int
	}{
		{"GET", "/", "GitHub 雷达", "no-cache", 200},
		{"GET", "/repos/42?range=30d", "", "", 404},
		{"GET", "/repos/radar-demo/browser-agent?range=30d", "GitHub 雷达", "no-cache", 200},
		{"GET", "/repos/org/.github", "GitHub 雷达", "no-cache", 200},
		{"GET", "/categories/mcp", "GitHub 雷达", "no-cache", 200},
		{"GET", "/topics", "GitHub 雷达", "no-cache", 200},
		{"GET", "/topics/terminal-tools?view=all", "GitHub 雷达", "no-cache", 200},
		{"GET", "/topics/terminal-tools/missing.js", "", "", 404},
		{"GET", "/starred", "GitHub 雷达", "no-cache", 200},
		{"GET", "/admin", "GitHub 雷达", "no-store", 200},
		{"GET", "/admin/topics", "GitHub 雷达", "no-store", 200},
		{"GET", "/admin/login?return=%2Fadmin%2Fruns", "GitHub 雷达", "no-store", 200},
		{"GET", "/admin/repos/42", "", "", 404},
		{"GET", "/admin/repos/radar-demo/browser-agent", "GitHub 雷达", "no-store", 200},
		{"GET", "/admin/repos/radar-demo/browser-agent/missing.js", "", "", 404},
		{"GET", "/system?tab=rules", "GitHub 雷达", "no-store", 200},
		{"HEAD", "/setup", "", "no-store", 200},
		{"GET", "/assets/index-TEST.js", "console.log", "immutable", 200},
		{"GET", "/radar.svg", "<svg", "no-cache", 200},
		{"GET", "/assets/missing.js", "", "", 404},
		{"GET", "/repos/radar-demo/browser-agent/missing.js", "", "", 404},
		{"GET", "/api/v1/setup/status", "initialized", "", 200},
		{"GET", "/api/unknown", "not_found", "no-store", 404},
		{"POST", "/api/v1/unknown", "not_found", "no-store", 404},
		{"POST", "/repos/42", "", "", 404},
	} {
		t.Run(tc.method+" "+tc.url, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.url, nil))
			if rec.Code != tc.status || (tc.body != "" && !strings.Contains(rec.Body.String(), tc.body)) ||
				(tc.cache != "" && !strings.Contains(rec.Header().Get("Cache-Control"), tc.cache)) {
				t.Fatalf("实际响应 %d %v %q", rec.Code, rec.Header(), rec.Body.String())
			}
			if (tc.method == "HEAD" || tc.status == 404) && strings.Contains(rec.Body.String(), "GitHub 雷达") {
				t.Fatal("API 或缺失文件不能被 SPA 入口吞掉")
			}
			if strings.HasPrefix(tc.url, "/admin") && tc.status == 200 && rec.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
				t.Fatal("后台页面应排除搜索索引")
			}
			if strings.HasPrefix(tc.url, "/topics") && rec.Header().Get("X-Robots-Tag") != "" {
				t.Fatal("前台专题应允许搜索索引")
			}
		})
	}
}

func TestMissingDistributionKeepsAPIAvailable(t *testing.T) {
	router := gin.New()
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	Register(router, fstest.MapFS{})
	for route, want := range map[string]int{"/": 503, "/healthz": 200, "/assets/main.js": 404, "/api/missing": 404} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("GET", route, nil))
		if rec.Code != want {
			t.Fatalf("%s = %d，期望 %d", route, rec.Code, want)
		}
	}
}
