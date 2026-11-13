package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/storage"
)

const (
	refreshInterval = 7 * 24 * time.Hour
	retryInterval   = time.Hour
)

var (
	repos         = storage.RepositoryColumns
	vectors       = storage.RepoEmbeddingColumns
	embeddingJoin = "JOIN " + storage.RepoEmbeddingTable + " ON " + storage.RepoEmbeddingTable + "." +
		vectors.RepoID + " = " + storage.RepositoryTable + "." + repos.RepoID
)

// Worker 在可中止的后台生命周期中分批检查来源，页面请求无需访问模型。
type Worker struct {
	DB              *gorm.DB
	Config          config.Embedding
	Embed           func(context.Context, string) ([]byte, error)
	Readme          func(context.Context, storage.Repository) (string, error)
	Logger          *slog.Logger
	Budget          aibudget.Service
	LegacyAdmission bool
}

func (w Worker) Run(ctx context.Context) error {
	tick := time.NewTicker(time.Duration(w.Config.IntervalMinutes) * time.Minute)
	defer tick.Stop()
	for !lifecycle.Stopping(ctx) {
		if err := w.once(ctx, time.Now()); err != nil && ctx.Err() == nil {
			w.Logger.Warn("相似项目向量扫描失败", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-lifecycle.SchedulingDone(ctx):
		case <-tick.C:
		}
	}
	return nil
}

func (w Worker) once(ctx context.Context, now time.Time) error {
	db := w.DB.WithContext(ctx)
	// 和自动摘要共用恢复后的持久化暂停标记，确认前无需请求 GitHub 或模型。
	paused, err := storage.BooleanSetting(ctx, db, storage.AutoAIResumeKey)
	if err != nil || paused {
		return err
	}
	if w.Budget.DB != nil {
		remaining, err := w.Budget.Remaining(db, admission.Maintenance, now)
		if err != nil || remaining == 0 {
			return err
		}
	}
	key := ModelKey(w.Config)
	table := storage.RepoEmbeddingTable + "."
	var batch []storage.Repository
	err = db.Model(&storage.Repository{}).Select(storage.RepositoryTable+".*").
		Joins("LEFT "+embeddingJoin).Where(storage.PublicRepositoryCondition).
		Where("NOT EXISTS (?)", db.Model(&storage.AdmissionDecision{}).Select("1").
			Where("repo_id = repositories.repo_id AND operation = ? AND state <> ? AND next_check_at > ? AND updated_at >= repositories.updated_at",
				"embedding", admission.Pass, now.UnixMilli())).
		Where(table+vectors.RepoID+" IS NULL OR "+table+vectors.ModelKey+" <> ? OR "+table+vectors.NextCheckAt+" <= ?", key, now.UnixMilli()).
		Order(table + vectors.NextCheckAt).Order(storage.RepositoryTable + "." + repos.RepoID).Limit(w.Config.BatchSize).Find(&batch).Error
	if err != nil {
		return err
	}
	for _, repo := range batch {
		if lifecycle.Stopping(ctx) {
			break
		}
		if err := w.process(ctx, repo, key, now); err != nil {
			return err
		}
	}
	return nil
}

func (w Worker) process(ctx context.Context, repo storage.Repository, key string, now time.Time) error {
	db := w.DB.WithContext(ctx)
	if !w.LegacyAdmission {
		allowed, err := w.admitted(db, repo, now)
		if err != nil || !allowed {
			return err
		}
	}
	var row storage.RepoEmbedding
	if err := db.Find(&row, vectors.RepoID+" = ?", repo.RepoID).Error; err != nil {
		return err
	}
	if row.ModelKey != key {
		row = storage.RepoEmbedding{RepoID: repo.RepoID, ModelKey: key}
	}
	readme, err := w.Readme(ctx, repo)
	if err != nil {
		return w.retry(ctx, row, now, err)
	}
	// 等待 GitHub 时仓库可能被屏蔽或撤回，先读取仍可公开的最新元数据。
	err = db.Where(storage.PublicRepositoryCondition).Take(&repo, repos.RepoID+" = ?", repo.RepoID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	input, err := buildInput(db, repo, readme)
	if err != nil {
		return err
	}
	row.NextCheckAt = now.Add(refreshInterval).UnixMilli()
	fingerprint := digest(input)
	switch {
	case input == "":
		row.Vector = nil
		row.NextCheckAt = now.Add(retryInterval).UnixMilli()
	case row.SourceFingerprint != fingerprint || len(row.Vector) == 0:
		// 读取 Topics 和摘要时也可能发生屏蔽，最后一次确认紧邻外部请求。
		public, err := stillPublic(db, repo.RepoID)
		if err != nil || !public {
			return err
		}
		if !w.LegacyAdmission {
			var about string
			if repo.Description != nil {
				about = *repo.Description
			}
			if !repo.StarredByUser && !admission.Readable(about, repo.Name) && !admission.Readable(readme, repo.Name) {
				return storage.SaveAdmission(db, repo.RepoID, "embedding", admission.Decision{State: admission.Defer,
					Reason: "no_readable_source", RetryAt: now.Add(refreshInterval).UnixMilli()}, admission.Evidence{}, now)
			}
			allowed, err := w.admitted(db, repo, time.Now())
			if err != nil || !allowed {
				return err
			}
		}
		stored := false
		original := row
		sendCtx := context.WithValue(ctx, repositoryInputKey{}, repositoryInput{ID: repo.RepoID, FullName: repo.FullName,
			Save: func(tx *gorm.DB, vector []byte) error {
				var latest storage.Repository
				if err := tx.Find(&latest, "repo_id = ?", repo.RepoID).Error; err != nil {
					return err
				}
				if !latest.IsPublic() || latest.FullName != repo.FullName {
					// 不能落盘的已发结果保持未决，避免身份恢复时自动付费重发相同输入。
					return aibudget.ErrUncertain
				}
				row.Vector, row.SourceFingerprint = vector, fingerprint
				if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
					return err
				}
				stored = true
				return nil
			}})
		vector, err := w.Embed(sendCtx, input)
		if err != nil {
			return w.retry(ctx, original, now, err)
		}
		if stored {
			return nil
		}
		row.Vector = vector
	}
	row.SourceFingerprint = fingerprint
	return save(db, row)
}

// admitted 使用与摘要相同的内容门槛，记录被过滤的仓库以避免反复扫头部。
func (w Worker) admitted(db *gorm.DB, repo storage.Repository, now time.Time) (bool, error) {
	d, evidence, err := storage.AIDecision(db, repo, "embedding", false, now)
	if err != nil {
		return false, err
	}
	return d.State == admission.Pass, storage.SaveAdmission(db, repo.RepoID, "embedding", d, evidence, now)
}

func (w Worker) retry(ctx context.Context, row storage.RepoEmbedding, now time.Time, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	w.Logger.Warn("相似项目向量生成暂缓", "repoId", row.RepoID, "error", err)
	row.NextCheckAt = now.Add(retryInterval).UnixMilli()
	return save(w.DB.WithContext(ctx), row)
}

func save(db *gorm.DB, row storage.RepoEmbedding) error {
	return db.Transaction(func(tx *gorm.DB) error {
		public, err := stillPublic(tx, row.RepoID)
		if err != nil || !public {
			return err
		}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
	})
}

func stillPublic(db *gorm.DB, id int64) (bool, error) {
	var repo storage.Repository
	err := db.Select(repos.RepoID).Where(storage.PublicRepositoryCondition).Take(&repo, repos.RepoID+" = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// buildInput 复用 GitHub 主题词，缺少时才使用 AI Topics。只采纳当前有效的摘要。
func buildInput(db *gorm.DB, repo storage.Repository, readme string) (string, error) {
	topics := repo.GitHubTopics()
	if len(topics) == 0 {
		var generated storage.RepoAITopics
		if err := db.Find(&generated, storage.RepoAITopicsColumns.RepoID+" = ?", repo.RepoID).Error; err != nil {
			return "", err
		}
		if generated.RepoID != 0 {
			if err := json.Unmarshal([]byte(generated.TopicsJSON), &topics); err != nil {
				return "", err
			}
		}
	}
	var summary storage.AISummary
	s := storage.AISummaryColumns
	if err := db.Select(s.SummaryZh).Where(s.SummaryStatus+" = ?", consts.StatusReady).
		Find(&summary, s.RepoID+" = ?", repo.RepoID).Error; err != nil {
		return "", err
	}
	var about string
	if repo.Description != nil {
		about = *repo.Description
	}
	sections := []struct {
		label, value string
		maxBytes     int
	}{
		{"About", about, 1024},
		{"Topics", strings.Join(topics, ", "), 512},
		{"README", readme, 4096},
		{"摘要", summary.SummaryZh, 1536},
	}
	var parts []string
	for _, section := range sections {
		value := strings.Join(strings.Fields(section.value), " ")
		if len(value) > section.maxBytes {
			end := section.maxBytes
			for !utf8.RuneStart(value[end]) {
				end--
			}
			value = value[:end]
		}
		if value != "" {
			parts = append(parts, section.label+": "+value)
		}
	}
	return strings.Join(parts, "\n"), nil
}
