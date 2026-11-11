package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// GitHub 控制 API 版本、上游超时和各资源的本地软预算。
type GitHub struct {
	TokenEnv               string `yaml:"token_env" json:"tokenEnv"`
	APIVersion             string `yaml:"api_version" json:"apiVersion"`
	TimeoutSeconds         int    `yaml:"timeout_seconds" json:"timeoutSeconds"`
	CoreSoftBudgetPerHour  int    `yaml:"core_soft_budget_per_hour" json:"coreSoftBudgetPerHour"`
	RepoSoftBudgetPerHour  int    `yaml:"repo_soft_budget_per_hour" json:"repoSoftBudgetPerHour"`
	CoreReserveRemaining   int    `yaml:"core_reserve_remaining" json:"coreReserveRemaining"`
	SearchSoftBudgetPerMin int    `yaml:"search_soft_budget_per_minute" json:"searchSoftBudgetPerMinute"`
	WorkerConcurrency      int    `yaml:"worker_concurrency" json:"workerConcurrency"`
}

// Scheduler 定义扫描、租约、四档容量与采样周期。
type Scheduler struct {
	TickMinutes          int            `yaml:"tick_minutes" json:"tickMinutes"`
	TrendingDailyMinutes int            `yaml:"trending_daily_minutes" json:"-"`
	TrendingOtherMinutes int            `yaml:"trending_other_minutes" json:"-"`
	SearchRoundMinutes   int            `yaml:"search_round_minutes" json:"-"`
	MaxHot               int            `yaml:"max_hot" json:"maxHot"`
	MaxActive            int            `yaml:"max_active" json:"maxActive"`
	MaxNormal            int            `yaml:"max_normal" json:"maxNormal"`
	MaxMonitoredRepos    int            `yaml:"max_monitored_repos" json:"maxMonitoredRepos"`
	IntervalsMinutes     map[string]int `yaml:"intervals_minutes" json:"intervalsMinutes"`
	LeaseSeconds         int            `yaml:"lease_seconds" json:"leaseSeconds"`
}

func (s Scheduler) Interval(grade string) time.Duration {
	return time.Duration(s.IntervalsMinutes[grade]) * time.Minute
}

// Discovery 配置 Trending 来源和轮换执行的 Search 查询。
type Discovery struct {
	TrendingEnabled   bool         `yaml:"trending_enabled"`
	TrendingLanguages []string     `yaml:"trending_languages"`
	SearchRules       []SearchRule `yaml:"search_rules"`
}

type SearchRule struct {
	Key      string `yaml:"key"`
	Query    string `yaml:"query"`
	Enabled  bool   `yaml:"enabled"`
	Priority int    `yaml:"priority"`
}

func collectionDefaults() (GitHub, Scheduler, Discovery) {
	github := GitHub{
		TokenEnv: "GITHUB_TOKEN", APIVersion: "2026-03-10", TimeoutSeconds: 15,
		CoreSoftBudgetPerHour: 2500, RepoSoftBudgetPerHour: 2100, CoreReserveRemaining: 300,
		SearchSoftBudgetPerMin: 6, WorkerConcurrency: 1,
	}
	scheduler := Scheduler{
		TickMinutes: 5, TrendingDailyMinutes: 60, TrendingOtherMinutes: 360,
		SearchRoundMinutes: 30, MaxHot: 200, MaxActive: 800, MaxNormal: 2000, MaxMonitoredRepos: 5000,
		IntervalsMinutes: map[string]int{"S": 15, "A": 60, "B": 360, "C": 1440}, LeaseSeconds: 180,
	}
	discovery := Discovery{
		TrendingEnabled: true, TrendingLanguages: []string{"go", "python", "typescript", "rust"},
	}
	for _, topic := range []string{"ai-agent", "mcp", "agent-skill", "coding-agent", "browser-automation", "llm", "rag", "tts"} {
		discovery.SearchRules = append(discovery.SearchRules, SearchRule{
			Key: topic, Query: "topic:" + topic, Enabled: true, Priority: 20,
		})
	}
	return github, scheduler, discovery
}

var sourceSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9+_-]*$`)

// ValidSourceSlug 校验分类、语言和 Search 规则的稳定键。
func ValidSourceSlug(value string) bool { return sourceSlug.MatchString(value) }

// TrendingSource 从规则稳定键解析来源和周期，队列与管理接口共用。
func TrendingSource(key string) (source, language, period string, err error) {
	index := strings.LastIndexByte(key, ':')
	if index < 0 {
		return "", "", "", fmt.Errorf("Trending 规则键缺少周期")
	}
	source, period = key[:index], key[index+1:]
	if period != "daily" && period != "weekly" && period != "monthly" {
		return "", "", "", fmt.Errorf("Trending 周期必须为 daily、weekly 或 monthly")
	}
	if source == "all" {
		return source, "", period, nil
	}
	language, ok := strings.CutPrefix(source, "language:")
	if !ok || !ValidSourceSlug(language) {
		return "", "", "", fmt.Errorf("Trending 来源必须为 all 或 language:<语言>")
	}
	return source, language, period, nil
}

func (cfg Runtime) validateCollection() error {
	g := cfg.GitHub
	if _, err := time.Parse(time.DateOnly, g.APIVersion); err != nil {
		return fmt.Errorf("github.api_version 必须为 YYYY-MM-DD")
	}
	if g.TimeoutSeconds < 1 || int64(g.TimeoutSeconds) > DurationLimit(time.Second) ||
		g.WorkerConcurrency < 1 || g.WorkerConcurrency > 3 ||
		g.CoreSoftBudgetPerHour < 1 || g.RepoSoftBudgetPerHour < 1 || g.RepoSoftBudgetPerHour > g.CoreSoftBudgetPerHour ||
		g.SearchSoftBudgetPerMin < 1 || g.CoreReserveRemaining < 0 {
		return fmt.Errorf("github 的超时、并发或请求预算无效")
	}
	s := cfg.Scheduler
	if s.TickMinutes < 1 || int64(s.TickMinutes) > DurationLimit(time.Minute) ||
		s.LeaseSeconds < 3 || int64(s.LeaseSeconds) > DurationLimit(time.Second) || s.MaxMonitoredRepos < 1 ||
		s.MaxHot < 0 || s.MaxActive < 0 || s.MaxNormal < 0 {
		return fmt.Errorf("scheduler 的周期、租期或容量无效")
	}
	remaining := s.MaxMonitoredRepos
	var requestsPerHour float64
	for i, grade := range []string{"S", "A", "B", "C"} {
		minutes := s.IntervalsMinutes[grade]
		if minutes < 5 || int64(minutes) > DurationLimit(time.Minute)-5 {
			return fmt.Errorf("scheduler.intervals_minutes.%s 必须是有效的分钟数，至少为 5", grade)
		}
		count := min(remaining, []int{s.MaxHot, s.MaxActive, s.MaxNormal, remaining}[i])
		requestsPerHour += float64(count) * 60 / float64(minutes)
		remaining -= count
	}
	if requestsPerHour > float64(g.RepoSoftBudgetPerHour) {
		return fmt.Errorf("scheduler 容量与采样周期需要约 %.1f 次每小时，超过仓库采样预算", requestsPerHour)
	}
	return nil
}

// ValidateDiscoverySeed 校验首次导入用的旧发现配置。
func (cfg Config) ValidateDiscoverySeed() error {
	s := cfg.Scheduler
	for _, minutes := range []int{s.TrendingDailyMinutes, s.TrendingOtherMinutes, s.SearchRoundMinutes} {
		if minutes < 5 || int64(minutes) > DurationLimit(time.Minute) {
			return fmt.Errorf("发现来源周期必须是有效的分钟数，至少为 5")
		}
	}
	languages := make(map[string]bool)
	for _, language := range cfg.Discovery.TrendingLanguages {
		if !sourceSlug.MatchString(language) || languages[language] {
			return fmt.Errorf("discovery.trending_languages 含无效或重复来源")
		}
		languages[language] = true
	}
	keys := make(map[string]bool)
	for _, rule := range cfg.Discovery.SearchRules {
		if !sourceSlug.MatchString(rule.Key) || keys[rule.Key] || strings.TrimSpace(rule.Query) == "" {
			return fmt.Errorf("discovery.search_rules 含无效或重复查询")
		}
		keys[rule.Key] = true
	}
	return nil
}
