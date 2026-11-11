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
	t.Setenv("ADMIN_TOKEN", "")
	t.Setenv("AI_BASE_URL", "https://example.com/v1")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_MODEL", "example-model")

	if _, err := cfg.AdminToken(); err == nil || !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Fatalf("应明确报告缺少 ADMIN_TOKEN，得到 %v", err)
	}
	if got := cfg.WithRuntime(cfg.SeedRuntime()).MissingAISettings(); !reflect.DeepEqual(got, []string{"AI_API_KEY"}) {
		t.Fatalf("缺失 AI 配置应为 AI_API_KEY，得到 %v", got)
	}
	t.Setenv("ADMIN_TOKEN", "test-token")
	if got, err := cfg.AdminToken(); err != nil || got != "test-token" {
		t.Fatalf("应读取管理员令牌，得到错误 %v", err)
	}
}
