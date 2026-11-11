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
	phaseZero, err := goose.NewProvider(goose.DialectSQLite3, pool, files, goose.WithExcludeVersions([]int64{2, 3, 4, 5}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phaseZero.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(t.Context(), "INSERT INTO system_settings(key,value_json,updated_at) VALUES ('existing.setting','true',1234)"); err != nil {
		t.Fatal(err)
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 4 {
		t.Fatalf("阶段 0 应追加四个迁移版本: count=%d err=%v", count, err)
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

// 在已有样本及最新榜单的阶段 1 库上升级，保留真实时间及观测顺序。
func TestUpgradeBackfillsPhaseOneAnchors(t *testing.T) {
	pool, err := storage.OpenBootstrap(t.Context(), filepath.Join(t.TempDir(), "phase-one.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	old, err := goose.NewProvider(goose.DialectSQLite3, pool, files, goose.WithExcludeVersions([]int64{4, 5}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO repositories(repo_id,owner,name,full_name,html_url,discovered_at,last_discovered_at,next_fetch_at,updated_at) VALUES (42,'a','b','a/b','',0,0,0,0)",
		"INSERT INTO repo_snapshots(repo_id,sample_slot,observed_at,stars,forks) VALUES (42,0,5400000,80,0),(42,300000,3900000,100,0)",
		`INSERT INTO maintenance_state(key,value,updated_at) VALUES ('trending:language:go:weekly','{"observedSlot":300000,"observedAt":345678,"size":0}',345678)`,
	} {
		if _, err := pool.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 2 {
		t.Fatalf("旧库应只应用新增版本: count=%d err=%v", count, err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	var pending storage.PendingMetric
	if err := db.Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending.BucketStart != 3600000 || pending.OpenObservedAt != 3900000 || pending.CloseObservedAt != 5400000 ||
		pending.StarsOpen != 100 || pending.StarsClose != 80 || pending.SampleCount != 2 {
		t.Fatalf("旧库小时锚点未使用真实观测顺序: %+v", pending)
	}
	var batch storage.TrendingBatchRecord
	if err := db.Take(&batch).Error; err != nil || batch.SourceKey != "language:go" || batch.Window != "weekly" || batch.ObservedAt != 345678 {
		t.Fatalf("旧库成功空榜应保留真实时间: %+v %v", batch, err)
	}
}
