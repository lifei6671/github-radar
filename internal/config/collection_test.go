package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCategoriesAndCapacityValidation(t *testing.T) {
	categories, err := LoadCategories("")
	if err != nil {
		t.Fatal(err)
	}
	if got := Classify(categories, []string{"MCP", "tts"}); !slices.Equal(got, []string{"mcp", "media"}) {
		t.Fatalf("分类应大小写归一化并允许多重匹配: %v", got)
	}
	if got := Classify(categories, []string{"unmatched"}); !slices.Equal(got, []string{"other"}) {
		t.Fatalf("未匹配主题应归入 other: %v", got)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("github:\n  repo_soft_budget_per_hour: 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = cfg.SeedConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SeedRuntime().Validate(); err == nil {
		t.Fatal("配置容量所需请求超过采集预算时必须明确报错")
	}
	if err := os.WriteFile(path, []byte("discovery:\n  trending_enabled: false\n  search_rules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfg.SeedConfig()
	if err != nil || cfg.Discovery.TrendingEnabled || len(cfg.Discovery.SearchRules) != 0 {
		t.Fatalf("应允许明确关闭全部发现源: %+v %v", cfg.Discovery, err)
	}
}
