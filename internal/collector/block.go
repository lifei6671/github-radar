package collector

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

// SetBlocked 按 GitHub 数字 ID 持久化屏蔽，并撤销尚未完成任务的写入资格。
func (q Queue) SetBlocked(ctx context.Context, id int64, blocked bool, now time.Time) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Take(&repo, "repo_id=?", id).Error; err != nil {
			return err
		}
		if repo.Blocked == blocked {
			return nil
		}
		at := now.UnixMilli()
		values := map[string]any{"blocked": blocked, "updated_at": at}
		if blocked && repo.State == "active" {
			values["state"] = "paused"
		}
		if !blocked {
			values["next_fetch_at"] = at
		}
		if err := tx.Model(&repo).Updates(values).Error; err != nil {
			return err
		}
		if !blocked {
			repo.Blocked = false
			return q.admit(tx, &repo, at)
		}

		unfinished := tx.Model(&storage.Job{}).Select("id").Where(
			"repo_id=? AND type IN ? AND status IN ('pending','running','failed')", id,
			[]string{"refresh_repo", "refresh_readme_summary", "translate_readme"})
		// 请求已经发出的 AI 台账保留为待确认，解除屏蔽后仍需人工确认可能的计费。
		if err := tx.Model(&storage.AICall{}).Where("job_id IN (?) AND status='sending'", unfinished).
			Updates(map[string]any{"status": "unknown", "error_code": "ai_delivery_unknown", "updated_at": at}).Error; err != nil {
			return err
		}
		if err := tx.Model(&storage.AICall{}).Where("job_id IN (?) AND status='reserved'", unfinished).
			Updates(map[string]any{"status": "failed", "error_code": "repo_blocked", "updated_at": at}).Error; err != nil {
			return err
		}
		const sent = "EXISTS(SELECT 1 FROM ai_calls WHERE job_id=jobs.id AND status='unknown')"
		return tx.Model(&storage.Job{}).Where("id IN (?)", unfinished).Updates(map[string]any{
			"status":          gorm.Expr("CASE WHEN " + sent + " THEN 'unknown' ELSE 'canceled' END"),
			"last_error_code": gorm.Expr("CASE WHEN " + sent + " THEN 'ai_delivery_unknown' ELSE 'repo_blocked' END"),
			"lease_token":     nil, "lease_until": nil, "updated_at": at,
		}).Error
	})
}
