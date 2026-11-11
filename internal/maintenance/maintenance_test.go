package maintenance

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

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
	if err := metrics.Compute(t.Context(), db, cfg, now, false); err != nil {
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
	if err := metrics.Compute(t.Context(), db, cfg, now.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, &storage.LatestMetric{}, "calculated_at=?", now.UnixMilli()); n != 2 {
		t.Fatalf("同一计算窗口被重复发布: %d", n)
	}
	if err := metrics.Compute(t.Context(), db, cfg, now.Add(48*time.Hour), false); err != nil {
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
