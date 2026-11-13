package collectionstore

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

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
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
		storage.RepositoryColumns.Owner: source.GetOwner().GetLogin(), storage.RepositoryColumns.Name: source.GetName(), storage.RepositoryColumns.FullName: source.GetFullName(),
		storage.RepositoryColumns.HTMLURL: source.GetHTMLURL(), storage.RepositoryColumns.Description: source.Description, storage.RepositoryColumns.TopicsJSON: string(data),
		storage.RepositoryColumns.PrimaryLanguage: source.Language, storage.RepositoryColumns.Stars: source.GetStargazersCount(), storage.RepositoryColumns.Forks: source.GetForksCount(),
		storage.RepositoryColumns.Archived: source.GetArchived(), storage.RepositoryColumns.Disabled: source.GetDisabled(), storage.RepositoryColumns.LicenseSPDXID: nil, storage.RepositoryColumns.LicenseName: nil,
		storage.RepositoryColumns.GitHubCreatedAt: nil, storage.RepositoryColumns.GitHubPushedAt: nil,
	}
	if source.License != nil {
		values[storage.RepositoryColumns.LicenseSPDXID], values[storage.RepositoryColumns.LicenseName] = source.License.SPDXID, source.License.Name
	}
	if source.CreatedAt != nil {
		values[storage.RepositoryColumns.GitHubCreatedAt] = source.CreatedAt.UnixMilli()
	}
	if source.PushedAt != nil {
		values[storage.RepositoryColumns.GitHubPushedAt] = source.PushedAt.UnixMilli()
	}
	return values
}

// detachName 解除旧名称寻址，历史、数字 ID 和关注状态始终保留。
func detachName(tx *gorm.DB, id int64, at int64) error {
	if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.RepoID+" = ?", id).Updates(map[string]any{
		storage.RepositoryColumns.NameIsCurrent: false, storage.RepositoryColumns.State: consts.StatusAddressPending, storage.RepositoryColumns.MetadataETag: nil, storage.RepositoryColumns.UpdatedAt: at,
	}).Error; err != nil {
		return err
	}
	return tx.Model(&storage.Job{}).
		Where(storage.JobColumns.RepoID+" = ? AND "+storage.JobColumns.Type+" = '"+consts.JobRefreshRepo+"' AND "+storage.JobColumns.DedupKey+" LIKE '"+consts.AutoJobPattern+"' AND "+storage.JobColumns.Status+" IN ('"+consts.StatusPending+"','"+consts.StatusFailed+"')", id).
		Updates(map[string]any{storage.JobColumns.Status: consts.StatusCanceled, storage.JobColumns.LastErrorCode: consts.StatusAddressPending, storage.JobColumns.UpdatedAt: at}).Error
}

// Discover 合并已确认的数字身份；发现元数据不会被当成 Star 成功采样。
func (q Queue) Discover(tx *gorm.DB, source *sdk.Repository, at int64) (bool, error) {
	if source.GetID() <= 0 || source.GetPrivate() {
		return false, errors.New("发现源返回无效仓库身份")
	}
	var repo storage.Repository
	lookupErr := tx.Take(&repo, storage.RepositoryColumns.RepoID+" = ?", source.GetID()).Error
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return false, lookupErr
	}
	if repo.Blocked {
		return false, nil
	}
	var occupied storage.Repository
	err := tx.Take(&occupied, storage.RepositoryColumns.FullName+" = ? COLLATE NOCASE AND "+storage.RepositoryColumns.NameIsCurrent+" = 1 AND "+storage.RepositoryColumns.RepoID+" <> ?", source.GetFullName(), source.GetID()).Error
	if err == nil {
		if err := detachName(tx, occupied.RepoID, at); err != nil {
			return false, err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		repo = storage.Repository{
			RepoID: source.GetID(), Owner: source.GetOwner().GetLogin(), Name: source.GetName(), FullName: source.GetFullName(),
			NameIsCurrent: true, HTMLURL: source.GetHTMLURL(), TopicsJSON: "[]", Grade: consts.GradeA, State: consts.StatusCandidate,
			DiscoveredAt: at, LastDiscoveredAt: at, NextFetchAt: at, UpdatedAt: at,
		}
		if err := tx.Create(&repo).Error; err != nil {
			return false, err
		}
	}
	values := map[string]any{storage.RepositoryColumns.LastDiscoveredAt: max(at, repo.LastDiscoveredAt), storage.RepositoryColumns.UpdatedAt: at}
	// 已有采样基线时，搜索索引中的旧计数不覆盖最新仓库详情。
	if repo.LastSuccessAt == nil || !repo.NameIsCurrent || repo.State == consts.StatusUnavailable || !strings.EqualFold(repo.FullName, source.GetFullName()) {
		values = metadata(source)
		values[storage.RepositoryColumns.LastDiscoveredAt], values[storage.RepositoryColumns.UpdatedAt] = max(at, repo.LastDiscoveredAt), at
		values[storage.RepositoryColumns.NameIsCurrent], values[storage.RepositoryColumns.MetadataETag] = true, nil
		// 只有成功采样才能清除不可访问标记，避免搜索索引提前恢复公开展示。
		if repo.State == consts.StatusUnavailable || repo.State == consts.StatusAddressPending {
			values[storage.RepositoryColumns.State] = consts.StatusPaused
		}
	}
	if source.GetDisabled() {
		values[storage.RepositoryColumns.State] = consts.StatusUnavailable
	}
	if err := tx.Model(&repo).Updates(values).Error; err != nil {
		return false, err
	}
	if err := tx.Take(&repo, storage.RepositoryColumns.RepoID+" = ?", repo.RepoID).Error; err != nil {
		return false, err
	}
	return true, q.admit(tx, &repo, at)
}

func (q Queue) admit(tx *gorm.DB, repo *storage.Repository, at int64) error {
	if !repo.MonitoringState().CanMonitor() {
		return nil
	}
	var total int64
	if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.State + " = '" + consts.StatusActive + "'").Count(&total).Error; err != nil {
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
	return tx.Model(repo).Updates(map[string]any{storage.RepositoryColumns.State: consts.StatusActive, storage.RepositoryColumns.Grade: grade, storage.RepositoryColumns.BelowGradeSince: nil, storage.RepositoryColumns.NextFetchAt: at, storage.RepositoryColumns.UpdatedAt: at}).Error
}

// canActivateFollow 保护已保存的下一次启动容量，避免重启前新增关注导致设置失效。
func (q Queue) canActivateFollow(tx *gorm.DB) (bool, error) {
	limit, err := q.followLimit(tx)
	if err != nil {
		return false, err
	}
	var count int64
	if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.State + "='" + consts.StatusActive + "' AND " + storage.RepositoryColumns.StarredByUser + "=1").Count(&count).Error; err != nil {
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
	}{{consts.GradeA, q.Cfg.MaxActive}, {consts.GradeB, q.Cfg.MaxNormal}} {
		var count int64
		if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.State+" = '"+consts.StatusActive+"' AND "+storage.RepositoryColumns.Grade+" = ?", option.grade).Count(&count).Error; err != nil {
			return "", err
		}
		if count < int64(option.limit) {
			return option.grade, nil
		}
	}
	return consts.GradeC, nil
}

// Rebalance 每日按关注、新上榜和发现时间更新准入；阶段 2 接入有效热度。
func (q Queue) Rebalance(ctx context.Context, now time.Time) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var followedActive int64
		if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.State + "='" + consts.StatusActive + "' AND " + storage.RepositoryColumns.StarredByUser + "=1").Count(&followedActive).Error; err != nil {
			return err
		}
		if followedActive > int64(q.Cfg.MaxMonitoredRepos) {
			return errors.New("监控容量小于已有活跃关注仓库数，请调整容量")
		}
		var marker storage.MaintenanceState
		err := tx.Take(&marker, storage.MaintenanceStateColumns.Key+" = ?", consts.AdmissionDayKey).Error
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
			SQL: fmt.Sprintf("%[1]s DESC, (SELECT %[2]s FROM %[3]s m WHERE m.%[4]s=%[5]s.%[6]s AND %[5]s.%[7]s='%[8]s') DESC, EXISTS (SELECT 1 FROM %[9]s t WHERE t.%[10]s = %[5]s.%[6]s AND t.%[11]s >= ?) DESC, %[12]s DESC, %[6]s",
				storage.RepositoryColumns.StarredByUser, storage.LatestMetricColumns.HotnessScore, storage.LatestMetricTable, storage.LatestMetricColumns.RepoID, storage.RepositoryTable, storage.RepositoryColumns.RepoID, storage.RepositoryColumns.State, consts.StatusActive, storage.TrendingObservationTable, storage.TrendingObservationColumns.RepoID, storage.TrendingObservationColumns.ObservedSlot, storage.RepositoryColumns.LastDiscoveredAt),
			Vars: []any{now.Add(-24 * time.Hour).UnixMilli()},
		}}
		candidates := tx.Where(storage.RepositoryColumns.State + " IN ('" + consts.StatusActive + "','" + consts.StatusCandidate + "','" + consts.StatusPaused + "') AND " + storage.RepositoryColumns.Blocked + "=0 AND " + storage.RepositoryColumns.Disabled + " = 0 AND " + storage.RepositoryColumns.NameIsCurrent + " = 1")
		// 先挑出可以恢复的关注项目，避免暂停落选仓库后又无法填补空位。
		if remaining := limit - followedActive; remaining > 0 {
			resumable := tx.Model(&storage.Repository{}).Select(storage.RepositoryColumns.RepoID).
				Where(storage.RepositoryColumns.State + " IN ('" + consts.StatusCandidate + "','" + consts.StatusPaused + "') AND " + storage.RepositoryColumns.Blocked + "=0 AND " + storage.RepositoryColumns.StarredByUser + "=1 AND " + storage.RepositoryColumns.Disabled + "=0 AND " + storage.RepositoryColumns.NameIsCurrent + "=1").
				Clauses(order).Limit(int(remaining))
			candidates = candidates.Where(storage.RepositoryColumns.State+"='"+consts.StatusActive+"' OR "+storage.RepositoryColumns.StarredByUser+"=0 OR "+storage.RepositoryColumns.RepoID+" IN (?)", resumable)
		} else {
			candidates = candidates.Where(storage.RepositoryColumns.State + "='" + consts.StatusActive + "' OR " + storage.RepositoryColumns.StarredByUser + "=0")
		}
		var selected []storage.Repository
		if err := candidates.Clauses(order).Limit(q.Cfg.MaxMonitoredRepos).Find(&selected).Error; err != nil {
			return err
		}
		ids := make([]int64, 0, len(selected))
		for _, repo := range selected {
			ids = append(ids, repo.RepoID)
		}
		paused := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.State + " = '" + consts.StatusActive + "' AND " + storage.RepositoryColumns.StarredByUser + " = 0")
		if len(ids) != 0 {
			paused = paused.Where(storage.RepositoryColumns.RepoID+" NOT IN ?", ids)
		}
		if err := paused.Updates(map[string]any{storage.RepositoryColumns.State: consts.StatusPaused, storage.RepositoryColumns.UpdatedAt: now.UnixMilli()}).Error; err != nil {
			return err
		}
		if err := tx.Model(&storage.Job{}).Where(fmt.Sprintf("%[1]s='%[2]s' AND %[3]s LIKE '%[4]s' AND %[5]s IN ('%[6]s','%[7]s') AND %[8]s IN (SELECT %[9]s FROM %[10]s WHERE %[11]s <> '%[12]s')",
			storage.JobColumns.Type, consts.JobRefreshRepo, storage.JobColumns.DedupKey, consts.AutoJobPattern, storage.JobColumns.Status, consts.StatusPending, consts.StatusFailed, storage.JobColumns.RepoID, storage.RepositoryColumns.RepoID, storage.RepositoryTable, storage.RepositoryColumns.State, consts.StatusActive)).
			Updates(map[string]any{storage.JobColumns.Status: consts.StatusCanceled, storage.JobColumns.UpdatedAt: now.UnixMilli()}).Error; err != nil {
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
		}{{consts.GradeS, q.Cfg.MaxHot}, {consts.GradeA, q.Cfg.MaxActive}, {consts.GradeB, q.Cfg.MaxNormal}} {
			var graded []storage.Repository
			if err := tx.Where(storage.RepositoryColumns.State+"='"+consts.StatusActive+"' AND "+storage.RepositoryColumns.Grade+"=?", option.grade).
				Order(storage.RepositoryColumns.StarredByUser + " DESC, " + storage.RepositoryColumns.LastDiscoveredAt + " DESC, " + storage.RepositoryColumns.RepoID).Find(&graded).Error; err != nil {
				return err
			}
			for _, repo := range graded[min(len(graded), option.limit):] {
				grade, err := q.availableGrade(tx)
				if err != nil {
					return err
				}
				if err := tx.Model(&repo).Updates(map[string]any{storage.RepositoryColumns.Grade: grade, storage.RepositoryColumns.BelowGradeSince: nil, storage.RepositoryColumns.UpdatedAt: now.UnixMilli()}).Error; err != nil {
					return err
				}
			}
		}
		marker = storage.MaintenanceState{Key: consts.AdmissionDayKey, Value: day, UpdatedAt: now.UnixMilli()}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
	})
}

// Refresh 接受手动请求，先按 requestId 查重再安排采样槽。
func (q Queue) Refresh(ctx context.Context, repoID int64, requestID string, now time.Time) (storage.Job, error) {
	var job storage.Job
	err := q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Take(&repo, storage.RepositoryColumns.RepoID+" = ?", repoID).Error; err != nil {
			return err
		}
		if repo.Blocked {
			return storage.ErrRepositoryBlocked
		}
		var err error
		job, err = q.enqueue(tx, consts.JobRefreshRepo, &repoID,
			fmtManualKey(repoID, requestID), collection.Payload{PlannedAt: now.UnixMilli(), Manual: true}, 200, now.UnixMilli())
		return err
	})
	return job, err
}

func fmtManualKey(id int64, requestID string) string {
	return fmt.Sprintf(consts.RefreshManualDedupFormat, id, requestID)
}

// Follow 在同一事务中处理关注、容量替换及采集任务。
func (q Queue) Follow(ctx context.Context, repoID int64, followed bool, now time.Time) error {
	return q.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Take(&repo, storage.RepositoryColumns.RepoID+" = ?", repoID).Error; err != nil {
			return err
		}
		if repo.Blocked && followed {
			return storage.ErrRepositoryBlocked
		}
		wasFollowed := repo.StarredByUser
		canBecomeActive := (repo.State == consts.StatusCandidate || repo.State == consts.StatusPaused) && !repo.Disabled && repo.NameIsCurrent
		if followed && !wasFollowed && (repo.State == consts.StatusActive || canBecomeActive) {
			allowed, err := q.canActivateFollow(tx)
			if err != nil {
				return err
			}
			if !allowed {
				return collection.ErrCapacity
			}
		}
		if followed && !wasFollowed && repo.State != consts.StatusActive {
			var count int64
			if err := tx.Model(&storage.Repository{}).Where(storage.RepositoryColumns.State + "='" + consts.StatusActive + "'").Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(q.Cfg.MaxMonitoredRepos) {
				var victim storage.Repository
				if err := tx.Where(storage.RepositoryColumns.State + "='" + consts.StatusActive + "' AND " + storage.RepositoryColumns.StarredByUser + "=0").
					Order("CASE " + storage.RepositoryColumns.Grade + " WHEN '" + consts.GradeC + "' THEN 0 WHEN '" + consts.GradeB + "' THEN 1 WHEN '" + consts.GradeA + "' THEN 2 ELSE 3 END, " + storage.RepositoryColumns.LastDiscoveredAt + ", " + storage.RepositoryColumns.RepoID + " DESC").
					Take(&victim).Error; errors.Is(err, gorm.ErrRecordNotFound) {
					return collection.ErrCapacity
				} else if err != nil {
					return err
				}
				if canBecomeActive {
					if err := tx.Model(&victim).Update(storage.RepositoryColumns.State, consts.StatusPaused).Error; err != nil {
						return err
					}
					if err := tx.Model(&storage.Job{}).Where(storage.JobColumns.RepoID+"=? AND "+storage.JobColumns.DedupKey+" LIKE '"+consts.AutoJobPattern+"' AND "+storage.JobColumns.Status+" IN ('"+consts.StatusPending+"','"+consts.StatusFailed+"')", victim.RepoID).
						Update(storage.JobColumns.Status, consts.StatusCanceled).Error; err != nil {
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
		if err := tx.Model(&repo).Updates(map[string]any{storage.RepositoryColumns.StarredByUser: followed, storage.RepositoryColumns.FollowedAt: followedAt, storage.RepositoryColumns.UpdatedAt: now.UnixMilli()}).Error; err != nil {
			return err
		}
		if followed {
			if err := q.admit(tx, &repo, now.UnixMilli()); err != nil {
				return err
			}
			if !wasFollowed {
				key := "follow:refresh_repo:" + strconv.FormatInt(repoID, 10) + ":" + strconv.FormatInt(*followedAt, 10)
				_, err := q.enqueue(tx, consts.JobRefreshRepo, &repoID, key, collection.Payload{PlannedAt: now.UnixMilli(), Manual: true}, 200, now.UnixMilli())
				return err
			}
		}
		return nil
	})
}
