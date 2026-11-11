package metrics

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
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
	if err := tx.Where(fmt.Sprintf(`%[1]s='%[2]s' AND %[3]s='%[4]s' AND %[5]s>? AND %[5]s<=?
AND EXISTS(SELECT 1 FROM %[6]s WHERE %[7]s='%[8]s' AND %[9]s='%[10]s')`,
		storage.TrendingBatchRecordColumns.SourceKey, consts.TrendingAll, storage.TrendingBatchRecordColumns.Window, consts.PeriodDaily, storage.TrendingBatchRecordColumns.ObservedAt, storage.DiscoveryRuleTable, storage.DiscoveryRuleColumns.SourceType, consts.SourceTrending, storage.DiscoveryRuleColumns.SourceKey, consts.TrendingAllDaily), asOf-7*Day, asOf).
		Order(storage.TrendingBatchRecordColumns.ObservedAt + " DESC, " + storage.TrendingBatchRecordColumns.ObservedSlot + " DESC").Find(&batches).Error; err != nil || len(batches) == 0 {
		return result, false, err
	}
	var rows []storage.TrendingObservation
	if err := tx.Table(storage.TrendingObservationTable+" AS o").Select("o.*").Joins(`JOIN `+storage.TrendingBatchRecordTable+` b
ON b.`+storage.TrendingBatchRecordColumns.SourceKey+`=o.`+storage.TrendingObservationColumns.SourceKey+` AND b.`+storage.TrendingBatchRecordColumns.Window+`=o.`+storage.TrendingObservationColumns.Window+` AND b.`+storage.TrendingBatchRecordColumns.ObservedSlot+`=o.`+storage.TrendingObservationColumns.ObservedSlot).
		Where("b."+storage.TrendingBatchRecordColumns.SourceKey+"='"+consts.TrendingAll+"' AND b."+storage.TrendingBatchRecordColumns.Window+"='"+consts.PeriodDaily+"' AND b."+storage.TrendingBatchRecordColumns.ObservedAt+">? AND b."+storage.TrendingBatchRecordColumns.ObservedAt+"<=?", asOf-7*Day, asOf).
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
