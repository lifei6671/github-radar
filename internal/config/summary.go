package config

import (
	"errors"
	"math"
	"time"
	_ "time/tzdata"
)

// Summary 保存摘要任务、来源检查、模型输入和计量设置。
type Summary struct {
	Workers          int     `json:"workers"`
	TimeoutSeconds   int     `json:"timeoutSeconds"`
	InputTokens      int     `json:"inputTokens"`
	OutputTokens     int     `json:"outputTokens"`
	ReadmeMaxBytes   int     `json:"readmeMaxBytes"`
	GitHubBudget     int     `json:"githubBudgetPerHour"`
	AutoDailyCalls   int     `json:"autoDailyCalls"`
	ManualDailyCalls int     `json:"manualDailyCalls"`
	PromptVersion    int     `json:"promptVersion"`
	HotRankLimit     int     `json:"hotRankLimit"`
	HighCheckHours   int     `json:"highCheckHours"`
	NormalCheckHours int     `json:"normalCheckHours"`
	LowCheckHours    int     `json:"lowCheckHours"`
	MaxAttempts      int     `json:"maxAttempts"`
	RepairAttempts   int     `json:"repairAttempts"`
	InputCostPerM    float64 `json:"inputCostPerMillion"`
	OutputCostPerM   float64 `json:"outputCostPerMillion"`
}

func DefaultSummary() Summary {
	return Summary{Workers: 1, TimeoutSeconds: 60, InputTokens: 8192, OutputTokens: 1536, ReadmeMaxBytes: 256 << 10,
		GitHubBudget: 120, AutoDailyCalls: 50, ManualDailyCalls: 20, PromptVersion: 1, HotRankLimit: 100,
		HighCheckHours: 24, NormalCheckHours: 72, LowCheckHours: 168, MaxAttempts: 4, RepairAttempts: 1}
}

// Recommendations 保存每日选题计划及已处理项目的再推荐门槛。
type Recommendations struct {
	Enabled       bool   `json:"enabled"`
	Timezone      string `json:"timezone"`
	Hour          int    `json:"hour"`
	Minute        int    `json:"minute"`
	Limit         int    `json:"limit"`
	ReentryGrowth int    `json:"reentryGrowth"`
	RetentionDays int    `json:"retentionDays"`
}

func DefaultRecommendations() Recommendations {
	return Recommendations{Enabled: true, Timezone: "Asia/Shanghai", Hour: 8, Minute: 30, Limit: 20, ReentryGrowth: 200, RetentionDays: 90}
}

func (r Runtime) validateSummary() error {
	a := r.AI.Summary
	if a.Workers < 1 || a.Workers > 8 || a.TimeoutSeconds < 1 || a.TimeoutSeconds > 600 ||
		a.InputTokens < 2048 || a.InputTokens > 256<<10 || a.OutputTokens < 256 || a.OutputTokens > 16<<10 ||
		a.ReadmeMaxBytes < 1024 || a.ReadmeMaxBytes > 256<<10 || a.AutoDailyCalls < 0 || a.ManualDailyCalls < 0 ||
		a.PromptVersion < 1 || a.HotRankLimit < 0 || a.MaxAttempts < 1 || a.MaxAttempts > 10 ||
		a.RepairAttempts < 0 || a.RepairAttempts >= a.MaxAttempts {
		return errors.New("摘要任务或模型额度设置无效")
	}
	// 每个 Worker 的子预算至少覆盖大 README 读取和模型调用前后的公开性与内容复核。
	if a.GitHubBudget < 8*a.Workers {
		return errors.New("README 的 GitHub 每小时预算须达到每个 Worker 至少 8 次")
	}
	for _, h := range []int{a.HighCheckHours, a.NormalCheckHours, a.LowCheckHours} {
		if h < 1 || int64(h) > DurationLimit(time.Hour) {
			return errors.New("README 检查周期无效")
		}
	}
	for _, cost := range []float64{a.InputCostPerM, a.OutputCostPerM} {
		if !(cost >= 0) || math.IsInf(cost, 0) {
			return errors.New("模型计价无效")
		}
	}
	p := r.Recommendations
	if _, err := time.LoadLocation(p.Timezone); err != nil || p.Timezone == "" || p.Timezone == "Local" {
		return errors.New("选题计划需要有效的 IANA 时区")
	}
	if p.Hour < 0 || p.Hour > 23 || p.Minute < 0 || p.Minute > 59 || p.Limit < 1 || p.Limit > 100 ||
		p.ReentryGrowth < 1 || p.RetentionDays < 1 || p.RetentionDays > 3650 {
		return errors.New("选题计划、数量或保留天数无效")
	}
	return nil
}
