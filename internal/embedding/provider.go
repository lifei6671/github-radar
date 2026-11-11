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

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
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
}

func NewProvider(cfg config.Embedding, key string) *Provider {
	return &Provider{endpoint: strings.TrimRight(cfg.BaseURL, "/") + consts.EmbeddingProviderPath, model: cfg.Model, key: key,
		client: &http.Client{Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Embed 使用独立的 OpenAI 兼容向量接口，供应商正文和密钥不进入诊断日志。
func (p *Provider) Embed(ctx context.Context, input string) ([]byte, error) {
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
	res, err := p.client.Do(req)
	if err != nil {
		return nil, errors.New("Embedding 请求失败或超时")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Embedding 服务返回 HTTP %d", res.StatusCode)
	}
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload); err != nil ||
		len(payload.Data) != 1 || payload.Data[0].Index != 0 {
		return nil, errors.New("Embedding 响应格式无效")
	}
	return encodeVector(payload.Data[0].Embedding)
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
