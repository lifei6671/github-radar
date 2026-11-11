package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/maintenance"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

const acceptanceReadmeMarker = "RADAR_ACCEPTANCE_PRIVATE_README_MARKER"

type acceptanceUpstream struct {
	stars          atomic.Int32
	readmeVersion  atomic.Int32
	modelCalls     atomic.Int32
	translateCalls atomic.Int32
	notModified    atomic.Int32
}

func (u *acceptanceUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4900")
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	w.Header().Set("X-RateLimit-Resource", "core")
	repo := map[string]any{
		"id": acceptanceRepoID, "name": "radar-workflow", "full_name": "fixture/radar-workflow", "owner": map[string]string{"login": "fixture"},
		"html_url": "https://github.com/fixture/radar-workflow", "description": "Toolkit for repeatable local development workflows.",
		"stargazers_count": u.stars.Load(), "forks_count": 5, "topics": []string{"mcp"}, "language": "Go", "visibility": "public",
		"created_at": time.Now().AddDate(0, -3, 0).UTC(), "pushed_at": time.Now().UTC(),
	}
	switch r.URL.Path {
	case "/search/repositories":
		w.Header().Set("X-RateLimit-Resource", "search")
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "incomplete_results": false, "items": []any{repo}})
	case "/trending/":
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<h1>Trending</h1><article class="Box-row"><h2><a href="/fixture/radar-workflow">fixture / radar-workflow</a></h2><span class="float-sm-right">42 stars today</span></article>`)
	case "/repositories/" + strconv.Itoa(acceptanceRepoID), "/repos/fixture/radar-workflow":
		tag := fmt.Sprintf(`"repo-%d"`, u.stars.Load())
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			u.notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(repo)
	case "/repos/fixture/radar-workflow/readme":
		version := u.readmeVersion.Load()
		tag := fmt.Sprintf(`"readme-%d"`, version)
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		readme := "# Workflow Toolkit\n\n" + strings.Repeat("The toolkit organizes command-line jobs into groups. Developers can use it to run repeatable workflows and see the status of each step. The documentation contains sample configuration and explains how to start the local runner. ", 4) +
			fmt.Sprintf("\n\nVersion %d. %s", version, acceptanceReadmeMarker)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "sha": fmt.Sprintf("%040x", version), "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString([]byte(readme)), "size": len(readme), "html_url": "https://github.com/fixture/radar-workflow/blob/main/README.md"})
	case "/v1/chat/completions":
		var input struct{ Messages []struct{ Content string } }
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Messages) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Contains(input.Messages[0].Content, "开源技术文档译者") {
			var data struct{ Segments []string }
			if json.Unmarshal([]byte(input.Messages[1].Content), &data) != nil || len(data.Segments) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			u.translateCalls.Add(1)
			translated := make([]string, len(data.Segments))
			for i := range translated {
				translated[i] = "用于组织本地开发任务的工作流工具。"
			}
			answer, _ := json.Marshal(map[string][]string{"t": translated})
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(answer)}}}})
			return
		}
		call := u.modelCalls.Add(1)
		answer, _ := json.Marshal(map[string]any{"source_language": "en",
			"summary_zh": fmt.Sprintf("第%d版。", call) + strings.Repeat("这个项目用于组织本地开发工作流。团队可以配置命令分组、重复运行任务并查看每个步骤的状态。文档提供了配置示例和启动方式。", 4),
			"features":   []string{"脚本分组", "状态输出"}, "audience": []string{"开发团队"}, "value_tags": []string{"自动化"},
			"evidence": []string{"仓库 README"}, "uncertainty": []string{}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(answer)}}},
			"usage": map[string]int{"prompt_tokens": 140, "completion_tokens": 260}})
	default:
		http.NotFound(w, r)
	}
}

type acceptanceRules struct {
	Revision int64
	Items    []settings.Rule
}

func TestAcceptanceBusiness(t *testing.T) {
	upstream := &acceptanceUpstream{}
	upstream.stars.Store(1000)
	upstream.readmeVersion.Store(1)
	fixture := httptest.NewServer(upstream)
	defer fixture.Close()
	path := acceptanceConfig(t, t.TempDir())
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	incomplete := filepath.Join(t.TempDir(), "missing-auth.yaml")
	if err := os.WriteFile(incomplete, []byte("server:\n  site_url: 'http://127.0.0.1'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"server", "-config", incomplete}, logger); err == nil || !strings.Contains(err.Error(), "auth.admin_password") {
		t.Fatal("缺少管理员密码应拒绝启动")
	}
	for range 2 {
		if err := run(t.Context(), []string{"migrate", "-config", path}, logger); err != nil {
			t.Fatal(err)
		}
	}
	initial := startAcceptanceServer(t, path, "AI_BASE_URL="+fixture.URL+"/v1", "AI_MODEL=fixture-model")
	anonymous := initial
	anonymous.admin = ""
	acceptanceJSON[map[string]string](t, anonymous, http.MethodGet, "/healthz", nil, 200)
	acceptanceJSON[map[string]string](t, anonymous, http.MethodGet, "/readyz", nil, 200)
	acceptanceJSON[map[string]string](t, anonymous, http.MethodGet, "/api/v1/setup/status", nil, 401)
	acceptanceJSON[map[string]string](t, initial, http.MethodGet, "/api/v1/repos", nil, 503)
	acceptanceJSON[map[string]bool](t, initial, http.MethodPost, "/api/v1/setup/initialize", nil, 200)
	if err := runBackup(t.Context(), strings.TrimPrefix(initial.base, "http://"), acceptanceAdmin, logger); err == nil || !strings.Contains(err.Error(), "r2_not_configured") {
		t.Fatalf("备份命令应完成密码登录并返回实际的 R2 配置状态: %v", err)
	}
	const rulePath = "/api/v1/system/discovery-rules"
	rules := acceptanceJSON[acceptanceRules](t, initial, http.MethodGet, rulePath, nil, 200)
	rules.Items = []settings.Rule{
		{SourceType: "search", SourceKey: "acceptance", Query: "topic:mcp stars:>=20", Enabled: true, IntervalMinutes: 60, Priority: 40},
		{SourceType: "trending", SourceKey: "all:daily", Enabled: true, IntervalMinutes: 60, Priority: 30},
	}
	acceptanceJSON[acceptanceRules](t, initial, http.MethodPut, rulePath, rules, 200)
	const runtimePath = "/api/v1/system/settings"
	runtime := acceptanceJSON[struct {
		Revision int64
		Settings config.Runtime
	}](t, initial, http.MethodGet, runtimePath, nil, 200)
	runtime.Settings.AI.Summary.AutoDailyCalls = 0
	// 本用例验证人工记录持久化，避免补入的测试历史触发业务上允许的重新入池。
	runtime.Settings.Recommendations.ReentryGrowth = 1000
	acceptanceJSON[map[string]any](t, initial, http.MethodPut, runtimePath, map[string]any{"revision": runtime.Revision, "settings": runtime.Settings}, 200)
	initial.stop()
	if !strings.Contains(initial.logs(), "GitHub 采集待配置") {
		t.Fatal("缺少 GitHub 令牌时应明确提示")
	}
	// 模拟升级后仍带旧 YAML，已经保存的规则和运行参数须保持。
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(file, "discovery:\n  trending_enabled: obsolete\n")
	if closeErr := file.Close(); err != nil || closeErr != nil {
		t.Fatal("写入验收 YAML 失败")
	}

	env := []string{"RADAR_ACCEPTANCE_UPSTREAM=" + fixture.URL, "GITHUB_TOKEN=acceptance-github", "AI_API_KEY=acceptance-model-key"}
	server := startAcceptanceServer(t, path, env...)
	if saved := acceptanceJSON[acceptanceRules](t, server, http.MethodGet, rulePath, nil, 200); len(saved.Items) != 2 {
		t.Fatal("重启未保留已保存的发现规则")
	}
	repoPath := "/api/v1/repos/" + strconv.Itoa(acceptanceRepoID)
	first := waitAcceptanceJSON[acceptanceRepoView](t, server, repoPath, func(view acceptanceRepoView) bool { return view.LastSuccessAt != nil })
	if first.Stars != 1000 || first.Growth24h.Delta != nil || first.Growth24h.Quality != "insufficient" {
		t.Fatal("首次采样应发布累计 Star 且增长未知")
	}
	type trendingPage struct {
		Items []struct {
			ID              int64
			TrendingEntries []struct{ Rank int }
		}
	}
	trending := waitAcceptanceJSON[trendingPage](t, server, "/api/v1/trending?sourceKey=all&period=daily", func(v trendingPage) bool { return len(v.Items) > 0 })
	if len(trending.Items) != 1 || trending.Items[0].ID != acceptanceRepoID || len(trending.Items[0].TrendingEntries) != 1 ||
		trending.Items[0].TrendingEntries[0].Rank != 1 {
		t.Fatal("GitHub Trending 应保留原始名次")
	}
	request := map[string]string{"requestId": "acceptance-refresh-304"}
	jobID := acceptanceJob(t, server, repoPath+"/refresh", request)
	if same := acceptanceJob(t, server, repoPath+"/refresh", request); same != jobID || upstream.notModified.Load() == 0 {
		t.Fatal("条件请求 304 或重复刷新幂等失效")
	}

	acceptanceJob(t, server, repoPath+"/summary", map[string]any{"requestId": "acceptance-summary-first", "force": false})
	summarized := acceptanceJSON[acceptanceRepoView](t, server, http.MethodGet, repoPath, nil, 200)
	if summarized.SummaryZh == nil || summarized.SummaryStatus != "ready" || len([]rune(*summarized.SummaryZh)) < 200 || upstream.modelCalls.Load() != 1 {
		t.Fatal("真实摘要任务未生成足量的中文摘要")
	}
	upstream.stars.Store(1250)
	acceptanceJob(t, server, repoPath+"/refresh", map[string]string{"requestId": "acceptance-refresh-growth"})
	view := acceptanceJSON[acceptanceRepoView](t, server, http.MethodGet, repoPath, nil, 200)
	if view.Stars != 1250 {
		t.Fatal("GitHub Star 更新未对 API 生效")
	}
	cached := acceptanceJSON[map[string]any](t, server, http.MethodPost, repoPath+"/summary", map[string]any{"requestId": "acceptance-summary-cached", "force": false}, 200)
	if cached["cached"] != true || upstream.modelCalls.Load() != 1 {
		t.Fatal("只改变 Star 应复用摘要缓存")
	}
	upstream.readmeVersion.Store(2)
	acceptanceJob(t, server, repoPath+"/summary", map[string]any{"requestId": "acceptance-summary-new", "force": true})
	updated := acceptanceJSON[acceptanceRepoView](t, server, http.MethodGet, repoPath, nil, 200)
	if updated.SummaryZh == nil || *updated.SummaryZh == *summarized.SummaryZh || upstream.modelCalls.Load() != 2 {
		t.Fatal("README 更新后的摘要未变化")
	}
	visitor := server
	visitor.admin = ""
	readmePath := "/api/v1/public/repos/" + strconv.Itoa(acceptanceRepoID) + "/readme"
	type readmeView struct {
		OriginalHTML, ChineseHTML string
		Complete                  bool
	}
	original := acceptanceJSON[readmeView](t, visitor, http.MethodGet, readmePath, nil, 200)
	if !strings.Contains(original.OriginalHTML, "<h1") || original.ChineseHTML != "" || upstream.translateCalls.Load() != 0 {
		t.Fatal("公开读取应展示 README 原文且不触发翻译")
	}
	translationRequest := map[string]string{"requestId": "acceptance-readme-translation"}
	acceptanceJSON[map[string]string](t, visitor, http.MethodPost, repoPath+"/readme/translate", translationRequest, 401)
	acceptanceJob(t, server, repoPath+"/readme/translate", translationRequest)
	translated := acceptanceJSON[readmeView](t, visitor, http.MethodGet, readmePath, nil, 200)
	if !translated.Complete || !strings.Contains(translated.ChineseHTML, "本地开发任务") ||
		translated.OriginalHTML != original.OriginalHTML || upstream.translateCalls.Load() == 0 {
		t.Fatal("已完成译文应与当前 README 原文一起发布")
	}
	acceptanceJSON[map[string]any](t, server, http.MethodPost, repoPath+"/follow", map[string]bool{"follow": true}, 200)
	topic := acceptanceJSON[struct{ ID int64 }](t, server, http.MethodPost, repoPath+"/topic", nil, 200)
	acceptanceJSON[map[string]any](t, server, http.MethodPatch, "/api/v1/topics/"+strconv.FormatInt(topic.ID, 10), map[string]string{"status": "accepted", "feedback": "验收反馈"}, 200)
	server.stop()
	if strings.Contains(server.logs(), acceptanceReadmeMarker) {
		t.Fatal("README 原文标识出现在服务日志")
	}
	t.Log("空库安装、迁移重跑、数据库规则重启、Search 与 Trending、Star 304 与摘要缓存通过")

	database := filepath.Join(filepath.Dir(path), "radar.db")
	seedAcceptanceHistory(t, database)
	var counts [3]int64
	for pass := range 2 {
		if err := run(t.Context(), []string{"maintenance-once", "-config", path}, logger); err != nil {
			t.Fatal(err)
		}
		current := acceptanceHistoryCounts(t, database)
		if pass == 1 && current != counts {
			t.Fatalf("重复聚合改变行数 %v → %v", counts, current)
		}
		counts = current
	}
	if counts[0] != 3 || counts[1] == 0 || counts[2] == 0 {
		t.Fatalf("原始清理或聚合行数不符 %v", counts)
	}
	// 不设置模型密钥仍可读取已有摘要、聚合与选题。
	reopened := startAcceptanceServer(t, path, "RADAR_ACCEPTANCE_UPSTREAM="+fixture.URL, "GITHUB_TOKEN=acceptance-github")
	detail := acceptanceJSON[acceptanceRepoView](t, reopened, http.MethodGet, repoPath, nil, 200)
	if !detail.Followed || detail.Growth24h.Delta == nil || *detail.Growth24h.Delta != 250 || detail.Growth24h.Quality != "exact" {
		t.Fatalf("跟踪状态或 24h 净增长不符，quality=%s", detail.Growth24h.Quality)
	}
	for window, resolution := range map[string]string{"30d": "hourly", "180d": "daily"} {
		trend := acceptanceJSON[struct {
			Resolution string
			Points     []struct{ Stars *int64 }
		}](t, reopened, http.MethodGet, repoPath+"/trend?range="+window, nil, 200)
		hasOldPoint := false
		for _, point := range trend.Points {
			if point.Stars != nil && *point.Stars == 850 {
				hasOldPoint = true
				break
			}
		}
		if trend.Resolution != resolution || !hasOldPoint {
			t.Fatalf("清理后 %s 趋势不可用", window)
		}
	}
	topics := acceptanceJSON[struct {
		Items []struct {
			ID               int64
			Status, Feedback string
		}
	}](t, reopened, http.MethodGet, "/api/v1/topics?status=accepted", nil, 200)
	if len(topics.Items) != 1 || topics.Items[0].ID != topic.ID || topics.Items[0].Feedback != "验收反馈" {
		t.Fatal("选题审核状态未持久化")
	}
	acceptanceJSON[map[string]any](t, reopened, http.MethodPost, repoPath+"/summary", map[string]any{"requestId": "without-model-key", "force": true}, 503)
	if os.Getenv("RADAR_ACCEPTANCE_BROWSER") == "1" {
		acceptanceBrowser(t, reopened, "业务链路")
	}
	reopened.stop()
	for _, suffix := range []string{"", "-wal", "-journal"} {
		data, err := os.ReadFile(database + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(acceptanceReadmeMarker)) {
			t.Fatal("README 原文标识落入数据库或事务文件")
		}
	}
	t.Log("24h 精确 +250、小时与日趋势、聚合重跑、清理、人工反馈和无 AI 密钥降级通过")
}

func acceptanceHistoryCounts(t *testing.T, path string) [3]int64 {
	t.Helper()
	pool, err := storage.OpenBootstrap(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var counts [3]int64
	err = pool.QueryRowContext(t.Context(), `SELECT
	 (SELECT COUNT(*) FROM repo_snapshots),
	 (SELECT COUNT(*) FROM metrics_hourly),
	 (SELECT COUNT(*) FROM metrics_daily)`).Scan(&counts[0], &counts[1], &counts[2])
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func seedAcceptanceHistory(t *testing.T, path string) {
	t.Helper()
	pool, err := storage.OpenBootstrap(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	var samples int64
	if err := db.Model(&storage.RepoSnapshot{}).Where("repo_id=?", acceptanceRepoID).Count(&samples).Error; err != nil || samples == 0 {
		t.Fatal("采集链路未写入实际快照")
	}
	var found int64
	if err := db.Model(&storage.SearchObservation{}).Where("repo_id=?", acceptanceRepoID).Count(&found).Error; err != nil || found == 0 {
		t.Fatal("Search 候选来源未落库")
	}
	if err := db.Delete(&storage.RepoSnapshot{}, "repo_id=?", acceptanceRepoID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&storage.MaintenanceState{}, "key IN ?", []string{maintenance.HourlyKey, maintenance.DailyKey, maintenance.CleanupKey, metrics.ComputeKey}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(15 * time.Minute)
	for days, stars := range map[int]int{15: 850, 7: 900, 1: 1000, 0: 1250} {
		at := now.AddDate(0, 0, -days).UnixMilli()
		if err := db.Create(&storage.RepoSnapshot{RepoID: acceptanceRepoID, SampleSlot: at, ObservedAt: at, Stars: stars, Forks: 5}).Error; err != nil {
			t.Fatal(err)
		}
	}
}
