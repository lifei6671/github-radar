package storage

import (
	"encoding/json"
	"errors"
)

var ErrRepositoryBlocked = errors.New("仓库已屏蔽")

// Repository 保存仓库身份、当前元数据及采样调度状态。
// 可空字段对应 SQL NULL，所有时间值使用 UTC Unix 毫秒。
type Repository struct {
	RepoID           int64   `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	Owner            string  `gorm:"column:owner"`
	Name             string  `gorm:"column:name"`
	FullName         string  `gorm:"column:full_name"`
	NameIsCurrent    bool    `gorm:"column:name_is_current"`
	HTMLURL          string  `gorm:"column:html_url"`
	Description      *string `gorm:"column:description"`
	TopicsJSON       string  `gorm:"column:topics_json"`
	PrimaryLanguage  *string `gorm:"column:primary_language"`
	LicenseSPDXID    *string `gorm:"column:license_spdx_id"`
	LicenseName      *string `gorm:"column:license_name"`
	Stars            int     `gorm:"column:stars"`
	Forks            int     `gorm:"column:forks"`
	Archived         bool    `gorm:"column:archived"`
	Disabled         bool    `gorm:"column:disabled"`
	Blocked          bool    `gorm:"column:blocked"`
	Grade            string  `gorm:"column:grade"`
	BelowGradeSince  *int64  `gorm:"column:below_grade_since"`
	StarredByUser    bool    `gorm:"column:starred_by_user"`
	FollowedAt       *int64  `gorm:"column:followed_at"`
	State            string  `gorm:"column:state"`
	GitHubCreatedAt  *int64  `gorm:"column:github_created_at"`
	GitHubPushedAt   *int64  `gorm:"column:github_pushed_at"`
	DiscoveredAt     int64   `gorm:"column:discovered_at"`
	LastDiscoveredAt int64   `gorm:"column:last_discovered_at"`
	LastCheckedAt    *int64  `gorm:"column:last_checked_at"`
	LastSuccessAt    *int64  `gorm:"column:last_success_at"`
	FirstNotFoundAt  *int64  `gorm:"column:first_not_found_at"`
	NextFetchAt      int64   `gorm:"column:next_fetch_at"`
	MetadataETag     *string `gorm:"column:metadata_etag"`
	UpdatedAt        int64   `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (Repository) TableName() string { return RepositoryTable }

// GitHubTopics 返回 GitHub 元数据中的原生主题词。
func (r Repository) GitHubTopics() []string {
	var topics []string
	_ = json.Unmarshal([]byte(r.TopicsJSON), &topics)
	return topics
}

// RepoSnapshot 的复合主键保证同仓库同槽只保留第一条成功观测。
type RepoSnapshot struct {
	RepoID     int64 `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	SampleSlot int64 `gorm:"column:sample_slot;primaryKey;autoIncrement:false"`
	ObservedAt int64 `gorm:"column:observed_at"`
	Stars      int   `gorm:"column:stars"`
	Forks      int   `gorm:"column:forks"`
}

func (RepoSnapshot) TableName() string { return RepoSnapshotTable }

// TrendingObservation 和 SearchObservation 各自保存发现来源。
type TrendingObservation struct {
	RepoID        int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	SourceKey     string `gorm:"column:source_key;primaryKey"`
	Window        string `gorm:"column:window;primaryKey"`
	ObservedSlot  int64  `gorm:"column:observed_slot;primaryKey;autoIncrement:false"`
	Rank          int    `gorm:"column:rank"`
	ReportedStars *int   `gorm:"column:reported_stars"`
}

func (TrendingObservation) TableName() string { return TrendingObservationTable }

type SearchObservation struct {
	RepoID       int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	SourceKey    string `gorm:"column:source_key;primaryKey"`
	ObservedSlot int64  `gorm:"column:observed_slot;primaryKey;autoIncrement:false"`
	ObservedAt   int64  `gorm:"column:observed_at"`
}

func (SearchObservation) TableName() string { return SearchObservationTable }

// DiscoveryRule 在数据库内保存来源的下次计划时间。
type DiscoveryRule struct {
	ID              int64  `gorm:"column:id;primaryKey;autoIncrement"`
	SourceType      string `gorm:"column:source_type"`
	SourceKey       string `gorm:"column:source_key"`
	Query           string `gorm:"column:query"`
	Enabled         bool   `gorm:"column:enabled"`
	IntervalMinutes int    `gorm:"column:interval_minutes"`
	NextRunAt       int64  `gorm:"column:next_run_at"`
	Priority        int    `gorm:"column:priority"`
	DisplayPosition int    `gorm:"column:display_position"`
}

func (DiscoveryRule) TableName() string { return DiscoveryRuleTable }

// Job 保存计划、重试及租约，PayloadJSON 不包含上游正文或凭据。
type Job struct {
	ID            int64   `gorm:"column:id;primaryKey;autoIncrement"`
	Type          string  `gorm:"column:type"`
	RepoID        *int64  `gorm:"column:repo_id"`
	DedupKey      string  `gorm:"column:dedup_key"`
	PayloadJSON   string  `gorm:"column:payload_json"`
	Priority      int     `gorm:"column:priority"`
	Status        string  `gorm:"column:status"`
	AvailableAt   int64   `gorm:"column:available_at"`
	LeaseUntil    *int64  `gorm:"column:lease_until"`
	LeaseToken    *string `gorm:"column:lease_token"`
	Attempts      int     `gorm:"column:attempts"`
	MaxAttempts   int     `gorm:"column:max_attempts"`
	LastErrorCode *string `gorm:"column:last_error_code"`
	CreatedAt     int64   `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt     int64   `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (Job) TableName() string { return JobTable }

// RateLimit 保存上游窗口及本地请求计数，服务重启后继续遵循限额。
type RateLimit struct {
	Resource        string `gorm:"column:resource;primaryKey"`
	LimitTotal      int    `gorm:"column:limit_total"`
	Remaining       int    `gorm:"column:remaining"`
	ResetAt         int64  `gorm:"column:reset_at"`
	PausedUntil     *int64 `gorm:"column:paused_until"`
	ObservedAt      int64  `gorm:"column:observed_at"`
	LocalWindow     int64  `gorm:"column:local_window"`
	LocalRequests   int    `gorm:"column:local_requests"`
	LimitedCount    int    `gorm:"column:limited_count"`
	SecondaryStreak int    `gorm:"column:secondary_streak"`
}

func (RateLimit) TableName() string { return RateLimitTable }

// MaintenanceState 保存最新完整 Trending 批次及调度水位。
type MaintenanceState struct {
	Key       string `gorm:"column:key;primaryKey"`
	Value     string `gorm:"column:value"`
	UpdatedAt int64  `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (MaintenanceState) TableName() string { return MaintenanceStateTable }

var MaintenanceStateColumns = struct {
	Key       string
	Value     string
	UpdatedAt string
}{
	Key:       "key",
	Value:     "value",
	UpdatedAt: "updated_at",
}
