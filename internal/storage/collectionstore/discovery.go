package collectionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/storage"
)

func (q Queue) TrendingBatch(ctx context.Context, source, period string) (collection.TrendingBatch, bool, error) {
	return trendingBatch(q.DB.WithContext(ctx), collection.BatchKey(source, period))
}

func trendingBatch(tx *gorm.DB, key string) (collection.TrendingBatch, bool, error) {
	var batch collection.TrendingBatch
	var row storage.MaintenanceState
	err := tx.Take(&row, storage.MaintenanceStateColumns.Key+" = ?", key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return batch, false, nil
	}
	if err != nil {
		return batch, false, err
	}
	err = json.Unmarshal([]byte(row.Value), &batch)
	return batch, err == nil, err
}

func (w writer) SaveTrending(change port.Trending) error {
	tx := w.db
	p, entries, observed := change.Plan, change.Entries, change.ObservedAt
	key := collection.BatchKey(p.SourceKey, p.Period)
	batch, found, err := trendingBatch(tx, key)
	if err != nil || (found && batch.ObservedSlot >= collection.Slot(p.PlannedAt)) {
		return err
	}
	for _, entry := range entries {
		if entry.Repository == nil {
			continue
		}
		applied, err := w.queue.Discover(tx, entry.Repository, observed)
		if err != nil {
			return err
		}
		if !applied {
			continue
		}
		observation := storage.TrendingObservation{
			RepoID: entry.Repository.GetID(), SourceKey: p.SourceKey, Window: p.Period,
			ObservedSlot: collection.Slot(p.PlannedAt), Rank: entry.Rank, ReportedStars: entry.ReportedStars,
		}
		if err := tx.Create(&observation).Error; err != nil {
			return err
		}
	}
	record := storage.TrendingBatchRecord{SourceKey: p.SourceKey, Window: p.Period,
		ObservedSlot: collection.Slot(p.PlannedAt), ObservedAt: observed}
	if err := tx.Create(&record).Error; err != nil {
		return err
	}
	data, err := json.Marshal(collection.TrendingBatch{ObservedSlot: record.ObservedSlot, ObservedAt: observed, Size: len(entries)})
	if err != nil {
		return err
	}
	marker := storage.MaintenanceState{Key: key, Value: string(data), UpdatedAt: observed}
	return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
}

func (w writer) SaveSearch(change port.Search) error {
	tx := w.db
	p, observed := change.Plan, change.ObservedAt
	for _, repo := range change.Repositories {
		if repo == nil || repo.GetStargazersCount() < p.StarFloor {
			continue
		}
		applied, err := w.queue.Discover(tx, repo, observed)
		if err != nil {
			return err
		}
		if !applied {
			continue
		}
		observation := storage.SearchObservation{RepoID: repo.GetID(), SourceKey: p.SourceKey, ObservedSlot: collection.Slot(p.PlannedAt), ObservedAt: observed}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&observation).Error; err != nil {
			return err
		}
	}
	for _, next := range change.Next {
		if err := w.queue.enqueueSearch(tx, change.Parent, next); err != nil {
			return err
		}
	}
	return nil
}

func (q Queue) enqueueSearch(tx *gorm.DB, parent storage.Job, p collection.Payload) error {
	key := fmt.Sprintf(consts.SearchShardDedupFormat,
		p.RuleID, collection.Slot(p.PlannedAt), p.MinStars, p.MaxStars, p.CreatedFrom, p.CreatedUntil, p.Page)
	_, err := q.enqueue(tx, consts.JobDiscoverSearch, nil, key, p, parent.Priority, time.Now().UnixMilli())
	return err
}
