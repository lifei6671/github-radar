package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

// Runtime 是数据库持有的进程级设置。部署地址、库路径和密钥值不会序列化。
type Runtime struct {
	Server      Server      `json:"server"`
	Storage     Storage     `json:"storage"`
	GitHub      GitHub      `json:"github"`
	Scheduler   Scheduler   `json:"scheduler"`
	AI          AI          `json:"ai"`
	Metrics     Metrics     `json:"metrics"`
	Maintenance Maintenance `json:"maintenance"`
}

// RuntimeSettings 提取正在使用的设置，排除部署字段和一次性导入字段。
func (cfg Config) RuntimeSettings() Runtime {
	values := Runtime{Server: cfg.Server, Storage: cfg.Storage, GitHub: cfg.GitHub, Scheduler: cfg.Scheduler, AI: cfg.AI,
		Metrics: cfg.Metrics, Maintenance: cfg.Maintenance}
	values.Server.Listen, values.Storage.Path = "", ""
	values.Scheduler.TrendingDailyMinutes, values.Scheduler.TrendingOtherMinutes, values.Scheduler.SearchRoundMinutes = 0, 0, 0
	values.AI.BaseURLEnv, values.AI.ModelEnv = "", ""
	return values
}

// SeedRuntime 只在旧数据库首次升级或新数据库首次启动时使用。
func (cfg Config) SeedRuntime() Runtime {
	values := cfg.RuntimeSettings()
	values.DefaultMetricsInterval()
	if values.AI.BaseURL == "" {
		values.AI.BaseURL = strings.TrimSpace(os.Getenv(cfg.AI.BaseURLEnv))
	}
	if values.AI.Model == "" {
		values.AI.Model = strings.TrimSpace(os.Getenv(cfg.AI.ModelEnv))
	}
	return values
}

// WithRuntime 保留启动所需的部署字段，其余由数据库快照提供。
func (cfg Config) WithRuntime(values Runtime) Config {
	values.Server.Listen, values.Storage.Path = cfg.Server.Listen, cfg.Storage.Path
	cfg.Server, cfg.Storage, cfg.GitHub, cfg.Scheduler, cfg.AI = values.Server, values.Storage, values.GitHub, values.Scheduler, values.AI
	cfg.Metrics, cfg.Maintenance = values.Metrics, values.Maintenance
	return cfg
}

// Validate 在导入、管理写入和启动读取时共用同一组运行参数约束。
func (r Runtime) Validate() error {
	if r.Server.ReadTimeoutSeconds < 1 || r.Server.WriteTimeoutSeconds < 1 ||
		int64(r.Server.ReadTimeoutSeconds) > DurationLimit(time.Second) || int64(r.Server.WriteTimeoutSeconds) > DurationLimit(time.Second) {
		return errors.New("HTTP 读写超时必须是有效的正数秒")
	}
	if r.Storage.BusyTimeoutMS < 0 || int64(r.Storage.BusyTimeoutMS) > 1<<31-1 || r.Storage.MaxOpenConns < 1 {
		return errors.New("SQLite 忙等待时间或连接数无效")
	}
	for _, name := range []string{r.GitHub.TokenEnv, r.AI.APIKeyEnv} {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "=\x00") {
			return errors.New("密钥环境变量名无效")
		}
	}
	if r.AI.BaseURL != "" {
		u, err := url.Parse(r.AI.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("AI Base URL 须为不含凭据、查询参数或片段的 HTTP(S) 地址")
		}
	}
	if err := r.validateCollection(); err != nil {
		return err
	}
	return r.validateMetrics()
}

// DurationLimit 避免管理接口写入会使 time.Duration 溢出的值。
func DurationLimit(unit time.Duration) int64 {
	return int64(1<<63-1) / int64(unit)
}
