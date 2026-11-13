package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/storage"
)

const maxDimensions = 8192

// ModelKey 区分供应商、模型及输入格式，查询只比较同一模型空间。
func ModelKey(cfg config.Embedding) string {
	return digest(strings.TrimRight(cfg.BaseURL, "/") + "\n" + cfg.Model + "\n" + consts.EmbeddingInputVersion)
}

func digest(input string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(input))) }

type Provider struct {
	endpoint, model, key string
	client               *http.Client
	budget               aibudget.Service
	pricing              config.Embedding
}

func NewProvider(cfg config.Embedding, key string, budget aibudget.Service) *Provider {
	return &Provider{endpoint: strings.TrimRight(cfg.BaseURL, "/") + consts.EmbeddingProviderPath, model: cfg.Model, key: key,
		budget: budget, pricing: cfg,
		client: &http.Client{Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type repositoryInput struct {
	ID       int64
	FullName string
	Save     func(*gorm.DB, []byte) error
}

type repositoryInputKey struct{}

// Embed 使用独立的 OpenAI 兼容向量接口，供应商正文和密钥不进入诊断日志。
func (p *Provider) Embed(ctx context.Context, input string) (vector []byte, err error) {
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}{Model: p.model, Input: input})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("无法构造 Embedding 请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)
	source, indexed := ctx.Value(repositoryInputKey{}).(repositoryInput)
	operation := "embedding_query"
	// 公开搜索使用自动维护池，不能占用管理员保留的 100 次。
	if indexed {
		operation = "embedding_index"
		if source.Save == nil {
			return nil, aibudget.ErrNoPermit
		}
	}
	now := time.Now()
	// 同一逻辑输入不因重启重发未决调用。实际的新尝试总有独立请求键。
	identity := fmt.Sprintf("%s:%d:%s", operation, source.ID, digest(p.endpoint+"\n"+p.model+"\n"+input))
	permitted, entry, err := p.budget.Start(ctx, aibudget.Request{LogicalKey: identity, Lane: admission.Maintenance,
		Operation: operation, Provider: "embedding:" + digest(p.endpoint), Model: p.model,
		HeldCost: float64(len(input)) * p.pricing.InputCostPerM / 1e6}, now, func(tx *gorm.DB, _ *aibudget.Request) error {
		paused, err := storage.BooleanSetting(ctx, tx, storage.AutoAIResumeKey)
		if err != nil {
			return err
		}
		if paused {
			return aibudget.ErrExhausted
		}
		if indexed {
			var repo storage.Repository
			if err := tx.Find(&repo, "repo_id = ?", source.ID).Error; err != nil {
				return err
			}
			if !repo.IsPublic() || repo.FullName != source.FullName {
				return aibudget.ErrNoPermit
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var tokens *int64
	status := consts.StatusUnknown
	defer func() {
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var cost *float64
		if tokens != nil && p.pricing.InputCostPerM > 0 {
			cost = new(float64(*tokens) * p.pricing.InputCostPerM / 1e6)
		}
		settle := func(tx *gorm.DB) error {
			if status == "unused" {
				return aibudget.Unused(tx, entry.ReservationKey, time.Now())
			}
			if status == consts.StatusSucceeded && indexed {
				if e := source.Save(tx, vector); e != nil {
					return e
				}
			}
			return aibudget.Settle(tx, entry.ReservationKey, status, tokens, nil, cost, time.Since(now), time.Now())
		}
		if e := p.budget.DB.WithContext(settleCtx).Transaction(settle); e != nil {
			err = errors.Join(err, e)
		}
	}()
	if err := aibudget.Consume(permitted); err != nil {
		status = "unused"
		return nil, err
	}
	req = req.WithContext(permitted)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, errors.New("Embedding 请求失败或超时")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if res.StatusCode < 500 && res.StatusCode != http.StatusRequestTimeout {
			status = consts.StatusFailed
		}
		return nil, fmt.Errorf("Embedding 服务返回 HTTP %d", res.StatusCode)
	}
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens *int64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload); err != nil ||
		len(payload.Data) != 1 || payload.Data[0].Index != 0 {
		return nil, errors.New("Embedding 响应格式无效")
	}
	tokens = payload.Usage.PromptTokens
	vector, err = encodeVector(payload.Data[0].Embedding)
	status = consts.StatusSucceeded
	if err != nil {
		status = consts.StatusFailed
	}
	return vector, err
}

// encodeVector 转为 sqlite-vec 使用的小端 float32 BLOB，归一化后避免距离计算溢出。
func encodeVector(values []float32) ([]byte, error) {
	if len(values) == 0 || len(values) > maxDimensions {
		return nil, errors.New("Embedding 维数无效")
	}
	var squared float64
	for _, value := range values {
		squared += float64(value) * float64(value)
	}
	if squared == 0 || math.IsNaN(squared) || math.IsInf(squared, 0) {
		return nil, errors.New("Embedding 向量无效")
	}
	length := math.Sqrt(squared)
	data := make([]byte, 4*len(values))
	for index, value := range values {
		binary.LittleEndian.PutUint32(data[4*index:], math.Float32bits(float32(float64(value)/length)))
	}
	return data, nil
}
