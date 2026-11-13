package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

const (
	Core                = "core"
	Search              = consts.SourceSearch
	RepoRefreshResource = "repo_refresh"
	ReadmeCheckResource = "readme_check"
	StarHistoryResource = "star_history"
	repoLimit           = RepoRefreshResource
	readmeLimit         = ReadmeCheckResource
	secondary           = consts.CodeGitHubSecondary
)

// Policy 区分独立的 Search 预算、仓库详情子预算和优先请求。
type Policy struct {
	Resource     string
	RepositoryID int64
	Grade        string
	Sample       bool
	Priority     bool
	ReadmeBudget int
	StarHistory  bool
}

type limiter struct {
	db  *gorm.DB
	cfg config.GitHub
}

func rateState(tx *gorm.DB, resource string) (storage.RateLimit, error) {
	r := storage.RateLimit{Resource: resource}
	err := tx.Take(&r, storage.RateLimitColumns.Resource+" = ?", resource).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	}
	return r, err
}

func saveRate(tx *gorm.DB, r *storage.RateLimit) error {
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(r).Error
}

// reserve 在请求前原子消耗本地额度，进程重启也不会重置窗口。
func (l limiter) reserve(ctx context.Context, p Policy, now time.Time) error {
	ms := now.UnixMilli()
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if p.RepositoryID > 0 {
			// 每次实际 HTTP 请求都检查屏蔽，覆盖重定向和 README 的后续 blob 请求。
			var blocked int64
			if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.RepoID+"=? AND "+storage.RepositoryColumns.Blocked+"=1", p.RepositoryID).Count(&blocked).Error; err != nil {
				return err
			}
			if blocked > 0 {
				return storage.ErrRepositoryBlocked
			}
		}
		global, err := rateState(tx, secondary)
		if err != nil {
			return err
		}
		if global.PausedUntil != nil && *global.PausedUntil > ms {
			return &Failure{Code: consts.CodeGitHubSecondaryLimit, Until: *global.PausedUntil}
		}
		r, err := rateState(tx, p.Resource)
		if err != nil {
			return err
		}
		if r.PausedUntil != nil && *r.PausedUntil > ms {
			return &Failure{Code: consts.CodeGitHubPrimaryLimit, Until: *r.PausedUntil}
		}
		if r.ResetAt > ms {
			if r.Remaining == 0 {
				return &Failure{Code: consts.CodeGitHubPrimaryLimit, Until: r.ResetAt + 1000}
			}
			if p.Resource == Core && !p.Priority {
				reserve := l.cfg.CoreReserveRemaining
				if p.Grade == consts.GradeB || p.Grade == consts.GradeC {
					reserve = max(reserve, 1000)
				}
				if r.Remaining < reserve {
					return &Failure{Code: consts.CodeGitHubCoreReserve, Until: r.ResetAt + 1000}
				}
			}
		}

		budget, window := l.cfg.CoreSoftBudgetPerHour, time.Hour
		if p.Resource == Search {
			budget, window = l.cfg.SearchSoftBudgetPerMin, time.Minute
		}
		if err := consumeLocal(tx, &r, budget, window, ms); err != nil {
			return err
		}
		if p.Sample {
			repo, err := rateState(tx, repoLimit)
			if err != nil {
				return err
			}
			return consumeLocal(tx, &repo, l.cfg.RepoSoftBudgetPerHour, time.Hour, ms)
		}
		if p.ReadmeBudget > 0 {
			// README 与公开性复核共享独立的本地预算，防止批量摘要占满采样窗口。
			readme, err := rateState(tx, readmeLimit)
			if err != nil {
				return err
			}
			return consumeLocal(tx, &readme, p.ReadmeBudget, time.Hour, ms)
		}
		if p.StarHistory {
			history, err := rateState(tx, StarHistoryResource)
			if err != nil {
				return err
			}
			return consumeLocal(tx, &history, metrics.StarHistoryBudget, time.Hour, ms)
		}
		return nil
	})
}

func consumeLocal(tx *gorm.DB, r *storage.RateLimit, budget int, window time.Duration, now int64) error {
	end := r.LocalWindow + window.Milliseconds()
	if r.LocalWindow == 0 || end <= now {
		r.LocalWindow, r.LocalRequests = now, 0
	} else if r.LocalRequests >= budget {
		return &Failure{Code: consts.CodeGitHubLocalBudget, Until: end}
	}
	r.LocalRequests++
	return saveRate(tx, r)
}

// observe 保存实际响应头。次级限流作用于同一凭据的全部 REST 资源。
func (l limiter) observe(ctx context.Context, p Policy, resp *http.Response, kind string, started, now time.Time) (int64, error) {
	resource := resp.Header.Get("X-RateLimit-Resource")
	if resource == "" {
		resource = p.Resource
	}
	remaining, remErr := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	total, totalErr := strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	reset, resetErr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	var until int64
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := rateState(tx, resource)
		if err != nil {
			return err
		}
		if remErr == nil && totalErr == nil && resetErr == nil {
			newReset := time.Unix(reset, 0).UnixMilli()
			// 同一窗口内的乱序响应不能把剩余额度调高。
			if newReset >= r.ResetAt {
				if newReset == r.ResetAt && r.ObservedAt != 0 {
					remaining = min(remaining, r.Remaining)
				}
				r.LimitTotal, r.Remaining, r.ResetAt = total, remaining, newReset
				r.ObservedAt = now.UnixMilli()
			}
		}
		switch kind {
		case "primary":
			until = max(r.ResetAt+1000, now.Add(time.Second).UnixMilli(), retryAfter(resp.Header, now))
			if resetErr != nil {
				until = max(until, now.Add(time.Minute).UnixMilli())
			}
			if r.PausedUntil != nil {
				until = max(until, *r.PausedUntil)
			}
			r.PausedUntil = &until
			r.LimitedCount++
		case "secondary":
			g, err := rateState(tx, secondary)
			if err != nil {
				return err
			}
			delay := time.Minute * time.Duration(1<<min(g.SecondaryStreak, 5))
			until = max(now.Add(delay).UnixMilli(), retryAfter(resp.Header, now))
			if g.PausedUntil != nil {
				until = max(until, *g.PausedUntil)
			}
			g.PausedUntil = &until
			g.SecondaryStreak++
			if err := saveRate(tx, &g); err != nil {
				return err
			}
			r.LimitedCount++
		default:
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
				g, err := rateState(tx, secondary)
				if err != nil {
					return err
				}
				if g.PausedUntil != nil && *g.PausedUntil <= started.UnixMilli() {
					g.PausedUntil, g.SecondaryStreak = nil, 0
					if err := saveRate(tx, &g); err != nil {
						return err
					}
				}
			}
		}
		return saveRate(tx, &r)
	})
	return until, err
}

func retryAfter(header http.Header, now time.Time) int64 {
	value := header.Get("Retry-After")
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second).UnixMilli()
	}
	if date, err := http.ParseTime(value); err == nil {
		return date.UnixMilli()
	}
	return 0
}
