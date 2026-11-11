package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const acceptanceAdmin = "radar-acceptance-local-admin"
const acceptanceSessionSecret = "radar-acceptance-session-signing-key"

type acceptanceServer struct {
	base    string
	pid     int
	admin   string
	session *http.Cookie
	stop    func()
	running func() bool
	logs    func() string
}

func acceptanceConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "radar.yaml")
	db := filepath.ToSlash(filepath.Join(dir, "radar.db"))
	writeAcceptanceConfig(t, path, "127.0.0.1:0", db, 0o600)
	return path
}

func writeAcceptanceConfig(t *testing.T, path, listen, db string, mode os.FileMode) {
	t.Helper()
	content := fmt.Sprintf("server:\n  listen: %q\n  site_url: 'http://127.0.0.1'\nstorage:\n  path: %q\nauth:\n  admin_password: %q\n  session_secret: %q\n",
		listen, db, acceptanceAdmin, acceptanceSessionSecret)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// 每次启动真实服务子进程，使用系统分配的端口。关闭 stdin 触发与生产相同的优雅停机。
func startAcceptanceServer(t *testing.T, path string, env ...string) acceptanceServer {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestAcceptanceProcess$")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(),
		"RADAR_ACCEPTANCE_CHILD_CONFIG="+path,
		"GITHUB_TOKEN=", "AI_API_KEY=", "AI_BASE_URL=", "AI_MODEL=", "RADAR_ACCEPTANCE_UPSTREAM=")
	cmd.Env = append(cmd.Env, env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	bound := make(chan string, 1)
	done := make(chan error, 1)
	exited := make(chan struct{})
	var logMu sync.Mutex
	var logOutput bytes.Buffer
	go func() {
		defer close(bound)
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			logMu.Lock()
			logOutput.Write(scan.Bytes())
			logOutput.WriteByte('\n')
			logMu.Unlock()
			var line struct {
				Listen  string `json:"listen"`
				Message string `json:"msg"`
			}
			if json.Unmarshal(scan.Bytes(), &line) == nil && line.Message == "HTTP 服务已启动" {
				bound <- line.Listen
			}
		}
		done <- cmd.Wait()
		close(exited)
		close(done)
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = stdin.Close()
		select {
		case err := <-done:
			if err != nil && t.Context().Err() == nil {
				t.Errorf("验收进程退出失败: %v", err)
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("验收进程未在期限内退出")
		}
	}
	t.Cleanup(stop)
	select {
	case listen := <-bound:
		if listen == "" {
			t.Fatal("验收进程未成功启动")
		}
		server := acceptanceServer{base: "http://" + listen, pid: cmd.Process.Pid, stop: stop,
			running: func() bool {
				select {
				case <-exited:
					return false
				default:
					return true
				}
			},
			logs: func() string {
				logMu.Lock()
				defer logMu.Unlock()
				return logOutput.String()
			},
		}
		server.login(t)
		return server
	case <-time.After(15 * time.Second):
		t.Fatal("验收进程启动超时")
	}
	return acceptanceServer{}
}

func (s *acceptanceServer) login(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cookie, err := loginAdmin(t.Context(), client, s.base, acceptanceAdmin)
	if err != nil {
		t.Fatal(err)
	}
	s.admin, s.session = acceptanceAdmin, cookie
}

func TestAcceptanceProcess(t *testing.T) {
	path := os.Getenv("RADAR_ACCEPTANCE_CHILD_CONFIG")
	if path == "" {
		t.Skip("由验收测试启动")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	if raw := os.Getenv("RADAR_ACCEPTANCE_UPSTREAM"); raw != "" {
		target, err := url.Parse(raw)
		if err != nil {
			t.Fatal("上游测试地址无效")
		}
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil
		defer base.CloseIdleConnections()
		http.DefaultTransport = githubAcceptanceTransport{target, base}
	}
	if err := run(ctx, []string{"server", "-config", path}, slog.New(slog.NewJSONHandler(os.Stdout, nil))); err != nil {
		t.Fatal("验收子进程退出异常")
	}
}

// 只将 GitHub 入口导向测试服务，正常 R2 TLS 和生产请求代码不变。
type githubAcceptanceTransport struct {
	target    *url.URL
	transport *http.Transport
}

func (g githubAcceptanceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "api.github.com" || req.URL.Host == "github.com" {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host = g.target.Scheme, g.target.Host
		req.Host = g.target.Host
	}
	return g.transport.RoundTrip(req)
}

func (s acceptanceServer) request(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if s.admin != "" {
		req.AddCookie(s.session)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, data, err
}

func acceptanceJSON[T any](t *testing.T, s acceptanceServer, method, path string, body any, want int) T {
	t.Helper()
	status, data, err := s.request(t.Context(), method, path, body)
	var result T
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if status != want {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &failure)
		t.Fatalf("%s %s: HTTP %d, code=%s, 预期 %d", method, path, status, failure.Code, want)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("%s %s: 响应不是预期的 JSON", method, path)
	}
	return result
}

func waitAcceptance(t *testing.T, timeout time.Duration, description string, check func(context.Context) (bool, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := check(ctx)
		if err != nil {
			t.Fatalf("%s: %v", description, err)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", description, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitAcceptanceJSON[T any](t *testing.T, s acceptanceServer, path string, ok func(T) bool) T {
	t.Helper()
	var result T
	waitAcceptance(t, 25*time.Second, "等待 "+path, func(ctx context.Context) (bool, error) {
		status, data, err := s.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return false, err
		}
		if status == http.StatusNotFound {
			return false, nil
		}
		if status != http.StatusOK || json.Unmarshal(data, &result) != nil {
			return false, fmt.Errorf("状态或格式异常 HTTP %d", status)
		}
		return ok(result), nil
	})
	return result
}

func acceptanceJob(t *testing.T, s acceptanceServer, path string, body any) int64 {
	t.Helper()
	result := acceptanceJSON[struct {
		JobID     int64
		StatusURL string
	}](t, s, http.MethodPost, path, body, http.StatusAccepted)
	if result.JobID <= 0 || !strings.HasPrefix(result.StatusURL, "/api/v1/jobs/") {
		t.Fatal("任务未受理")
	}
	job := waitAcceptanceJSON[struct {
		Status    string
		ErrorCode *string
	}](t, s, result.StatusURL, func(j struct {
		Status    string
		ErrorCode *string
	}) bool {
		return j.Status != "pending" && j.Status != "running"
	})
	if job.Status != "succeeded" {
		t.Fatalf("任务 %d 状态 %s, error=%v", result.JobID, job.Status, job.ErrorCode)
	}
	return result.JobID
}

func acceptanceBrowser(t *testing.T, s acceptanceServer, pattern string, env ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "node_modules/@playwright/test/cli.js", "test", "--config", "playwright.acceptance.config.ts", "-g", pattern)
	cmd.Dir = filepath.Join("..", "..", "web")
	cmd.Env = append(os.Environ(), "RADAR_ACCEPTANCE_ORIGIN="+s.base, "RADAR_ACCEPTANCE_ADMIN="+s.admin,
		"RADAR_ACCEPTANCE_RESULTS_DIR="+t.TempDir())
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	// 失败日志也需掩去可能来自浏览器表单操作的凭据。
	message := string(out)
	for _, value := range env {
		name, secret, _ := strings.Cut(value, "=")
		if secret != "" && (strings.Contains(name, "ACCESS_KEY") || strings.Contains(name, "SECRET")) {
			message = strings.ReplaceAll(message, secret, "[已隐藏]")
		}
	}
	if err != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("浏览器验收失败: %v\n%s", err, message)
	}
	if err != nil {
		t.Fatal("浏览器验收超时")
	}
	t.Log(strings.TrimSpace(message))
}
