package api

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/storage"
)

// discoveryRuns 返回受管理员权限保护的运行记录和按规则判断的异常。
func (a *collectionAPI) discoveryRuns(c *gin.Context) {
	limit, ok := pageLimit(c)
	if !ok {
		return
	}
	sourceType, sourceKey := c.Query("sourceType"), c.Query("sourceKey")
	if (sourceType == "") != (sourceKey == "") || sourceType != "" && sourceType != consts.SourceSearch && sourceType != consts.SourceTrending {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidFilter})
		return
	}
	items, alerts := []gin.H{}, []gin.H{}
	now := time.Now().UnixMilli()
	err := a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		filter := func(query *gorm.DB) *gorm.DB {
			if sourceKey != "" {
				return query.Where("source_type = ? AND source_key = ?", sourceType, sourceKey)
			}
			return query
		}
		var runs []storage.DiscoveryRuleRun
		if err := filter(tx.Model(&storage.DiscoveryRuleRun{})).Order("planned_at DESC, id DESC").Limit(limit).Find(&runs).Error; err != nil {
			return err
		}
		var ids []int64
		for _, row := range runs {
			ids = append(ids, row.ID)
		}
		type totals struct{ RunID, UniqueCount, NewCount int64 }
		var hits []totals
		if len(ids) > 0 {
			if err := tx.Model(&storage.DiscoveryRuleRunRepo{}).
				Select("run_id, COUNT(*) AS unique_count, SUM(newly_discovered) AS new_count").
				Where("run_id IN ?", ids).Group("run_id").Scan(&hits).Error; err != nil {
				return err
			}
		}
		counts := make(map[int64]totals, len(hits))
		for _, hit := range hits {
			counts[hit.RunID] = hit
		}
		for _, row := range runs {
			var delay *int64
			if row.StartedAt > 0 {
				delay = new(max(0, row.StartedAt-row.PlannedAt))
			} else if row.FinishedAt == 0 {
				delay = new(max(0, now-row.PlannedAt))
			}
			count := counts[row.ID]
			items = append(items, gin.H{
				"id": row.ID, "sourceType": row.SourceType, "sourceKey": row.SourceKey, "pass": row.Pass,
				"plannedAt": iso(row.PlannedAt), "startedAt": discoveryTime(row.StartedAt), "finishedAt": discoveryTime(row.FinishedAt),
				"createdFrom": discoveryTime(row.CreatedFrom), "createdUntil": discoveryTime(row.CreatedUntil), "delayMs": delay,
				"upstreamTotal": row.UpstreamTotal, "plannedShards": row.PlannedShards, "completedShards": row.CompletedShards,
				"uniqueRepos": count.UniqueCount, "newRepos": count.NewCount, "status": row.Status, "errorCode": row.ErrorCode,
				"complete": row.Status == consts.StatusSucceeded && row.CompletedShards == row.PlannedShards,
			})
		}
		var rules []storage.DiscoveryRule
		if err := filter(tx.Model(&storage.DiscoveryRule{})).Where("enabled = ?", true).Order("display_position, id").Find(&rules).Error; err != nil {
			return err
		}
		var rates []storage.RateLimit
		if err := tx.Where("resource IN ?", []string{"search", "github_secondary"}).Find(&rates).Error; err != nil {
			return err
		}
		searchPaused := false
		for _, rate := range rates {
			searchPaused = searchPaused || rate.PausedUntil != nil && *rate.PausedUntil > now ||
				rate.Resource == "search" && rate.ResetAt > now && rate.Remaining == 0
		}
		for _, rule := range rules {
			reason, err := discoveryRunAlert(tx, rule, now, searchPaused)
			if err != nil {
				return err
			}
			if reason != "" {
				alerts = append(alerts, gin.H{"sourceType": rule.SourceType, "sourceKey": rule.SourceKey, "reason": reason})
			}
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "alerts": alerts})
}

func discoveryTime(value int64) *string {
	if value == 0 {
		return nil
	}
	return optionalISO(&value)
}

// 只提示连续三轮失败、持续十五分钟的限额等待或两轮周期未开工（最短一小时）。
func discoveryRunAlert(tx *gorm.DB, rule storage.DiscoveryRule, now int64, searchPaused bool) (string, error) {
	var latest []storage.DiscoveryRuleRun
	if err := tx.Where("rule_id = ? AND pass = ? AND status <> ?", rule.ID, collection.DiscoveryPrimary, consts.StatusCanceled).
		Order("planned_at DESC").Limit(3).Find(&latest).Error; err != nil {
		return "", err
	}
	var waiting []storage.DiscoveryRuleRun
	if err := tx.Where("rule_id = ? AND finished_at = 0 AND planned_at <= ?", rule.ID, now-int64(15*time.Minute/time.Millisecond)).
		Order("planned_at DESC").Find(&waiting).Error; err != nil {
		return "", err
	}
	for _, run := range waiting {
		if run.ErrorCode != nil {
			switch *run.ErrorCode {
			case consts.CodeGitHubPrimaryLimit, consts.CodeGitHubSecondaryLimit, consts.CodeGitHubLocalBudget, consts.CodeGitHubCoreReserve:
				return "quota_backlog", nil
			}
		}
		if searchPaused && rule.SourceType == consts.SourceSearch {
			return "quota_backlog", nil
		}
	}
	incomplete := func(run storage.DiscoveryRuleRun) bool {
		return run.FinishedAt > 0 && (run.Status != consts.StatusSucceeded || run.CompletedShards < run.PlannedShards)
	}
	if len(latest) == 3 && incomplete(latest[0]) && incomplete(latest[1]) && incomplete(latest[2]) {
		return "consecutive_failures", nil
	}
	threshold := max(int64(time.Hour/time.Millisecond), int64(rule.IntervalMinutes)*2*int64(time.Minute/time.Millisecond))
	baseline := rule.NextRunAt
	for _, run := range latest {
		if run.StartedAt > 0 {
			baseline = min(baseline, run.StartedAt)
			break
		}
		baseline = min(baseline, run.PlannedAt)
	}
	if baseline <= now-threshold {
		return "not_run", nil
	}
	return "", nil
}
