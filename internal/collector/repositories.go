package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/google/go-github/v88/github"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

func metadata(source *sdk.Repository) map[string]any {
	topics := source.Topics
	if topics == nil {
		topics = []string{}
	}
	data, _ := json.Marshal(topics)
	values := map[string]any{
		"owner": source.GetOwner().GetLogin(), "name": source.GetName(), "full_name": source.GetFullName(),
		"html_url": source.GetHTMLURL(), "description": source.Description, "topics_json": string(data),
		"primary_language": source.Language, "stars": source.GetStargazersCount(), "forks": source.GetForksCount(),
		"archived": source.GetArchived(), "disabled": source.GetDisabled(), "license_spdx_id": nil, "license_name": nil,
		"github_created_at": nil, "github_pushed_at": nil,
	}
	if source.License != nil {
		values["license_spdx_id"], values["license_name"] = source.License.SPDXID, source.License.Name
	}
	if source.CreatedAt != nil {
		values["github_created_at"] = source.CreatedAt.UnixMilli()
	}
	if source.PushedAt != nil {
		values["github_pushed_at"] = source.PushedAt.UnixMilli()
	}
	return values
}

// detachName 解除旧名称寻址，历史、数字 ID 和关注状态始终保留。
func detachName(tx *gorm.DB, id int64, at int64) error {
	if err := tx.Model(&storage.Repository{}).Where("repo_id = ?", id).Updates(map[string]any{
		"name_is_current": false, "state": "address_pending", "metadata_etag": nil, "updated_at": at,
	}).Error; err != nil {
		return err
	}
	return tx.Model(&storage.Job{}).
		Where("repo_id = ? AND type = 'refresh_repo' AND dedup_key LIKE 'auto:%' AND status IN ('pending','failed')", id).
		Updates(map[string]any{"status": "canceled", "last_error_code": "address_pending", "updated_at": at}).Error
}

// discover 合并已确认的数字身份；发现元数据不会被当成 Star 成功采样。
func (q Queue) discover(tx *gorm.DB, source *sdk.Repository, at int64) error {
	if source.GetID() <= 0 || source.GetPrivate() {
		return errors.New("发现源返回无效仓库身份")
	}
	var occupied storage.Repository
	err := tx.Take(&occupied, "full_name = ? COLLATE NOCASE AND name_is_current = 1 AND repo_id <> ?", source.GetFullName(), source.GetID()).Error
	if err == nil {
		if err := detachName(tx, occupied.RepoID, at); err != nil {
			return err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	var repo storage.Repository
	err = tx.Take(&repo, "repo_id = ?", source.GetID()).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		repo = storage.Repository{
			RepoID: source.GetID(), Owner: source.GetOwner().GetLogin(), Name: source.GetName(), FullName: source.GetFullName(),
			NameIsCurrent: true, HTMLURL: source.GetHTMLURL(), TopicsJSON: "[]", Grade: "A", State: "candidate",
			DiscoveredAt: at, LastDiscoveredAt: at, NextFetchAt: at, UpdatedAt: at,
		}
		if err := tx.Create(&repo).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	values := map[string]any{"last_discovered_at": max(at, repo.LastDiscoveredAt), "updated_at": at}
	// 已有采样基线时，搜索索引中的旧计数不覆盖最新仓库详情。
	if repo.LastSuccessAt == nil || !repo.NameIsCurrent || repo.State == "unavailable" || !strings.EqualFold(repo.FullName, source.GetFullName()) {
		values = metadata(source)
		values["last_discovered_at"], values["updated_at"] = max(at, repo.LastDiscoveredAt), at
		values["name_is_current"], values["metadata_etag"] = true, nil
		if repo.State == "unavailable" || repo.State == "address_pending" {
			values["state"] = "paused"
			values["first_not_found_at"] = nil
		}
	}
	if source.GetDisabled() {
		values["state"] = "unavailable"
	}
	if err := tx.Model(&repo).Updates(values).Error; err != nil {
		return err
	}
	if err := tx.Take(&repo, "repo_id = ?", repo.RepoID).Error; err != nil {
		return err
	}
	return q.admit(tx, &repo, at)
}

func (q Queue) admit(tx *gorm.DB, repo *storage.Repository, at int64) error {
	if repo.State == "active" || repo.Disabled || !repo.NameIsCurrent || (repo.State != "candidate" && repo.State != "paused") {
		return nil
	}
	var total int64
	if err := tx.Model(&storage.Repository{}).Where("state = 'active'").Count(&total).Error; err != nil {
		return err
	}
	if total >= int64(q.Cfg.MaxMonitoredRepos) {
		return nil
	}
	if repo.StarredByUser {
		allowed, err := q.canActivateFollow(tx)
		if err != nil || !allowed {
			return err
		}
	}
	grade, err := q.availableGrade(tx)
	if err != nil {
		return err
	}
	return tx.Model(repo).Updates(map[string]any{"state": "active", "grade": grade, "next_fetch_at": at, "updated_at": at}).Error
}

// canActivateFollow 保护已保存的下一次启动容量，避免重启前新增关注导致设置失效。
func (q Queue) canActivateFollow(tx *gorm.DB) (bool, error) {
	limit, err := q.followLimit(tx)
	if err != nil {
		return false, err
	}
	var count int64
	if err := tx.Model(&storage.Repository{}).Where("state='active' AND starred_by_user=1").Count(&count).Error; err != nil {
		return false, err
	}
	return count < limit, nil
}

func (q Queue) followLimit(tx *gorm.DB) (int64, error) {
	snapshot, err := (settings.Store{DB: tx}).Get(tx.Statement.Context)
	if err != nil {
		return 0, err
	}
	return int64(min(q.Cfg.MaxMonitoredRepos, snapshot.Settings.Scheduler.MaxMonitoredRepos)), nil
}

func (q Queue) availableGrade(tx *gorm.DB) (string, error) {
	for _, option := range []struct {
		grade string
		limit int
	}{{"A", q.Cfg.MaxActive}, {"B", q.Cfg.MaxNormal}} {
		var count int64
		if err := tx.Model(&storage.Repository{}).Where("state = 'active' AND grade = ?", option.grade).Count(&count).Error; err != nil {
			return "", err
		}
		if count < int64(option.limit) {
			return option.grade, nil
		}
	}
	return "C", nil
}

// Rebalance 每日按关注、新上榜和发现时间更新准入；阶段 2 接入有效热度。
func (q Queue) Rebalance(ctx context.Context, now time.Time) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var followedActive int64
		if err := tx.Model(&storage.Repository{}).Where("state='active' AND starred_by_user=1").Count(&followedActive).Error; err != nil {
			return err
		}
		if followedActive > int64(q.Cfg.MaxMonitoredRepos) {
			return errors.New("监控容量小于已有活跃关注仓库数，请调整容量")
		}
		var marker storage.MaintenanceState
		err := tx.Take(&marker, "key = 'admission.day'").Error
		day := fmt.Sprintf("%s:%d:%d:%d:%d", now.UTC().Format(time.DateOnly),
			q.Cfg.MaxHot, q.Cfg.MaxActive, q.Cfg.MaxNormal, q.Cfg.MaxMonitoredRepos)
		if err == nil && marker.Value == day {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		limit, err := q.followLimit(tx)
		if err != nil {
			return err
		}
		order := clause.OrderBy{Expression: clause.Expr{
			SQL:  "starred_by_user DESC, EXISTS (SELECT 1 FROM trending_observations t WHERE t.repo_id = repositories.repo_id AND t.observed_slot >= ?) DESC, last_discovered_at DESC, repo_id",
			Vars: []any{now.Add(-24 * time.Hour).UnixMilli()},
		}}
		candidates := tx.Where("state IN ('active','candidate','paused') AND disabled = 0 AND name_is_current = 1")
		// 先挑出可以恢复的关注项目，避免暂停落选仓库后又无法填补空位。
		if remaining := limit - followedActive; remaining > 0 {
			resumable := tx.Model(&storage.Repository{}).Select("repo_id").
				Where("state IN ('candidate','paused') AND starred_by_user=1 AND disabled=0 AND name_is_current=1").
				Clauses(order).Limit(int(remaining))
			candidates = candidates.Where("state='active' OR starred_by_user=0 OR repo_id IN (?)", resumable)
		} else {
			candidates = candidates.Where("state='active' OR starred_by_user=0")
		}
		var selected []storage.Repository
		if err := candidates.Clauses(order).Limit(q.Cfg.MaxMonitoredRepos).Find(&selected).Error; err != nil {
			return err
		}
		ids := make([]int64, 0, len(selected))
		for _, repo := range selected {
			ids = append(ids, repo.RepoID)
		}
		paused := tx.Model(&storage.Repository{}).Where("state = 'active' AND starred_by_user = 0")
		if len(ids) != 0 {
			paused = paused.Where("repo_id NOT IN ?", ids)
		}
		if err := paused.Updates(map[string]any{"state": "paused", "updated_at": now.UnixMilli()}).Error; err != nil {
			return err
		}
		if err := tx.Model(&storage.Job{}).Where("type='refresh_repo' AND dedup_key LIKE 'auto:%' AND status IN ('pending','failed') AND repo_id IN (SELECT repo_id FROM repositories WHERE state <> 'active')").
			Updates(map[string]any{"status": "canceled", "updated_at": now.UnixMilli()}).Error; err != nil {
			return err
		}
		for _, repo := range selected {
			if err := q.admit(tx, &repo, now.UnixMilli()); err != nil {
				return err
			}
		}
		// 配置缩小分档容量时保留高优先级项目，其余项目降低采样频率。
		for _, option := range []struct {
			grade string
			limit int
		}{{"S", q.Cfg.MaxHot}, {"A", q.Cfg.MaxActive}, {"B", q.Cfg.MaxNormal}} {
			var graded []storage.Repository
			if err := tx.Where("state='active' AND grade=?", option.grade).
				Order("starred_by_user DESC, last_discovered_at DESC, repo_id").Find(&graded).Error; err != nil {
				return err
			}
			for _, repo := range graded[min(len(graded), option.limit):] {
				grade, err := q.availableGrade(tx)
				if err != nil {
					return err
				}
				if err := tx.Model(&repo).Updates(map[string]any{"grade": grade, "updated_at": now.UnixMilli()}).Error; err != nil {
					return err
				}
			}
		}
		marker = storage.MaintenanceState{Key: "admission.day", Value: day, UpdatedAt: now.UnixMilli()}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
	})
}

// Refresh 接受手动请求，先按 requestId 查重再安排采样槽。
func (q Queue) Refresh(ctx context.Context, repoID int64, requestID string, now time.Time) (storage.Job, error) {
	var job storage.Job
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Take(&repo, "repo_id = ?", repoID).Error; err != nil {
			return err
		}
		var err error
		job, err = q.enqueue(tx, "refresh_repo", &repoID,
			fmtManualKey(repoID, requestID), Payload{PlannedAt: now.UnixMilli(), Manual: true}, 200, now.UnixMilli())
		return err
	})
	return job, err
}

func fmtManualKey(id int64, requestID string) string {
	return "manual:refresh_repo:" + strconv.FormatInt(id, 10) + ":" + requestID
}

// Follow 在同一事务中处理关注、容量替换及采集任务。
func (q Queue) Follow(ctx context.Context, repoID int64, followed bool, now time.Time) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Take(&repo, "repo_id = ?", repoID).Error; err != nil {
			return err
		}
		wasFollowed := repo.StarredByUser
		canBecomeActive := (repo.State == "candidate" || repo.State == "paused") && !repo.Disabled && repo.NameIsCurrent
		if followed && !wasFollowed && (repo.State == "active" || canBecomeActive) {
			allowed, err := q.canActivateFollow(tx)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrCapacity
			}
		}
		if followed && !wasFollowed && repo.State != "active" {
			var count int64
			if err := tx.Model(&storage.Repository{}).Where("state='active'").Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(q.Cfg.MaxMonitoredRepos) {
				var victim storage.Repository
				if err := tx.Where("state='active' AND starred_by_user=0").
					Order("CASE grade WHEN 'C' THEN 0 WHEN 'B' THEN 1 WHEN 'A' THEN 2 ELSE 3 END, last_discovered_at, repo_id DESC").
					Take(&victim).Error; errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrCapacity
				} else if err != nil {
					return err
				}
				if canBecomeActive {
					if err := tx.Model(&victim).Update("state", "paused").Error; err != nil {
						return err
					}
					if err := tx.Model(&storage.Job{}).Where("repo_id=? AND dedup_key LIKE 'auto:%' AND status IN ('pending','failed')", victim.RepoID).
						Update("status", "canceled").Error; err != nil {
						return err
					}
				}
			}
		}
		var followedAt *int64
		if followed {
			followedAt = repo.FollowedAt
			if followedAt == nil {
				value := now.UnixMilli()
				followedAt = &value
			}
		}
		if err := tx.Model(&repo).Updates(map[string]any{"starred_by_user": followed, "followed_at": followedAt, "updated_at": now.UnixMilli()}).Error; err != nil {
			return err
		}
		if followed {
			if err := q.admit(tx, &repo, now.UnixMilli()); err != nil {
				return err
			}
			if !wasFollowed {
				key := "follow:refresh_repo:" + strconv.FormatInt(repoID, 10) + ":" + strconv.FormatInt(*followedAt, 10)
				_, err := q.enqueue(tx, "refresh_repo", &repoID, key, Payload{PlannedAt: now.UnixMilli(), Manual: true}, 200, now.UnixMilli())
				return err
			}
		}
		return nil
	})
}
