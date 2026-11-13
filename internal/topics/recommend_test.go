package topics_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/topicstore"
	"github.com/lifei6671/github-radar/internal/topics"
	"github.com/lifei6671/github-radar/migrations"
)

func TestDailySelectionAndReentry(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "topics.db")
	cfg.Recommendations.Limit = 4
	pool, err := storage.Open(t.Context(), cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := migrations.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 0, 31, 0, 0, time.UTC)
	desc := "Workflow automation project."
	for id := int64(1); id <= 4; id++ {
		name := fmt.Sprintf("repo-%d", id)
		r := storage.Repository{RepoID: id, Owner: "owner", Name: name, FullName: "owner/" + name, NameIsCurrent: true,
			TopicsJSON: "[]", State: "active", Grade: "A", StarredByUser: id == 4, Description: &desc}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
		m := storage.LatestMetric{RepoID: id, CalculatedAt: now.UnixMilli(), DataQuality: "estimated", WindowDetailsJSON: `{}`}
		switch id {
		case 1:
			m.Delta24h = new(int64(100))
			m.WindowDetailsJSON = `{"24h":{"quality":"exact"}}`
		case 2:
			m.TrendingRank, m.TrendingScore = new(2), new(50.0)
		case 3:
			m.PersistenceScore = new(66.7)
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	marker := storage.MaintenanceState{Key: metrics.ComputeKey, Value: "1", UpdatedAt: now.Add(-48 * time.Hour).UnixMilli()}
	if err := db.Create(&marker).Error; err != nil {
		t.Fatal(err)
	}
	if err := topics.Generate(t.Context(), topicstore.Store{DB: db}, cfg.Recommendations, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var rows []storage.TopicCandidate
	if err := db.Find(&rows).Error; err != nil || len(rows) != 0 {
		t.Fatal("本地计划时刻前不应生成")
	}
	if err := topics.Generate(t.Context(), topicstore.Store{DB: db}, cfg.Recommendations, now); err != nil {
		t.Fatal(err)
	}
	var dailyMark storage.MaintenanceState
	if err := db.Find(&dailyMark, "key=?", topics.RecommendationDayKey).Error; err != nil || dailyMark.Key != "" {
		t.Fatal("旧指标不应占用当天选题机会")
	}
	if err := db.Model(&marker).Update("updated_at", now.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := topics.Generate(t.Context(), topicstore.Store{DB: db}, cfg.Recommendations, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Order("id").Find(&rows).Error; err != nil || len(rows) != 4 {
		t.Fatalf("四类入池和同日幂等失败: %v %+v", err, rows)
	}
	for i, r := range rows {
		if r.RepoID != int64(i+1) || r.Status != "candidate" || r.Reason == "" {
			t.Fatalf("候选组顺序或字段有误: %+v", rows)
		}
	}
	first := rows[0]
	feedback := "保留人工判断"
	if err := db.Model(&first).Updates(map[string]any{"status": "published", "feedback": feedback, "reviewed_at": now.UnixMilli(),
		"source_fingerprint": "readme:old", "reader_value": "工作流"}).Error; err != nil {
		t.Fatal(err)
	}
	next := now.AddDate(0, 0, 1)
	if err := db.Model(&storage.LatestMetric{}).Where("repo_id=1").Updates(map[string]any{"delta_24h": 150, "calculated_at": next.UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := topics.Generate(t.Context(), topicstore.Store{DB: db}, cfg.Recommendations, next); err != nil {
		t.Fatal(err)
	}
	if err := db.Take(&first, "id=?", first.ID).Error; err != nil || first.Status != "published" {
		t.Fatal("小幅变化不应覆盖人工状态")
	}
	next = next.AddDate(0, 0, 1)
	if err := db.Model(&marker).Update("updated_at", next.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&storage.LatestMetric{}).Where("repo_id=1").Updates(map[string]any{"delta_24h": 400, "calculated_at": next.UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := topics.Generate(t.Context(), topicstore.Store{DB: db}, cfg.Recommendations, next); err != nil {
		t.Fatal(err)
	}
	if err := db.Take(&first, "id=?", first.ID).Error; err != nil || first.Status != "candidate" ||
		first.Feedback == nil || *first.Feedback != feedback || first.ReviewedAt == nil ||
		first.ReaderValue != "工作流" || first.SourceFingerprint == nil || *first.SourceFingerprint != "readme:old" {
		t.Fatal("明显增长回到候选时应保留人工记录")
	}
	if err := db.Model(&first).Update("status", "published").Error; err != nil {
		t.Fatal(err)
	}
	doc := storage.AISummary{RepoID: 1, SourceFingerprint: "readme:new", SourceQuality: "sufficient", SourceLanguage: "en",
		FeaturesJSON: `["工作流"]`, AudienceJSON: "[]", ValueTagsJSON: "[]", SummaryStatus: "ready"}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if err := topics.Generate(t.Context(), topicstore.Store{DB: db}, cfg.Recommendations, next.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Take(&first, "id=?", first.ID).Error; err != nil || first.Status != "published" {
		t.Fatal("仅修改 README 且功能证据相同，不应重复打开已发布选题")
	}
	if err := db.Model(&doc).Update("features_json", `["工作流","定时调度"]`).Error; err != nil {
		t.Fatal(err)
	}
	next = next.AddDate(0, 0, 2)
	if err := db.Model(&marker).Update("updated_at", next.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&storage.LatestMetric{}).Where("repo_id=1").Update("calculated_at", next.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := topics.Generate(t.Context(), topicstore.Store{DB: db}, cfg.Recommendations, next); err != nil {
		t.Fatal(err)
	}
	if err := db.Take(&first, "id=?", first.ID).Error; err != nil || first.Status != "candidate" ||
		first.SourceFingerprint == nil || *first.SourceFingerprint != "readme:new" {
		t.Fatal("README 和功能证据同时变化应允许重新入池")
	}
	var count int64
	if err := db.Model(&storage.TopicCandidate{}).Where("repo_id=1").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("重新进入不应制造重复选题")
	}
}
