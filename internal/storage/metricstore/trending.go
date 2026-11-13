package metricstore

import (
	"fmt"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// LoadTrending 读取全站 daily 的完整批次，空榜也会中断连续上榜。
func (r transaction) LoadTrending(asOf int64) ([]metrics.TrendingBatch, error) {
	tx := r.db
	var batches []storage.TrendingBatchRecord
	if err := tx.Where(fmt.Sprintf(`%[1]s='%[2]s' AND %[3]s='%[4]s' AND %[5]s>? AND %[5]s<=?
AND EXISTS(SELECT 1 FROM %[6]s WHERE %[7]s='%[8]s' AND %[9]s='%[10]s')`,
		storage.TrendingBatchRecordColumns.SourceKey, consts.TrendingAll, storage.TrendingBatchRecordColumns.Window, consts.PeriodDaily, storage.TrendingBatchRecordColumns.ObservedAt, storage.DiscoveryRuleTable, storage.DiscoveryRuleColumns.SourceType, consts.SourceTrending, storage.DiscoveryRuleColumns.SourceKey, consts.TrendingAllDaily), asOf-7*metrics.Day, asOf).
		Order(storage.TrendingBatchRecordColumns.ObservedAt + " DESC, " + storage.TrendingBatchRecordColumns.ObservedSlot + " DESC").Find(&batches).Error; err != nil || len(batches) == 0 {
		return nil, err
	}
	var rows []storage.TrendingObservation
	if err := tx.Table(storage.TrendingObservationTable+" AS o").Select("o.*").Joins(`JOIN `+storage.TrendingBatchRecordTable+` b
ON b.`+storage.TrendingBatchRecordColumns.SourceKey+`=o.`+storage.TrendingObservationColumns.SourceKey+` AND b.`+storage.TrendingBatchRecordColumns.Window+`=o.`+storage.TrendingObservationColumns.Window+` AND b.`+storage.TrendingBatchRecordColumns.ObservedSlot+`=o.`+storage.TrendingObservationColumns.ObservedSlot).
		Where("b."+storage.TrendingBatchRecordColumns.SourceKey+"='"+consts.TrendingAll+"' AND b."+storage.TrendingBatchRecordColumns.Window+"='"+consts.PeriodDaily+"' AND b."+storage.TrendingBatchRecordColumns.ObservedAt+">? AND b."+storage.TrendingBatchRecordColumns.ObservedAt+"<=?", asOf-7*metrics.Day, asOf).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	members := make(map[int64][]metrics.RankedRepository)
	for _, row := range rows {
		members[row.ObservedSlot] = append(members[row.ObservedSlot], metrics.RankedRepository{RepoID: row.RepoID, Rank: row.Rank})
	}
	result := make([]metrics.TrendingBatch, 0, len(batches))
	for _, batch := range batches {
		result = append(result, metrics.TrendingBatch{ObservedAt: batch.ObservedAt, Members: members[batch.ObservedSlot]})
	}
	return result, nil
}
