package summary

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

const topReposSQL = `SELECT m.repo_id FROM latest_metrics m JOIN repositories r ON r.repo_id=m.repo_id
WHERE r.state='active' AND m.hotness_score IS NOT NULL ORDER BY m.hotness_score DESC,m.repo_id LIMIT ?`

const recentTopicSQL = `SELECT 1 FROM topic_candidates t WHERE t.repo_id=r.repo_id
AND t.status IN ('candidate','shortlisted') AND t.updated_at>=?`

// Schedule 将初次发现和到期的来源检查写入队列，已有未决任务由原 Worker 处理。
func (q Queue) Schedule(ctx context.Context, now time.Time) error {
	ms := now.UnixMilli()
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			RepoID       int64
			HighPriority bool
		}
		err := tx.Raw(`
WITH hot AS (`+topReposSQL+`),
eligible AS (
  SELECT r.repo_id,m.hotness_score,
    (r.grade IN ('S','A') OR r.starred_by_user=1 OR r.repo_id IN (SELECT repo_id FROM hot)
     OR EXISTS(`+recentTopicSQL+`)) AS high_priority
  FROM repositories r LEFT JOIN latest_metrics m ON m.repo_id=r.repo_id
  WHERE r.state='active' AND r.disabled=0 AND r.name_is_current=1
)
SELECT e.repo_id,e.high_priority FROM eligible e
LEFT JOIN summary_checks c ON c.repo_id=e.repo_id
LEFT JOIN ai_summaries s ON s.repo_id=e.repo_id
WHERE ((e.high_priority AND ?>0) OR s.repo_id IS NOT NULL)
AND (c.repo_id IS NULL OR c.next_check_at<=?
 OR (e.high_priority AND c.last_error_code IS NULL AND s.repo_id IS NOT NULL AND
     (s.model_provider<>? OR s.model_name<>? OR s.prompt_version<>? OR s.source_fingerprint<>c.source_fingerprint)))
AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.repo_id=e.repo_id AND j.type=? AND j.status IN `+unresolved+`)
ORDER BY e.high_priority DESC,e.hotness_score DESC,e.repo_id LIMIT 100`,
			q.AI.Summary.HotRankLimit, now.Add(-24*time.Hour).UnixMilli(), q.AI.Summary.AutoDailyCalls, ms,
			ProviderKey(q.AI), q.AI.Model, q.AI.Summary.PromptVersion, JobType).Scan(&rows).Error
		if err != nil {
			return err
		}
		for _, row := range rows {
			priority := 40
			if row.HighPriority {
				priority = 100
			}
			key := fmt.Sprintf("auto:summary:%d:%d", row.RepoID, ms)
			if _, err := q.insert(tx, row.RepoID, key, payload{}, priority, ms); err != nil {
				return err
			}
		}
		return nil
	})
}

func (q Queue) eligible(ctx context.Context, repo storage.Repository, now time.Time) (bool, error) {
	if repo.State != "active" || q.AI.Summary.AutoDailyCalls == 0 {
		return false, nil
	}
	if repo.Grade == "S" || repo.Grade == "A" || repo.StarredByUser {
		return true, nil
	}
	var ok bool
	err := q.DB.WithContext(ctx).Raw(`
SELECT EXISTS(SELECT 1 FROM repositories r WHERE r.repo_id=? AND
  (r.repo_id IN (`+topReposSQL+`) OR EXISTS(`+recentTopicSQL+`)))`,
		repo.RepoID, q.AI.Summary.HotRankLimit, now.Add(-24*time.Hour).UnixMilli()).Scan(&ok).Error
	return ok, err
}

func (q Queue) nextCheck(repo storage.Repository, now time.Time) int64 {
	hours := q.AI.Summary.LowCheckHours
	switch {
	case repo.StarredByUser || repo.Grade == "S" || repo.Grade == "A":
		hours = q.AI.Summary.HighCheckHours
	case repo.Grade == "B":
		hours = q.AI.Summary.NormalCheckHours
	}
	return now.Add(time.Duration(hours) * time.Hour).UnixMilli()
}
