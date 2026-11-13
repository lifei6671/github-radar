package storage

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
)

// AdmissionDecision 保存每个入口的最近决策；历史档位变更另存在观察轨迹。
type AdmissionDecision struct {
	RepoID        int64  `gorm:"primaryKey;autoIncrement:false"`
	Operation     string `gorm:"primaryKey"`
	State         string
	Reason        string
	Lane          string
	EvidenceJSON  string
	NextCheckAt   int64
	PolicyVersion int
	UpdatedAt     int64 `gorm:"autoUpdateTime:false"`
}

func (AdmissionDecision) TableName() string { return "repo_admission_decisions" }

// AdmissionDaily 保留每天最近一次决策，仓库清理后仍可统计历史漏判。
type AdmissionDaily struct {
	Day                 string `gorm:"primaryKey"`
	RepoID              int64  `gorm:"primaryKey;autoIncrement:false"`
	Operation           string `gorm:"primaryKey"`
	State, Reason, Lane string
	Stars               int
	AgeDays             *int
	DecidedAt           int64
}

func (AdmissionDaily) TableName() string { return "repo_admission_daily" }

// SaveAdmission 仅在决策或证据变化时覆盖，避免调度扫描重复写入。
func SaveAdmission(db *gorm.DB, id int64, operation string, d admission.Decision, e admission.Evidence, now time.Time) error {
	evidence, err := json.Marshal(e)
	if err != nil {
		return err
	}
	row := AdmissionDecision{RepoID: id, Operation: operation, State: d.State, Reason: d.Reason, Lane: d.Lane,
		EvidenceJSON: string(evidence), NextCheckAt: d.RetryAt, PolicyVersion: 1, UpdatedAt: now.UnixMilli()}
	result := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repo_id"}, {Name: "operation"}},
		DoUpdates: clause.AssignmentColumns([]string{"state", "reason", "lane", "evidence_json", "next_check_at", "policy_version", "updated_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "repo_admission_decisions.state <> excluded.state OR repo_admission_decisions.reason <> excluded.reason OR repo_admission_decisions.lane <> excluded.lane OR repo_admission_decisions.evidence_json <> excluded.evidence_json OR repo_admission_decisions.updated_at < ?", Vars: []any{max(now.Add(-time.Hour).UnixMilli(), admission.DayStart(now).UnixMilli())}}}},
	}).Create(&row)
	if result.Error != nil || result.RowsAffected == 0 {
		return result.Error
	}
	var repo Repository
	if err := db.Select("stars", "github_created_at").Find(&repo, "repo_id = ?", id).Error; err != nil {
		return err
	}
	daily := AdmissionDaily{Day: now.UTC().Format(time.DateOnly), RepoID: id, Operation: operation, State: d.State,
		Reason: d.Reason, Lane: d.Lane, Stars: repo.Stars, DecidedAt: now.UnixMilli()}
	if age, ok := admission.AgeDays(repo.GitHubCreatedAt, now); ok {
		daily.AgeDays = &age
	}
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&daily).Error
}

// IsPublic 与后台公开源条件保持一致，包括尚未确认恢复的 404。
func (r Repository) IsPublic() bool {
	return r.NameIsCurrent && !r.Blocked && !r.Disabled && r.FirstNotFoundAt == nil &&
		(r.State == consts.StatusActive || r.State == consts.StatusCandidate || r.State == consts.StatusPaused)
}

// AdmissionEvidence 读取精确本地窗口、官方爆发和完整全站日榜中的近期前列。
func AdmissionEvidence(db *gorm.DB, id int64, now time.Time) (admission.Evidence, error) {
	var evidence admission.Evidence
	var latest LatestMetric
	if err := db.Select("window_details_json", "breakout_json").Find(&latest, "repo_id = ?", id).Error; err != nil {
		return evidence, err
	}
	var windows map[string]metrics.Window
	if json.Unmarshal([]byte(latest.WindowDetailsJSON), &windows) == nil {
		w := windows[consts.Window24h]
		if w.Quality == metrics.Exact && w.StartAt != nil && w.EndAt != nil {
			evidence.Exact24h, evidence.StartAt, evidence.EndAt = w.Delta, *w.StartAt, *w.EndAt
		}
	}
	// 复采窗独立于榜单口径，可用于候选及暂停项目的准入。
	var probe AdmissionDecision
	if err := db.Select("evidence_json").Find(&probe, "repo_id = ? AND operation = ?", id, "probe").Error; err != nil {
		return evidence, err
	}
	var sampled admission.Evidence
	if json.Unmarshal([]byte(probe.EvidenceJSON), &sampled) == nil && sampled.EndAt > evidence.EndAt {
		evidence.Exact24h, evidence.StartAt, evidence.EndAt = sampled.Exact24h, sampled.StartAt, sampled.EndAt
	}
	var burst metrics.Breakout
	if json.Unmarshal([]byte(latest.BreakoutJSON), &burst) == nil {
		evidence.BurstAt, evidence.Source = burst.At, burst.Source
	}
	var official RepoStarHistoryStatus
	if err := db.Select("breakout_at").Find(&official, "repo_id = ? AND breakout_at BETWEEN ? AND ?",
		id, now.Add(-48*time.Hour).UnixMilli(), now.UnixMilli()).Error; err != nil {
		return evidence, err
	}
	if official.BreakoutAt != nil && *official.BreakoutAt > evidence.BurstAt {
		evidence.BurstAt, evidence.Source = *official.BreakoutAt, metrics.BreakoutGitHub
	}
	var trending TrendingBatchRecord
	err := StrongTrending(db, now).Select("b.observed_at").Where("o.repo_id = ?", id).
		Order("b.observed_at DESC").Limit(1).Find(&trending).Error
	evidence.Trending = trending.ObservedAt
	if err != nil {
		return evidence, err
	}
	var shared struct{ At int64 }
	err = db.Table("repo_discovery_events").Select("COALESCE(MAX(first_observed_at),0) AS at").
		Where("repo_id = ? AND source_url <> '' AND first_observed_at BETWEEN ? AND ?",
			id, now.Add(-7*24*time.Hour).UnixMilli(), now.UnixMilli()).Scan(&shared).Error
	evidence.SharedAt = shared.At
	return evidence, err
}

// StrongTrending 仅使用已完成的全站日榜，语言榜或未完成的抓取不能直接晋级。
func StrongTrending(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Table(TrendingBatchRecordTable+" b").
		Joins("JOIN "+TrendingObservationTable+" o ON o.source_key = b.source_key AND o.window = b.window AND o.observed_slot = b.observed_slot").
		Where("o.rank BETWEEN 1 AND 10 AND o.source_key = ? AND o.window = ?", consts.TrendingAll, consts.PeriodDaily).
		Where("b.observed_at BETWEEN ? AND ?", now.Add(-36*time.Hour).UnixMilli(), now.UnixMilli())
}

// AIDecision 只读现有公开元数据；README 检查不主动请求 GitHub。
func AIDecision(db *gorm.DB, repo Repository, operation string, manual bool, now time.Time) (admission.Decision, admission.Evidence, error) {
	in := admission.Input{ID: repo.RepoID, Name: repo.Name, Stars: repo.Stars, CreatedAt: repo.GitHubCreatedAt, PushedAt: repo.GitHubPushedAt,
		Public: repo.IsPublic(), Followed: repo.StarredByUser, Manual: manual, Derivative: repo.DerivedProject}
	if !in.Public || manual {
		return admission.AIEligible(in, operation, now), in.Evidence, nil
	}
	if repo.Description != nil {
		in.Description = *repo.Description
	}
	var err error
	in.Evidence, err = AdmissionEvidence(db, repo.RepoID, now)
	if err != nil {
		return admission.Decision{}, in.Evidence, err
	}
	if !admission.Readable(in.Description, repo.Name) {
		var document RepoReadmeDocument
		if err := db.Select("source_markdown").Find(&document, "repo_id = ? AND source_repo_name = ?", repo.RepoID, repo.FullName).Error; err != nil {
			return admission.Decision{}, in.Evidence, err
		}
		in.Readme = string(document.SourceMarkdown[:min(len(document.SourceMarkdown), 8192)])
	}
	var summary AISummary
	if err := db.Select("repo_id").Find(&summary, "repo_id = ? AND summary_status = ? AND summary_zh <> ''", repo.RepoID, consts.StatusReady).Error; err != nil {
		return admission.Decision{}, in.Evidence, err
	}
	in.HasSummary = summary.RepoID != 0
	age, known := admission.AgeDays(repo.GitHubCreatedAt, now)
	in.Valuable = known && age <= 30 && repo.Stars >= 20 ||
		repo.Stars >= 1000 && repo.GitHubPushedAt != nil && *repo.GitHubPushedAt >= now.Add(-15*24*time.Hour).UnixMilli() && *repo.GitHubPushedAt <= now.UnixMilli()
	if !in.Valuable {
		var topic TopicCandidate
		if err := db.Select("id").Limit(1).Find(&topic, "repo_id = ? AND status IN ? AND updated_at >= ?",
			repo.RepoID, []string{consts.StatusCandidate, consts.StatusShortlisted}, now.Add(-7*24*time.Hour).UnixMilli()).Error; err != nil {
			return admission.Decision{}, in.Evidence, err
		}
		in.Valuable = topic.ID != 0
	}
	return admission.AIEligible(in, operation, now), in.Evidence, nil
}
