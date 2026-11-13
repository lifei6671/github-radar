package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestAcceptanceRuntimeReload(t *testing.T) {
	upstream := &acceptanceUpstream{}
	upstream.stars.Store(1000)
	upstream.readmeVersion.Store(1)
	models := make(chan string, 4)
	release := make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	var searches atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/repositories" {
			searches.Add(1)
		}
		if r.URL.Path == "/v1/chat/completions" {
			body, err := io.ReadAll(r.Body)
			var input struct{ Model string }
			if err != nil || json.Unmarshal(body, &input) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			select {
			case models <- input.Model:
			case <-r.Context().Done():
				return
			}
			if input.Model == "model-before-save" {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
		}
		upstream.ServeHTTP(w, r)
	}))
	defer fixture.Close()
	defer finish()

	dir := t.TempDir()
	server := startAcceptanceServer(t, acceptanceConfig(t, dir), "RADAR_ACCEPTANCE_UPSTREAM="+fixture.URL,
		"RADAR_TEST_LIVE_TOKEN="+acceptanceGitHubToken, "AI_API_KEY="+acceptanceAIKey)
	acceptanceJSON[map[string]bool](t, server, http.MethodPost, "/api/v1/setup/initialize", nil, 200)
	// 隔离手动摘要，避免模型变更后的自动来源复查占用同一个仓库任务槽。
	pool, err := storage.OpenBootstrap(t.Context(), filepath.Join(dir, "radar.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.ExecContext(t.Context(), "UPDATE system_settings SET value_json='true' WHERE key=?", storage.AutoAIResumeKey)
	if err == nil {
		_, err = pool.ExecContext(t.Context(), `INSERT INTO report_editorials
		(kind,issue_key,publication_status,coverage_days,fingerprint,headline,lead,stories_json,repo_ids_json,source_issues,observed_from,observed_to,generated_at) VALUES
		('weekly','2020-W46','preview',1,'fixture-week','周报采集预览','正文','[]','[]',1,1,2,?),
		('monthly','2020-11','preview',1,'fixture-month','月报采集预览','正文','[]','[]',1,1,2,?)`,
			time.Now().UnixMilli(), time.Now().UnixMilli())
	}
	closeErr := pool.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("暂停测试库自动 AI：%v，关闭连接：%v", err, closeErr)
	}
	const rulePath = "/api/v1/system/discovery-rules"
	rules := acceptanceJSON[acceptanceRules](t, server, http.MethodGet, rulePath, nil, 200)
	rules.Items = []settings.Rule{{SourceType: "search", SourceKey: "hot-reload", Query: "topic:mcp", MinStars: new(20), Enabled: true, IntervalMinutes: 60, Priority: 40}}
	rules = acceptanceJSON[acceptanceRules](t, server, http.MethodPut, rulePath, rules, 200)
	const runtimePath = "/api/v1/system/settings"
	const backupStatusPath = "/api/v1/system/backup/status"
	type runtimeReply struct {
		Revision        int64          `json:"revision"`
		Settings        config.Runtime `json:"settings"`
		RestartRequired bool           `json:"restartRequired"`
	}
	save := func(current runtimeReply) runtimeReply {
		t.Helper()
		return acceptanceJSON[runtimeReply](t, server, http.MethodPut, runtimePath,
			map[string]any{"revision": current.Revision, "settings": current.Settings}, 200)
	}
	current := acceptanceJSON[runtimeReply](t, server, http.MethodGet, runtimePath, nil, 200)
	current.Settings.GitHub.TokenEnv = "RADAR_TEST_LIVE_TOKEN"
	current.Settings.GitHub.SearchSoftBudgetPerMin = 1
	current.Settings.Scheduler.TickMinutes = 10
	current.Settings.Scheduler.MetricsIntervalMinutes = 20
	current.Settings.ReportPublication = config.ReportPublication{WeeklyMinDays: 4, MonthlyMinCoverage: 80}
	current.Settings.AI.BaseURL = fixture.URL + "/v1"
	current.Settings.AI.Model = "model-before-save"
	current.Settings.AI.Summary.AutoDailyCalls = 0
	if saved := save(current); saved.RestartRequired {
		t.Fatal("只保存业务配置应在同进程生效")
	}
	for kind, days := range map[string]int{"weekly": 4, "monthly": 24} {
		reply := acceptanceJSON[struct{ Report struct{ RequiredDays int } }](t, server, http.MethodGet,
			"/api/v1/public/daily-reports?kind="+kind, nil, 200)
		if reply.Report.RequiredDays != days || !server.running() {
			t.Fatalf("同进程的 %s 门槛应立即更新为 %d 天，实际 %d", kind, days, reply.Report.RequiredDays)
		}
	}
	repoPath := "/api/v1/repos/" + strconv.Itoa(acceptanceRepoID)
	waitAcceptanceJSON[acceptanceRepoView](t, server, repoPath, func(view acceptanceRepoView) bool { return view.LastSuccessAt != nil })
	if searches.Load() != 1 {
		t.Fatal("第一条搜索规则应已使用本地每分钟一次的预算")
	}
	job := acceptanceJSON[struct{ StatusURL string }](t, server, http.MethodPost, repoPath+"/summary",
		map[string]any{"requestId": "model-before-reload", "force": true}, 202)
	select {
	case got := <-models:
		if got != "model-before-save" {
			t.Fatalf("第一笔调用的模型为 %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("第一笔模型请求未开始")
	}

	rules = acceptanceJSON[acceptanceRules](t, server, http.MethodGet, rulePath, nil, 200)
	rules.Items = append(rules.Items, settings.Rule{SourceType: "search", SourceKey: "hot-reload-next", Query: "topic:ai", MinStars: new(20), Enabled: true, IntervalMinutes: 60, Priority: 40})
	acceptanceJSON[acceptanceRules](t, server, http.MethodPut, rulePath, rules, 200)
	current = acceptanceJSON[runtimeReply](t, server, http.MethodGet, runtimePath, nil, 200)
	current.Settings.GitHub.SearchSoftBudgetPerMin = 3
	current.Settings.AI.Model = "model-after-save"
	current.Settings.AI.Summary.ManualDailyCalls = 17
	current.Settings.Server.ReadTimeoutSeconds++
	current.Settings.Storage.BusyTimeoutMS++
	if saved := save(current); !saved.RestartRequired {
		t.Fatal("HTTP 或 SQLite 修改应提示重启")
	}
	type restartStatus struct{ RestartRequired bool }
	if !acceptanceJSON[restartStatus](t, server, http.MethodGet, backupStatusPath, nil, 200).RestartRequired {
		t.Fatal("备份状态应同步显示待重启配置")
	}
	limits := acceptanceJSON[struct {
		GitHubConfigured bool
		AI               struct{ Manual struct{ Limit int } }
	}](t, server, http.MethodGet, "/api/v1/system/limits", nil, 200)
	if !limits.GitHubConfigured || limits.AI.Manual.Limit != 17 || !server.running() {
		t.Fatal("同一进程的 API 应立即读取新配置")
	}
	finish()
	type jobStatus struct{ Status string }
	first := waitAcceptanceJSON(t, server, job.StatusURL, func(j jobStatus) bool { return j.Status != "pending" && j.Status != "running" })
	if first.Status != "succeeded" {
		t.Fatalf("保存前已发出的模型请求应正常完成，实际 %s", first.Status)
	}
	acceptanceJob(t, server, repoPath+"/summary", map[string]any{"requestId": "model-after-reload", "force": true})
	select {
	case got := <-models:
		if got != "model-after-save" || upstream.modelCalls.Load() != 2 {
			t.Fatalf("下一笔应只调用新模型，实际 %q，调用次数 %d", got, upstream.modelCalls.Load())
		}
	default:
		t.Fatal("第二笔模型请求未到达供应商")
	}
	waitAcceptance(t, 5*time.Second, "新 Search 预算下执行第二条搜索规则", func(_ context.Context) (bool, error) {
		return searches.Load() == 2, nil
	})
	current = acceptanceJSON[runtimeReply](t, server, http.MethodGet, runtimePath, nil, 200)
	current.Settings.Server.ReadTimeoutSeconds--
	current.Settings.Storage.BusyTimeoutMS--
	if save(current).RestartRequired || acceptanceJSON[restartStatus](t, server, http.MethodGet, backupStatusPath, nil, 200).RestartRequired {
		t.Fatal("改回启动值后应清除待重启状态")
	}
}
