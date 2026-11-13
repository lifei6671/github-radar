package collectionstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// recordProbeGrowth 保留实际复采窗，供采集与 AI 准入独立读取。
func (q Queue) recordProbeGrowth(tx *gorm.DB, repo *storage.Repository, now time.Time) error {
	var snapshots []storage.RepoSnapshot
	at := now.UnixMilli()
	if err := tx.Select("observed_at", "stars").Where("repo_id = ? AND observed_at BETWEEN ? AND ?", repo.RepoID, at-2*metrics.Day, at).
		Order("observed_at DESC").Find(&snapshots).Error; err != nil {
		return err
	}
	points := make([]metrics.Point, 0, len(snapshots))
	for _, row := range snapshots {
		points = append(points, metrics.Point{At: row.ObservedAt, Stars: int64(row.Stars)})
	}
	tolerance := int(q.probeTolerance() / time.Minute)
	window, _ := metrics.ObservationProbeGrowth(points, at, metrics.BoundaryToleranceMinutes{Exact: tolerance, Max: tolerance})
	if window.Quality == metrics.Exact && window.Delta != nil && *window.Delta >= 15 {
		e := admission.Evidence{Exact24h: window.Delta, StartAt: *window.StartAt, EndAt: *window.EndAt}
		if err := storage.SaveAdmission(tx, repo.RepoID, "probe", admission.Decision{State: "probing", Reason: "exact_probe_growth"}, e, now); err != nil {
			return err
		}
	}
	return nil
}

// probesEnabled 旧分档仍使用同样的有限候选探测；显式回退时恢复原行为。
func (q Queue) probesEnabled() bool { return q.Cfg.ObservationPool || !q.Cfg.LegacyMonitoring }

// monitorDecision 快速路径只读现有来源；重复 Search 不成为可信增长。
func monitorDecision(tx *gorm.DB, repo storage.Repository, now time.Time) (admission.Input, admission.Decision, error) {
	in := admission.Input{ID: repo.RepoID, Public: repo.IsPublic(), Followed: repo.StarredByUser,
		CreatedAt: repo.GitHubCreatedAt, PushedAt: repo.GitHubPushedAt, Stars: repo.Stars}
	regular, qualifies := admission.RegularMonitoring(in, now)
	// 常规低频席位先用 GitHub 元数据判断；活跃复核、暂停复热和已采样候选再读证据。
	if in.Public && !in.Followed && qualifies && repo.State == consts.StatusCandidate {
		return in, admission.Decision{State: admission.Pass, Reason: regular, Priority: 20}, nil
	}
	if in.Public && !in.Followed && (repo.State != consts.StatusCandidate || repo.LastSuccessAt != nil) {
		var err error
		in.Evidence, err = storage.AdmissionEvidence(tx, repo.RepoID, now)
		if err != nil {
			return in, admission.Decision{}, err
		}
	}
	d := admission.Monitoring(in, now)
	if repo.State == consts.StatusPaused && !repo.StarredByUser && d.State == admission.Pass {
		var prior storage.ObservationTransition
		if err := tx.Where("repo_id = ? AND to_state = ?", repo.RepoID, consts.StatusPaused).
			Order("occurred_at DESC, id DESC").Limit(1).Find(&prior).Error; err != nil {
			return in, admission.Decision{}, err
		}
		if prior.Reason == "quiet_14d" && (d.Priority <= 20 ||
			max(in.Evidence.EndAt, in.Evidence.Trending, in.Evidence.BurstAt) <= prior.OccurredAt) {
			d = admission.Decision{State: admission.Defer, Reason: "awaiting_new_signal", RetryAt: now.Add(24 * time.Hour).UnixMilli()}
		}
	}
	return in, d, nil
}

// admitByPolicy 在两个分档模式中都执行分龄准入。观察池由指标事务分配席位。
func (q Queue) admitByPolicy(tx *gorm.DB, repo *storage.Repository, now time.Time) error {
	if repo.State == consts.StatusActive {
		return nil
	}
	in, d, err := monitorDecision(tx, *repo, now)
	if err != nil {
		return err
	}
	if d.State == admission.Pass {
		d.State = "eligible"
		if !q.Cfg.ObservationPool {
			at := now.UnixMilli()
			var count int64
			if err := tx.Model(&storage.Repository{}).Where("state = ?", consts.StatusActive).Count(&count).Error; err != nil {
				return err
			}
			allowed := count < int64(q.Cfg.MaxMonitoredRepos)
			if allowed && repo.StarredByUser {
				allowed, err = q.canActivateFollow(tx)
				if err != nil {
					return err
				}
			}
			if allowed {
				grade := consts.GradeC
				if d.Priority >= 40 {
					grade, err = q.availableGrade(tx)
					if err != nil {
						return err
					}
				}
				if err := storage.RecordObservation(tx, *repo, consts.StatusActive, grade, d.Reason, at,
					metrics.ObservationEvidence{Stars: repo.Stars, Exact24h: in.Evidence.Exact24h}); err != nil {
					return err
				}
				if err := tx.Model(repo).Updates(map[string]any{"state": consts.StatusActive, "grade": grade,
					"below_grade_since": nil, "next_fetch_at": at, "updated_at": at, "observation_probe_due_at": 0}).Error; err != nil {
					return err
				}
				d.State = "monitoring"
			}
		} else if repo.StarredByUser {
			if err := q.admitFollowedObservation(tx, repo, now.UnixMilli()); err != nil {
				return err
			}
			if repo.State == consts.StatusActive {
				d.State = "monitoring"
			}
		}
	}
	return storage.SaveAdmission(tx, repo.RepoID, "monitor", d, in.Evidence, now)
}

// rebalanceByPolicy 逐批回看存量。缺测时不暂停；只有连续精确低增长才降频。
func (q Queue) rebalanceByPolicy(ctx context.Context, now time.Time) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var followed int64
		if err := tx.Model(&storage.Repository{}).Where("state = ? AND starred_by_user = ?", consts.StatusActive, true).Count(&followed).Error; err != nil {
			return err
		}
		if followed > int64(q.Cfg.MaxMonitoredRepos) {
			return errors.New("监控容量小于已有活跃关注仓库数，请调整容量")
		}
		var total int64
		if err := tx.Model(&storage.Repository{}).Where("state = ?", consts.StatusActive).Count(&total).Error; err != nil {
			return err
		}
		if over := min(40, int(total)-q.Cfg.MaxMonitoredRepos); over > 0 {
			var victims []storage.Repository
			if err := tx.Where("state = ? AND starred_by_user = ?", consts.StatusActive, false).
				Order("grade DESC, stars, last_discovered_at, repo_id DESC").Limit(over).Find(&victims).Error; err != nil {
				return err
			}
			for _, repo := range victims {
				if err := pauseObservationVictim(tx, repo, now.UnixMilli()); err != nil {
					return err
				}
			}
		}
		var active []storage.Repository
		if err := tx.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).Where("repositories.state = ? AND repositories.starred_by_user = ?", consts.StatusActive, false).
			Where("NOT EXISTS (?)", tx.Model(&storage.AdmissionDecision{}).Select("1").
				Where("repo_id = repositories.repo_id AND operation = ? AND updated_at > ?", "monitor", now.Add(-24*time.Hour).UnixMilli())).
			Order("repositories.repo_id").Limit(40).Find(&active).Error; err != nil {
			return err
		}
		for _, repo := range active {
			if err := q.reviewLegacyMonitor(tx, repo, now); err != nil {
				return err
			}
		}
		var candidates []storage.Repository
		if err := tx.Model(&storage.Repository{}).Where(storage.PublicRepositoryCondition).
			Where("repositories.state IN ? AND (repositories.starred_by_user = ? OR repositories.stars >= ? OR repositories.last_success_at IS NOT NULL)",
				[]string{consts.StatusCandidate, consts.StatusPaused}, true, 200).
			Where("NOT EXISTS (?)", tx.Model(&storage.AdmissionDecision{}).Select("1").
				Where("repo_id = repositories.repo_id AND operation = ? AND updated_at > ?", "monitor", now.Add(-time.Hour).UnixMilli())).
			Order("repositories.starred_by_user DESC, repositories.stars DESC, repositories.repo_id").Limit(80).Find(&candidates).Error; err != nil {
			return err
		}
		for _, repo := range candidates {
			if err := q.admitByPolicy(tx, &repo, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// reviewLegacyMonitor 将旧池的采样逐档降频；两周零增长才允许暂停冷却档。
func (q Queue) reviewLegacyMonitor(tx *gorm.DB, repo storage.Repository, now time.Time) error {
	in, d, err := monitorDecision(tx, repo, now)
	if err != nil {
		return err
	}
	if d.State == admission.Pass {
		d.State = "monitoring"
	} else if until, grace := admission.CrossAgeGrace(in, now); grace {
		d.State, d.Reason, d.RetryAt = "monitoring", "age_boundary_grace", until
	} else {
		d.State = "monitoring"
		var snapshots []storage.RepoSnapshot
		at := now.UnixMilli()
		if err := tx.Select("observed_at", "stars").Where("repo_id = ? AND observed_at BETWEEN ? AND ?", repo.RepoID, at-21*metrics.Day, at).
			Order("observed_at DESC").Find(&snapshots).Error; err != nil {
			return err
		}
		points := make([]metrics.Point, 0, len(snapshots))
		for _, row := range snapshots {
			points = append(points, metrics.Point{At: row.ObservedAt, Stars: int64(row.Stars)})
		}
		window, _ := metrics.ObservationProbeGrowth(points, at, metrics.BoundaryToleranceMinutes{Exact: 90, Max: 90})
		grade, state := repo.Grade, repo.State
		if window.Quality == metrics.Exact && window.Delta != nil && *window.Delta < 15 {
			var previous metrics.Window
			if len(points) > 1 {
				// 从第一天的实测起点继续寻找相邻日窗，不能把重叠窗口当作连续两天。
				for i, p := range points {
					if p.At == *window.StartAt {
						previous, _ = metrics.ObservationProbeGrowth(points[i:], p.At, metrics.BoundaryToleranceMinutes{Exact: 90, Max: 90})
						break
					}
				}
			}
			if previous.Quality == metrics.Exact && previous.Delta != nil && *previous.Delta < 15 {
				switch repo.Grade {
				case consts.GradeS:
					grade = consts.GradeA
				case consts.GradeA:
					grade = consts.GradeB
				case consts.GradeB:
					grade = consts.GradeC
				}
				d.Reason = "slowed_48h"
			}
		}
		if grade == consts.GradeC && repo.Grade == grade && metrics.QuietObservation(points, at, q.Cfg.Interval(grade).Milliseconds()) {
			var cooled int64
			if err := tx.Model(&storage.ObservationTransition{}).Where("repo_id = ? AND to_grade = ? AND from_grade <> ? AND occurred_at >= ?",
				repo.RepoID, consts.GradeC, consts.GradeC, at-3*metrics.Day).Count(&cooled).Error; err != nil {
				return err
			}
			if cooled != 0 {
				d.Reason = "cooling_dwell"
				return storage.SaveAdmission(tx, repo.RepoID, "monitor", d, in.Evidence, now)
			}
			var retained int64
			if err := tx.Model(&storage.AISummary{}).Where("repo_id = ? AND summary_status = ?", repo.RepoID, consts.StatusReady).Count(&retained).Error; err != nil {
				return err
			}
			if retained == 0 {
				if err := tx.Model(&storage.TopicCandidate{}).Where("repo_id = ? AND status IN ?", repo.RepoID,
					[]string{consts.StatusCandidate, consts.StatusShortlisted}).Count(&retained).Error; err != nil {
					return err
				}
			}
			if retained == 0 {
				state, d.State, d.Reason = consts.StatusPaused, "deferred", "quiet_14d"
			}
		}
		if grade != repo.Grade || state != repo.State {
			if err := storage.RecordObservation(tx, repo, state, grade, d.Reason, at,
				metrics.ObservationEvidence{Stars: repo.Stars, Exact24h: window.Delta, WindowStartAt: window.StartAt, WindowEndAt: window.EndAt}); err != nil {
				return err
			}
			if err := tx.Model(&repo).Updates(map[string]any{"grade": grade, "state": state, "updated_at": at,
				"next_fetch_at": now.Add(q.Cfg.Interval(grade)).UnixMilli()}).Error; err != nil {
				return err
			}
			if err := storage.UpdateObservationJobs(tx, repo.RepoID, at, true, 0); err != nil {
				return err
			}
		}
	}
	return storage.SaveAdmission(tx, repo.RepoID, "monitor", d, in.Evidence, now)
}
