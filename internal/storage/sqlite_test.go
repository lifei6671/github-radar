package storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

func TestOpenAppliesPragmasOnEachConnection(t *testing.T) {
	ctx := t.Context()
	pool, err := storage.Open(ctx, config.Storage{
		Path: filepath.Join(t.TempDir(), "雷达 #1.db"), WAL: true,
		BusyTimeoutMS: 1250, MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// 同时借出连接，确保验证的是两个独立的 SQLite 连接。
	for range 2 {
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		checkPragmas(t, ctx, conn)
	}
}

func checkPragmas(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	var mode string
	var foreignKeys, busyTimeout int
	for query, target := range map[string]any{
		"PRAGMA journal_mode": &mode,
		"PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA busy_timeout": &busyTimeout,
	} {
		if err := conn.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if mode != "wal" || foreignKeys != 1 || busyTimeout != 1250 {
		t.Fatalf("连接设置不符: journal_mode=%s foreign_keys=%d busy_timeout=%d", mode, foreignKeys, busyTimeout)
	}
}

func TestMigrationAndGORMMapping(t *testing.T) {
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

	if applied, err := migrations.Up(ctx, pool); err != nil || applied == 0 {
		t.Fatalf("首次迁移应执行: applied=%d err=%v", applied, err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	if initialized, err := storage.Initialized(ctx, db); err != nil || initialized {
		t.Fatalf("空库应为未初始化: initialized=%v err=%v", initialized, err)
	}

	// 用 GORM 写入，再从迁移定义的列读取，验证显式映射。
	setting := storage.SystemSetting{Key: "app.initialized", ValueJSON: "true"}
	if err := db.WithContext(ctx).Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrations.Up(ctx, pool); err != nil || applied != 0 {
		t.Fatalf("重复迁移应跳过已应用版本: applied=%d err=%v", applied, err)
	}
	var value string
	var updatedAt int64
	if err := pool.QueryRowContext(ctx, "SELECT value_json, updated_at FROM system_settings WHERE key = ?", setting.Key).
		Scan(&value, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if value != "true" || updatedAt != setting.UpdatedAt || updatedAt < 1_000_000_000_000 {
		t.Fatalf("持久化设置不符: value_json=%q updated_at=%d", value, updatedAt)
	}
	if initialized, err := storage.Initialized(ctx, db); err != nil || !initialized {
		t.Fatalf("应读取已保存的初始化状态: initialized=%v err=%v", initialized, err)
	}
}
