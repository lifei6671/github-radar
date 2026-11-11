package topics

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

// Add 幂等加入选题池。已有选题保留人工状态，重新打开由选题状态接口处理。
func Add(ctx context.Context, db *gorm.DB, id int64, now time.Time) (storage.TopicCandidate, error) {
	var candidate storage.TopicCandidate
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Find(&candidate, "repo_id=? AND angle_key='project'", id).Error; err != nil {
			return err
		}
		if candidate.ID != 0 {
			if candidate.ReviewedAt != nil {
				return nil
			}
			// 已自动入池的项目也可被人工保留，不改变审核状态。
			ms := now.UnixMilli()
			candidate.ReviewedAt = &ms
			return tx.Model(&candidate).Update("reviewed_at", ms).Error
		}
		var row input
		result := tx.Raw(repoSignalsSQL+"WHERE r.repo_id=?", now.Add(-signalAge).UnixMilli(), id).Scan(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := save(tx, option{input: row, score: topicScore(row, now)}, 4, now.UnixMilli()); err != nil {
			return err
		}
		// 人工加入属于明确的处理记录，普通候选清理不会删除它。
		if err := tx.Model(&storage.TopicCandidate{}).Where("repo_id=? AND angle_key='project'", id).
			Update("reviewed_at", now.UnixMilli()).Error; err != nil {
			return err
		}
		return tx.Take(&candidate, "repo_id=? AND angle_key='project'", id).Error
	})
	return candidate, err
}
