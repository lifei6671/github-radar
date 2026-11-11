package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	pool, err := storage.OpenBootstrap(t.Context(), filepath.Join(t.TempDir(), "similar.db"))
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
	return db
}

func repository(id int64, name string) storage.Repository {
	return storage.Repository{RepoID: id, Owner: "demo", Name: name, FullName: "demo/" + name,
		NameIsCurrent: true, State: consts.StatusActive, Grade: consts.GradeC, TopicsJSON: "[]"}
}

func TestSimilarRanksAndFiltersSQLiteVectors(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultEmbedding()
	cfg.Enabled, cfg.Model = true, "fixture"
	key := ModelKey(cfg)
	fixtures := []struct {
		repo   storage.Repository
		values []float32
		model  string
	}{
		{repository(1, "browser"), []float32{1, 0, 0}, key},
		{repository(2, "replay"), []float32{4, 1, 0}, key},
		{repository(3, "screenshots"), []float32{3, 2, 0}, key},
		{repository(4, "audio"), []float32{0, 0, 1}, key},
		{repository(5, "blocked"), []float32{1, 0, 0}, key},
		{repository(6, "private"), []float32{1, 0, 0}, key},
		{repository(7, "other-model"), []float32{1, 0, 0}, "other"},
		{repository(8, "other-dimensions"), []float32{1, 0}, key},
		{repository(9, "historical-name"), []float32{1, 0, 0}, key},
		{repository(10, "disabled"), []float32{1, 0, 0}, key},
		{repository(11, "visibility-pending"), []float32{1, 0, 0}, key},
		{repository(12, "no-vector"), nil, key},
		{repository(13, "audio-caption"), []float32{0, 1, 4}, key},
	}
	fixtures[4].repo.Blocked = true
	fixtures[5].repo.State = consts.StatusUnavailable
	fixtures[8].repo.NameIsCurrent = false
	fixtures[9].repo.Disabled = true
	fixtures[10].repo.FirstNotFoundAt = new(int64)
	for _, row := range fixtures {
		if err := db.Create(&row.repo).Error; err != nil {
			t.Fatal(err)
		}
		if row.values == nil {
			continue
		}
		vector, err := encodeVector(row.values)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&storage.RepoEmbedding{RepoID: row.repo.RepoID, ModelKey: row.model, Vector: vector}).Error; err != nil {
			t.Fatal(err)
		}
	}
	assertMatches := func(id int64, expected ...int64) {
		t.Helper()
		items, err := Similar(db, cfg, id)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, item := range items {
			got = append(got, item.RepoID)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("相似结果 %v，期望 %v", got, expected)
		}
	}
	assertMatches(1, 2, 3)
	assertMatches(4, 13)
	assertMatches(12)
	if err := db.Model(&storage.Repository{}).Where(repos.RepoID+" = ?", 2).Update(repos.Blocked, true).Error; err != nil {
		t.Fatal(err)
	}
	assertMatches(1, 3)
	if _, err := Similar(db, cfg, 2); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("屏蔽仓库应撤下自身相似列表，得到 %v", err)
	}
	cfg.Enabled = false
	assertMatches(1)
}

func TestWorkerUsesPublicSourcesAndReusesUnchangedVector(t *testing.T) {
	db := testDB(t)
	active := repository(1, "browser")
	active.TopicsJSON, active.Description = `["automation"]`, new("浏览器自动化")
	blocked := repository(2, "blocked")
	blocked.Blocked = true
	changed := repository(3, "withdrawn-during-read")
	for _, repo := range []storage.Repository{active, blocked, changed} {
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&storage.AISummary{RepoID: 1, SummaryZh: "支持录制操作", SummaryStatus: consts.StatusReady, SourceQuality: consts.SourceSufficient,
		FeaturesJSON: "[]", AudienceJSON: "[]", ValueTagsJSON: "[]"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&storage.RepoAITopics{RepoID: 1, TopicsJSON: `["fallback"]`}).Error; err != nil {
		t.Fatal(err)
	}

	readme := "Records navigation"
	requests := make(chan string, 8)
	var rejected atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("Embedding 未使用独立请求路径和密钥")
		}
		var payload struct{ Model, Input string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != "demo-model" {
			t.Error("Embedding 请求无效")
		}
		requests <- payload.Input
		if rejected.Load() {
			http.Error(w, "fixture error", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,2]}]}`)
	}))
	defer provider.Close()
	cfg := config.DefaultEmbedding()
	cfg.BaseURL, cfg.Model = provider.URL+"/v1", "demo-model"
	worker := Worker{DB: db, Config: cfg, Embed: NewProvider(cfg, "fixture").Embed, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Readme: func(_ context.Context, repo storage.Repository) (string, error) {
			switch repo.RepoID {
			case 2:
				t.Error("屏蔽仓库不应读取 README")
			case 3:
				err := db.Model(&storage.Repository{}).Where(repos.RepoID+" = ?", 3).Update(repos.Blocked, true).Error
				return "不可发送给模型", err
			}
			return readme, nil
		}}
	now := time.Now()
	var inputs []string
	run := func(at time.Time) {
		t.Helper()
		if err := worker.once(t.Context(), at); err != nil {
			t.Fatal(err)
		}
		for len(requests) > 0 {
			inputs = append(inputs, <-requests)
		}
	}
	run(now)
	want := "About: 浏览器自动化\nTopics: automation\nREADME: Records navigation\n摘要: 支持录制操作"
	if !reflect.DeepEqual(inputs, []string{want}) {
		t.Fatalf("来源输入错误: %q", inputs)
	}
	var saved []storage.RepoEmbedding
	if err := db.Find(&saved).Error; err != nil || len(saved) != 1 || len(saved[0].Vector) != 8 {
		t.Fatalf("应只保存公开仓库的向量，得到 %v", err)
	}
	run(now.Add(refreshInterval))
	if len(inputs) != 1 {
		t.Fatal("内容未变时不应重复调用供应商")
	}
	readme = "Records and replays navigation"
	rejected.Store(true)
	next := now.Add(2 * refreshInterval)
	run(next)
	run(next.Add(retryInterval / 2))
	if len(inputs) != 2 {
		t.Fatal("失败后应等待下次检查")
	}
	rejected.Store(false)
	run(next.Add(retryInterval))
	if len(inputs) != 3 || !strings.Contains(inputs[2], readme) {
		t.Fatal("来源变化后应重新生成")
	}
}

func TestWorkerRechecksVisibilityAfterReadingStoredSources(t *testing.T) {
	db := testDB(t)
	repo := repository(1, "withdrawn-during-summary")
	repo.Description = new("初次检查仍为公开")
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	// 模拟读取缓存摘要期间已提交的屏蔽。
	err := db.Callback().Query().After("gorm:query").Register("test:withdraw_after_summary", func(query *gorm.DB) {
		if query.Statement.Table == storage.AISummaryTable {
			if err := db.Model(&storage.Repository{}).Where(repos.RepoID+" = ?", 1).Update(repos.Blocked, true).Error; err != nil {
				query.AddError(err)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{DB: db, Config: config.DefaultEmbedding(),
		Readme: func(context.Context, storage.Repository) (string, error) { return "公开 README", nil },
		Embed: func(context.Context, string) ([]byte, error) {
			t.Error("屏蔽提交后不应向模型发送项目材料")
			return encodeVector([]float32{1, 0})
		}}
	if err := worker.once(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Take(&repo, repos.RepoID+" = ?", 1).Error; err != nil || !repo.Blocked {
		t.Fatalf("测试未提交屏蔽，得到 %v", err)
	}
}

func TestWorkerWaitsForRestoreResume(t *testing.T) {
	db := testDB(t)
	repo := repository(1, "after-restore")
	repo.Description = new("演示恢复")
	if err := db.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	setPaused := func(value string) {
		t.Helper()
		if err := db.Save(&storage.SystemSetting{Key: storage.AutoAIResumeKey, ValueJSON: value}).Error; err != nil {
			t.Fatal(err)
		}
	}
	reads, calls := 0, 0
	worker := Worker{DB: db, Config: config.DefaultEmbedding(),
		Readme: func(context.Context, storage.Repository) (string, error) { reads++; return "", nil },
		Embed: func(context.Context, string) ([]byte, error) {
			calls++
			return encodeVector([]float32{1, 0})
		}}
	now := time.Now()
	setPaused("true")
	if err := worker.once(t.Context(), now); err != nil || reads != 0 || calls != 0 {
		t.Fatalf("恢复暂停时不应发起外部请求: reads=%d calls=%d err=%v", reads, calls, err)
	}
	setPaused("false")
	if err := worker.once(t.Context(), now); err != nil || reads != 1 || calls != 1 {
		t.Fatalf("管理员确认后应恢复生成: reads=%d calls=%d err=%v", reads, calls, err)
	}
}
