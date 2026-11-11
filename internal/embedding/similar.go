package embedding

import (
	"database/sql"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

const resultLimit = 5

type Match struct {
	RepoID          int64   `gorm:"column:repo_id" json:"id"`
	FullName        string  `gorm:"column:full_name" json:"fullName"`
	Description     *string `gorm:"column:description" json:"description"`
	PrimaryLanguage *string `gorm:"column:primary_language" json:"language"`
	Stars           int     `gorm:"column:stars" json:"stars"`
	Distance        float64 `gorm:"column:distance" json:"-"`
}

// Similar 在同一读快照检查来源和候选，只排序同模型、同维数的公开仓库。
func Similar(db *gorm.DB, cfg config.Embedding, id int64) ([]Match, error) {
	items := make([]Match, 0, resultLimit)
	err := db.Transaction(func(tx *gorm.DB) error {
		var source storage.Repository
		if err := tx.Select(repos.RepoID).Where(storage.PublicRepositoryCondition).
			Take(&source, repos.RepoID+" = ?", id).Error; err != nil {
			return err
		}
		if !cfg.Enabled {
			return nil
		}
		key := ModelKey(cfg)
		var seed storage.RepoEmbedding
		if err := tx.Find(&seed, vectors.RepoID+" = ? AND "+vectors.ModelKey+" = ?", id, key).Error; err != nil || len(seed.Vector) == 0 {
			return err
		}
		repo, emb := storage.RepositoryTable+".", storage.RepoEmbeddingTable+"."
		selection := repo + repos.RepoID + ", " + repo + repos.FullName + ", " + repo + repos.Description + ", " + repo + repos.PrimaryLanguage + ", " + repo + repos.Stars +
			", vec_distance_cosine(" + emb + vectors.Vector + ", ?) AS distance"
		var candidates []Match
		if err := tx.Model(&storage.Repository{}).Select(selection, seed.Vector).Joins(embeddingJoin).
			Where(storage.PublicRepositoryCondition).Where(repo+repos.RepoID+" <> ?", id).
			Where(emb+vectors.ModelKey+" = ? AND length("+emb+vectors.Vector+") = ?", key, len(seed.Vector)).
			Order("distance").Order(repo + repos.RepoID).Limit(resultLimit).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, match := range candidates {
			if match.Distance <= 1-cfg.MinSimilarity {
				items = append(items, match)
			}
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return items, err
}
