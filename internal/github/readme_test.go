package github

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadmeBoundedBlobAndConditionalRequest(t *testing.T) {
	sha := strings.Repeat("a", 40)
	body := strings.Repeat("example body\n", 1000)
	var large atomic.Bool
	client, _, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/readme":
			if r.Header.Get("Accept") != "application/vnd.github.object+json" {
				t.Error("缺少对象媒体类型")
			}
			if r.Header.Get("If-None-Match") == `"test"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"test"`)
			encoding, content := "base64", base64.StdEncoding.EncodeToString([]byte(body))
			if large.Load() {
				encoding, content = "none", ""
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "sha": sha, "size": len(body), "encoding": encoding,
				"content": content, "html_url": "https://github.com/owner/repo/blob/main/README.md"})
		case "/repos/owner/repo/git/blobs/" + sha:
			if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				t.Error("blob 应按原始媒体类型读取")
			}
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	})
	const limit = 1200
	for _, isLarge := range []bool{false, true} {
		large.Store(isLarge)
		result, err := client.Readme(t.Context(), "owner", "repo", "", limit, Policy{})
		if err != nil || string(result.Body) != body[:limit] || !result.Truncated || result.SHA != sha || result.ETag != `"test"` {
			t.Fatalf("README 限量或元数据错误 (large=%v): bytes=%d err=%v", isLarge, len(result.Body), err)
		}
	}
	modified, err := client.Readme(t.Context(), "owner", "repo", `"test"`, limit, Policy{})
	if err != nil || !modified.NotModified || len(modified.Body) != 0 {
		t.Fatal("304 不能返回正文")
	}
	missing, err := client.Readme(t.Context(), "owner", "missing", "", limit, Policy{})
	if err != nil || !missing.Missing {
		t.Fatal("没有 README 时应允许 description 回退")
	}
	policy := Policy{ReadmeBudget: 1}
	if _, err := client.Readme(t.Context(), "owner", "repo", `"test"`, limit, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Readme(t.Context(), "owner", "repo", `"test"`, limit, policy); err == nil {
		t.Fatal("摘要子预算耗尽后应暂停")
	}
	if _, err := client.Readme(t.Context(), "owner", "repo", `"test"`, limit, Policy{Sample: true}); err != nil {
		t.Fatalf("摘要子预算不应耗尽采样子预算: %v", err)
	}
}
