package config

import (
	"errors"
	"math"
	"time"
	_ "time/tzdata"
)

const (
	// MaxAutoDailyCalls 与 MaxManualDailyCalls 是所有供应商共享的 UTC 日上限。
	MaxAutoDailyCalls   = 4500
	MaxManualDailyCalls = 500
	// Token 上限允许配置长上下文供应商，单次模型实际支持范围由供应商决定。
	MaxSummaryInputTokens  = 1_048_576
	MaxSummaryOutputTokens = 393_216
)

// Summary 保存摘要任务、来源检查、模型输入和计量设置。
type Summary struct {
	// LegacyAdmission 回滚自动准入策略；模型额度和公开性校验始终生效。
	LegacyAdmission bool `json:"legacyAdmission,omitempty"`
	// ExplorationRandomPercent 是探索槽位中按年龄、Star 档随机抽取的比例。
	ExplorationRandomPercent int     `json:"explorationRandomPercent"`
	Workers                  int     `json:"workers"`
	TimeoutSeconds           int     `json:"timeoutSeconds"`
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	ReadmeMaxBytes           int     `json:"readmeMaxBytes"`
	GitHubBudget             int     `json:"githubBudgetPerHour"`
	AutoDailyCalls           int     `json:"autoDailyCalls"`
	ManualDailyCalls         int     `json:"manualDailyCalls"`
	PromptVersion            int     `json:"promptVersion"`
	HotRankLimit             int     `json:"hotRankLimit"`
	HighCheckHours           int     `json:"highCheckHours"`
	NormalCheckHours         int     `json:"normalCheckHours"`
	LowCheckHours            int     `json:"lowCheckHours"`
	MaxAttempts              int     `json:"maxAttempts"`
	RepairAttempts           int     `json:"repairAttempts"`
	InputCostPerM            float64 `json:"inputCostPerMillion"`
	OutputCostPerM           float64 `json:"outputCostPerMillion"`
	// DailySoftCost 只约束自动任务，DailyHardCost 同时约束人工；0 表示停用。
	DailySoftCost float64 `json:"dailySoftCost,omitempty"`
	DailyHardCost float64 `json:"dailyHardCost,omitempty"`
}

func DefaultSummary() Summary {
	return Summary{ExplorationRandomPercent: 30, Workers: 1, TimeoutSeconds: 60, InputTokens: 131_072, OutputTokens: 16_384, ReadmeMaxBytes: 256 << 10,
		GitHubBudget: 120, AutoDailyCalls: MaxAutoDailyCalls, ManualDailyCalls: MaxManualDailyCalls, PromptVersion: 1, HotRankLimit: 100,
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
	if a.AutoDailyCalls < 0 || a.AutoDailyCalls > MaxAutoDailyCalls || a.ManualDailyCalls < 0 || a.ManualDailyCalls > MaxManualDailyCalls {
		return errors.New("每日 AI 调用上限须在自动 0 至 4500 次、手动 0 至 500 次之间")
	}
	if a.InputTokens < 2048 || a.InputTokens > MaxSummaryInputTokens || a.OutputTokens < 256 || a.OutputTokens > MaxSummaryOutputTokens {
		return errors.New("模型输入预算须在 2048 至 1048576 Token 之间，输出预算须在 256 至 393216 Token 之间")
	}
	if a.ExplorationRandomPercent < 0 || a.ExplorationRandomPercent > 100 ||
		a.Workers < 1 || a.Workers > 8 || a.TimeoutSeconds < 1 || a.TimeoutSeconds > 600 ||
		a.ReadmeMaxBytes < 1024 || a.ReadmeMaxBytes > 256<<10 ||
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
	for _, cost := range []float64{a.InputCostPerM, a.OutputCostPerM, a.DailySoftCost, a.DailyHardCost} {
		if !(cost >= 0) || math.IsInf(cost, 0) {
			return errors.New("模型计价无效")
		}
	}
	if a.DailyHardCost > 0 && a.DailySoftCost > a.DailyHardCost {
		return errors.New("模型金额软上限不得高于硬上限")
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
