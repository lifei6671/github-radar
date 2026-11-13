package embedding

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/config"
)

func TestSearchVectorSharedAcrossCanceledRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	cfg := config.DefaultEmbedding()
	cfg.Enabled, cfg.BaseURL, cfg.Model = true, server.URL, "fixture"
	vectors := NewQueryVectors(cfg, "fixture", aibudget.ForConfig(testDB(t), config.DefaultSummary()))
	firstContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, second := make(chan QueryVector, 1), make(chan QueryVector, 1)
	go func() { first <- vectors.Vector(firstContext, "查询词") }()
	select {
	case <-started:
	case <-time.After(queryTimeout):
		t.Fatal("模型调用没有启动")
	}
	// 用户切换筛选时首个 HTTP 请求会被取消，第二个同词请求仍可得到向量。
	cancel()
	go func() { second <- vectors.Vector(t.Context(), "查询词") }()
	close(release)
	a, b := <-first, <-second
	if a.Key == "" || a.Key != b.Key || calls.Load() != 1 {
		t.Fatal("同词搜索应共享同一个有界模型请求")
	}
}
