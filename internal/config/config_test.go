package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadOverrides(t *testing.T) {
	// 部分 YAML 应覆盖指定字段并保留其他默认值。
	path := filepath.Join(t.TempDir(), "radar.yaml")
	content := "server:\n  listen: '127.0.0.1:8090'\nstorage:\n  wal: false\nai:\n  enabled: false\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:8090" || !cfg.Storage.WAL || !cfg.AI.Enabled {
		t.Fatalf("启动加载不应解析旧业务字段: %+v", cfg)
	}
	cfg, err = cfg.SeedConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:8090" || cfg.Storage.WAL ||
		cfg.Storage.BusyTimeoutMS != 5000 || cfg.Storage.MaxOpenConns != 4 {
		t.Fatalf("YAML 覆盖结果不符: %+v %+v", cfg.Server, cfg.Storage)
	}
	if cfg.AI.Enabled || len(cfg.MissingAISettings()) != 0 {
		t.Fatal("明确关闭 AI 时不应报告缺少 AI 配置")
	}
}

func TestCredentials(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_BASE_URL", "https://example.com/v1")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_MODEL", "example-model")

	if got := cfg.WithRuntime(cfg.SeedRuntime()).MissingAISettings(); !reflect.DeepEqual(got, []string{"AI_API_KEY"}) {
		t.Fatalf("缺失 AI 配置应为 AI_API_KEY，得到 %v", got)
	}
}

func TestDeploymentAuthConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.yaml")
	const password = "测试密码-actual spaces "
	const secret = "test-signing-key-with-at-least-32-bytes"
	for _, tc := range []struct{ site, password, secret, missing string }{
		{"", "", "", "server.site_url"},
		{"https://radar.example.com/team", password, secret, "server.site_url"},
		{"https://radar.example.com", "too short", secret, "auth.admin_password"},
		{"https://radar.example.com", strings.Repeat("x", 513), secret, "auth.admin_password"},
		{"https://radar.example.com", password, "short", "auth.session_secret"},
		{"https://radar.example.com/", password, secret, ""},
	} {
		content := "server:\n  site_url: '" + tc.site + "'\nauth:\n  admin_password: '" + tc.password + "'\n  session_secret: '" + tc.secret + "'\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err == nil {
			err = cfg.ValidateAdmin()
		}
		if tc.missing != "" {
			if err == nil || !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("应指出配置项 %s，得到 %v", tc.missing, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		cfg = cfg.WithRuntime(cfg.SeedRuntime())
		if cfg.Server.SiteURL != "https://radar.example.com" || cfg.Auth.AdminPassword != password || cfg.Auth.SessionSecret != secret {
			t.Fatal("网站源地址应归一化，并在加载数据库配置后保留部署凭据")
		}
		if cfg.RuntimeSettings().Server.SiteURL != "" {
			t.Fatal("网站源地址不应出现在数据库运行设置中")
		}
	}
}

func TestMalformedYAMLDoesNotExposeCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radar.yaml")
	const sensitive = "sensitive-credential-phrase"
	if err := os.WriteFile(path, []byte("auth: '"+sensitive+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "第 1 行") || strings.Contains(err.Error(), sensitive[:7]) {
		t.Fatal("YAML 类型错误应保留行号并隐去凭据")
	}
}
