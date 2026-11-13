package collector

import (
	"context"
	"errors"
	"strings"
	"time"

	sdk "github.com/google/go-github/v88/github"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

// normalize 只在名称冲突或失效状态需要确认时调用稳定的数字 ID 接口。
func (e *Engine) normalize(ctx context.Context, source *sdk.Repository) (*sdk.Repository, error) {
	if source == nil {
		return nil, errors.New("发现源缺少仓库数据")
	}
	blocked, err := e.Queue.IsBlocked(ctx, source.GetID(), source.GetFullName())
	if err != nil || blocked {
		return nil, err
	}
	if source.GetID() <= 0 {
		result, err := e.GitHub.Repository(ctx, source.GetOwner().GetLogin(), source.GetName(), "", gh.Policy{Resource: gh.Core})
		if err == nil && result.Repository == nil {
			return nil, errors.New("发现源缺少仓库数据")
		}
		return result.Repository, err
	}
	known, found, err := e.Queue.KnownRepository(ctx, source.GetID(), source.GetFullName())
	if err != nil {
		return nil, err
	}
	if !found {
		return source, nil
	}
	if known.RepoID != source.GetID() || !strings.EqualFold(known.FullName, source.GetFullName()) ||
		known.State == consts.StatusUnavailable || known.State == consts.StatusAddressPending {
		result, err := e.GitHub.RepositoryByID(ctx, source.GetID(), gh.Policy{Resource: gh.Core, RepositoryID: source.GetID()})
		if errors.Is(err, storage.ErrRepositoryBlocked) {
			return nil, nil
		}
		if err == nil && result.Repository == nil {
			return nil, errors.New("发现源缺少仓库数据")
		}
		return result.Repository, err
	}
	return source, nil
}

func (e *Engine) discoverTrending(ctx context.Context, p collection.Payload) (port.Change, error) {
	entries, err := e.Trending.Fetch(ctx, p.Language, p.Period)
	if err != nil {
		return nil, err
	}
	old, found, err := e.Queue.TrendingBatch(ctx, p.SourceKey, p.Period)
	if err != nil {
		return nil, err
	}
	if found && old.Size >= 10 && len(entries) > 0 && len(entries)*3 < old.Size {
		return nil, errors.New("Trending 解析数量异常，保留旧批次")
	}
	ids := make(map[int64]bool)
	for i := range entries {
		entries[i].Repository, err = e.normalize(ctx, entries[i].Repository)
		if err != nil {
			return nil, err
		}
		if entries[i].Repository == nil {
			continue
		}
		id := entries[i].Repository.GetID()
		if id <= 0 || ids[id] {
			return nil, errors.New("Trending 批次身份无效或重复")
		}
		ids[id] = true
	}
	observed := time.Now().UnixMilli()
	batch := port.Trending{Plan: p, Entries: entries, ObservedAt: observed}
	return func(w port.Writer) error { return w.SaveTrending(batch) }, nil
}

func (e *Engine) search(ctx context.Context, job storage.Job, p collection.Payload) (port.Change, error) {
	query, err := p.SearchQuery()
	if err != nil {
		return nil, err
	}
	if query == "" {
		if p.RunID == nil {
			return nil, nil
		}
		return func(w port.Writer) error { return w.SaveSearch(port.Search{Parent: job, Plan: p}) }, nil
	}
	page, err := e.GitHub.Search(ctx, query, max(p.Page, 1))
	if err != nil {
		return nil, err
	}
	result := port.Search{Parent: job, Plan: p, UpstreamTotal: page.Total}
	if page.Total > 1000 {
		if p.Page > 1 {
			// 翻页时结果集扩张，不能用已经落库的半个分片冒充一轮完整扫描。
			return nil, &gh.Failure{Code: consts.CodeGitHubIncompleteSearch, SearchTotal: &page.Total}
		}
		var highest *int
		if len(page.Repositories) > 0 {
			stars := page.Repositories[0].GetStargazersCount()
			highest = &stars
		}
		result.Next, err = collection.SplitSearch(p, highest)
		result.Split = true
		if err != nil {
			if p.RunID != nil {
				return nil, &gh.Failure{Code: consts.CodeGitHubIncompleteSearch, SearchTotal: &page.Total}
			}
			return nil, err
		}
	} else {
		for i := range page.Repositories {
			if repo := page.Repositories[i]; repo != nil && (repo.GetStargazersCount() < p.StarFloor ||
				p.AgeWindow != "" && (repo.GetCreatedAt().IsZero() || repo.GetCreatedAt().UnixMilli() < p.CreatedFrom || repo.GetCreatedAt().UnixMilli() > p.CreatedUntil)) {
				page.Repositories[i] = nil
				continue
			}
			page.Repositories[i], err = e.normalize(ctx, page.Repositories[i])
			if err != nil {
				return nil, err
			}
		}
		result.Repositories, result.ObservedAt = page.Repositories, time.Now().UnixMilli()
		if page.NextPage == max(p.Page, 1)+1 && page.NextPage <= 20 {
			next := p
			next.Page = page.NextPage
			result.Next = []collection.Payload{next}
		} else if page.NextPage != 0 {
			return nil, &gh.Failure{Code: consts.CodeGitHubIncompleteSearch}
		}
	}
	return func(w port.Writer) error { return w.SaveSearch(result) }, nil
}
