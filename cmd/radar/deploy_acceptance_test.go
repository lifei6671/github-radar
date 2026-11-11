package main

import (
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/backup"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
)

func TestAcceptanceCompose(t *testing.T) {
	if os.Getenv("RADAR_ACCEPTANCE_DOCKER") != "1" {
		t.Skip("设置 RADAR_ACCEPTANCE_DOCKER=1 运行隔离的 Compose 验收")
	}
	var connection *backup.Connection
	if os.Getenv("RADAR_ACCEPTANCE_R2_FILE") != "" || os.Getenv("RADAR_ACCEPTANCE_R2_ENDPOINT") != "" {
		// 先登记云端清理。测试退出时须等容器退出，再确认 R2 前缀为空。
		conn, _ := openLiveR2(t)
		connection = &conn
	}
	project := "radar-acceptance-" + strings.ToLower(rand.Text())
	name := project + "-radar"
	compose := []string{"compose", "--project-name", project, "--file", "deploy/compose.yaml"}
	docker := func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = append(os.Environ(), "ADMIN_TOKEN="+acceptanceAdmin, "GITHUB_TOKEN=", "AI_API_KEY=", "AI_BASE_URL=", "AI_MODEL=")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	command := func(args ...string) string {
		t.Helper()
		out, err := docker(t.Context(), args...)
		if err != nil {
			t.Fatalf("Docker 命令失败: %v\n%s", err, out)
		}
		return out
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
		defer cancel()
		out, err := docker(ctx, "stop", "--time", "30", name)
		if err != nil && !strings.Contains(out, "No such container") {
			t.Errorf("停止本次 Compose 容器失败: %s", out)
		}
		out, err = docker(ctx, "rm", "--force", name)
		if err != nil && !strings.Contains(out, "No such container") {
			t.Errorf("移除本次 Compose 容器失败: %s", out)
		}
		out, err = docker(ctx, append(compose, "down", "--remove-orphans", "--volumes", "--rmi", "local")...)
		if err != nil {
			t.Errorf("清理本次 Compose 项目失败: %s", out)
		}
	})
	command(append(compose, "config", "--quiet")...)
	launch := func(build bool) (string, acceptanceServer) {
		t.Helper()
		args := append(compose, "run", "--no-deps", "--detach", "--no-TTY", "--publish", "127.0.0.1::8080")
		if build {
			args = append(args, "--build")
		}
		command(append(args, "--name", name, "radar")...)
		id := command("inspect", "--format", "{{.Id}}", name)
		binding := command("port", id, "8080/tcp")
		host, _, err := net.SplitHostPort(binding)
		if err != nil || host != "127.0.0.1" {
			t.Fatal("Compose 验收服务未绑定到本机回环地址")
		}
		s := acceptanceServer{base: "http://" + binding, admin: acceptanceAdmin}
		waitAcceptance(t, 20*time.Second, "容器 SQLite 就绪", func(ctx context.Context) (bool, error) {
			code, _, err := s.request(ctx, http.MethodGet, "/readyz", nil)
			return err == nil && code == 200, nil
		})
		return id, s
	}

	first, server := launch(true)
	if user := command("inspect", "--format", "{{.Config.User}}", first); user != "10001:10001" {
		t.Fatal("运行镜像未使用项目指定的非 root 用户")
	}
	mount := command("inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Type}} {{.Name}}{{end}}{{end}}`, first)
	if mount != "volume "+project+"_radar-data" {
		t.Fatalf("SQLite 未挂载独立持久卷: %q", mount)
	}
	command("exec", first, "sh", "-c", "! command -v node >/dev/null && test -x /usr/local/bin/radar")
	mode := command("exec", first, "stat", "-c", "%a", "/app/data/github-radar.db")
	if mode != "600" {
		t.Fatal("容器内 SQLite 文件权限应为 0600")
	}
	anonymous := server
	anonymous.admin = ""
	acceptanceJSON[map[string]string](t, anonymous, http.MethodGet, "/api/v1/setup/status", nil, 401)
	status, body, err := server.request(t.Context(), http.MethodGet, "/api/v1/route-does-not-exist", nil)
	if err != nil || status != 404 || strings.Contains(strings.ToLower(string(body)), "<!doctype") {
		t.Fatal("未知 API 不应回退到 SPA")
	}
	acceptanceBrowser(t, server, "Compose 首次")
	out, err := docker(t.Context(), "exec", first, "/usr/local/bin/radar", "backup", "-config", "/app/config.yaml")
	if err == nil || !strings.Contains(out, "r2_not_configured") {
		t.Fatal("容器中的 backup 命令未访问本机备份接口")
	}

	const categoryPath = "/api/v1/system/categories"
	categories := acceptanceJSON[struct {
		Revision int64
		Items    []config.Category
	}](t, server, http.MethodGet, categoryPath, nil, 200)
	categories.Items = []config.Category{{Slug: "acceptance", Name: "验收专用主题", AnyTopics: []string{"mcp"}}}
	acceptanceJSON[map[string]any](t, server, http.MethodPut, categoryPath, map[string]any{"revision": categories.Revision, "items": categories.Items}, 200)
	const rulesPath = "/api/v1/system/discovery-rules"
	rules := acceptanceJSON[acceptanceRules](t, server, http.MethodGet, rulesPath, nil, 200)
	rules.Items = []settings.Rule{}
	acceptanceJSON[acceptanceRules](t, server, http.MethodPut, rulesPath, rules, 200)
	const backupPath = "/api/v1/system/backup/settings"
	plan := backup.SettingsUpdate{Schedule: backup.Schedule{Hour: 5, Minute: 15, Timezone: "Asia/Shanghai"}, RetentionDays: 7}
	acceptanceJSON[backup.SettingsView](t, server, http.MethodPut, backupPath, plan, 200)
	command("stop", first)
	command("rm", first)
	second, recreated := launch(false)
	if second == first {
		t.Fatal("应以新容器验证持久卷")
	}
	state := acceptanceJSON[map[string]bool](t, recreated, http.MethodGet, "/api/v1/setup/status", nil, 200)
	saved := acceptanceJSON[backup.SettingsView](t, recreated, http.MethodGet, backupPath, nil, 200)
	currentRules := acceptanceJSON[acceptanceRules](t, recreated, http.MethodGet, rulesPath, nil, 200)
	if !state["initialized"] || saved.RetentionDays != 7 || saved.Schedule != plan.Schedule || currentRules.Items == nil || len(currentRules.Items) != 0 {
		t.Fatal("Compose 重建后初始化标记、备份计划或空规则未持久化")
	}
	acceptanceBrowser(t, recreated, "Compose 重建")
	if actual := command("inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}`, second); actual != project+"_radar-data" {
		t.Fatalf("重建未使用原持久卷: %s", actual)
	}
	t.Log("Compose 实际构建、回环部署、非 root 单 Go 服务、浏览器深链接和容器重建持久化通过")
	if connection == nil {
		return
	}
	plan.R2 = connection
	acceptanceJSON[backup.SettingsView](t, recreated, http.MethodPut, backupPath, plan, 200)
	acceptanceBackup(t, recreated)
	backupStatusPath := "/api/v1/system/backup/status"
	stateBefore := acceptanceJSON[backup.Status](t, recreated, http.MethodGet, backupStatusPath, nil, 200)
	if stateBefore.LastBackup == nil {
		t.Fatal("容器未上传真实 R2 备份")
	}
	pid := command("inspect", "--format", "{{.State.Pid}}", second)
	acceptanceBrowser(t, recreated, "云端后台恢复", "RADAR_ACCEPTANCE_BACKUP_DATE="+stateBefore.LastBackup.Date)
	stateAfter := acceptanceJSON[backup.Status](t, recreated, http.MethodGet, backupStatusPath, nil, 200)
	if pid == "0" || command("inspect", "--format", "{{.State.Pid}}", second) != pid || stateAfter.LastRestore == nil ||
		stateAfter.LastRestore.Status != "completed" || !stateAfter.LastRestore.DatabaseApplied {
		t.Fatal("容器中的真实 R2 恢复未在原 Go 进程完成")
	}
	t.Log("Compose 运行镜像完成真实 R2 上传与后台恢复，容器 Go PID 保持")
}
