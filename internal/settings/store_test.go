package settings

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

func testStore(t *testing.T) (Store, config.Config) {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "settings.db")
	pool, err := storage.Open(t.Context(), cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if _, err := migrations.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	return Store{DB: db}, cfg
}

func TestImportPreservesExistingRulesAndExplicitEmptyLists(t *testing.T) {
	store, cfg := testStore(t)
	rule := storage.DiscoveryRule{
		SourceType: "search", SourceKey: "existing", Query: "topic:custom",
		Enabled: false, IntervalMinutes: 55, Priority: 12, NextRunAt: time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := store.DB.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Initialize(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var existing []storage.DiscoveryRule
	if err := store.DB.Find(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if len(existing) != 1 || existing[0] != rule {
		t.Fatalf("已有规则定义、ID 和运行时间应保留: %+v", existing)
	}
	revision, _, err := store.SaveDiscoveryRules(t.Context(), snapshot.Revision, []Rule{})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err = store.SaveCategories(t.Context(), revision, []config.Category{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.CategoriesFile = "missing.yaml"
	after, err := store.Initialize(t.Context(), cfg)
	if err != nil || after.Revision != revision {
		t.Fatalf("再次启动不应读取种子: %+v %v", after, err)
	}
	if _, items, err := store.DiscoveryRules(t.Context()); err != nil || len(items) != 0 {
		t.Fatalf("已清空规则不能被重新填入: %+v %v", items, err)
	}
	if items, err := store.Categories(t.Context()); err != nil || len(items) != 0 {
		t.Fatalf("已清空分类不能被重新填入: %+v %v", items, err)
	}
}

func TestFailedSeedRollsBackAndCanBeRetried(t *testing.T) {
	store, cfg := testStore(t)
	cfg.Discovery.SearchRules[0].Query = ""
	if _, err := store.Initialize(t.Context(), cfg); err == nil {
		t.Fatal("无效的首次导入应失败")
	}
	if _, err := store.Get(t.Context()); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("失败后不能留下配置修订号: %v", err)
	}
	items, err := store.Categories(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatalf("首次导入失败应回滚已写的分类: %+v %v", items, err)
	}
	cfg.Discovery.SearchRules[0].Query = "topic:fixed"
	if _, err := store.Initialize(t.Context(), cfg); err != nil {
		t.Fatalf("修正后应能重新导入: %v", err)
	}
}

func TestConcurrentEditsCannotOverwriteSameRevision(t *testing.T) {
	store, cfg := testStore(t)
	snapshot, err := store.Initialize(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, slug := range []string{"first", "second"} {
		group.Go(func() {
			_, _, err := store.SaveCategories(t.Context(), snapshot.Revision, []config.Category{{
				Slug: slug, Name: slug, AnyTopics: []string{slug},
			}})
			results <- err
		})
	}
	group.Wait()
	close(results)
	writes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			writes++
		case errors.Is(err, ErrRevision):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if writes != 1 || conflicts != 1 {
		t.Fatalf("同一修订号应只接受一次保存: writes=%d conflicts=%d", writes, conflicts)
	}
}
