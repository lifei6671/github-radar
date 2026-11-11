package collector

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (e *Engine) refresh(ctx context.Context, job storage.Job, p Payload) (func(*gorm.DB) error, error) {
	if job.RepoID == nil {
		return nil, errors.New("仓库采集任务缺少 repo_id")
	}
	var baseline storage.Repository
	if err := e.Queue.DB.WithContext(ctx).Take(&baseline, storage.RepositoryColumns.RepoID+" = ?", *job.RepoID).Error; err != nil {
		return nil, err
	}
	if baseline.Blocked || (!p.Manual && baseline.State != consts.StatusActive) {
		return func(*gorm.DB) error { return nil }, nil
	}
	policy := gh.Policy{Resource: gh.Core, RepositoryID: baseline.RepoID, Sample: true, Grade: baseline.Grade, Priority: p.Manual || baseline.Grade == consts.GradeS}
	var result gh.RepositoryResult
	var requestErr error
	started := time.Now()
	if !baseline.NameIsCurrent {
		result, requestErr = e.GitHub.RepositoryByID(ctx, baseline.RepoID, policy)
	} else {
		etag := ""
		// 曾不可访问的仓库需要新的 200 来确认公开性。
		if baseline.MetadataETag != nil && baseline.LastSuccessAt != nil &&
			baseline.FirstNotFoundAt == nil && baseline.State != consts.StatusUnavailable {
			etag = *baseline.MetadataETag
		}
		result, requestErr = e.GitHub.Repository(ctx, baseline.Owner, baseline.Name, etag, policy)
	}
	observed := time.Now()
	var failure *gh.Failure
	if requestErr != nil && (!errors.As(requestErr, &failure) || (failure.Status != 404 && failure.Status != 410)) {
		return nil, requestErr
	}
	return func(tx *gorm.DB) error {
		var current storage.Repository
		if err := tx.Take(&current, storage.RepositoryColumns.RepoID+" = ?", baseline.RepoID).Error; err != nil {
			return err
		}
		at := observed.UnixMilli()
		// 快照归属计划槽，元数据只接受更新的观察；两者分别判断。
		updateCurrent := (current.LastSuccessAt == nil || *current.LastSuccessAt < at) &&
			strings.EqualFold(current.FullName, baseline.FullName)
		// 采样发出后若已观测到仓库不可访问，先前发出的请求不能恢复公开展示。
		if current.FirstNotFoundAt != nil && current.LastCheckedAt != nil && *current.LastCheckedAt >= started.UnixMilli() {
			updateCurrent = false
		}
		if failure != nil {
			if !updateCurrent {
				return nil
			}
			values := map[string]any{storage.RepositoryColumns.LastCheckedAt: at, storage.RepositoryColumns.UpdatedAt: at}
			switch {
			case failure.Status == 410:
				values[storage.RepositoryColumns.State] = consts.StatusUnavailable
				if current.FirstNotFoundAt == nil {
					values[storage.RepositoryColumns.FirstNotFoundAt] = at
				}
			case current.FirstNotFoundAt != nil && at-*current.FirstNotFoundAt >= time.Hour.Milliseconds():
				values[storage.RepositoryColumns.State] = consts.StatusUnavailable
			case current.FirstNotFoundAt == nil:
				values[storage.RepositoryColumns.FirstNotFoundAt] = at
			}
			values[storage.RepositoryColumns.NextFetchAt] = observed.Add(time.Hour).UnixMilli()
			return tx.Model(&current).Updates(values).Error
		}

		values := map[string]any{
			storage.RepositoryColumns.LastCheckedAt: at, storage.RepositoryColumns.LastSuccessAt: at, storage.RepositoryColumns.FirstNotFoundAt: nil, storage.RepositoryColumns.UpdatedAt: at,
			storage.RepositoryColumns.NextFetchAt: observed.Add(e.Cfg.Scheduler.Interval(current.Grade) + time.Duration(current.RepoID%300)*time.Second).UnixMilli(),
		}
		stars, forks := baseline.Stars, baseline.Forks
		if result.NotModified {
			// 发出请求时的可靠 ETag 基线决定这次 304 的采样值。
			if baseline.LastSuccessAt == nil || baseline.MetadataETag == nil {
				return errors.New("304 缺少匹配的可靠基线")
			}
			updateCurrent = updateCurrent && current.MetadataETag != nil && current.LastSuccessAt != nil &&
				*current.MetadataETag == *baseline.MetadataETag && *current.LastSuccessAt == *baseline.LastSuccessAt
		} else {
			source := result.Repository
			if source == nil {
				return errors.New("仓库详情响应为空")
			}
			if source.GetID() != baseline.RepoID {
				// 名称复用只为新身份建档，绝不把新计数写入旧仓库历史。
				if !updateCurrent {
					return nil
				}
				if err := detachName(tx, baseline.RepoID, at); err != nil {
					return err
				}
				_, err := e.Queue.discover(tx, source, at)
				return err
			}
			for key, value := range metadata(source) {
				values[key] = value
			}
			values[storage.RepositoryColumns.NameIsCurrent], values[storage.RepositoryColumns.MetadataETag] = true, result.ETag
			stars, forks = source.GetStargazersCount(), source.GetForksCount()
			if source.GetDisabled() {
				values[storage.RepositoryColumns.State] = consts.StatusUnavailable
			} else if current.State == consts.StatusUnavailable || current.State == consts.StatusAddressPending {
				values[storage.RepositoryColumns.State] = consts.StatusPaused
			}
		}
		sample := storage.RepoSnapshot{RepoID: current.RepoID, SampleSlot: Slot(p.PlannedAt), ObservedAt: at, Stars: stars, Forks: forks}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&sample).Error; err != nil {
			return err
		}
		if !updateCurrent {
			return nil
		}
		if !result.NotModified {
			var occupied storage.Repository
			err := tx.Take(&occupied, storage.RepositoryColumns.FullName+" = ? COLLATE NOCASE AND "+storage.RepositoryColumns.NameIsCurrent+"=1 AND "+storage.RepositoryColumns.RepoID+"<>?", result.Repository.GetFullName(), current.RepoID).Error
			if err == nil {
				if err := detachName(tx, occupied.RepoID, at); err != nil {
					return err
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if err := tx.Model(&current).Updates(values).Error; err != nil {
			return err
		}
		if err := tx.Take(&current, storage.RepositoryColumns.RepoID+" = ?", current.RepoID).Error; err != nil {
			return err
		}
		return e.Queue.admit(tx, &current, at)
	}, nil
}
