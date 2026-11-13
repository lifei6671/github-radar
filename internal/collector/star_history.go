package collector

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

// StarHistorySource 复用采集的 GitHub 客户端和配额控制。
type StarHistorySource interface {
	StarHistory(context.Context, int64, int, int, gh.Policy) (gh.StarHistoryPage, error)
}

func (e *Engine) starHistory(ctx context.Context, job storage.Job, p collection.Payload) (port.Change, error) {
	if e.History == nil || job.RepoID == nil || p.Page <= 0 || !p.Manual && p.Page != 1 {
		return nil, &gh.Failure{Code: consts.CodeInvalidRange}
	}
	repo, err := e.Queue.Repository(ctx, *job.RepoID)
	if err != nil {
		return nil, err
	}
	if repo.Blocked || repo.Disabled || !repo.NameIsCurrent || repo.FirstNotFoundAt != nil || repo.State == consts.StatusUnavailable {
		return nil, &gh.Failure{Code: consts.CodeGitHubNotFound}
	}
	policy := gh.Policy{RepositoryID: repo.RepoID, Grade: repo.Grade, Priority: p.Manual, StarHistory: true}
	fetchedAt := time.Now().UnixMilli()
	size := metrics.StarHistoryAutoWeeks
	if p.Manual {
		size = metrics.StarHistoryPageSize
	}
	page, err := e.History.StarHistory(ctx, repo.RepoID, p.Page, size, policy)
	if err != nil {
		return nil, err
	}
	if p.HistoryBefore != 0 && (len(page.Weeks) == 0 || p.HistoryBefore-page.Weeks[0].Week < 6*metrics.Day || p.HistoryBefore-page.Weeks[0].Week > 8*metrics.Day) {
		// 周边界在翻页期间移动时，不能把错位的一页当作完整回补。
		return nil, &gh.Failure{Code: consts.CodeStarHistoryPageChanged}
	}
	remote, err := e.GitHub.RepositoryByID(ctx, repo.RepoID, policy)
	var failure *gh.Failure
	unavailable := errors.As(err, &failure) && failure.Status == http.StatusGone
	if err == nil && remote.Repository != nil && remote.Repository.GetID() == repo.RepoID {
		r := remote.Repository
		unavailable = r.GetPrivate() || r.GetDisabled() || r.GetVisibility() != "" && r.GetVisibility() != "public"
	}
	if unavailable {
		// 只有数字仓库接口确认撤回时才改变可见性；历史接口自身的 404 只影响历史任务。
		return func(tx port.Writer) error {
			return tx.SaveStarHistory(job, p, port.StarHistory{FetchedAt: fetchedAt, Unavailable: true})
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if remote.Repository == nil || remote.Repository.GetID() != repo.RepoID {
		return nil, &gh.Failure{Code: consts.CodeGitHubInvalidRepository}
	}
	next := 0
	if p.Manual && p.Page < metrics.StarHistoryMaxPages {
		next = page.NextPage
	}
	return func(tx port.Writer) error {
		return tx.SaveStarHistory(job, p, port.StarHistory{Weeks: page.Weeks, NextPage: next, FetchedAt: fetchedAt})
	}, nil
}
