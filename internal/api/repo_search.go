package api

import (
	"encoding/hex"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/embedding"
	"github.com/lifei6671/github-radar/internal/storage"
)

// instr 按字面搜索，用户输入中的 % 和 _ 不扩展为通配符。
var literalRepoSearch = "instr(lower(" + storage.RepositoryTable + "." + storage.RepositoryColumns.FullName +
	"), lower(?)) > 0 OR instr(lower(coalesce(" + storage.RepositoryTable + "." + storage.RepositoryColumns.Description + ",'')), lower(?)) > 0"

// prepareRepoSearch 在 SQLite 事务外取查询向量。游标绑定实际检索方式与模型向量。
func (a *collectionAPI) prepareRepoSearch(c *gin.Context, filters repoFilters, sort string) (embedding.QueryVector, error) {
	var vector embedding.QueryVector
	if sort != consts.SortRelevance {
		return vector, nil
	}
	var expected string
	if raw, exists := c.GetQuery("cursor"); exists {
		cursor, err := decodeCursor(raw, sort, filters)
		if err != nil {
			return vector, errInvalidCursor
		}
		expected = cursor.Vector
		if expected == "" {
			// 首页已回退到字面搜索时，后续页沿用原排序。
			return vector, nil
		}
		if _, err := hex.DecodeString(expected); len(expected) != 64 || err != nil {
			return vector, errInvalidCursor
		}
	}
	if vectors := runtimeFor(c).QueryVectors; vectors != nil {
		ctx := c.Request.Context()
		db := a.db.WithContext(ctx)
		paused, err := storage.BooleanSetting(ctx, db, storage.AutoAIResumeKey)
		if err != nil {
			return vector, err
		}
		if !paused {
			ready, err := a.hasSearchVectors(c, db, filters)
			if err != nil {
				return vector, err
			}
			if ready {
				vector = vectors.Vector(ctx, filters.Q)
			}
		}
	}
	if expected != "" && vector.Key != expected {
		return embedding.QueryVector{}, errInvalidCursor
	}
	return vector, nil
}

func (a *collectionAPI) hasSearchVectors(c *gin.Context, tx *gorm.DB, filters repoFilters) (bool, error) {
	repo, vector := storage.RepositoryTable+".", storage.RepoEmbeddingTable+"."
	r, v := storage.RepositoryColumns, storage.RepoEmbeddingColumns
	// 先验证专题并套用筛选，避免为没有可用向量的搜索付费。
	db, err := a.filterRepoList(c, tx, tx.Model(&storage.Repository{}).Where(readableRepoCondition(c)), filters)
	if err != nil {
		return false, err
	}
	var row storage.Repository
	err = db.Select(repo+r.RepoID).
		Joins("JOIN "+storage.RepoEmbeddingTable+" ON "+vector+v.RepoID+" = "+repo+r.RepoID).
		Where(vector+v.ModelKey+" = ? AND "+vector+v.Vector+" IS NOT NULL", embedding.ModelKey(runtimeFor(c).Config.Embedding)).
		Limit(1).Find(&row).Error
	return row.RepoID != 0, err
}

func (a *collectionAPI) relevanceRepoPage(c *gin.Context, tx *gorm.DB, filters repoFilters, vector embedding.QueryVector, limit int) (gin.H, error) {
	repo, vectorTable := storage.RepositoryTable+".", storage.RepoEmbeddingTable+"."
	r, v := storage.RepositoryColumns, storage.RepoEmbeddingColumns
	db, err := a.filterRepoList(c, tx, publicRepos(c, tx.Model(&storage.Repository{})), filters)
	if err != nil {
		return nil, err
	}
	rank := "CASE WHEN lower(" + repo + r.FullName + ") = lower(?) THEN 0 WHEN lower(" + repo + r.Name +
		") = lower(?) THEN 1 WHEN " + literalRepoSearch + " THEN 2 ELSE 3 END AS text_rank"
	selectSQL := repo + "*, " + rank
	args := []any{filters.Q, filters.Q, filters.Q, filters.Q}
	where, key, alias, direction, compare := "text_rank < 3", r.DiscoveredAt, "integer_key", " DESC", " < ?"
	var whereArgs []any
	if vector.Key != "" {
		cfg := runtimeFor(c).Config.Embedding
		// 左连接保留没有向量的字面命中，后台语义候选也只使用公开向量。
		join := "LEFT JOIN " + storage.RepoEmbeddingTable + " ON " + vectorTable + v.RepoID + " = " + repo + r.RepoID +
			" AND " + vectorTable + v.ModelKey + " = ? AND length(" + vectorTable + v.Vector + ") = ? AND " + storage.PublicRepositoryCondition
		db = db.Joins(join, embedding.ModelKey(cfg), len(vector.Data))
		selectSQL += ", CASE WHEN " + vectorTable + v.Vector + " IS NOT NULL THEN vec_distance_cosine(" + vectorTable + v.Vector + ", ?) END AS vector_distance"
		args = append(args, vector.Data)
		where, whereArgs = where+" OR vector_distance <= ?", []any{1 - cfg.MinSimilarity}
		key, alias, direction, compare = "COALESCE(vector_distance, 2)", "real_key", " ASC", " > ?"
	}
	ranked := db.Select(selectSQL, args...)
	db = tx.Table("(?) AS search_rows", ranked).Where(where, whereArgs...)
	if raw, exists := c.GetQuery("cursor"); exists {
		cursor, err := decodeCursor(raw, consts.SortRelevance, filters)
		if err != nil || cursor.Vector != vector.Key {
			return nil, errInvalidCursor
		}
		value, err := cursor.sortValue()
		if err != nil {
			return nil, errInvalidCursor
		}
		db = db.Where("text_rank > ? OR (text_rank = ? AND ("+key+compare+" OR ("+key+" = ? AND "+r.RepoID+" > ?)))",
			*cursor.Last.Rank, *cursor.Last.Rank, value, value, cursor.Last.RepoID)
	}
	var rows []struct {
		storage.Repository
		Rank       int     `gorm:"column:text_rank"`
		IntegerKey int64   `gorm:"column:integer_key"`
		RealKey    float64 `gorm:"column:real_key"`
	}
	if err := db.Select("search_rows.*, " + key + " AS " + alias).
		Order("text_rank").Order(alias + direction).Order(r.RepoID).Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		cursor := repoCursor{Version: 1, Sort: consts.SortRelevance, Filters: filters, Vector: vector.Key}
		cursor.Last.RepoID, cursor.Last.Rank = last.RepoID, &last.Rank
		cursor.Last.Value = last.IntegerKey
		if vector.Key != "" {
			cursor.Last.Value = last.RealKey
		}
		next, err = encodeRepoCursor(cursor)
		if err != nil {
			return nil, err
		}
	}
	repos := make([]storage.Repository, 0, len(rows))
	for _, row := range rows {
		repos = append(repos, row.Repository)
	}
	items, err := a.repoListCards(c, tx, repos)
	return gin.H{"items": items, "nextCursor": next, "updatedAt": iso(time.Now().UnixMilli())}, err
}
