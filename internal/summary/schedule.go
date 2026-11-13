package summary

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
)

var topReposSQL = fmt.Sprintf(`SELECT m.%[1]s FROM %[2]s m JOIN %[3]s r ON r.%[4]s=m.%[1]s
WHERE r.%[5]s='%[6]s' AND m.%[7]s IS NOT NULL ORDER BY m.%[7]s DESC,m.%[1]s LIMIT ?`,
	storage.LatestMetricColumns.RepoID, storage.LatestMetricTable, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.RepositoryColumns.State, consts.StatusActive, storage.LatestMetricColumns.HotnessScore)

var recentTopicSQL = `SELECT 1 FROM ` + storage.TopicCandidateTable + ` t WHERE t.` + storage.TopicCandidateColumns.RepoID + `=r.` + storage.RepositoryColumns.RepoID + `
AND t.` + storage.TopicCandidateColumns.Status + ` IN ('` + consts.StatusCandidate + `','` + consts.StatusShortlisted + `') AND t.` + storage.TopicCandidateColumns.UpdatedAt + `>=?`

// Schedule 将初次发现和到期的来源检查写入队列，已有未决任务由原 Worker 处理。
func (q Queue) Schedule(ctx context.Context, now time.Time) error {
	ms := now.UnixMilli()
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		paused, err := storage.BooleanSetting(ctx, tx, storage.AutoAIResumeKey)
		if err != nil || paused {
			return err
		}
		// 补齐主题词使用剩余自动额度，避免为低优先级仓库堆积等待额度的任务。
		var topicSlots int
		start := now.UTC().Truncate(24 * time.Hour)
		if err := tx.Raw(fmt.Sprintf(`
SELECT max(0, ? - (SELECT count(*) FROM %[1]s WHERE %[2]s='%[3]s' AND %[4]s>=? AND %[4]s<?)
  - (SELECT count(*) FROM %[5]s WHERE %[6]s IN (?,?) AND %[7]s<200 AND %[8]s<=?
     AND %[9]s IN ('%[10]s','%[11]s','%[12]s')))`,
			storage.AICallTable, storage.AICallColumns.Origin, consts.OriginAuto, storage.AICallColumns.ReservedAt, storage.JobTable, storage.JobColumns.Type, storage.JobColumns.Priority, storage.JobColumns.AvailableAt, storage.JobColumns.Status, consts.StatusPending, consts.StatusRunning, consts.StatusFailed),
			q.AI.Summary.AutoDailyCalls, start.UnixMilli(), start.Add(24*time.Hour).UnixMilli(), JobType, TranslationJob, ms).Scan(&topicSlots).Error; err != nil {
			return err
		}
		if topicSlots > 0 {
			if err := prioritizeThemeBackfill(tx, ms); err != nil {
				return err
			}
		}
		var rows []struct {
			RepoID        int64
			HighPriority  bool
			HasSummary    bool
			ThemeBackfill bool
		}
		nativeMatch := strings.ReplaceAll(storage.HasNativeThemeSQL, "repositories.topics_json", "r.topics_json")
		err = tx.Raw(`
WITH hot AS (`+topReposSQL+`),
eligible AS (
  SELECT r.`+storage.RepositoryColumns.RepoID+`,m.`+storage.LatestMetricColumns.HotnessScore+`,
    (json_array_length(r.topics_json)=0 OR (`+storage.HasTopicThemesSQL+` AND NOT `+nativeMatch+`)) AS needs_topics,
    (EXISTS (SELECT 1 FROM ai_summaries a WHERE a.repo_id=r.repo_id AND a.summary_status='ready')
     AND `+storage.HasTopicThemesSQL+` AND NOT `+nativeMatch+`
     AND NOT EXISTS (SELECT 1 FROM repo_ai_topics a WHERE a.repo_id=r.repo_id AND a.theme_catalog_key=`+storage.ThemeCatalogKeySQL+`)) AS theme_backfill,
    (r.`+storage.RepositoryColumns.Grade+` IN ('`+consts.GradeS+`','`+consts.GradeA+`') OR r.`+storage.RepositoryColumns.StarredByUser+`=1 OR r.`+storage.RepositoryColumns.RepoID+` IN (SELECT `+storage.LatestMetricColumns.RepoID+` FROM hot)
     OR EXISTS(`+recentTopicSQL+fmt.Sprintf(`)) AS high_priority
  FROM %[1]s r LEFT JOIN %[2]s m ON m.%[3]s=r.%[4]s
  WHERE r.%[7]s=0 AND r.%[8]s=0 AND r.%[9]s=1 AND r.first_not_found_at IS NULL
  AND (r.%[5]s='%[6]s' OR (r.%[5]s IN ('paused','candidate') AND EXISTS(SELECT 1 FROM %[13]s WHERE %[10]s=r.%[4]s)))
)
SELECT e.%[4]s AS repo_id,e.high_priority,s.%[10]s IS NOT NULL AS has_summary,e.theme_backfill FROM eligible e
LEFT JOIN %[11]s c ON c.%[12]s=e.%[4]s
LEFT JOIN %[13]s s ON s.%[10]s=e.%[4]s
LEFT JOIN repo_readme_documents d ON d.repo_id=e.%[4]s
WHERE ((e.high_priority AND ?>0) OR s.%[10]s IS NOT NULL OR (e.needs_topics AND ?>0))
AND (c.%[12]s IS NULL OR c.%[14]s<=?
 OR (?>0 AND e.theme_backfill AND c.%[15]s IS NULL)
 OR (c.%[15]s IS NULL AND ((d.repo_id IS NULL AND s.summary_status='ready') OR
     (d.repo_id IS NOT NULL AND d.readme_sha<>COALESCE(c.last_seen_readme_sha,''))))
 OR (?>0 AND e.high_priority AND c.%[15]s IS NULL AND s.%[10]s IS NOT NULL AND
     (s.%[16]s<>? OR s.%[17]s<>? OR s.%[18]s<>? OR s.%[19]s<>c.%[20]s)))
AND NOT EXISTS(SELECT 1 FROM %[21]s j WHERE j.%[22]s=e.%[4]s AND j.%[23]s=? AND j.%[24]s IN `,
			storage.RepositoryTable, storage.LatestMetricTable, storage.LatestMetricColumns.RepoID, storage.RepositoryColumns.RepoID,
			storage.RepositoryColumns.State, consts.StatusActive, storage.RepositoryColumns.Blocked, storage.RepositoryColumns.Disabled, storage.RepositoryColumns.NameIsCurrent,
			storage.AISummaryColumns.RepoID, storage.SummaryCheckTable, storage.SummaryCheckColumns.RepoID, storage.AISummaryTable,
			storage.SummaryCheckColumns.NextCheckAt, storage.SummaryCheckColumns.LastErrorCode,
			storage.AISummaryColumns.ModelProvider, storage.AISummaryColumns.ModelName, storage.AISummaryColumns.PromptVersion,
			storage.AISummaryColumns.SourceFingerprint, storage.SummaryCheckColumns.SourceFingerprint,
			storage.JobTable, storage.JobColumns.RepoID, storage.JobColumns.Type, storage.JobColumns.Status)+unresolved+`)
ORDER BY e.theme_backfill DESC,(d.repo_id IS NULL AND s.summary_status='ready') DESC,e.high_priority DESC,(NOT e.high_priority AND s.`+storage.AISummaryColumns.RepoID+` IS NOT NULL) DESC,
 e.`+storage.LatestMetricColumns.HotnessScore+` DESC,e.`+storage.RepositoryColumns.RepoID+` LIMIT 100`,
			q.AI.Summary.HotRankLimit, now.Add(-24*time.Hour).UnixMilli(), q.AI.Summary.AutoDailyCalls, topicSlots, ms,
			topicSlots, q.AI.Summary.AutoDailyCalls, ProviderKey(q.AI), q.AI.Model, q.AI.Summary.PromptVersion, JobType).Scan(&rows).Error
		if err != nil {
			return err
		}
		translationsScheduled := false
		for _, row := range rows {
			topicsOnly := !row.HighPriority && !row.HasSummary
			if topicsOnly && !translationsScheduled && topicSlots > 0 {
				// 为译文留出一部分余量，避免长尾主题词占用全部可用名额。
				count, err := q.scheduleTranslations(tx, ms, min(topicSlots, max(1, q.AI.Summary.AutoDailyCalls/10)))
				if err != nil {
					return err
				}
				topicSlots -= count
				translationsScheduled = true
			}
			if topicsOnly && topicSlots <= 0 {
				break
			}
			priority := 40
			if row.ThemeBackfill && topicSlots > 0 {
				priority = 120
			} else if row.HighPriority {
				priority = 100
			} else if topicsOnly {
				priority = 20
			}
			topicSlots--
			key := fmt.Sprintf(consts.SummaryAutoDedupFormat, row.RepoID, ms)
			if _, err := q.insert(tx, JobType, row.RepoID, key, payload{}, priority, ms); err != nil {
				return err
			}
		}
		if !translationsScheduled && topicSlots > 0 {
			_, err := q.scheduleTranslations(tx, ms, topicSlots)
			return err
		}
		return nil
	})
}

func prioritizeThemeBackfill(tx *gorm.DB, now int64) error {
	// 已有待重试的导读任务沿用原身份。仅当 SQLite 有匹配版本的正文时，提前唤醒等待 GitHub 本地额度的任务。
	return tx.Exec(`
WITH backfill AS (
 SELECT jobs.id, (d.repo_id IS NOT NULL AND c.last_seen_readme_sha=d.readme_sha) AS cached
 FROM jobs JOIN repositories ON repositories.repo_id=jobs.repo_id
 JOIN ai_summaries s ON s.repo_id=jobs.repo_id AND s.summary_status='ready'
 LEFT JOIN repo_readme_documents d ON d.repo_id=s.repo_id AND d.readme_sha=s.readme_sha AND d.complete=1
 LEFT JOIN summary_checks c ON c.repo_id=s.repo_id
 WHERE jobs.type=? AND jobs.status IN ('pending','failed') AND jobs.priority<200 AND `+storage.HasTopicThemesSQL+`
 AND `+storage.PublicRepositoryCondition+` AND NOT `+storage.HasNativeThemeSQL+`
 AND NOT EXISTS (SELECT 1 FROM repo_ai_topics a WHERE a.repo_id=jobs.repo_id AND a.theme_catalog_key=`+storage.ThemeCatalogKeySQL+`)
)
UPDATE jobs SET priority=max(priority,120),
 available_at=CASE WHEN last_error_code=? AND id IN (SELECT id FROM backfill WHERE cached)
                   THEN min(available_at,?) ELSE available_at END
WHERE id IN (SELECT id FROM backfill)
 AND (priority<120 OR (last_error_code=? AND available_at>? AND id IN (SELECT id FROM backfill WHERE cached)))`,
		JobType, consts.CodeGitHubLocalBudget, now, consts.CodeGitHubLocalBudget, now).Error
}

func (q Queue) eligible(ctx context.Context, repo storage.Repository, now time.Time) (bool, error) {
	if repo.State != consts.StatusActive || q.AI.Summary.AutoDailyCalls == 0 {
		return false, nil
	}
	if repo.Grade == consts.GradeS || repo.Grade == consts.GradeA || repo.StarredByUser {
		return true, nil
	}
	var ok bool
	err := q.DB.WithContext(ctx).Raw(`
SELECT EXISTS(SELECT 1 FROM `+storage.RepositoryTable+` r WHERE r.`+storage.RepositoryColumns.RepoID+`=? AND
  (r.`+storage.RepositoryColumns.RepoID+` IN (`+topReposSQL+`) OR EXISTS(`+recentTopicSQL+`)))`,
		repo.RepoID, q.AI.Summary.HotRankLimit, now.Add(-24*time.Hour).UnixMilli()).Scan(&ok).Error
	return ok, err
}

func (q Queue) nextCheck(repo storage.Repository, now time.Time) int64 {
	hours := q.AI.Summary.LowCheckHours
	switch {
	case repo.StarredByUser || repo.Grade == consts.GradeS || repo.Grade == consts.GradeA:
		hours = q.AI.Summary.HighCheckHours
	case repo.Grade == consts.GradeB:
		hours = q.AI.Summary.NormalCheckHours
	}
	return now.Add(time.Duration(hours) * time.Hour).UnixMilli()
}
