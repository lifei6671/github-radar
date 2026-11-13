package collectionstore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

const (
	observationProbePrefix     = "auto:refresh_repo:observation:"
	observationProbePattern    = observationProbePrefix + "%"
	observationProbeSubjects   = "(repositories.state IN ('candidate', 'paused') OR (repositories.state = 'active' AND repositories.grade = 'C'))"
	observationSamplingKey     = "observation.sampling.v1"
	observationRecheckSubjects = "repositories.first_not_found_at IS NOT NULL AND repositories.name_is_current = 1 AND repositories.blocked = 0 AND repositories.disabled = 0 AND repositories.state IN ('candidate', 'paused', 'active')"
)

func isObservationProbe(job storage.Job) bool {
	return strings.HasPrefix(job.DedupKey, observationProbePrefix)
}

// scheduleObservationProbes 给候选安排首采和 24 小时后的复采，优先保住已经约定的复采窗口。
func (q Queue) scheduleObservationProbes(tx *gorm.DB, now time.Time) error {
	at := now.UnixMilli()
	probes := tx.Model(&storage.Job{}).Where("type = ? AND dedup_key LIKE ?", consts.JobRefreshRepo, observationProbePattern)
	var recent, pending int64
	if err := probes.Session(&gorm.Session{}).Where("created_at > ?", at-metrics.Hour).Count(&recent).Error; err != nil {
		return err
	}
	open := tx.Model(&storage.Job{}).Select("1").Where("type = ? AND status IN ?", consts.JobRefreshRepo,
		[]string{consts.StatusPending, consts.StatusRunning, consts.StatusFailed})
	if err := probes.Session(&gorm.Session{}).Where("status IN ?", []string{consts.StatusPending, consts.StatusRunning, consts.StatusFailed}).Count(&pending).Error; err != nil {
		return err
	}
	limit := min(metrics.ObservationProbesPerHour-int(recent), 40-int(pending))
	if limit <= 0 {
		return nil
	}
	var rows []struct {
		RepoID, Signal, Due int64
		Recheck             bool
		Trusted             bool
		Stars               int
		Created             *int64
	}
	// 官方信号和传播链接可为暂停或冷却仓库触发复核，普通 Search 重复命中不会重开探测。
	trending := storage.StrongTrending(tx, now).Select("MAX(b.observed_at)").Where("o.repo_id = repositories.repo_id")
	if !q.Cfg.LegacyMonitoring {
		// 连续上榜属于同一轮传播。探测过的仓库需重新入榜或获得新官方/人工证据。
		trending = trending.Where("repositories.observation_probe_signal_at = 0 OR NOT EXISTS (?)",
			tx.Table(storage.TrendingObservationTable+" prior").Select("1").
				Where("prior.repo_id = o.repo_id AND prior.source_key = o.source_key AND prior.window = o.window AND prior.rank BETWEEN 1 AND 10").
				Where("prior.observed_slot < o.observed_slot AND prior.observed_slot >= o.observed_slot - ?", 2*metrics.Day))
	}
	signals := tx.Model(&storage.Repository{}).Select(`repositories.repo_id, repositories.stars, repositories.github_created_at AS created, repositories.observation_probe_due_at AS due,
		CASE WHEN repositories.state = 'candidate' AND repositories.stars >= ? AND repositories.discovered_at > COALESCE(signal.breakout_at, 0)
		THEN repositories.discovered_at ELSE COALESCE(signal.breakout_at, 0) END AS initial_signal,
		(SELECT MAX(first_observed_at) FROM repo_discovery_events e WHERE e.repo_id = repositories.repo_id AND e.source_url <> '' AND first_observed_at <= ?) AS source_signal,
		(?) AS trending_signal, COALESCE(signal.breakout_at, 0) AS official_signal,
		repositories.observation_probe_signal_at AS previous_signal, repositories.first_not_found_at IS NOT NULL AS recheck`, metrics.ObservationMinStars, at, trending).
		Joins("LEFT JOIN repo_star_history_status signal ON signal.repo_id = repositories.repo_id AND signal.breakout_at BETWEEN ? AND ? AND (signal.confirmed_at IS NULL OR signal.confirmed_at <> signal.breakout_at)",
			at-metrics.StarHistorySignalAge, at).
		Where(observationEligible()).
		Where("NOT EXISTS (?)", open.Where("jobs.repo_id = repositories.repo_id"))
	candidates := tx.Table("(?) AS signals", signals).Select(`repo_id, due, recheck, stars, created,
		(COALESCE(source_signal,0) > previous_signal OR COALESCE(trending_signal,0) > previous_signal OR official_signal > previous_signal) AS trusted,
		CASE WHEN COALESCE(source_signal, 0) >= initial_signal AND COALESCE(source_signal, 0) >= COALESCE(trending_signal, 0) THEN COALESCE(source_signal, 0)
		WHEN COALESCE(trending_signal, 0) > initial_signal THEN trending_signal ELSE initial_signal END AS signal, previous_signal`)
	err := tx.Table("(?) AS probes", candidates).Select("repo_id, signal, due, recheck, trusted, stars, created").
		Where("(due > 0 AND due <= ?) OR (due = 0 AND signal > previous_signal AND (previous_signal = 0 OR signal >= previous_signal + ?) AND signal >= ?)",
			at, 2*metrics.Day, at-7*metrics.Day).
		Order("CASE WHEN due > 0 THEN 0 ELSE 1 END, due, trusted DESC, signal DESC, repo_id").Limit(160).Scan(&rows).Error
	if err != nil {
		return err
	}
	// 先保护复采，其次强证据，再选靠近分龄门槛和稳定抽样的低 Star 候选。
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		priority := func(due int64, trusted bool, stars int, created *int64) int {
			if due > 0 {
				return 3
			}
			if trusted {
				return 2
			}
			age, ok := admission.AgeDays(created, now)
			if ok && ((age <= 7 && stars >= 100) || (age > 7 && age <= 14 && stars >= 250) ||
				(age > 14 && age <= 30 && stars >= 500)) {
				return 1
			}
			return 0
		}
		x, y := priority(a.Due, a.Trusted, a.Stars, a.Created), priority(b.Due, b.Trusted, b.Stars, b.Created)
		if x != y {
			return x > y
		}
		if x == 0 {
			return admission.DailySample(a.RepoID, now) < admission.DailySample(b.RepoID, now)
		}
		return false
	})
	for _, row := range rows {
		if limit == 0 {
			break
		}
		// 复采已经错过精确窗时清除约定，等待下一条证据。
		if !row.Recheck && row.Due > 0 && at-row.Due > q.probeTolerance().Milliseconds() {
			if err := tx.Model(&storage.Repository{}).Where("repo_id = ?", row.RepoID).Update("observation_probe_due_at", 0).Error; err != nil {
				return err
			}
			continue
		}
		p := collection.Payload{PlannedAt: at, ObservationProbe: true, ObservationFollowup: row.Due > 0 && !row.Recheck,
			ObservationSignalAt: row.Signal, ObservationRecheck: row.Recheck}
		priority := 32
		if row.Due > 0 {
			priority = 45
		}
		key := fmt.Sprintf("%s%d:%d", observationProbePrefix, row.RepoID, collection.Slot(at))
		if _, err := q.enqueue(tx, consts.JobRefreshRepo, &row.RepoID, key, p, priority, at); err != nil {
			return err
		}
		if !q.Cfg.LegacyMonitoring {
			reason := "initial_probe"
			if p.ObservationFollowup {
				reason = "scheduled_24h_probe"
			} else if p.ObservationRecheck {
				reason = "visibility_recheck"
			} else if row.Trusted {
				reason = "trusted_source_probe"
			}
			if err := storage.SaveAdmission(tx, row.RepoID, "probe_schedule",
				admission.Decision{State: "probing", Reason: reason}, admission.Evidence{}, now); err != nil {
				return err
			}
		}
		limit--
	}
	return nil
}

func observationEligible() string {
	return "((" + storage.PublicRepositoryCondition + ") AND " + observationProbeSubjects + ") OR (" + observationRecheckSubjects + " AND repositories.observation_probe_due_at > 0)"
}

func (q Queue) probeTolerance() time.Duration {
	if q.ObservationExactMinutes == nil {
		return 90 * time.Minute
	}
	return time.Duration(*q.ObservationExactMinutes) * time.Minute
}

// cancelIneligibleObservationProbes 清理已屏蔽、下线或档位已变的候选任务，避免堵住探测名额。
func cancelIneligibleObservationProbes(tx *gorm.DB, now int64, enabled bool) error {
	jobs := tx.Model(&storage.Job{}).Where("type = ? AND dedup_key LIKE ?", consts.JobRefreshRepo, observationProbePattern).
		Where("(status IN ? OR (status = ? AND lease_until <= ?))",
			[]string{consts.StatusPending, consts.StatusFailed}, consts.StatusRunning, now)
	if enabled {
		eligible := tx.Model(&storage.Repository{}).Select("repo_id").Where(observationEligible())
		jobs = jobs.Where("repo_id NOT IN (?)", eligible)
	}
	return jobs.Updates(map[string]any{"status": consts.StatusCanceled, "updated_at": now,
		"lease_token": nil, "lease_until": nil}).Error
}

// alignObservationSchedule 阻止旧高频计划在新配置生效后集中发出。
func (q Queue) alignObservationSchedule(tx *gorm.DB, at int64) error {
	var marker storage.MaintenanceState
	periods := q.Cfg.IntervalsMinutes
	value := fmt.Sprintf("%d/%d/%d/%d", periods[consts.GradeS], periods[consts.GradeA], periods[consts.GradeB], periods[consts.GradeC])
	if err := tx.Find(&marker, "key = ?", observationSamplingKey).Error; err != nil || marker.Value == value {
		return err
	}
	for _, grade := range []string{consts.GradeS, consts.GradeA, consts.GradeB, consts.GradeC} {
		delta := q.Cfg.Interval(grade).Milliseconds()
		repos := tx.Model(&storage.Repository{}).Where("state = ? AND grade = ? AND last_success_at IS NOT NULL AND first_not_found_at IS NULL",
			consts.StatusActive, grade)
		future := repos.Session(&gorm.Session{}).Select("repo_id").Where("last_success_at + ? > ?", delta, at)
		if err := tx.Model(&storage.Job{}).Where("repo_id IN (?) AND type = ? AND dedup_key LIKE ? AND dedup_key NOT LIKE ? AND status IN ?",
			future, consts.JobRefreshRepo, consts.AutoJobPattern, observationProbePattern,
			[]string{consts.StatusPending, consts.StatusFailed}).Updates(map[string]any{
			"status": consts.StatusCanceled, "updated_at": at, "last_error_code": "observation_changed",
		}).Error; err != nil {
			return err
		}
		if err := repos.Update("next_fetch_at", gorm.Expr("last_success_at + ?", delta)).Error; err != nil {
			return err
		}
	}
	marker = storage.MaintenanceState{Key: observationSamplingKey, Value: value, UpdatedAt: at}
	return tx.Save(&marker).Error
}

// finishObservationProbe 只有结算时才消耗触发信号，恢复备份撤销未执行任务后仍可重建计划。
func finishObservationProbe(tx *gorm.DB, id int64, p collection.Payload, next int64) error {
	query := tx.Model(&storage.Repository{}).Where("repo_id = ?", id)
	if p.ObservationFollowup || p.ObservationRecheck {
		return query.Update("observation_probe_due_at", next).Error
	}
	return query.Where("observation_probe_signal_at <= ?", p.ObservationSignalAt).
		Updates(map[string]any{"observation_probe_signal_at": p.ObservationSignalAt, "observation_probe_due_at": next}).Error
}

func settleFailedObservationProbe(tx *gorm.DB, job storage.Job, at int64) error {
	var payload collection.Payload
	if job.RepoID == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	if payload.ObservationRecheck {
		// 一轮网络失败后仍可低频复核，直到 GitHub 确认 404/410 或恢复公开访问。
		return tx.Model(&storage.Repository{}).Where("repo_id = ? AND first_not_found_at IS NOT NULL", *job.RepoID).
			Update("observation_probe_due_at", at+metrics.Day).Error
	}
	return finishObservationProbe(tx, *job.RepoID, payload, 0)
}

// admitFollowedObservation 先在总容量内为人工重点安排采样；满载时从最低档替换未关注项目。
func (q Queue) admitFollowedObservation(tx *gorm.DB, repo *storage.Repository, at int64) error {
	allowed, err := q.canActivateFollow(tx)
	if err != nil || !allowed {
		return err
	}
	var counts []struct {
		Grade string
		Count int
	}
	active := tx.Model(&storage.Repository{}).Where("state = ?", consts.StatusActive)
	if err := active.Session(&gorm.Session{}).Select("grade, COUNT(*) AS count").Group("grade").Scan(&counts).Error; err != nil {
		return err
	}
	levels := map[string]int{}
	total := 0
	for _, row := range counts {
		levels[row.Grade] = row.Count
		total += row.Count
	}
	// 首轮指标会一次收回旧配置的超额档位，此前不能用一次替换进一步扰动旧仓库。
	if total > q.Cfg.MaxMonitoredRepos {
		return nil
	}
	limits := q.Cfg.ObservationCapacities()
	options := []struct {
		grade string
		limit int
	}{{consts.GradeA, limits[1]}, {consts.GradeB, limits[2]}, {consts.GradeC, limits[3]}, {consts.GradeS, limits[0]}}
	var victim storage.Repository
	if total == q.Cfg.MaxMonitoredRepos {
		var replaceable []string
		for _, option := range options {
			if option.limit > 0 && levels[option.grade] <= option.limit {
				replaceable = append(replaceable, option.grade)
			}
		}
		if err := active.Session(&gorm.Session{}).Where("starred_by_user = ? AND grade IN ?", false, replaceable).
			Order("grade DESC, stars, last_discovered_at, repo_id DESC").Limit(1).Find(&victim).Error; err != nil {
			return err
		}
		if victim.RepoID == 0 {
			return nil
		}
		levels[victim.Grade]--
	}
	for _, option := range options {
		if levels[option.grade] >= option.limit {
			continue
		}
		if victim.RepoID != 0 {
			if err := pauseObservationVictim(tx, victim, at); err != nil {
				return err
			}
		}
		if err := storage.RecordObservation(tx, *repo, consts.StatusActive, option.grade, "manual_follow", at,
			metrics.ObservationEvidence{Stars: repo.Stars, RequestedGrade: consts.GradeA}); err != nil {
			return err
		}
		if err := storage.UpdateObservationJobs(tx, repo.RepoID, at, true, 0); err != nil {
			return err
		}
		return tx.Model(repo).Updates(map[string]any{"state": consts.StatusActive, "grade": option.grade,
			"below_grade_since": nil, "next_fetch_at": at, "updated_at": at, "observation_probe_due_at": 0}).Error
	}
	// 启用配置后的首轮指标计算可能尚未收回超额档位，届时会纳入已关注项目。
	return nil
}

func pauseObservationVictim(tx *gorm.DB, repo storage.Repository, at int64) error {
	if err := storage.RecordObservation(tx, repo, consts.StatusPaused, repo.Grade, "capacity", at,
		metrics.ObservationEvidence{Stars: repo.Stars}); err != nil {
		return err
	}
	// 保留已到期的身份复核。容量调整不能让短时 404 永久隐藏仓库。
	updates := map[string]any{"state": consts.StatusPaused, "updated_at": at, "observation_probe_due_at": 0}
	if repo.FirstNotFoundAt != nil {
		updates["observation_probe_due_at"] = max(at, *repo.FirstNotFoundAt+metrics.Hour)
	}
	if err := tx.Model(&repo).Updates(updates).Error; err != nil {
		return err
	}
	return storage.UpdateObservationJobs(tx, repo.RepoID, at, true, 0)
}
