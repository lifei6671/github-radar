package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcceptanceManualDiscovery(t *testing.T) {
	var moved, allow atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") && !allow.Load() {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4900")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		w.Header().Set("X-RateLimit-Resource", "core")
		id, name, private := 802001, "fixture/low-star", false
		switch r.URL.Path {
		case "/repos/fixture/low-star":
			if moved.Load() {
				http.Redirect(w, r, "/repos/transferred/new-name", http.StatusMovedPermanently)
				return
			}
		case "/repos/transferred/new-name":
			name = "transferred/new-name"
		case "/repos/fixture/private":
			id, name, private = 802002, "fixture/private", true
		default:
			http.NotFound(w, r)
			return
		}
		owner, short, _ := strings.Cut(name, "/")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": short, "full_name": name,
			"owner": map[string]string{"login": owner}, "html_url": "https://github.com/" + name,
			"stargazers_count": 3, "forks_count": 0, "private": private})
	}))
	defer upstream.Close()
	path := acceptanceConfig(t, t.TempDir())
	server := startAcceptanceServer(t, path, "GITHUB_TOKEN="+acceptanceGitHubToken, "RADAR_ACCEPTANCE_UPSTREAM="+upstream.URL)
	acceptanceJSON[map[string]bool](t, server, http.MethodPost, "/api/v1/setup/initialize", nil, 200)
	rulePath := "/api/v1/system/discovery-rules"
	rules := acceptanceJSON[acceptanceRules](t, server, http.MethodGet, rulePath, nil, 200)
	for i := range rules.Items {
		rules.Items[i].Enabled = false
	}
	acceptanceJSON[acceptanceRules](t, server, http.MethodPut, rulePath, rules, 200)
	const endpoint = "/api/v1/repo-submissions"
	type result struct {
		Items []struct {
			Line      int
			JobID     int64
			ErrorCode string
		}
	}
	type batchJobs struct {
		Items []struct {
			ID           int64
			RepoID       int64
			RepoFullName string
			Status       string
			ErrorCode    string
		}
	}
	submit := func(s acceptanceServer, id, repos, source, note string) result {
		t.Helper()
		return acceptanceJSON[result](t, s, http.MethodPost, endpoint,
			map[string]string{"requestId": id, "repositories": repos, "sourceUrl": source, "note": note}, 202)
	}
	completed := func(s acceptanceServer, id string, count int) batchJobs {
		t.Helper()
		return waitAcceptanceJSON(t, s, endpoint+"/"+id, func(j batchJobs) bool {
			if len(j.Items) != count {
				return false
			}
			for _, row := range j.Items {
				if row.Status != "succeeded" && row.Status != "dead" {
					return false
				}
			}
			return true
		})
	}
	visitor := server
	visitor.admin, visitor.session = "", nil
	acceptanceJSON[map[string]string](t, visitor, http.MethodPost, endpoint, nil, 401)
	firstSource := "https://example.test/post/1"
	firstInputs := "https://github.com/Fixture/low-star.git\nfixture/low-star\n\nhttps://example.test/other\nfixture/missing\nfixture/private"
	first := submit(server, "first", firstInputs, firstSource, "发现于社区讨论")
	if len(first.Items) != 5 || first.Items[0].JobID == 0 || first.Items[0].JobID != first.Items[1].JobID ||
		first.Items[2].Line != 4 || first.Items[2].ErrorCode != "invalid_submission_repo" {
		t.Fatalf("逐行校验或重复地址去重失败: %+v", first.Items)
	}
	if retry := submit(server, "first", firstInputs, firstSource, "发现于社区讨论"); retry.Items[0].JobID != first.Items[0].JobID {
		t.Fatal("相同请求 ID 重试创建了新任务")
	}
	conflict := acceptanceJSON[map[string]string](t, server, http.MethodPost, endpoint,
		map[string]string{"requestId": "first", "repositories": firstInputs + "\nfixture/another", "sourceUrl": "https://example.test/different"}, 409)
	if conflict["code"] != "submission_conflict" {
		t.Fatalf("同一批次 ID 的输入冲突未报告: %+v", conflict)
	}
	server.stop()
	allow.Store(true)
	server = startAcceptanceServer(t, path, "GITHUB_TOKEN="+acceptanceGitHubToken, "RADAR_ACCEPTANCE_UPSTREAM="+upstream.URL)
	jobs := completed(server, "first", 3)
	if jobs.Items[0].RepoID != 802001 || jobs.Items[0].Status != "succeeded" || jobs.Items[1].ErrorCode != "github_not_found" || jobs.Items[2].ErrorCode != "github_gone" {
		t.Fatalf("重启后的成功和失败状态不符: %+v", jobs.Items)
	}
	repoPath := "/api/v1/repos/802001"
	type detail struct {
		ID              int64
		FullName        string
		Stars           int
		DiscoveredAt    string
		DiscoveryEvents []struct {
			SourceType      string
			SourceURL       string
			FirstObservedAt string
			Note            string
		}
	}
	before := acceptanceJSON[detail](t, server, http.MethodGet, repoPath, nil, 200)
	if before.Stars != 3 || len(before.DiscoveryEvents) != 1 || before.DiscoveryEvents[0].Note != "发现于社区讨论" ||
		before.DiscoveryEvents[0].SourceType != "manual" || before.DiscoveryEvents[0].SourceURL != firstSource || before.DiscoveryEvents[0].FirstObservedAt == "" {
		t.Fatalf("低 Star 仓库或来源未保存: %+v", before)
	}
	visitor.base = server.base
	acceptanceJSON[map[string]string](t, visitor, http.MethodGet, "/api/v1/public/repos/by-name/fixture/low-star", nil, 404)
	moved.Store(true)
	submit(server, "moved", "fixture/low-star\ntransferred/new-name", "https://example.test/post/2", "转移后的讨论")
	for _, job := range completed(server, "moved", 2).Items {
		if job.RepoID != before.ID || job.RepoFullName != "transferred/new-name" || job.Status != "succeeded" {
			t.Fatalf("转移应保留数字 ID 并更新名称: %+v", job)
		}
	}
	submit(server, "repeat", "transferred/new-name", firstSource, "重复提交不覆盖原备注")
	completed(server, "repeat", 1)
	after := acceptanceJSON[detail](t, server, http.MethodGet, repoPath, nil, 200)
	if after.FullName != "transferred/new-name" || after.DiscoveredAt != before.DiscoveredAt || len(after.DiscoveryEvents) != 2 ||
		after.DiscoveryEvents[1] != before.DiscoveryEvents[0] {
		t.Fatalf("重复来源或仓库首次发现时间变动: %+v", after)
	}
	if followed := acceptanceJSON[map[string]bool](t, server, http.MethodPost, repoPath+"/follow", map[string]bool{"follow": true}, 200); !followed["followed"] {
		t.Fatal("低 Star 仓库未加入重点监控")
	}
	acceptanceJSON[map[string]bool](t, server, http.MethodPatch, repoPath+"/block", map[string]bool{"blocked": true}, 200)
	submit(server, "blocked", "fixture/low-star", "https://example.test/post/3", "")
	if job := completed(server, "blocked", 1).Items[0]; job.Status != "dead" || job.ErrorCode != "repo_blocked" {
		t.Fatalf("屏蔽后通过旧地址提交仍被处理: %+v", job)
	}
	if blocked := acceptanceJSON[detail](t, server, http.MethodGet, repoPath, nil, 200); len(blocked.DiscoveryEvents) != 2 {
		t.Fatalf("屏蔽后来源被修改: %+v", blocked.DiscoveryEvents)
	}
}
