package migrations

import (
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestUpgradeFromPhaseZeroPreservesSettings(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "phase-zero.db")
	pool, err := storage.Open(t.Context(), cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// 先用原有版本建立阶段 0 数据库，再通过正常入口升级。
	phaseZero, err := goose.NewProvider(goose.DialectSQLite3, pool, files, goose.WithExcludeVersions([]int64{2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phaseZero.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(t.Context(), "INSERT INTO system_settings(key,value_json,updated_at) VALUES ('existing.setting','true',1234)"); err != nil {
		t.Fatal(err)
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 2 {
		t.Fatalf("阶段 0 应只追加两个迁移版本: count=%d err=%v", count, err)
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 0 {
		t.Fatalf("重复升级应无新迁移: count=%d err=%v", count, err)
	}
	var value string
	var updated int64
	if err := pool.QueryRowContext(t.Context(), "SELECT value_json,updated_at FROM system_settings WHERE key='existing.setting'").
		Scan(&value, &updated); err != nil || value != "true" || updated != 1234 {
		t.Fatalf("升级破坏了旧设置: value=%s updated=%d err=%v", value, updated, err)
	}
}
