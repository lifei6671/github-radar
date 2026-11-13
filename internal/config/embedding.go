package config

import (
	"errors"
	"math"
	"net/url"
	"strings"

	"github.com/lifei6671/github-radar/internal/consts"
)

// Embedding 由后台维护，独立于摘要模型配置。数据库只保存密钥环境变量名。
type Embedding struct {
	Enabled         bool    `json:"enabled"`
	BaseURL         string  `json:"baseUrl"`
	Model           string  `json:"model"`
	APIKeyEnv       string  `json:"apiKeyEnv"`
	BatchSize       int     `json:"batchSize"`
	IntervalMinutes int     `json:"intervalMinutes"`
	MinSimilarity   float64 `json:"minSimilarity"`
	// InputCostPerM 用于统一账本的向量估价。
	InputCostPerM float64 `json:"inputCostPerMillion,omitempty"`
}

func DefaultEmbedding() Embedding {
	return Embedding{APIKeyEnv: consts.EmbeddingAPIKeyEnv, BatchSize: 3, IntervalMinutes: 10, MinSimilarity: .55}
}

func (cfg Embedding) validate() error {
	if !(cfg.InputCostPerM >= 0) || math.IsInf(cfg.InputCostPerM, 0) {
		return errors.New("Embedding 模型计价无效")
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 20 || cfg.IntervalMinutes < 1 || cfg.IntervalMinutes > 1440 {
		return errors.New("相似项目每轮处理量须为 1 至 20，扫描间隔须为 1 至 1440 分钟")
	}
	if !(cfg.MinSimilarity >= 0 && cfg.MinSimilarity <= 1) {
		return errors.New("相似度门槛须在 0 至 1 之间")
	}
	if cfg.APIKeyEnv == "" || strings.TrimSpace(cfg.APIKeyEnv) != cfg.APIKeyEnv || strings.ContainsAny(cfg.APIKeyEnv, "=\x00") {
		return errors.New("Embedding 密钥环境变量名无效")
	}
	if cfg.Enabled && (cfg.BaseURL == "" || strings.TrimSpace(cfg.Model) == "") {
		return errors.New("启用相似项目须配置 Embedding API 地址和模型")
	}
	if cfg.BaseURL != "" {
		u, err := url.Parse(cfg.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
			u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.BaseURL, "#") {
			return errors.New("Embedding API 地址须为不含凭据、查询参数或片段的 HTTP(S) 地址")
		}
	}
	return nil
}
