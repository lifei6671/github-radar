package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/google/go-github/v88/github"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

// normalize 只在名称冲突或失效状态需要确认时调用稳定的数字 ID 接口。
func (e *Engine) normalize(ctx context.Context, source *sdk.Repository) (*sdk.Repository, error) {
	if source == nil {
		return nil, errors.New("发现源缺少仓库数据")
	}
	db := e.Queue.DB.WithContext(ctx)
	blocked := db.Model(&storage.Repository{}).Where(storage.RepositoryColumns.Blocked + "=1")
	if source.GetID() > 0 {
		blocked = blocked.Where(storage.RepositoryColumns.RepoID+"=?", source.GetID())
	} else {
		// Trending 只有名称时，先跳过当前已屏蔽地址，避免额外请求仓库详情。
		blocked = blocked.Where(storage.RepositoryColumns.FullName+"=? COLLATE NOCASE AND "+storage.RepositoryColumns.NameIsCurrent+"=1", source.GetFullName())
	}
	var count int64
	if err := blocked.Count(&count).Error; err != nil || count > 0 {
		return nil, err
	}
	if source.GetID() <= 0 {
		result, err := e.GitHub.Repository(ctx, source.GetOwner().GetLogin(), source.GetName(), "", gh.Policy{Resource: gh.Core})
		if err == nil && result.Repository == nil {
			return nil, errors.New("发现源缺少仓库数据")
		}
		return result.Repository, err
	}
	var known storage.Repository
	err := db.Take(&known,
		storage.RepositoryColumns.RepoID+" = ? OR ("+storage.RepositoryColumns.FullName+" = ? COLLATE NOCASE AND "+storage.RepositoryColumns.NameIsCurrent+"=1)", source.GetID(), source.GetFullName()).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return source, nil
	}
	if err != nil {
		return nil, err
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

// TrendingBatch 保存真实观测时间，空榜也有批次；失败时该记录保持原值。
type TrendingBatch struct {
	ObservedSlot int64 `json:"observedSlot"`
	ObservedAt   int64 `json:"observedAt"`
	Size         int   `json:"size"`
}

func BatchKey(source, period string) string {
	return consts.TrendingStatePrefix + source + ":" + period
}

func (e *Engine) discoverTrending(ctx context.Context, p Payload) (func(*gorm.DB) error, error) {
	entries, err := e.Trending.Fetch(ctx, p.Language, p.Period)
	if err != nil {
		return nil, err
	}
	var previous storage.MaintenanceState
	err = e.Queue.DB.WithContext(ctx).Take(&previous, storage.MaintenanceStateColumns.Key+" = ?", BatchKey(p.SourceKey, p.Period)).Error
	if err == nil {
		var old TrendingBatch
		if err := json.Unmarshal([]byte(previous.Value), &old); err != nil {
			return nil, err
		}
		if old.Size >= 10 && len(entries) > 0 && len(entries)*3 < old.Size {
			return nil, errors.New("Trending 解析数量异常，保留旧批次")
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
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
	return func(tx *gorm.DB) error {
		key := BatchKey(p.SourceKey, p.Period)
		var existing storage.MaintenanceState
		err := tx.Take(&existing, storage.MaintenanceStateColumns.Key+" = ?", key).Error
		if err == nil {
			var batch TrendingBatch
			if err := json.Unmarshal([]byte(existing.Value), &batch); err != nil {
				return err
			}
			if batch.ObservedSlot >= Slot(p.PlannedAt) {
				return nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		for _, entry := range entries {
			if entry.Repository == nil {
				continue
			}
			applied, err := e.Queue.discover(tx, entry.Repository, observed)
			if err != nil {
				return err
			}
			if !applied {
				continue
			}
			observation := storage.TrendingObservation{
				RepoID: entry.Repository.GetID(), SourceKey: p.SourceKey, Window: p.Period,
				ObservedSlot: Slot(p.PlannedAt), Rank: entry.Rank, ReportedStars: entry.ReportedStars,
			}
			if err := tx.Create(&observation).Error; err != nil {
				return err
			}
		}
		record := storage.TrendingBatchRecord{SourceKey: p.SourceKey, Window: p.Period,
			ObservedSlot: Slot(p.PlannedAt), ObservedAt: observed}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		data, err := json.Marshal(TrendingBatch{record.ObservedSlot, observed, len(entries)})
		if err != nil {
			return err
		}
		marker := storage.MaintenanceState{Key: key, Value: string(data), UpdatedAt: observed}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
	}, nil
}

func (e *Engine) search(ctx context.Context, job storage.Job, p Payload) (func(*gorm.DB) error, error) {
	query := p.Query + " is:public"
	if !strings.Contains(query, "stars:") {
		query += " stars:>=20"
	}
	if !strings.Contains(query, "pushed:") {
		query += " pushed:>=" + time.UnixMilli(p.PlannedAt).UTC().AddDate(0, 0, -90).Format(time.DateOnly)
	}
	if p.StarRange {
		upper := strconv.Itoa(p.MaxStars)
		query += fmt.Sprintf(" stars:%d..%s", p.MinStars, upper)
	}
	if p.CreatedFrom > 0 {
		query += " created:" + time.UnixMilli(p.CreatedFrom).UTC().Format(time.RFC3339) +
			".." + time.UnixMilli(p.CreatedUntil).UTC().Format(time.RFC3339)
	}
	page, err := e.GitHub.Search(ctx, query, max(p.Page, 1))
	if err != nil {
		return nil, err
	}
	if page.Total > 1000 {
		children, err := splitSearch(p, page.Repositories)
		if err != nil {
			return nil, err
		}
		return func(tx *gorm.DB) error {
			for _, child := range children {
				if err := e.enqueueSearch(tx, job, child); err != nil {
					return err
				}
			}
			return nil
		}, nil
	}
	for i := range page.Repositories {
		page.Repositories[i], err = e.normalize(ctx, page.Repositories[i])
		if err != nil {
			return nil, err
		}
	}
	observed := time.Now().UnixMilli()
	return func(tx *gorm.DB) error {
		for _, repo := range page.Repositories {
			if repo == nil {
				continue
			}
			applied, err := e.Queue.discover(tx, repo, observed)
			if err != nil {
				return err
			}
			if !applied {
				continue
			}
			observation := storage.SearchObservation{RepoID: repo.GetID(), SourceKey: p.SourceKey, ObservedSlot: Slot(p.PlannedAt), ObservedAt: observed}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&observation).Error; err != nil {
				return err
			}
		}
		if page.NextPage > p.Page && page.NextPage <= 20 {
			p.Page = page.NextPage
			return e.enqueueSearch(tx, job, p)
		}
		return nil
	}, nil
}

// splitSearch 先按 Star 数拆分，再按创建时间拆分，避免把搜索的一千条上限当成完整结果。
func splitSearch(p Payload, repos []*sdk.Repository) ([]Payload, error) {
	low, high := p.MinStars, p.MaxStars
	if !p.StarRange && len(repos) > 0 {
		high = repos[0].GetStargazersCount()
	}
	left, right := p, p
	left.Page, right.Page = 1, 1
	left.StarRange, right.StarRange = true, true
	left.MinStars, left.MaxStars = low, high
	right.MinStars, right.MaxStars = low, high
	if high > low {
		mid := low + (high-low)/2
		left.MinStars, left.MaxStars = low, mid
		right.MinStars, right.MaxStars = mid+1, high
		return []Payload{left, right}, nil
	}
	from, until := p.CreatedFrom, p.CreatedUntil
	if from == 0 {
		from = time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		until = p.PlannedAt / 1000 * 1000
	}
	if until-from < 1000 {
		return nil, errors.New("Search 分片仍超过一千条，请缩小该来源查询")
	}
	mid := (from + (until-from)/2) / 1000 * 1000
	left.CreatedFrom, left.CreatedUntil = from, mid
	right.CreatedFrom, right.CreatedUntil = mid+1000, until
	return []Payload{left, right}, nil
}

func (e *Engine) enqueueSearch(tx *gorm.DB, parent storage.Job, p Payload) error {
	key := fmt.Sprintf(consts.SearchShardDedupFormat,
		p.RuleID, Slot(p.PlannedAt), p.MinStars, p.MaxStars, p.CreatedFrom, p.CreatedUntil, p.Page)
	_, err := e.Queue.enqueue(tx, consts.JobDiscoverSearch, nil, key, p, parent.Priority, time.Now().UnixMilli())
	return err
}
