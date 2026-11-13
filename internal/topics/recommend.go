package topics

import (
	"context"
	"strings"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	domain "github.com/lifei6671/github-radar/internal/domain/topics"
	"github.com/lifei6671/github-radar/internal/metrics"
)

const RecommendationDayKey = "topics.recommendation_day"
const signalAge = 24 * time.Hour

type Store interface {
	Transaction(context.Context, func(Transaction) error) error
}

type Transaction interface {
	Marker(key string) (value string, updatedAt int64, found bool, err error)
	SaveMarker(key, value string, at int64) error
	Recommendable(minSignalAt int64) ([]domain.Signals, map[int64]domain.Candidate, error)
	Previous(repoID int64) (domain.Candidate, error)
	LoadSignals(repoID, minSignalAt int64) (domain.Signals, error)
	Save(domain.Candidate) (domain.Candidate, error)
	MarkReviewed(id, at int64) error
}

// Generate 每个配置时区的自然日只发布一轮选题，来源不同的候选轮流入池。
func Generate(ctx context.Context, store Store, cfg config.Recommendations, now time.Time) error {
	if !cfg.Enabled {
		return nil
	}
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return err
	}
	local := now.In(location)
	if local.Hour()*60+local.Minute() < cfg.Hour*60+cfg.Minute {
		return nil
	}
	day := cfg.Timezone + "/" + local.Format(time.DateOnly)
	return store.Transaction(ctx, func(tx Transaction) error {
		cutoff := now.Add(-signalAge).UnixMilli()
		// 停机恢复时等指标 Worker 发布有效信号，再写当天的选题完成水位。
		_, at, found, err := tx.Marker(metrics.ComputeKey)
		if err != nil || !found || at < cutoff {
			return err
		}
		last, _, _, err := tx.Marker(RecommendationDayKey)
		if err != nil || (strings.HasPrefix(last, cfg.Timezone+"/") && last >= day) {
			return err
		}
		signals, history, err := tx.Recommendable(cutoff)
		if err != nil {
			return err
		}
		for _, candidate := range domain.Recommend(signals, history, cfg.Limit, cfg.ReentryGrowth, now) {
			if _, err := tx.Save(candidate); err != nil {
				return err
			}
		}
		return tx.SaveMarker(RecommendationDayKey, day, now.UnixMilli())
	})
}
