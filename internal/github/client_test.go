package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *gorm.DB, config.GitHub) {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "client.db")
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
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base := server.URL + "/"
	client, err := newClient(db, cfg.GitHub, "fixture-token", &base)
	if err != nil {
		t.Fatal(err)
	}
	return client, db, cfg.GitHub
}

const repositoryJSON = `{"id":1,"owner":{"login":"owner"},"name":"repo","full_name":"owner/repo","html_url":"https://github.com/owner/repo","stargazers_count":10000,"forks_count":12,"topics":["mcp"],"license":{"spdx_id":"NOASSERTION","name":"Other"}}`

func TestRepositoryETagAndRedirect(t *testing.T) {
	var calls atomic.Int32
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("缺少认证或 API 版本")
		}
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4900")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		if r.URL.Path == "/repos/owner/old" {
			http.Redirect(w, r, "/repositories/1", http.StatusMovedPermanently)
			return
		}
		if r.URL.Path == "/repositories/1" && r.Header.Get("If-None-Match") != "" {
			t.Error("改名重定向不应沿用旧 URL 的 ETag")
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(repositoryJSON))
	})
	first, err := client.Repository(t.Context(), "owner", "repo", "", Policy{Sample: true})
	if err != nil || first.Repository.GetStargazersCount() != 10000 || first.ETag != `"v1"` {
		t.Fatalf("200 详情不符: %+v %v", first, err)
	}
	second, err := client.Repository(t.Context(), "owner", "repo", first.ETag, Policy{Sample: true})
	if err != nil || !second.NotModified {
		t.Fatalf("应识别 304: %+v %v", second, err)
	}
	if _, err := client.Repository(t.Context(), "owner", "old", first.ETag, Policy{Sample: true}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("重定向也应计为网络请求，得到 %d", calls.Load())
	}
}

func TestResponseClassificationAndPersistentPause(t *testing.T) {
	cases := []struct {
		name, body, remaining, retry, code string
		status                             int
	}{
		{"404", "{}", "4000", "", "github_not_found", 404},
		{"410", "{}", "4000", "", "github_gone", 410},
		{"permission", `{"message":"Resource not accessible"}`, "4000", "", "github_forbidden", 403},
		{"primary", `{"message":"API rate limit exceeded"}`, "0", "", "github_primary_limit", 403},
		{"secondary", `{"message":"You have exceeded a secondary rate limit."}`, "4000", "120", "github_secondary_limit", 403},
		{"429", "{}", "", "120", "github_secondary_limit", 429},
		{"upstream", "{}", "4000", "", "github_upstream", 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client, db, cfg := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("X-RateLimit-Resource", "core")
				w.Header().Set("X-RateLimit-Limit", "5000")
				w.Header().Set("X-RateLimit-Remaining", tc.remaining)
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			before := time.Now()
			_, err := client.Repository(t.Context(), "owner", "repo", "", Policy{})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("错误分类不符: %v", err)
			}
			if failure.Until > 0 {
				if tc.retry != "" && failure.Until < before.Add(120*time.Second).UnixMilli() {
					t.Fatal("未遵守 Retry-After")
				}
				restarted, err := newClient(db, cfg, "fixture-token", nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = restarted.Repository(t.Context(), "owner", "repo", "", Policy{})
				if !errors.As(err, &failure) || failure.Until <= time.Now().UnixMilli() || calls.Load() != 1 {
					t.Fatalf("重启后应在发请求前遵守持久化限流: calls=%d err=%v", calls.Load(), err)
				}
			}
		})
	}
}

func TestIndependentBudgetsAndSearchPagination(t *testing.T) {
	var calls atomic.Int32
	client, db, cfg := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/search/repositories" {
			w.Header().Set("Link", "<http://example.com/search/repositories?page=2>; rel=\"next\"")
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 80, "incomplete_results": false, "items": []any{}})
			return
		}
		_, _ = w.Write([]byte(repositoryJSON))
	})
	client.limits.cfg.SearchSoftBudgetPerMin = 1
	page, err := client.Search(t.Context(), "topic:mcp", 1)
	if err != nil || page.NextPage != 2 || page.Total != 80 {
		t.Fatalf("分页不符: %+v %v", page, err)
	}
	base := client.sdk.BaseURL()
	cfg.SearchSoftBudgetPerMin = 1
	restarted, err := newClient(db, cfg, "fixture-token", &base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Search(t.Context(), "topic:mcp", 1); err == nil {
		t.Fatal("重建客户端不应重置本地搜索预算")
	}
	if _, err := restarted.Repository(t.Context(), "owner", "repo", "", Policy{Sample: true}); err != nil {
		t.Fatalf("搜索限额不应阻塞 core: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("不应发送超出预算的请求: %d", calls.Load())
	}
}

func TestRedirectBudgetPreservesDeferral(t *testing.T) {
	var calls atomic.Int32
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/repositories/1", http.StatusMovedPermanently)
	})
	client.limits.cfg.CoreSoftBudgetPerHour = 1
	_, err := client.Repository(t.Context(), "owner", "old", "", Policy{})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "github_local_budget" ||
		failure.Until <= time.Now().UnixMilli() || calls.Load() != 1 {
		t.Fatalf("重定向的限额错误必须保留恢复时间: calls=%d err=%v", calls.Load(), err)
	}
}
