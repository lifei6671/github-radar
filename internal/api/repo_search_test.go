package api

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/embedding"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestSemanticRepoSearchRanksFiltersAndPaginates(t *testing.T) {
	_, db := collectionRouter(t)
	if err := storage.InitializeEmpty(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	const zh = "能把网页操作录制并回放"
	const en = "tools that align speech with subtitle timestamps"
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Model, Input string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || r.URL.Path != "/v1/embeddings" ||
			input.Model != "test-model" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("搜索应复用已配置的 Embedding 模型与接口")
		}
		switch input.Input {
		case zh:
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0,0]}]}`)
		case en, "RADAR-DEMO/REPLAY-KIT", "replay-kit":
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[0,0,1]}]}`)
		case "fallback":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Error("查询词不应包含仓库资料")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(provider.Close)
	t.Setenv(consts.EmbeddingAPIKeyEnv, "test-key")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg = testAdminConfig(cfg)
	cfg.Embedding.Enabled, cfg.Embedding.BaseURL, cfg.Embedding.Model = true, provider.URL+"/v1", "test-model"
	key := embedding.ModelKey(cfg.Embedding)
	fixtures := []struct {
		name, about, language, topics string
		vector                        []float32
		blocked, hidden, otherModel   bool
	}{
		{"browser-agent", "Browser navigation automation", "TypeScript", "[]", []float32{1, 0, 0}, false, false, false},
		{"replay-kit", "Browser workflows with replay", "TypeScript", "[]", []float32{4, 1, 0}, false, false, false},
		{"snapshot-check", "Visual regression for browser pages", "Go", `["mcp"]`, []float32{3, 2, 0}, false, false, false},
		{"caption-aligner", "语音字幕自动对齐", "Python", "[]", []float32{0, 0, 1}, false, false, false},
		{"mentions-replay", "fallback reference for radar-demo/replay-kit", "Go", "[]", nil, false, false, false},
		{"blocked", "屏蔽的浏览器工具", "Go", `["mcp"]`, []float32{1, 0, 0}, true, false, false},
		{"withdrawn", "撤回的浏览器工具", "Go", `["mcp"]`, []float32{1, 0, 0}, false, true, false},
		{"transcriber", "音频转写并标记时间", "Python", "[]", []float32{0, 1, 4}, false, false, false},
		{"different-model", "另一模型的浏览器工具", "Go", `["mcp"]`, []float32{1, 0, 0}, false, false, true},
		{"fallback-tool", "fallback test", "Go", "[]", []float32{1, 0}, false, false, false},
		{"unpublished", "Browser automation awaiting summary", "Kotlin", `["mcp"]`, []float32{1, 0, 0}, false, false, false},
	}
	for index, row := range fixtures {
		id := int64(index + 1)
		repo := storage.Repository{RepoID: id, Owner: "radar-demo", Name: row.name, FullName: "radar-demo/" + row.name,
			NameIsCurrent: !row.hidden, State: consts.StatusActive, Grade: consts.GradeC, Description: &row.about,
			PrimaryLanguage: &row.language, TopicsJSON: row.topics, Blocked: row.blocked, DiscoveredAt: id, LastSuccessAt: new(id)}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
		if row.name != "unpublished" {
			seedReadySummary(t, db, id, id)
		}
		if row.vector == nil {
			continue
		}
		vector := make([]byte, len(row.vector)*4)
		for i, value := range row.vector {
			binary.LittleEndian.PutUint32(vector[i*4:], math.Float32bits(value))
		}
		model := key
		if row.otherModel {
			model = "previous-model"
		}
		if err := db.Create(&storage.RepoEmbedding{RepoID: id, ModelKey: model, Vector: vector}).Error; err != nil {
			t.Fatal(err)
		}
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(db, pool, logger, cfg, nil)
	const endpoint = "/api/v1/public/repos?"
	request(t, router, http.MethodGet, endpoint+"q="+url.QueryEscape(zh)+"&theme=missing", "", "", 404)
	if calls.Load() != 0 {
		t.Fatal("无效专题无需调用模型")
	}
	unpublished := request(t, router, http.MethodGet, endpoint+"q="+url.QueryEscape(zh)+"&language=Kotlin", "", "", 200)
	if len(unpublished["items"].([]any)) != 0 || calls.Load() != 0 {
		t.Fatalf("未发布的向量不应触发公开语义检索: %v", unpublished)
	}
	search := func(q string, extra string, expected ...string) map[string]any {
		t.Helper()
		page := request(t, router, http.MethodGet, endpoint+"q="+url.QueryEscape(q)+extra, "", "", 200)
		var names []string
		for _, item := range page["items"].([]any) {
			names = append(names, item.(map[string]any)["fullName"].(string))
		}
		if !reflect.DeepEqual(names, expected) {
			t.Fatalf("%q %s 的结果为 %v，期望 %v", q, extra, names, expected)
		}
		return page
	}
	search(zh, "", "radar-demo/browser-agent", "radar-demo/replay-kit", "radar-demo/snapshot-check")
	search(zh, "&language=Go&category=mcp", "radar-demo/snapshot-check")
	search(zh, "&language=Python&category=mcp")
	first := search(zh, "&limit=1", "radar-demo/browser-agent")
	cursor := url.QueryEscape(first["nextCursor"].(string))
	search(zh, "&cursor="+cursor, "radar-demo/replay-kit", "radar-demo/snapshot-check")
	request(t, router, http.MethodGet, endpoint+"q="+url.QueryEscape(zh)+"&language=Go&cursor="+cursor, "", "", 400)
	if calls.Load() != 1 {
		t.Fatal("同一查询的筛选和分页应复用向量，非法游标无需调用模型")
	}
	search(en, "", "radar-demo/caption-aligner", "radar-demo/transcriber")
	search("RADAR-DEMO/REPLAY-KIT", "", "radar-demo/replay-kit", "radar-demo/mentions-replay", "radar-demo/caption-aligner", "radar-demo/transcriber")
	search("replay-kit", "&limit=1", "radar-demo/replay-kit")
	search("replay-kit", "&sort=discovered", "radar-demo/mentions-replay", "radar-demo/replay-kit")
	fallback := search("fallback", "&limit=1", "radar-demo/fallback-tool")
	search("fallback", "&cursor="+url.QueryEscape(fallback["nextCursor"].(string)), "radar-demo/mentions-replay")
	if calls.Load() != 5 {
		t.Fatal("字面回退分页不应改用语义排序")
	}
	if err := db.Save(&storage.SystemSetting{Key: storage.AutoAIResumeKey, ValueJSON: "true"}).Error; err != nil {
		t.Fatal(err)
	}
	search(zh, "")
	request(t, router, http.MethodGet, endpoint+"q="+url.QueryEscape(zh)+"&cursor="+cursor, "", "", 400)
	if calls.Load() != 5 {
		t.Fatal("恢复暂停时应直接使用字面搜索")
	}
	if err := db.Save(&storage.SystemSetting{Key: storage.AutoAIResumeKey, ValueJSON: "false"}).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv(consts.EmbeddingAPIKeyEnv, "")
	router = NewRouter(db, pool, logger, cfg, nil)
	search(zh, "")
	search("replay-kit", "", "radar-demo/replay-kit", "radar-demo/mentions-replay")
	if calls.Load() != 5 {
		t.Fatal("缺少密钥时应直接使用字面搜索")
	}
}
