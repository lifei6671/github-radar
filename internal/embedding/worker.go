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

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
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
	DB     *gorm.DB
	Config config.Embedding
	Embed  func(context.Context, string) ([]byte, error)
	Readme func(context.Context, storage.Repository) (string, error)
	Logger *slog.Logger
}

func (w Worker) Run(ctx context.Context) error {
	tick := time.NewTicker(time.Duration(w.Config.IntervalMinutes) * time.Minute)
	defer tick.Stop()
	for ctx.Err() == nil {
		if err := w.once(ctx, time.Now()); err != nil && ctx.Err() == nil {
			w.Logger.Warn("相似项目向量扫描失败", "error", err)
		}
		select {
		case <-ctx.Done():
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
	key := ModelKey(w.Config)
	table := storage.RepoEmbeddingTable + "."
	var batch []storage.Repository
	err = db.Model(&storage.Repository{}).Select(storage.RepositoryTable+".*").
		Joins("LEFT "+embeddingJoin).Where(storage.PublicRepositoryCondition).
		Where(table+vectors.RepoID+" IS NULL OR "+table+vectors.ModelKey+" <> ? OR "+table+vectors.NextCheckAt+" <= ?", key, now.UnixMilli()).
		Order(table + vectors.NextCheckAt).Order(storage.RepositoryTable + "." + repos.RepoID).Limit(w.Config.BatchSize).Find(&batch).Error
	if err != nil {
		return err
	}
	for _, repo := range batch {
		if err := w.process(ctx, repo, key, now); err != nil {
			return err
		}
	}
	return nil
}

func (w Worker) process(ctx context.Context, repo storage.Repository, key string, now time.Time) error {
	db := w.DB.WithContext(ctx)
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
		vector, err := w.Embed(ctx, input)
		if err != nil {
			return w.retry(ctx, row, now, err)
		}
		row.Vector = vector
	}
	row.SourceFingerprint = fingerprint
	return save(db, row)
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
