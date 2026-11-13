package trending

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// 经过 URL 编码后，C# 和 C++ 都应完整到达 GitHub 的语言路径。
func TestFetchLanguagePath(t *testing.T) {
	for _, tt := range []struct{ language, escaped string }{{"c#", "c%23"}, {"c++", "c++"}} {
		t.Run(tt.language, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/trending/"+tt.language || r.URL.EscapedPath() != "/trending/"+tt.escaped || r.URL.Query().Get("since") != "weekly" {
					http.Error(w, "语言路径或周期有误", http.StatusBadRequest)
					return
				}
				http.ServeFile(w, r, "testdata/repositories.html")
			}))
			defer server.Close()
			client := New(time.Second)
			client.BaseURL = server.URL
			entries, err := client.Fetch(t.Context(), tt.language, "weekly")
			if err != nil || len(entries) != 2 || entries[0].Repository.GetID() != 101 {
				t.Fatalf("特殊语言路径未返回榜单: %v %v", entries, err)
			}
		})
	}
}

func TestOversizedPageCannotBecomePartialSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<h1>Trending</h1><article class="Box-row"><h2><a href="/owner/repo">repo</a></h2></article>`)
		_, _ = io.WriteString(w, strings.Repeat(" ", 4<<20))
	}))
	defer server.Close()
	client := New(time.Second)
	client.BaseURL = server.URL
	if _, err := client.Fetch(t.Context(), "", "daily"); err == nil {
		t.Fatal("超限页面不能被解析成部分成功榜单")
	}
}

func TestParseRepositories(t *testing.T) {
	file, err := os.Open("testdata/repositories.html")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	entries, err := Parse(file)
	if err != nil || len(entries) != 2 {
		t.Fatalf("榜单解析失败: %d %v", len(entries), err)
	}
	first := entries[0]
	if first.Repository.GetID() != 101 || first.Repository.GetFullName() != "owner/alpha" ||
		first.Repository.GetStargazersCount() != 12345 || first.ReportedStars == nil || *first.ReportedStars != 1234 || first.Rank != 1 {
		t.Fatalf("累计 Star 和榜单增量解析不符: %+v", first)
	}
}

func TestParseEmptyAndChangedMarkup(t *testing.T) {
	// 只有明确的空榜提示可以生成成功空批次。
	empty := `<h1>Trending</h1><h2 class="blankslate-heading">It looks like we don’t have any trending repositories for Brainfuck.</h2>`
	if entries, err := Parse(strings.NewReader(empty)); err != nil || len(entries) != 0 {
		t.Fatalf("明确空榜不应失败: %v", err)
	}
	for _, html := range []string{
		"<h1>Sign in</h1>",
		"<h1>Trending</h1><div>Unexpected markup</div>",
		`<h1>Trending</h1><article class="Box-row"><h2><a href="/login">Login</a></h2></article>`,
	} {
		if _, err := Parse(strings.NewReader(html)); err == nil {
			t.Fatal("页面结构变化不能被当成空榜")
		}
	}
}
