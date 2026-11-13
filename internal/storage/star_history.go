package storage

import (
	"encoding/json"

	"github.com/lifei6671/github-radar/internal/domain/metrics"
)

// RepoStarHistoryWeek 与本地采样独立，按上游周首更新七日计数。
type RepoStarHistoryWeek struct {
	RepoID        int64 `gorm:"primaryKey;autoIncrement:false"`
	WeekTimestamp int64 `gorm:"primaryKey;autoIncrement:false"`
	DaysJSON      string
	FetchedAt     int64
}

func (RepoStarHistoryWeek) TableName() string { return "repo_star_history_weeks" }

// Decode 将存储数据转回领域输入，保留缺测值。
func (w RepoStarHistoryWeek) Decode() (metrics.StarHistoryWeekData, error) {
	data := metrics.StarHistoryWeekData{Week: w.WeekTimestamp, FetchedAt: w.FetchedAt}
	err := json.Unmarshal([]byte(w.DaysJSON), &data.Days)
	return data, err
}

// RepoStarHistoryStatus 分开保存最近成功及失败，便于失败时继续展示已有历史。
type RepoStarHistoryStatus struct {
	RepoID        int64 `gorm:"primaryKey;autoIncrement:false"`
	CheckedAt     int64
	FetchedAt     int64
	LastErrorCode *string
	BreakoutAt    *int64
	ConfirmedAt   *int64
	BreakoutJSON  string
}

func (RepoStarHistoryStatus) TableName() string { return "repo_star_history_status" }
