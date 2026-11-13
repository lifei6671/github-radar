package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	domain "github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/metricstore"
	"github.com/lifei6671/github-radar/migrations"
)

// 保持指标读快照时模拟管理员屏蔽。写入应当立刻完成，旧计算不能复活该仓库。
func TestMetricsComputeAllowsWriterAndRejectsChangedMonitoringState(t *testing.T) {
	db, cfg, now := fixture(t)
	if err := db.Callback().Query().After("gorm:query").Register("test:competing_metrics_write", func(tx *gorm.DB) {
		if tx.Statement.Table != storage.RepositoryTable || tx.Error != nil {
			return
		}
		_ = db.Callback().Query().Remove("test:competing_metrics_write")
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		err := db.WithContext(ctx).Model(&storage.Repository{}).Where("repo_id = ?", 1).
			Updates(map[string]any{"blocked": true, "state": "paused"}).Error
		if err != nil {
			t.Errorf("指标计算持有读快照时无法写入: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.Compute(t.Context(), metricstore.Store{DB: db}, cfg, now, true); !errors.Is(err, metricstore.ErrMonitoringChanged) {
		t.Fatalf("旧指标快照未因并发监控变更被拒绝: %v", err)
	}
	var repo storage.Repository
	if err := db.Take(&repo, "repo_id = ?", 1).Error; err != nil || !repo.Blocked || repo.State != "paused" {
		t.Fatalf("屏蔽状态没有保留: %+v %v", repo, err)
	}
	var markers int64
	if err := db.Model(&storage.MaintenanceState{}).Where("key = ?", metrics.ComputeKey).Count(&markers).Error; err != nil || markers != 0 {
		t.Fatalf("已拒绝的指标快照不应推进水位: %d %v", markers, err)
	}
}

// 晋级若与旧档位采样交错，必须基于新快照重新安排采样。
func TestMetricsPromotionRejectsConcurrentSamplingPlan(t *testing.T) {
	db, cfg, now := fixture(t)
	cfg.Scheduler = cfg.Scheduler.ObservationTrial()
	cfg.Scheduler.LegacyMonitoring = true
	if err := db.Model(&storage.Repository{}).Where("repo_id = ?", 1).Update("grade", "C").Error; err != nil {
		t.Fatal(err)
	}
	sample(t, db, 1, now.Add(-24*time.Hour), 100)
	sample(t, db, 1, now, 650)
	next := now.Add(72 * time.Hour).UnixMilli()
	if err := db.Callback().Query().After("gorm:query").Register("test:sample_during_metrics", func(tx *gorm.DB) {
		if tx.Statement.Table != storage.RepositoryTable || tx.Error != nil {
			return
		}
		_ = db.Callback().Query().Remove("test:sample_during_metrics")
		err := db.Model(&storage.Repository{}).Where("repo_id = ?", 1).
			Updates(map[string]any{"last_success_at": now.Add(time.Second).UnixMilli(), "next_fetch_at": next}).Error
		if err != nil {
			t.Errorf("并发采样未落盘: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	store := metricstore.Store{DB: db}
	if err := metrics.Compute(t.Context(), store, cfg, now, true); !errors.Is(err, metricstore.ErrMonitoringChanged) {
		t.Fatalf("旧采样计划未被拒绝: %v", err)
	}
	if err := metrics.Compute(t.Context(), store, cfg, now.Add(2*time.Second), true); err != nil {
		t.Fatal(err)
	}
	var repo storage.Repository
	if err := db.Take(&repo, "repo_id = ?", 1).Error; err != nil || repo.Grade != "S" || repo.NextFetchAt >= next {
		t.Fatalf("重新计算后未按晋级计划采样: %+v %v", repo, err)
	}
}

// 精确净增和过去七个独立窗共同触发晋级，官方计数不能代替任何本地端点。
func TestBreakoutPromotionPersistsLocalEvidence(t *testing.T) {
	db, cfg, now := fixture(t)
	cfg.Metrics.HotGrowth, cfg.Metrics.TopGrowthRepos = 9999, 0
	for i := 1; i <= 8; i++ {
		sample(t, db, 1, now.Add(-time.Duration(i)*24*time.Hour), 500)
	}
	sample(t, db, 1, now, 600)
	state := storage.RepoStarHistoryStatus{RepoID: 1, BreakoutAt: new(now.Add(-12 * time.Hour).UnixMilli()), BreakoutJSON: `{"source":"github_daily_stars"}`}
	if err := db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	if err := metrics.Compute(t.Context(), metricstore.Store{DB: db}, cfg, now, true); err != nil {
		t.Fatal(err)
	}
	var repo storage.Repository
	if err := db.Take(&repo, "repo_id = 1").Error; err != nil {
		t.Fatal(err)
	}
	var evidence domain.Breakout
	if err := json.Unmarshal([]byte(repo.BreakoutGradeJSON), &evidence); err != nil || repo.Grade != "S" ||
		evidence.Source != domain.BreakoutLocal || evidence.Value != 100 || evidence.CoveredDays != 7 || evidence.MinimumRatio != 2.5 {
		t.Fatalf("爆发晋级缺少实际证据: grade=%s evidence=%+v err=%v", repo.Grade, evidence, err)
	}
	if err := db.Take(&state, "repo_id = 1").Error; err != nil || state.ConfirmedAt == nil || *state.ConfirmedAt != *state.BreakoutAt {
		t.Fatalf("同一官方信号经本地复核后应保留确认状态: %+v %v", state, err)
	}
}

func fixture(t *testing.T) (*gorm.DB, config.Config, time.Time) {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "metrics.db")
	cfg = cfg.WithRuntime(cfg.SeedRuntime())
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
	now := time.Date(2026, 11, 11, 12, 5, 0, 0, time.UTC)
	at := now.AddDate(0, 0, -80).UnixMilli()
	for _, repo := range []storage.Repository{
		{RepoID: 1, Owner: "owner", Name: "one", FullName: "owner/one", NameIsCurrent: true, TopicsJSON: "[]", Grade: "A", State: "active", DiscoveredAt: at, LastDiscoveredAt: at},
		{RepoID: 2, Owner: "owner", Name: "two", FullName: "owner/two", NameIsCurrent: true, TopicsJSON: "[]", Grade: "A", State: "active", DiscoveredAt: at, LastDiscoveredAt: at},
	} {
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, cfg, now
}

func sample(t *testing.T, db *gorm.DB, id int64, at time.Time, stars int) {
	t.Helper()
	row := storage.RepoSnapshot{RepoID: id, SampleSlot: at.UnixMilli(), ObservedAt: at.UnixMilli(), Stars: stars}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *gorm.DB, model any, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// 校验同一轮真实端点、负值、小时层锚点、完整榜单和时间老化。
func TestComputeGrowthAndHeat(t *testing.T) {
	db, cfg, now := fixture(t)
	if err := db.Create(&storage.DiscoveryRule{SourceType: "trending", SourceKey: "all:daily", IntervalMinutes: 60}).Error; err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		offset time.Duration
		stars  int
	}{
		{-30 * 24 * time.Hour, 800}, {-7 * 24 * time.Hour, 1000}, {-48 * time.Hour, 1100},
		{-24*time.Hour - 5*time.Minute, 1200}, {-24*time.Hour + 5*time.Minute, 1225},
		{-time.Hour, 1460}, {0, 1450},
	} {
		sample(t, db, 1, now.Add(p.offset), p.stars)
	}
	sample(t, db, 2, now, 10000)
	for i := 0; i < 8; i++ {
		sample(t, db, 1, now.Truncate(24*time.Hour).AddDate(0, 0, -i), 1400-i*20)
	}
	for i := 1; i <= 2; i++ {
		at := now.Add(-time.Duration(i) * time.Hour).UnixMilli()
		batch := storage.TrendingBatchRecord{SourceKey: "all", Window: "daily", ObservedAt: at, ObservedSlot: at}
		if err := db.Create(&batch).Error; err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			o := storage.TrendingObservation{RepoID: 1, SourceKey: "all", Window: "daily", ObservedSlot: at, Rank: 2}
			if err := db.Create(&o).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Rollup(t.Context(), db, cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(t.Context(), db, cfg.Maintenance, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.RepoSnapshot{}, "observed_at < ?", now.AddDate(0, 0, -14).UnixMilli()); n != 0 {
		t.Fatalf("旧原始快照剩余 %d", n)
	}
	if err := metrics.Compute(t.Context(), metricstore.Store{DB: db}, cfg, now, false); err != nil {
		t.Fatal(err)
	}
	var row storage.LatestMetric
	if err := db.Take(&row, "repo_id=1").Error; err != nil {
		t.Fatal(err)
	}
	for want, actual := range map[int64]*int64{-10: row.Delta1h, 250: row.Delta24h, 450: row.Delta7d, 650: row.Delta30d} {
		if actual == nil || *actual != want {
			t.Fatalf("窗口增量应为 %d，指标为 %+v", want, row)
		}
	}
	accel := 251.0 / 101.0
	want := .35*100*math.Log1p(250)/math.Log1p(1000) + .20*100*math.Log1p(450)/math.Log1p(5000) +
		.15*100*math.Log1p(accel-1)/math.Log1p(3) + .15*100
	if row.HotnessScore == nil || math.Abs(*row.HotnessScore-want) > 1e-9 || row.DataQuality != "exact" ||
		row.TrendingFrequency != 1 || row.TrendingStreak != 0 || row.TrendingRank != nil || row.TrendingScore == nil || *row.TrendingScore != 0 {
		t.Fatalf("热度或空榜信号错误: %+v，热度应为 %.6f", row, want)
	}
	var single storage.LatestMetric
	if err := db.Take(&single, "repo_id=2").Error; err != nil {
		t.Fatal(err)
	}
	if single.Delta24h != nil || single.HotnessScore != nil || single.DataQuality != "insufficient" {
		t.Fatalf("单样本不应生成增长: %+v", single)
	}
	var repo storage.Repository
	if err := db.Take(&repo, "repo_id=1").Error; err != nil || repo.Grade != "S" {
		t.Fatalf("精确增长应升 S: %v %+v", err, repo)
	}
	if err := metrics.Compute(t.Context(), metricstore.Store{DB: db}, cfg, now.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.LatestMetric{}, "calculated_at=?", now.UnixMilli()); n != 2 {
		t.Fatalf("同一计算窗口被重复发布: %d", n)
	}
	if err := metrics.Compute(t.Context(), metricstore.Store{DB: db}, cfg, now.Add(48*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := db.Take(&row, "repo_id=1").Error; err != nil {
		t.Fatal(err)
	}
	if row.Delta24h != nil || row.HotnessScore != nil || row.TrendingScore != nil {
		t.Fatalf("停止采样后不能继续显示旧的增长和热度: %+v", row)
	}
}

// 校验迟到样本只累计一次，清理从原始层到日层逐级执行。
func TestRollupAndRetention(t *testing.T) {
	db, cfg, now := fixture(t)
	cfg.Maintenance.BatchRows = 1
	cfg.Maintenance.RawDays, cfg.Maintenance.HourlyDays, cfg.Maintenance.DailyDays = 1, 2, 3
	day := now.Truncate(24*time.Hour).AddDate(0, 0, -2)
	open := day.Add(5 * time.Minute)
	closeAt := day.Add(55 * time.Minute)
	sample(t, db, 1, open, 100)
	sample(t, db, 1, closeAt, 130)
	sample(t, db, 1, closeAt, 999)
	sample(t, db, 1, day.Add(23*time.Hour+50*time.Minute), 160)
	if err := Cleanup(t.Context(), db, cfg.Maintenance, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.RepoSnapshot{}, "repo_id=1"); n != 3 {
		t.Fatalf("无聚合水位时删除了原始观测: %d", n)
	}
	if err := Rollup(t.Context(), db, cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := Cleanup(t.Context(), db, cfg.Maintenance, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.RepoSnapshot{}, "repo_id=1"); n != 0 {
		t.Fatalf("已聚合的过期样本未清理: %d", n)
	}

	late := day.Add(time.Minute)
	sample(t, db, 1, late, 90)
	if err := Cleanup(t.Context(), db, cfg.Maintenance, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.RepoSnapshot{}, "repo_id=1"); n != 1 {
		t.Fatalf("同一水位内的迟到样本须等待聚合: %d", n)
	}
	for range 2 {
		if err := Rollup(t.Context(), db, cfg, now); err != nil {
			t.Fatal(err)
		}
	}
	var hour storage.HourlyMetric
	if err := db.Take(&hour, "repo_id=1 AND hour_start=?", day.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if hour.SampleCount != 3 || hour.Quality != "exact" || hour.Delta == nil || *hour.Delta != 40 ||
		hour.OpenObservedAt != late.UnixMilli() || hour.CloseObservedAt != closeAt.UnixMilli() {
		t.Fatalf("小时桶真实端点或计数错误: %+v", hour)
	}
	var daily storage.DailyMetric
	if err := db.Take(&daily, "repo_id=1 AND day=?", day.Format(time.DateOnly)).Error; err != nil {
		t.Fatal(err)
	}
	if daily.SampleCount != 4 || daily.Delta == nil || *daily.Delta != 70 || daily.Quality != "exact" {
		t.Fatalf("日桶累加了小时差值或重复样本: %+v", daily)
	}
	for _, status := range []string{"succeeded", "canceled", "dead", "failed", "unknown", "pending", "running"} {
		job := storage.Job{Type: "refresh_repo", DedupKey: status, Status: status, PayloadJSON: "{}", MaxAttempts: 1, UpdatedAt: now.AddDate(0, 0, -31).UnixMilli()}
		if err := db.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := Cleanup(t.Context(), db, cfg.Maintenance, now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.HourlyMetric{}, "repo_id=1"); n != 0 {
		t.Fatalf("已进入日层的小时记录未清理: %d", n)
	}
	if n := count(t, db, &storage.DailyMetric{}, "repo_id=1"); n != 1 {
		t.Fatalf("日记录提前清理: %d", n)
	}
	if n := count(t, db, &storage.Job{}, "status IN ('failed','unknown','pending','running')"); n != 4 {
		t.Fatalf("非终态任务被清理: %d", n)
	}
	if n := count(t, db, &storage.Job{}, "status IN ('succeeded','canceled','dead')"); n != 0 {
		t.Fatalf("终态任务未按期清理: %d", n)
	}
	if err := Cleanup(t.Context(), db, cfg.Maintenance, now.AddDate(0, 0, 2)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.DailyMetric{}, "repo_id=1"); n != 0 {
		t.Fatalf("超期日聚合未清理: %d", n)
	}
}

func TestCleanupKeepsUnacknowledgedModelCallsAndReviewedTopics(t *testing.T) {
	db, cfg, now := fixture(t)
	old := now.AddDate(0, 0, -100).UnixMilli()
	for index, status := range []string{"succeeded", "unknown"} {
		job := storage.Job{RepoID: new(int64(1)), Type: "refresh_readme_summary", DedupKey: status,
			Status: []string{"succeeded", "canceled"}[index], PayloadJSON: "{}", MaxAttempts: 3, UpdatedAt: old}
		if err := db.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
		call := storage.AICall{CallKey: status, JobID: job.ID, Origin: "manual", Provider: "fixture",
			Status: status, ReservedAt: old, UpdatedAt: old}
		if err := db.Create(&call).Error; err != nil {
			t.Fatal(err)
		}
	}
	for id := int64(1); id <= 2; id++ {
		topic := storage.TopicCandidate{RepoID: id, AngleKey: "project", TitleZh: "选题", Status: "candidate", UpdatedAt: old}
		if id == 2 {
			topic.ReviewedAt = &old
		}
		if err := db.Create(&topic).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := Cleanup(t.Context(), db, cfg.Maintenance, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.AICall{}, "status='unknown'"); n != 1 {
		t.Fatal("未确认调用不能随已取消任务删除")
	}
	if n := count(t, db, &storage.Job{}, "type='refresh_readme_summary'"); n != 1 {
		t.Fatal("已结束的模型调用和任务未一同删除")
	}
	if err := cleanTopics(db, now.AddDate(0, 0, -90).UnixMilli(), 1); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.TopicCandidate{}, "repo_id=2"); n != 1 {
		t.Fatal("人工审核记录不能自动清理")
	}
	if n := count(t, db, &storage.TopicCandidate{}, "repo_id=1"); n != 0 {
		t.Fatal("过期普通选题应清理")
	}
}

func TestCleanupKeepsCandidatesWithManualDiscoverySources(t *testing.T) {
	db, _, now := fixture(t)
	if err := db.Exec("UPDATE repositories SET state = 'candidate'").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO repo_discovery_events (repo_id, source_type, source_key, first_observed_at)
		VALUES (1, 'manual', '', ?)`, now.AddDate(0, 0, -80).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := cleanCandidates(db, now.AddDate(0, 0, -30).UnixMilli(), 1); err != nil {
		t.Fatal(err)
	}
	if count(t, db, &storage.Repository{}, "repo_id = 1") != 1 || count(t, db, &storage.Repository{}, "repo_id = 2") != 0 {
		t.Fatal("清理候选时应保留带有人工来源记录的仓库")
	}
}
