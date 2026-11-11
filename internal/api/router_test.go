package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lifei6671/github-radar/internal/backup"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

func TestHealthAndSetupStatus(t *testing.T) {
	ctx := t.Context()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "radar.db")
	pool, err := storage.Open(ctx, cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	snapshot, err := (settings.Store{DB: db}).Initialize(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.WithRuntime(snapshot.Settings)
	manager, err := backup.New(ctx, pool, db, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(db, pool, logger, testAdminConfig(cfg), nil, manager)
	session := testAdminCookie(t)

	// 健康检查公开，安装状态对未认证请求统一返回 401。
	cases := []struct {
		path, body string
		cookie     *http.Cookie
		status     int
	}{
		{"/healthz", `{"status":"ok"}`, nil, http.StatusOK},
		{"/readyz", `{"status":"ready"}`, nil, http.StatusOK},
		{"/api/v1/setup/status", `{"code":"unauthorized"}`, nil, http.StatusUnauthorized},
		{"/api/v1/setup/status", `{"initialized":false}`, session, http.StatusOK},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.cookie != nil {
			req.AddCookie(tc.cookie)
		}
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status || strings.TrimSpace(rec.Body.String()) != tc.body {
			t.Errorf("%s: 得到 %d %s，期望 %d %s", tc.path, rec.Code, rec.Body, tc.status, tc.body)
		}
		if tc.status == http.StatusUnauthorized && rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("管理接口的 401 不应被缓存")
		}
	}

	// 安装前状态查询可用，备份管理在完成初始化后才开放。
	for _, tc := range []struct {
		method, path, code string
		auth               bool
		status             int
	}{
		{"GET", "/system/backup/status", "unauthorized", false, 401},
		{"GET", "/system/backup/status", "initialized", true, 200},
		{"GET", "/system/backup/settings", "setup_required", true, 503},
		{"GET", "/setup/backups", "setup_session_expired", true, 409},
		{"POST", "/setup/initialize", "initialized", true, 200},
		{"POST", "/setup/initialize", "already_initialized", true, 409},
		{"GET", "/system/backup/settings", "retentionDays", true, 200},
		{"POST", "/system/backup/run", "r2_not_configured", true, 409},
		{"GET", "/setup/operations/missing", "backup_operation_not_found", true, 404},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, nil)
		if tc.auth {
			req.AddCookie(session)
		}
		router.ServeHTTP(rec, req)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}

	// 数据库关闭后，存活检查仍成功，就绪检查应返回 503。
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/healthz": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: 得到 %d，期望 %d", path, rec.Code, want)
		}
	}
}
