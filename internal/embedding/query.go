package embedding

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/config"
)

const (
	maxQueryBytes  = 512
	queryCacheSize = 128
	queryCacheAge  = 15 * time.Minute
	queryTimeout   = 2 * time.Second
)

type QueryVector struct {
	Data []byte
	Key  string
}

type cachedQuery struct {
	vector QueryVector
	until  time.Time
}

// QueryVectors 复用近期搜索向量，不把用户搜索词写入数据库。
type QueryVectors struct {
	provider *Provider
	modelKey string
	mu       sync.Mutex
	cache    map[string]cachedQuery
	requests singleflight.Group
	limit    *rate.Limiter
}

func NewQueryVectors(cfg config.Embedding, key string, budget aibudget.Service) *QueryVectors {
	if !cfg.Enabled || key == "" {
		return nil
	}
	return &QueryVectors{provider: NewProvider(cfg, key, budget), modelKey: ModelKey(cfg),
		cache: make(map[string]cachedQuery), limit: rate.NewLimiter(rate.Every(time.Second), 5)}
}

func (q *QueryVectors) Vector(ctx context.Context, text string) QueryVector {
	if ctx.Err() != nil || len(text) > maxQueryBytes || !utf8.ValidString(text) {
		return QueryVector{}
	}
	key := digest(text)
	if vector := q.cached(key); vector.Key != "" {
		return vector
	}
	result, _, _ := q.requests.Do(key, func() (any, error) {
		if vector := q.cached(key); vector.Key != "" {
			return vector, nil
		}
		if !q.limit.Allow() {
			return QueryVector{}, nil
		}
		// 切换筛选取消的是单个请求。共享调用限时完成，调用方仍等待结束以便恢复排空。
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), queryTimeout)
		defer cancel()
		data, err := q.provider.Embed(shared, text)
		if err != nil {
			return QueryVector{}, nil
		}
		vector := QueryVector{Data: data, Key: digest(q.modelKey + string(data))}
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.cache) >= queryCacheSize {
			var oldest string
			var expires time.Time
			for key, entry := range q.cache {
				if expires.IsZero() || entry.until.Before(expires) {
					oldest, expires = key, entry.until
				}
			}
			delete(q.cache, oldest)
		}
		q.cache[key] = cachedQuery{vector: vector, until: time.Now().Add(queryCacheAge)}
		return vector, nil
	})
	return result.(QueryVector)
}

func (q *QueryVectors) cached(key string) QueryVector {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.cache[key]
	if !time.Now().Before(entry.until) {
		delete(q.cache, key)
		return QueryVector{}
	}
	return entry.vector
}
