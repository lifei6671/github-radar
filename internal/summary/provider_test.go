package summary

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lifei6671/github-radar/internal/config"
)

func TestChatProviderSingleRequestAndSafeFailure(t *testing.T) {
	var count, code atomic.Int32
	code.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("请求契约错误")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["tools"] != nil ||
			body["response_format"].(map[string]any)["type"] != "json_object" ||
			body["thinking"] != nil || body["max_tokens"] != nil ||
			body["max_completion_tokens"] != float64(config.DefaultSummary().OutputTokens) ||
			len(body["messages"].([]any)) != 2 {
			t.Error("模型只应收到无工具的 JSON 模式请求")
		}
		w.Header().Set("X-Request-Id", "request_fixture")
		w.WriteHeader(int(code.Load()))
		if code.Load() == http.StatusOK {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":20,"completion_tokens":8}}`))
		} else {
			_, _ = w.Write([]byte(marker))
		}
	}))
	defer server.Close()
	p := NewChatProvider(config.AI{BaseURL: server.URL + "/v1", Model: "fixture", Summary: config.DefaultSummary()}, "fixture")
	ok := p.Generate(t.Context(), "public excerpt")
	if ok.Content != "{}" || ok.RequestID != "request_fixture" || ok.InputTokens == nil || *ok.InputTokens != 20 {
		t.Fatalf("正常结构化响应错误: %+v", ok)
	}
	code.Store(http.StatusTooManyRequests)
	failed := p.Generate(t.Context(), "public excerpt")
	if failed.Code != "ai_rate_limited" || !failed.Retry || failed.Content != "" || failed.Unknown {
		t.Fatalf("明确拒绝应交给队列重试: %+v", failed)
	}
	code.Store(http.StatusGatewayTimeout)
	unknown := p.Generate(t.Context(), "public excerpt")
	if !unknown.Unknown || unknown.Retry || unknown.Content != "" || count.Load() != 3 {
		t.Fatalf("未知结果不可自动重发: %+v", unknown)
	}
}

type requestTransport func(*http.Request) (*http.Response, error)

func (f requestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeepSeekFlashUsesNonThinkingJSONMode(t *testing.T) {
	cfg := config.AI{BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-flash", Summary: config.DefaultSummary()}
	p := NewChatProvider(cfg, "fixture")
	p.http.Transport = requestTransport(func(r *http.Request) (*http.Response, error) {
		var body struct {
			MaxTokens     int               `json:"max_tokens"`
			MaxCompletion *int              `json:"max_completion_tokens"`
			Thinking      map[string]string `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.MaxTokens != cfg.Summary.OutputTokens || body.MaxCompletion != nil || body.Thinking["type"] != "disabled" {
			t.Errorf("DeepSeek Flash 请求参数错误: %+v", body)
		}
		data := `{"choices":[{"message":{"content":"   "}}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(data))}, nil
	})
	result := p.Generate(t.Context(), "public excerpt")
	if result.Code != "invalid_output" || !result.Retry {
		t.Fatalf("空模型响应应进入原有重试流程: %+v", result)
	}
}
