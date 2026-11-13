package storage

import (
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
)

// DiscoveryRuleRun 保留计划快照，即使管理员随后修改或删除规则，历史仍可查询。
type DiscoveryRuleRun struct {
	ID              int64
	RuleID          int64
	SourceType      string
	SourceKey       string
	PlannedAt       int64
	Pass            string
	CreatedFrom     int64
	CreatedUntil    int64
	Query           string
	MinStars        int
	StartedAt       int64
	FinishedAt      int64
	UpstreamTotal   *int
	PlannedShards   int
	CompletedShards int
	Status          string
	ErrorCode       *string
}

func (DiscoveryRuleRun) TableName() string { return "discovery_rule_runs" }

// DiscoveryRuleRunRepo 只在一轮内按数字 ID 计数，跨页和跨分片重复命中不会累加。
type DiscoveryRuleRunRepo struct {
	RunID           int64 `gorm:"primaryKey;autoIncrement:false"`
	RepoID          int64 `gorm:"primaryKey;autoIncrement:false"`
	NewlyDiscovered bool
}

func (DiscoveryRuleRunRepo) TableName() string { return "discovery_rule_run_repos" }

// SettleDiscoveryRun 根据当前事务内所有子任务推导整轮状态。失败时保留部分结果。
func SettleDiscoveryRun(tx *gorm.DB, id *int64, now int64) error {
	if id == nil {
		return nil
	}
	type group struct {
		Status string
		Count  int
	}
	var groups []group
	if err := tx.Model(&Job{}).Select("status, COUNT(*) AS count").Where("discovery_run_id = ?", *id).Group("status").Scan(&groups).Error; err != nil {
		return err
	}
	counts := make(map[string]int, len(groups))
	for _, row := range groups {
		counts[row.Status] = row.Count
	}
	status, finished := consts.StatusSucceeded, now
	switch {
	case counts[consts.StatusFailed] > 0:
		status, finished = consts.StatusFailed, 0
	case counts[consts.StatusPending]+counts[consts.StatusRunning] > 0:
		status, finished = consts.StatusRunning, 0
	case counts[consts.StatusCanceled] > 0:
		status = consts.StatusCanceled
	case counts[consts.StatusDead] > 0:
		status = consts.StatusDead
	}
	var issue Job
	if status != consts.StatusSucceeded {
		query := tx.Select("last_error_code").Where("discovery_run_id = ? AND last_error_code IS NOT NULL", *id)
		if status != consts.StatusRunning {
			query = query.Where("status = ?", status)
		}
		if err := query.
			Order("updated_at DESC, id DESC").Limit(1).Find(&issue).Error; err != nil {
			return err
		}
	}
	return tx.Model(&DiscoveryRuleRun{}).Where("id = ?", *id).
		Updates(map[string]any{"status": status, "finished_at": finished, "error_code": issue.LastErrorCode}).Error
}
