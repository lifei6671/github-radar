package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestPrepareDatabaseImportsYAMLOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	database := filepath.Join(dir, "radar.db")
	t.Setenv("AI_BASE_URL", "https://first.example/v1")
	t.Setenv("AI_MODEL", "first-model")
	legacy := fmt.Sprintf("server:\n  read_timeout_seconds: 17\nstorage:\n  path: %q\n  wal: false\n  busy_timeout_ms: 1200\n", filepath.ToSlash(database))
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// 离线升级表结构时还没有业务环境，不能提前固定默认设置。
	if err := run(t.Context(), []string{"migrate", "-config", path}, logger); err != nil {
		t.Fatal(err)
	}
	migrated, err := storage.OpenBootstrap(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	var seeded int
	err = migrated.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM system_settings WHERE key='runtime.settings.v1'").Scan(&seeded)
	migrated.Close()
	if err != nil || seeded != 0 {
		t.Fatalf("离线迁移不应写入业务配置: count=%d err=%v", seeded, err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = database
	running, applied, err := prepareDatabase(t.Context(), cfg)
	if err != nil || applied != 0 || running.Server.ReadTimeoutSeconds != 17 || running.Storage.WAL ||
		running.AI.BaseURL != "https://first.example/v1" || running.AI.Model != "first-model" {
		t.Fatalf("首次导入不符: applied=%d err=%v settings=%+v", applied, err, running.RuntimeSettings())
	}
	pool, err := storage.Open(t.Context(), running.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	store := settings.Store{DB: db}
	current, err := store.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current.Settings.Storage.WAL = true
	current.Settings.Storage.BusyTimeoutMS = 1450
	current.Settings.Storage.MaxOpenConns = 2
	current.Settings.Server.ReadTimeoutSeconds = 24
	current.Settings.GitHub.TimeoutSeconds = 27
	current.Settings.AI.Model = "database-model"
	if _, err := store.SaveRuntime(t.Context(), current.Revision, current.Settings); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}

	// 重启时业务 YAML 即使无效，数据库中已经保存的设置也应正常应用。
	t.Setenv("AI_MODEL", "later-environment-model")
	stale := "server:\n  listen: '127.0.0.1:8090'\n  read_timeout_seconds: obsolete\n" +
		"storage:\n  wal: false\n  busy_timeout_ms: obsolete\n" +
		"categories_file: missing-categories.yaml\ndiscovery:\n  trending_languages: ['invalid language']\n"
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = database
	running, applied, err = prepareDatabase(t.Context(), cfg)
	if err != nil || applied != 0 || running.Server.Listen != "127.0.0.1:8090" || running.Server.ReadTimeoutSeconds != 24 ||
		running.GitHub.TimeoutSeconds != 27 || running.AI.Model != "database-model" {
		t.Fatalf("重启未应用已存配置: applied=%d err=%v settings=%+v", applied, err, running.RuntimeSettings())
	}
	reopened, err := storage.Open(t.Context(), running.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var mode string
	var timeout int
	if err := reopened.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := reopened.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || timeout != 1450 || reopened.Stats().MaxOpenConnections != 2 {
		t.Fatalf("连接池没有使用数据库参数: WAL=%s timeout=%d pool=%d", mode, timeout, reopened.Stats().MaxOpenConnections)
	}
}
