package metrics

import (
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

type trendingSignal struct {
	frequency, streak int
	rank              *int
	score             *float64
}

// trendingSignals 读取全站 daily 的完整批次，空榜也会中断连续上榜。
func trendingSignals(tx *gorm.DB, asOf int64) (map[int64]trendingSignal, bool, error) {
	result := make(map[int64]trendingSignal)
	var batches []storage.TrendingBatchRecord
	if err := tx.Where(`source_key='all' AND window='daily' AND observed_at>? AND observed_at<=?
AND EXISTS(SELECT 1 FROM discovery_rules WHERE source_type='trending' AND source_key='all:daily')`, asOf-7*Day, asOf).
		Order("observed_at DESC, observed_slot DESC").Find(&batches).Error; err != nil || len(batches) == 0 {
		return result, false, err
	}
	var rows []storage.TrendingObservation
	if err := tx.Table("trending_observations AS o").Select("o.*").Joins(`JOIN trending_batches b
ON b.source_key=o.source_key AND b.window=o.window AND b.observed_slot=o.observed_slot`).
		Where("b.source_key='all' AND b.window='daily' AND b.observed_at>? AND b.observed_at<=?", asOf-7*Day, asOf).
		Find(&rows).Error; err != nil {
		return result, false, err
	}
	members := make(map[int64][]storage.TrendingObservation)
	for _, row := range rows {
		members[row.ObservedSlot] = append(members[row.ObservedSlot], row)
	}
	fresh := batches[0].ObservedAt > asOf-Day
	for index, batch := range batches {
		for _, row := range members[batch.ObservedSlot] {
			s := result[row.RepoID]
			s.frequency++
			if s.streak == index {
				s.streak++
			}
			if index == 0 {
				rank := row.Rank
				s.rank = &rank
				if fresh {
					value := 100 / float64(max(rank, 1))
					s.score = &value
				}
			}
			result[row.RepoID] = s
		}
	}
	return result, fresh, nil
}
