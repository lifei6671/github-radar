package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/storage"
)

// jobOperation 区分首次清洗、纯主题、译文及不调用模型的发布复核。
func jobOperation(job storage.Job) string {
	var p payload
	_ = json.Unmarshal([]byte(job.PayloadJSON), &p)
	switch {
	case job.Type == ReportJob:
		return "report"
	case job.Type == TranslationJob && p.ReadmeVerifySHA != "":
		return "verify"
	case job.Type == TranslationJob:
		return "translation"
	case p.TopicsOnly:
		return "topics"
	default:
		return "summary"
	}
}

// checkAdmission 使用与入队相同的规则复核排队任务，并保存当前决定。
func (q Queue) checkAdmission(tx *gorm.DB, job storage.Job, now time.Time) (admission.Decision, error) {
	if job.RepoID == nil {
		return admission.Decision{State: admission.Reject, Reason: "not_public"}, nil
	}
	var repo storage.Repository
	if err := tx.Find(&repo, "repo_id = ?", *job.RepoID).Error; err != nil {
		return admission.Decision{}, err
	}
	if repo.RepoID == 0 {
		return admission.Decision{State: admission.Reject, Reason: "not_public"}, nil
	}
	op := jobOperation(job)
	manual := job.IsManual()
	if q.AI.Summary.LegacyAdmission && !manual {
		if !repo.IsPublic() {
			return admission.Decision{State: admission.Reject, Reason: "not_public"}, nil
		}
		return admission.Decision{State: admission.Pass, Lane: admission.Maintenance}, nil
	}
	d, evidence, err := storage.AIDecision(tx, repo, op, manual, now)
	if err != nil || manual {
		return d, err
	}
	return d, storage.SaveAdmission(tx, repo.RepoID, op, d, evidence, now)
}

// cancelUnsent 只撤销还没有发出模型请求的工作，已预留的调用记为未发送。
func cancelUnsent(tx *gorm.DB, job storage.Job, d admission.Decision, now time.Time, attempts int) error {
	ms := now.UnixMilli()
	if err := tx.Model(&storage.AICall{}).Where("job_id = ? AND status = ?", job.ID, consts.StatusReserved).
		Updates(map[string]any{"status": consts.StatusFailed, "error_code": consts.CodeReservationUnused, "updated_at": ms}).Error; err != nil {
		return err
	}
	return tx.Model(&job).Updates(jobFields(consts.StatusCanceled, d.Reason, ms, max(ms, d.RetryAt), attempts)).Error
}

// stopAdmission 在执行期关闭任务，但保留旧摘要、译文及未知发送状态。
func (q Queue) stopAdmission(ctx context.Context, job storage.Job, d admission.Decision) error {
	return q.settle(ctx, job, outcome{status: consts.StatusCanceled, code: d.Reason, attempts: max(0, job.Attempts-1),
		apply: func(tx *gorm.DB) error {
			if job.RepoID == nil {
				return nil
			}
			evidence, err := storage.AdmissionEvidence(tx, *job.RepoID, time.Now())
			if err != nil {
				return err
			}
			return storage.SaveAdmission(tx, *job.RepoID, jobOperation(job), d, evidence, time.Now())
		}})
}

type admittedRepo struct {
	storage.Repository
	Decision   admission.Decision `gorm:"-"`
	TopicsOnly bool
}

// scheduleAdmitted 有界扫描到期公开仓库。质量过滤先于入队，缺主题只允许维护已有优质内容。
func (q Queue) scheduleAdmitted(ctx context.Context, now time.Time) error {
	var scanned, rejected, queued, canceled int
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		paused, err := storage.BooleanSetting(ctx, tx, storage.AutoAIResumeKey)
		if err != nil || paused {
			return err
		}
		canceled, err = q.pruneAdmission(tx, now)
		if err != nil {
			return err
		}
		ms := now.UnixMilli()
		budget := aibudget.ForConfig(q.DB, q.AI.Summary)
		remaining, err := budget.Remaining(tx, "", now)
		if err != nil {
			return err
		}
		// 升级前已发布的仓库有少量独立回填席位，避免被旧摘要积压长期挡住。
		if remaining > 0 {
			if err := q.schedulePurposeBackfill(tx, now, budget); err != nil {
				return fmt.Errorf("安排技术用途回填: %w", err)
			}
		}
		var outstanding int64
		if err := tx.Model(&storage.Job{}).Where("type IN ? AND priority < 200 AND (status IN ? OR (status = ? AND available_at <= ?))",
			[]string{JobType, TranslationJob, ReportJob}, []string{consts.StatusPending, consts.StatusRunning}, consts.StatusFailed, ms).Count(&outstanding).Error; err != nil {
			return err
		}
		slots := min(100, remaining) - int(outstanding)
		if slots <= 0 {
			return nil
		}
		var rows []admittedRepo
		pending := tx.Model(&storage.Job{}).Select("1").Where("jobs.repo_id = repositories.repo_id AND jobs.type = ? AND jobs.status IN "+unresolved, JobType)
		// 重新唤醒收到新可信证据的仓库，不能等到精确窗口过期才复核。
		freshGrowth := tx.Table(storage.LatestMetricTable+" m").Select("1").
			Where("m.repo_id = repositories.repo_id AND m.delta_24h >= ? AND m.calculated_at > COALESCE(a.updated_at,0)", 50).
			Where(storage.MetricWindowSQL(consts.Window24h, "quality")+" = ? AND "+
				storage.MetricWindowSQL(consts.Window24h, "endAt")+" > ? AND "+
				storage.MetricWindowSQL(consts.Window24h, "endAt")+" > COALESCE(c.last_checked_at,0)", consts.QualityExact, now.Add(-24*time.Hour).UnixMilli())
		newProbe := tx.Model(&storage.AdmissionDecision{}).Select("1").Where("repo_id = repositories.repo_id AND operation = ? AND reason = ? AND updated_at > COALESCE(a.updated_at,0) AND updated_at > COALESCE(c.last_checked_at,0) AND updated_at > ?",
			"probe", "exact_probe_growth", now.Add(-24*time.Hour).UnixMilli())
		newTrending := storage.StrongTrending(tx, now).Select("1").Where("o.repo_id = repositories.repo_id AND b.observed_at > COALESCE(a.updated_at,0) AND b.observed_at > COALESCE(c.last_checked_at,0)")
		newOfficial := tx.Model(&storage.RepoStarHistoryStatus{}).Select("1").Where("repo_id = repositories.repo_id AND breakout_at > COALESCE(a.updated_at,0) AND breakout_at > COALESCE(c.last_checked_at,0) AND breakout_at BETWEEN ? AND ?",
			now.Add(-48*time.Hour).UnixMilli(), ms)
		if err := tx.Model(&storage.Repository{}).Select("repositories.*, CASE WHEN s.summary_status = 'ready' AND "+
			"s.model_provider = ? AND s.model_name = ? AND s.prompt_version = ? AND s.source_fingerprint = c.source_fingerprint AND "+
			"(t.repo_id IS NULL OR NOT (t."+storage.CurrentTagVocabularySQL+") OR t.theme_catalog_key <> "+storage.ThemeCatalogKeySQL+") "+
			"THEN 1 ELSE 0 END AS topics_only", ProviderKey(q.AI), q.AI.Model, q.AI.Summary.PromptVersion).Where(storage.PublicRepositoryCondition).
			Joins("LEFT JOIN repo_admission_decisions a ON a.repo_id = repositories.repo_id AND a.operation = ?", "summary").
			Joins("LEFT JOIN summary_checks c ON c.repo_id = repositories.repo_id").
			Joins("LEFT JOIN ai_summaries s ON s.repo_id = repositories.repo_id").
			Joins("LEFT JOIN repo_ai_topics t ON t.repo_id = repositories.repo_id").
			Where("a.repo_id IS NULL OR a.next_check_at <= ? OR a.updated_at < repositories.updated_at OR EXISTS (?) OR EXISTS (?) OR EXISTS (?) OR EXISTS (?)",
				ms, newTrending, newOfficial, freshGrowth, newProbe).
			Where("a.repo_id IS NULL OR a.reason <> ? OR a.next_check_at <= ? OR repositories.starred_by_user = ? OR repositories.github_pushed_at > a.updated_at",
				"verified_no_readable_source", ms, true).
			Where("c.repo_id IS NULL OR c.next_check_at <= ? OR EXISTS (?) OR EXISTS (?) OR EXISTS (?) OR EXISTS (?) OR (c.last_error_code IS NULL AND s.repo_id IS NOT NULL AND "+
				"(s.model_provider <> ? OR s.model_name <> ? OR s.prompt_version <> ? OR "+
				"(s.summary_status = ? AND (t.repo_id IS NULL OR NOT (t."+storage.CurrentTagVocabularySQL+") OR t.theme_catalog_key <> "+storage.ThemeCatalogKeySQL+"))))",
				ms, newTrending, newOfficial, freshGrowth, newProbe, ProviderKey(q.AI), q.AI.Model, q.AI.Summary.PromptVersion, consts.StatusReady).
			Where("NOT EXISTS (?)", pending).
			Order("repositories.starred_by_user DESC, a.updated_at ASC, repositories.stars DESC, repositories.repo_id").
			Limit(240).Find(&rows).Error; err != nil {
			return err
		}
		var eligible []admittedRepo
		laneSlots := make(map[string]int, 4)
		for _, lane := range []string{admission.Growth, admission.Exploration, admission.Revival, admission.Maintenance} {
			left, err := budget.Remaining(tx, lane, now)
			if err != nil {
				return err
			}
			laneSlots[lane] = left
		}
		// 预留少量续译席位，避免首次清洗的积压长期挡住已缓存正文的任务。
		if count := min(slots, laneSlots[admission.Maintenance], max(1, slots/10)); count > 0 {
			n, err := q.scheduleTranslations(tx, ms, count, true)
			if err != nil {
				return err
			}
			queued += n
			slots -= n
			laneSlots[admission.Maintenance] -= n
		}
		for _, row := range rows {
			scanned++
			op := "summary"
			if row.TopicsOnly {
				op = "topics"
			}
			d, evidence, err := storage.AIDecision(tx, row.Repository, op, false, now)
			if err != nil {
				return err
			}
			if err := storage.SaveAdmission(tx, row.RepoID, "summary", d, evidence, now); err != nil {
				return err
			}
			if op != "summary" {
				if err := storage.SaveAdmission(tx, row.RepoID, op, d, evidence, now); err != nil {
					return err
				}
			}
			if d.State == admission.Pass && laneSlots[d.Lane] > 0 {
				row.Decision = d
				eligible = append(eligible, row)
			}
			if d.State != admission.Pass {
				rejected++
			}
		}
		slices.SortStableFunc(eligible, func(a, b admittedRepo) int { return b.Decision.Priority - a.Decision.Priority })
		higher := 0
		for _, r := range eligible {
			if r.Decision.Lane == admission.Growth || r.Decision.Lane == admission.Revival {
				higher++
			}
		}
		eligible = explorationOrder(eligible, min(slots, higher+laneSlots[admission.Exploration]), q.AI.Summary.ExplorationRandomPercent, now)
		for _, row := range eligible {
			if slots <= 0 {
				break
			}
			if laneSlots[row.Decision.Lane] <= 0 {
				continue
			}
			key := fmt.Sprintf(consts.SummaryAutoDedupFormat, row.RepoID, ms)
			if _, err := q.insert(tx, JobType, row.RepoID, key, payload{TopicsOnly: row.TopicsOnly}, row.Decision.Priority, ms); err != nil {
				return err
			}
			slots--
			queued++
			laneSlots[row.Decision.Lane]--
		}
		if slots = min(slots, laneSlots[admission.Maintenance]); slots > 0 {
			n, err := q.scheduleTranslations(tx, ms, min(slots, max(1, remaining/10)), false)
			queued += n
			return err
		}
		return nil
	})
	if err == nil && q.Logger != nil && (rejected > 0 || queued > 0 || canceled > 0) {
		q.Logger.Info("AI 自动准入扫描", "scanned", scanned, "filtered", rejected, "queued", queued, "canceledUnsent", canceled)
	}
	return err
}

// pruneAdmission 分批关闭存量待发任务，保留 running、sending 和 unknown。
func (q Queue) pruneAdmission(tx *gorm.DB, now time.Time) (int, error) {
	var jobs []storage.Job
	if err := tx.Model(&storage.Job{}).
		Where("type IN ? AND priority < 200 AND status IN ?", []string{JobType, TranslationJob}, []string{consts.StatusPending, consts.StatusFailed}).
		Where("COALESCE(json_extract(payload_json,'$.manual'),0) = 0 AND COALESCE(json_extract(payload_json,'$.readmeVerifySHA'),'') = ''").
		Where("NOT EXISTS (?)", tx.Model(&storage.AICall{}).Select("1").Where("job_id = jobs.id AND status IN ?", []string{consts.StatusSending, consts.StatusUnknown})).
		Joins("LEFT JOIN repo_admission_decisions d ON d.repo_id = jobs.repo_id AND "+
			"d.operation = CASE WHEN jobs.type = ? THEN 'translation' WHEN json_extract(jobs.payload_json,'$.topicsOnly') = 1 THEN 'topics' ELSE 'summary' END",
			TranslationJob).
		Where("d.updated_at IS NULL OR d.updated_at < ? OR d.updated_at < jobs.updated_at", now.Add(-time.Hour).UnixMilli()).
		Select("jobs.*").Order("d.updated_at ASC, jobs.id").Limit(40).Find(&jobs).Error; err != nil {
		return 0, err
	}
	canceled := 0
	for _, job := range jobs {
		if job.IsManual() || jobOperation(job) == "verify" {
			continue
		}
		d, err := q.checkAdmission(tx, job, now)
		if err != nil {
			return 0, err
		}
		if d.State != admission.Pass {
			if err := cancelUnsent(tx, job, d, now, job.Attempts); err != nil {
				return 0, err
			}
			canceled++
		}
	}
	return canceled, nil
}

// explorationOrder 按年龄与 Star 档轮转随机样本；其余保留潜力排序。
func explorationOrder(rows []admittedRepo, slots, percent int, now time.Time) []admittedRepo {
	start := slices.IndexFunc(rows, func(r admittedRepo) bool { return r.Decision.Lane == admission.Exploration })
	if start < 0 || start >= slots || percent == 0 {
		return rows
	}
	end := start
	for end < len(rows) && rows[end].Decision.Lane == admission.Exploration {
		end++
	}
	count := min(slots-start, end-start)
	random := (count*percent + 50) / 100
	if random == 0 || count == end-start {
		return rows
	}
	chosen := make(map[int64]bool, count)
	top := start + count - random
	for _, r := range rows[start:top] {
		chosen[r.RepoID] = true
	}
	var bands [9][]admittedRepo
	for _, r := range rows[top:end] {
		age, _ := admission.AgeDays(r.GitHubCreatedAt, now)
		band := min(2, age/8) * 3
		if r.Stars >= 200 {
			band += 2
		} else if r.Stars >= 50 {
			band++
		}
		bands[band] = append(bands[band], r)
	}
	for i := range bands {
		slices.SortFunc(bands[i], func(a, b admittedRepo) int {
			x, y := admission.DailySample(a.RepoID, now), admission.DailySample(b.RepoID, now)
			if x < y {
				return -1
			}
			if x > y {
				return 1
			}
			return 0
		})
	}
	out := append([]admittedRepo(nil), rows[:top]...)
	for i := 0; random > 0; i++ {
		band := (int(now.Unix()/86400) + i) % len(bands)
		if len(bands[band]) == 0 {
			continue
		}
		r := bands[band][0]
		bands[band] = bands[band][1:]
		chosen[r.RepoID] = true
		out = append(out, r)
		random--
	}
	for _, r := range rows[top:end] {
		if !chosen[r.RepoID] {
			out = append(out, r)
		}
	}
	return append(out, rows[end:]...)
}
