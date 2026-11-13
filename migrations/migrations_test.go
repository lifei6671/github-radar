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
	phaseZero, err := goose.NewProvider(goose.DialectSQLite3, pool, files, goose.WithExcludeVersions([]int64{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phaseZero.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(t.Context(), "INSERT INTO system_settings(key,value_json,updated_at) VALUES ('existing.setting','true',1234)"); err != nil {
		t.Fatal(err)
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 23 {
		t.Fatalf("阶段 0 应追加二十三个迁移版本: count=%d err=%v", count, err)
	}
	if _, err := pool.ExecContext(t.Context(), "UPDATE project_themes SET name='自定义名称' WHERE slug='terminal-tools'"); err != nil {
		t.Fatal(err)
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 0 {
		t.Fatalf("重复升级应无新迁移: count=%d err=%v", count, err)
	}
	var name string
	if err := pool.QueryRowContext(t.Context(), "SELECT name FROM project_themes WHERE slug='terminal-tools'").Scan(&name); err != nil || name != "自定义名称" {
		t.Fatalf("重复升级应保留已配置的专题: name=%s err=%v", name, err)
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
	old, err := goose.NewProvider(goose.DialectSQLite3, pool, files, goose.WithExcludeVersions([]int64{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24}))
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
		"INSERT INTO discovery_rules(source_type,source_key,query,enabled,interval_minutes,next_run_at,priority) VALUES ('trending','all:daily','',1,60,1234,30),('search','mcp','topic:mcp',1,30,2345,20)",
		"INSERT INTO search_observations(repo_id,source_key,observed_slot,observed_at) VALUES (42,'mcp',0,1234),(42,'mcp',300000,300123)",
		"INSERT INTO trending_observations(repo_id,source_key,window,observed_slot,rank) VALUES (42,'all','daily',0,1),(42,'all','daily',300000,2)",
		`INSERT INTO jobs(type,dedup_key,payload_json,status,available_at,created_at,updated_at) VALUES
		 ('discover_search','pending-search','{"ruleId":2,"starRange":true,"minStars":0,"maxStars":99,"page":2}','failed',42,0,0),
		 ('discover_search','historical-search','{"ruleId":2}','succeeded',42,0,0),
		 ('discover_trending','pending-trending','{"ruleId":1}','pending',42,0,0)`,
	} {
		if _, err := pool.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 21 {
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
	var rules []storage.DiscoveryRule
	if err := db.Order("display_position").Find(&rules).Error; err != nil || len(rules) != 2 ||
		rules[0].SourceKey != "mcp" || rules[0].NextRunAt != 2345 || rules[0].MinStars != 200 ||
		rules[1].SourceKey != "all:daily" || rules[1].DisplayPosition != 1 || rules[1].MinStars != 0 {
		t.Fatalf("升级应保留旧规则展示顺序及计划时间: %+v %v", rules, err)
	}
	var oldPage storage.Job
	if err := db.Take(&oldPage, "dedup_key='pending-search'").Error; err != nil ||
		oldPage.Status != "canceled" || oldPage.LastErrorCode == nil || *oldPage.LastErrorCode != "source_rule_changed" {
		t.Fatalf("旧分页应撤销并从新规则重新安排: %+v %v", oldPage, err)
	}
	var changed int64
	if err := db.Model(&storage.Job{}).Where("(dedup_key='pending-trending' AND status<>'pending') OR (dedup_key='historical-search' AND status<>'succeeded')").
		Count(&changed).Error; err != nil || changed != 0 {
		t.Fatalf("Trending 和已完成任务不应更新: %d %v", changed, err)
	}
	var searchSources, trendingSources int64
	if err := pool.QueryRowContext(t.Context(), `SELECT
		count(*) FILTER (WHERE source_type='search' AND source_key='mcp'),
		count(*) FILTER (WHERE source_type='trending' AND source_key='')
		FROM repo_discovery_sources WHERE repo_id=42`).Scan(&searchSources, &trendingSources); err != nil ||
		searchSources != 1 || trendingSources != 1 {
		t.Fatalf("迁移应从观测明细去重回填两类历史来源: Search=%d Trending=%d err=%v", searchSources, trendingSources, err)
	}
}

func TestUpgradeSeparatesLegacyPeriodDraftsFromPublishedDailies(t *testing.T) {
	pool, err := storage.OpenBootstrap(t.Context(), filepath.Join(t.TempDir(), "editorials.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	old, err := goose.NewProvider(goose.DialectSQLite3, pool, files, goose.WithExcludeVersions([]int64{23, 24}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(t.Context(), `INSERT INTO report_editorials VALUES
		('daily','2026-11-12',3,'day-fingerprint','日报标题','正文','[]','[]',1,1,2,3),
		('weekly','2026-W46',1,'week-fingerprint','旧周报标题','正文','[]','[]',1,1,2,3),
		('monthly','2026-11',1,'month-fingerprint','旧月报标题','正文','[]','[]',1,1,2,3)`); err != nil {
		t.Fatal(err)
	}
	if count, err := Up(t.Context(), pool); err != nil || count != 2 {
		t.Fatalf("升级出刊状态: %d %v", count, err)
	}
	var count int
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM report_editorials
		WHERE (kind='daily' AND issue_number=3 AND headline='日报标题' AND publication_status='published')
		   OR (kind IN ('weekly','monthly') AND issue_number IS NULL AND publication_status='legacy')`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("升级应保留日报和原稿，并撤销旧周月报的刊号: %d %v", count, err)
	}
}
