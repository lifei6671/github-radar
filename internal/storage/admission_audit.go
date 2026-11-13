package storage

import (
	"cmp"
	"context"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
)

// AdmissionAudit 是单个 UTC 日的只读验收数据。日末采样口径与发布日界独立。
type AdmissionAudit struct {
	Day               string                    `json:"day"`
	Observed          bool                      `json:"observed"`
	Discovered        int64                     `json:"discovered"`
	ProbeSamples      int64                     `json:"probeSamples"`
	Monitoring        int64                     `json:"monitoringNow"`
	Monitored         *int64                    `json:"monitoringAtLastObservation,omitempty"`
	MonitoringEntries int64                     `json:"monitoringEntries"`
	MonitoringExits   int64                     `json:"monitoringExits"`
	FirstGuides       int64                     `json:"firstGuides"`
	CanceledAI        int64                     `json:"canceledAI"`
	FilteredRepos     int64                     `json:"filteredRepos"`
	Calls             []AdmissionCallTotals     `json:"calls"`
	Decisions         []AdmissionDecisionTotals `json:"decisions"`
	Trending          []AdmissionAuditSample    `json:"trending"`
	Deferred          []AdmissionAuditSample    `json:"deferredSample"`
}

// AdmissionDayTotal 按 UTC 日保存真实采样次数和最近一个监控席位读数。
type AdmissionDayTotal struct {
	Day          string `gorm:"primaryKey"`
	ProbeSamples int64
	Monitored    *int64
	ObservedAt   *int64
}

func (AdmissionDayTotal) TableName() string { return "admission_day_totals" }

// RecordProbeSample 仅由新快照写入事务调用，避免失败探测或同槽重复采样膨胀。
func RecordProbeSample(tx *gorm.DB, now time.Time) error {
	row := AdmissionDayTotal{Day: now.UTC().Format(time.DateOnly), ProbeSamples: 1}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "day"}},
		DoUpdates: clause.Assignments(map[string]any{"probe_samples": gorm.Expr("admission_day_totals.probe_samples + 1")})}).Create(&row).Error
}

// RecordMonitoringTotal 每轮指标成功后留下同事务的席位读数，缺失日期保持未知。
func RecordMonitoringTotal(tx *gorm.DB, now time.Time) error {
	var active int64
	if err := tx.Model(&Repository{}).Where("state = ?", consts.StatusActive).Count(&active).Error; err != nil {
		return err
	}
	at := now.UnixMilli()
	row := AdmissionDayTotal{Day: now.UTC().Format(time.DateOnly), Monitored: &active, ObservedAt: &at}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "day"}}, DoUpdates: clause.AssignmentColumns([]string{"monitored", "observed_at"})}).Create(&row).Error
}

// RepoFirstGuide 的首次就绪事实独立于后续更新、失效或重新发布。
type RepoFirstGuide struct {
	RepoID  int64 `gorm:"primaryKey;autoIncrement:false"`
	ReadyAt int64
}

func (RepoFirstGuide) TableName() string { return "repo_first_guides" }

// RecordFirstGuide 和有效导读一起提交，失败或未复核的输出不会计入。
func RecordFirstGuide(tx *gorm.DB, id int64, at int64) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RepoFirstGuide{RepoID: id, ReadyAt: at}).Error
}

type AdmissionCallTotals struct {
	Origin, Lane, Operation, Provider, Model, Status                     string
	Requests, InputTokens, OutputTokens, WithUsage, WithCost, DurationMS int64
	EstimatedCost, UnsettledEstimate                                     float64
}

type AdmissionDecisionTotals struct {
	Operation, State, Reason, Lane string
	Repos                          int64
}

type AdmissionAuditSample struct {
	RepoID           int64  `json:"repoId"`
	FullName         string `json:"fullName,omitempty"`
	Stars            int    `json:"stars"`
	AgeDays          *int   `json:"ageDays,omitempty"`
	Result           string `json:"result"`
	SearchCoverage   string `json:"searchCoverage,omitempty"`
	Reason           string `json:"reason,omitempty"`
	MonitoringState  string `json:"monitoringState,omitempty"`
	MonitoringReason string `json:"monitoringReason,omitempty"`
}

// ReadAdmissionAudit 汇总发现、实际探测、首篇导读及模型使用；每层单独统计。
func ReadAdmissionAudit(ctx context.Context, db *gorm.DB, day time.Time) (AdmissionAudit, error) {
	db = db.WithContext(ctx)
	day = admission.DayStart(day)
	start, end := day.UnixMilli(), day.Add(24*time.Hour).UnixMilli()
	out := AdmissionAudit{Day: day.Format(time.DateOnly)}
	var counters AdmissionDayTotal
	if err := db.Find(&counters, "day = ?", out.Day).Error; err != nil {
		return out, err
	}
	out.ProbeSamples, out.Monitored = counters.ProbeSamples, counters.Monitored
	out.Observed = counters.Day != ""
	for _, item := range []struct {
		query *gorm.DB
		dest  *int64
	}{
		{db.Model(&Repository{}).Where("discovered_at >= ? AND discovered_at < ?", start, end), &out.Discovered},
		{db.Model(&Repository{}).Where("state = ?", consts.StatusActive), &out.Monitoring},
		{db.Model(&ObservationTransition{}).Where("from_state <> ? AND to_state = ? AND occurred_at >= ? AND occurred_at < ?",
			consts.StatusActive, consts.StatusActive, start, end), &out.MonitoringEntries},
		{db.Model(&ObservationTransition{}).Where("from_state = ? AND to_state <> ? AND occurred_at >= ? AND occurred_at < ?",
			consts.StatusActive, consts.StatusActive, start, end), &out.MonitoringExits},
		{db.Model(&RepoFirstGuide{}).Where("ready_at >= ? AND ready_at < ?", start, end), &out.FirstGuides},
		{db.Model(&Job{}).Where("type IN ? AND status = ? AND last_error_code IN ? AND updated_at >= ? AND updated_at < ?",
			[]string{consts.JobRefreshSummary, consts.JobTranslateReadme}, consts.StatusCanceled,
			[]string{"no_readable_source", "verified_no_readable_source", "no_value_signal", "not_public", "ai_admission_changed"}, start, end), &out.CanceledAI},
		{db.Model(&AdmissionDaily{}).Distinct("repo_id").Where("day = ? AND operation IN ? AND state IN ?",
			out.Day, []string{"summary", "topics", "translation", "embedding"}, []string{admission.Defer, admission.Reject}), &out.FilteredRepos},
	} {
		if err := item.query.Count(item.dest).Error; err != nil {
			return out, err
		}
	}
	if err := db.Model(&AIBudgetReservation{}).Where("sending_at >= ? AND sending_at < ?", start, end).
		Select(`origin, lane, operation, provider, model, status, COUNT(*) AS requests,
		COALESCE(SUM(input_tokens),0) AS input_tokens, COALESCE(SUM(output_tokens),0) AS output_tokens,
		COUNT(input_tokens) AS with_usage, COUNT(estimated_cost) AS with_cost,
		COALESCE(SUM(estimated_cost),0) AS estimated_cost,
		COALESCE(SUM(CASE WHEN estimated_cost IS NULL THEN held_cost ELSE 0 END),0) AS unsettled_estimate,
		COALESCE(SUM(duration_ms),0) AS duration_ms`).
		Group("origin, lane, operation, provider, model, status").Order("origin, lane, operation, provider, model, status").Scan(&out.Calls).Error; err != nil {
		return out, err
	}
	if err := db.Model(&AdmissionDaily{}).Where("day = ?", out.Day).Select("operation, state, reason, lane, COUNT(*) AS repos").
		Group("operation, state, reason, lane").Order("operation, state, reason, lane").Scan(&out.Decisions).Error; err != nil {
		return out, err
	}
	trending, err := AuditTrending(ctx, db, day)
	if err != nil {
		return out, err
	}
	out.Trending = trending
	out.Deferred, err = deferredAudit(db, day)
	return out, err
}

// AuditTrending 对照完整全站榜单，并检查当天以前的 Search 是否实际覆盖。
func AuditTrending(ctx context.Context, db *gorm.DB, day time.Time) ([]AdmissionAuditSample, error) {
	db = db.WithContext(ctx)
	at := day.Add(24 * time.Hour)
	var ids []int64
	if err := StrongTrending(db, at).Where("b.observed_at >= ? AND b.observed_at < ?", day.UnixMilli(), at.UnixMilli()).
		Distinct("o.repo_id").Order("o.repo_id").Pluck("o.repo_id", &ids).Error; err != nil {
		return nil, err
	}
	return CompareAdmissionSamples(db, day, ids)
}

// CompareAdmissionSamples 也接受独立人工榜单 ID，明确标识未发现和已发现但未准入。
func CompareAdmissionSamples(db *gorm.DB, day time.Time, ids []int64) ([]AdmissionAuditSample, error) {
	end := admission.DayStart(day).Add(24 * time.Hour).UnixMilli()
	out := make([]AdmissionAuditSample, 0, len(ids))
	for _, id := range ids {
		item := AdmissionAuditSample{RepoID: id, Result: "not_discovered"}
		var repo Repository
		if err := db.Select("repo_id", "full_name", "stars", "github_created_at").Find(&repo, "repo_id = ? AND discovered_at < ?", id, end).Error; err != nil {
			return nil, err
		}
		if repo.RepoID != 0 {
			item.FullName, item.Stars = repo.FullName, repo.Stars
			if age, ok := admission.AgeDays(repo.GitHubCreatedAt, time.UnixMilli(end-1)); ok {
				item.AgeDays = &age
			}
			var search int64
			if err := db.Model(&SearchObservation{}).Where("repo_id = ? AND observed_at < ?", id, end).Count(&search).Error; err != nil {
				return nil, err
			}
			item.Result = "found_by_search"
			item.SearchCoverage = "observed"
			if search == 0 {
				item.Result = "found_by_other_source"
				// Search 明细有保留期，缺观测不是历史上从未命中的证明。
				item.SearchCoverage = "no_retained_observation"
			}
			var d AdmissionDaily
			if err := db.Where("repo_id = ? AND day = ? AND operation = ?", id, day.UTC().Format(time.DateOnly), "summary").Find(&d).Error; err != nil {
				return nil, err
			}
			if d.RepoID != 0 {
				item.Reason = d.Reason
				if d.State == admission.Defer || d.State == admission.Reject {
					item.Result += "_ai_filtered"
				} else {
					item.Result += "_ai_eligible"
				}
			} else {
				item.Result += "_ai_not_evaluated"
			}
			var monitor AdmissionDaily
			if err := db.Where("repo_id = ? AND day <= ? AND operation = ?", id, day.UTC().Format(time.DateOnly), "monitor").
				Order("day DESC").Limit(1).Find(&monitor).Error; err != nil {
				return nil, err
			}
			item.MonitoringState, item.MonitoringReason = monitor.State, monitor.Reason
		}
		out = append(out, item)
	}
	return out, nil
}

// deferredAudit 每个年龄和 Star 档最多抽两条；同一天重复生成报告结果一致。
func deferredAudit(db *gorm.DB, day time.Time) ([]AdmissionAuditSample, error) {
	var rows []AdmissionDaily
	if err := db.Where("day = ? AND operation = ? AND state IN ?", day.Format(time.DateOnly), "summary",
		[]string{admission.Defer, admission.Reject}).Find(&rows).Error; err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b AdmissionDaily) int {
		return cmp.Compare(admission.DailySample(a.RepoID, day), admission.DailySample(b.RepoID, day))
	})
	counts := map[[2]int]int{}
	out := make([]AdmissionAuditSample, 0, 24)
	for _, row := range rows {
		age, stars := 4, 0
		if row.AgeDays != nil {
			switch {
			case *row.AgeDays <= 7:
				age = 0
			case *row.AgeDays <= 14:
				age = 1
			case *row.AgeDays <= 30:
				age = 2
			default:
				age = 3
			}
		}
		switch {
		case row.Stars >= 1000:
			stars = 3
		case row.Stars >= 200:
			stars = 2
		case row.Stars >= 50:
			stars = 1
		}
		band := [2]int{age, stars}
		if counts[band] >= 2 {
			continue
		}
		counts[band]++
		var repo Repository
		if err := db.Select("full_name").Find(&repo, "repo_id = ?", row.RepoID).Error; err != nil {
			return nil, err
		}
		out = append(out, AdmissionAuditSample{RepoID: row.RepoID, FullName: repo.FullName, Stars: row.Stars,
			AgeDays: row.AgeDays, Result: row.State, Reason: row.Reason})
	}
	return out, nil
}
