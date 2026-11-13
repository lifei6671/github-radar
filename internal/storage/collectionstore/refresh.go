package collectionstore

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (w writer) SaveRefresh(change port.Refresh) error {
	tx := w.db
	baseline, p, result, failureStatus := change.Baseline, change.Plan, change.Result, change.FailureStatus
	started, observed := change.StartedAt, change.ObservedAt
	var current storage.Repository
	if err := tx.Take(&current, storage.RepositoryColumns.RepoID+" = ?", baseline.RepoID).Error; err != nil {
		return err
	}
	at := observed.UnixMilli()
	source := result.Repository
	hidden := source != nil && (source.GetPrivate() || source.GetDisabled() ||
		(source.GetVisibility() != "" && source.GetVisibility() != "public"))
	// 快照归属计划槽，元数据只接受更新的观察；两者分别判断。
	state := current.MonitoringState()
	updateCurrent := state.CanPublishObservation(baseline.FullName, started.UnixMilli(), at)
	if hidden && source.GetID() == baseline.RepoID {
		failureStatus = 410
	}
	if failureStatus != 0 {
		if !updateCurrent {
			return nil
		}
		values := map[string]any{storage.RepositoryColumns.LastCheckedAt: at, storage.RepositoryColumns.UpdatedAt: at}
		if state.ConfirmedMissing(failureStatus, at) {
			values[storage.RepositoryColumns.State] = consts.StatusUnavailable
			if w.queue.Cfg.ObservationPool {
				if err := storage.RecordObservation(tx, current, consts.StatusUnavailable, current.Grade, "github_unavailable", at, metrics.ObservationEvidence{Stars: current.Stars}); err != nil {
					return err
				}
				values["observation_probe_due_at"] = 0
			}
		}
		if state.FirstNotFoundAt == nil {
			values[storage.RepositoryColumns.FirstNotFoundAt] = at
			if w.queue.Cfg.ObservationPool && failureStatus == 404 && current.State != consts.StatusActive && state.CanMonitor() {
				values["observation_probe_due_at"] = at + metrics.Hour
			}
		}
		values[storage.RepositoryColumns.NextFetchAt] = observed.Add(time.Hour).UnixMilli()
		if err := tx.Model(&current).Updates(values).Error; err != nil {
			return err
		}
		if p.ObservationProbe {
			var next int64
			if failureStatus == 404 && state.FirstNotFoundAt == nil {
				next = at + metrics.Hour
			}
			return finishObservationProbe(tx, current.RepoID, p, next)
		}
		return nil
	}

	values := map[string]any{
		storage.RepositoryColumns.LastCheckedAt: at, storage.RepositoryColumns.LastSuccessAt: at, storage.RepositoryColumns.FirstNotFoundAt: nil, storage.RepositoryColumns.UpdatedAt: at,
		storage.RepositoryColumns.NextFetchAt: observed.Add(w.queue.Cfg.Interval(current.Grade) + time.Duration(current.RepoID%300)*time.Second).UnixMilli(),
	}
	stars, forks := baseline.Stars, baseline.Forks
	if result.NotModified {
		// 发出请求时的可靠 ETag 基线决定这次 304 的采样值。
		requestState := baseline.MonitoringState()
		if !requestState.HasBaseline() {
			return errors.New("304 缺少匹配的可靠基线")
		}
		updateCurrent = updateCurrent && state.MatchesBaseline(requestState)
	} else {
		if source == nil {
			return errors.New("仓库详情响应为空")
		}
		if source.GetID() != baseline.RepoID {
			// 名称复用只为新身份建档，绝不把新计数写入旧仓库历史。
			if !updateCurrent {
				return nil
			}
			if err := w.queue.detachName(tx, current, at); err != nil {
				return err
			}
			if hidden {
				return nil
			}
			_, err := w.queue.Discover(tx, source, at)
			return err
		}
		for key, value := range metadata(source) {
			values[key] = value
		}
		values[storage.RepositoryColumns.NameIsCurrent], values[storage.RepositoryColumns.MetadataETag] = true, result.ETag
		stars, forks = source.GetStargazersCount(), source.GetForksCount()
		if current.State == consts.StatusUnavailable || current.State == consts.StatusAddressPending {
			values[storage.RepositoryColumns.State] = consts.StatusPaused
			if w.queue.Cfg.ObservationPool {
				if err := storage.RecordObservation(tx, current, consts.StatusPaused, current.Grade, "identity_restored", at,
					metrics.ObservationEvidence{Stars: stars}); err != nil {
					return err
				}
			}
		}
	}
	sample := storage.RepoSnapshot{RepoID: current.RepoID, SampleSlot: collection.Slot(p.PlannedAt), ObservedAt: at, Stars: stars, Forks: forks}
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
			if err := w.queue.detachName(tx, occupied, at); err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	if err := tx.Model(&current).Updates(values).Error; err != nil {
		return err
	}
	if p.ObservationProbe {
		var next int64
		// 以实际首采时刻约定复采，排队或限流延迟不会改变 24 小时的比较起点。
		if w.queue.Cfg.ObservationPool && !p.ObservationFollowup &&
			(current.State != consts.StatusActive || current.Grade == consts.GradeC) {
			next = at + metrics.Day
		}
		if err := finishObservationProbe(tx, current.RepoID, p, next); err != nil {
			return err
		}
	} else if w.queue.Cfg.ObservationPool && state.FirstNotFoundAt != nil {
		// 人工采样先恢复身份时，清理原探测的复核计划。
		if err := tx.Model(&current).Update("observation_probe_due_at", 0).Error; err != nil {
			return err
		}
	}
	if err := tx.Take(&current, storage.RepositoryColumns.RepoID+" = ?", current.RepoID).Error; err != nil {
		return err
	}
	return w.queue.admit(tx, &current, at)
}
