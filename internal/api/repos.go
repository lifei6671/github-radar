package api

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

var errInvalidCursor = errors.New("无效分页游标")

// repoFilters 与游标内的过滤快照共用同一种表示。
type repoFilters struct {
	Q        string   `json:"q"`
	Language string   `json:"language"`
	State    string   `json:"state"`
	Followed bool     `json:"followed"`
	Category []string `json:"category"`
	Grade    string   `json:"grade,omitempty"`
	Tag      string   `json:"tag,omitempty"`
}

type repoCursor struct {
	Version int         `json:"v"`
	Sort    string      `json:"sort"`
	Filters repoFilters `json:"filters"`
	Last    struct {
		Value  any   `json:"value"`
		RepoID int64 `json:"repoId"`
	} `json:"last"`
}

func (a *collectionAPI) repos(c *gin.Context) {
	limit, ok := pageLimit(c)
	if !ok {
		return
	}
	filters, sort, err := a.filters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_filter"})
		return
	}
	if c.GetBool(publicReadKey) && filters.Followed {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_filter"})
		return
	}
	// 排序键、全局名次及计算时间来自同一读快照，计算任务可以并发提交新一轮结果。
	var page gin.H
	db := a.db.WithContext(c.Request.Context())
	err = db.Transaction(func(tx *gorm.DB) error {
		var err error
		page, err = a.repoPage(c, tx, filters, sort, limit)
		return err
	}, &sql.TxOptions{ReadOnly: true})
	if errors.Is(err, errInvalidCursor) {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_cursor"})
		return
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (a *collectionAPI) repoPage(c *gin.Context, tx *gorm.DB, filters repoFilters, sort string, limit int) (gin.H, error) {
	columns := map[string]string{
		"discovered": "repositories.discovered_at", "stars": "repositories.stars", "followed": "repositories.followed_at",
		"hotness":   "m.hotness_score",
		"growth24h": `CASE WHEN json_extract(m.window_details_json,'$."24h".quality')='exact' THEN m.delta_24h END`,
		"growth7d":  `CASE WHEN json_extract(m.window_details_json,'$."7d".quality')='exact' THEN m.delta_7d END`,
	}
	column := columns[sort]
	db := publicRepos(c, tx.Model(&storage.Repository{}))
	if metricSort(sort) {
		db = db.Joins("LEFT JOIN latest_metrics m ON m.repo_id=repositories.repo_id AND repositories.state='active'")
	}
	if filters.State != "" {
		db = db.Where("state = ?", filters.State)
	}
	if filters.Followed {
		db = db.Where("starred_by_user = 1")
	}
	if filters.Language != "" {
		db = db.Where("primary_language = ?", filters.Language)
	}
	if filters.Grade != "" {
		db = db.Where("grade=?", filters.Grade)
	}
	if filters.Tag != "" {
		db = db.Where(`EXISTS (SELECT 1 FROM ai_summaries s JOIN json_each(s.value_tags_json) tag
WHERE s.repo_id=repositories.repo_id AND tag.value=?)`, filters.Tag)
	}
	if filters.Q != "" {
		// instr 按字面搜索，用户输入中的 % 和 _ 不扩展为通配符。
		db = db.Where("instr(lower(full_name), lower(?)) > 0 OR instr(lower(coalesce(description,'')), lower(?)) > 0", filters.Q, filters.Q)
	}
	db = a.filterCategories(c, db, filters.Category)
	if raw, exists := c.GetQuery("cursor"); exists {
		cursor, err := decodeCursor(raw, sort, filters)
		if err != nil {
			return nil, errInvalidCursor
		}
		if cursor.Last.Value == nil {
			db = db.Where(column+" IS NULL").Where("repositories.repo_id > ?", cursor.Last.RepoID)
		} else {
			value, err := cursor.sortValue()
			if err != nil {
				return nil, errInvalidCursor
			}
			db = db.Where("("+column+" < ? OR ("+column+" = ? AND repositories.repo_id > ?) OR "+column+" IS NULL)",
				value, value, cursor.Last.RepoID)
		}
	}
	type row struct {
		storage.Repository
		IntegerKey *int64
		RealKey    *float64
	}
	keyAlias := "integer_key"
	if sort == "hotness" {
		keyAlias = "real_key"
	}
	var rows []row
	if err := db.Select("repositories.*, " + column + " AS " + keyAlias).
		Order(column + " DESC").Order("repositories.repo_id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		cursor := repoCursor{Version: 1, Sort: sort, Filters: filters}
		cursor.Last.RepoID = last.RepoID
		if last.IntegerKey != nil {
			value := json.Number(strconv.FormatInt(*last.IntegerKey, 10))
			cursor.Last.Value = value
		} else if last.RealKey != nil {
			value := json.Number(strconv.FormatFloat(*last.RealKey, 'g', -1, 64))
			cursor.Last.Value = value
		}
		data, err := json.Marshal(cursor)
		if err != nil {
			return nil, err
		}
		value := base64.RawURLEncoding.EncodeToString(data)
		next = &value
	}
	repos := make([]storage.Repository, 0, len(rows))
	for _, row := range rows {
		repos = append(repos, row.Repository)
	}
	views, err := a.metricViews(tx, repos)
	if err != nil {
		return nil, err
	}
	entries, err := a.latestEntries(tx)
	if err != nil {
		return nil, err
	}
	summaries, err := summaryViews(tx, repos, !c.GetBool(publicReadKey))
	if err != nil {
		return nil, err
	}
	items := make([]gin.H, 0, len(repos))
	for _, repo := range repos {
		items = append(items, a.card(c, repo, entries, views[repo.RepoID], summaries[repo.RepoID]))
	}
	now := iso(time.Now().UnixMilli())
	updatedAt := &now
	if metricSort(sort) {
		var marker storage.MaintenanceState
		if err := tx.Find(&marker, "key=?", metrics.ComputeKey).Error; err != nil {
			return nil, err
		}
		updatedAt = nil
		if marker.Key != "" {
			updatedAt = optionalISO(&marker.UpdatedAt)
		}
	}
	return gin.H{"items": items, "nextCursor": next, "updatedAt": updatedAt}, nil
}

// sortValue 保留整数键的精度，同时按原浮点值比较热度游标。
func (c repoCursor) sortValue() (any, error) {
	number, ok := c.Last.Value.(json.Number)
	if !ok {
		return nil, errors.New("排序游标必须为数值")
	}
	if c.Sort != "hotness" {
		return number.Int64()
	}
	value, err := number.Float64()
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return nil, errors.New("热度游标无效")
	}
	return value, nil
}

func (a *collectionAPI) filters(c *gin.Context) (repoFilters, string, error) {
	f := repoFilters{Q: strings.TrimSpace(c.Query("q")), Language: strings.TrimSpace(c.Query("language")), Category: []string{}}
	f.Grade, f.Tag = c.Query("grade"), strings.TrimSpace(c.Query("tag"))
	if f.Grade != "" && !slices.Contains([]string{"S", "A", "B", "C"}, f.Grade) {
		return f, "", errors.New("无效采样等级")
	}
	followed := c.DefaultQuery("followed", "false")
	if followed != "true" && followed != "false" {
		return f, "", errors.New("followed 必须为布尔值")
	}
	f.Followed = followed == "true"
	f.State = c.Query("state")
	if f.State == "" && !f.Followed {
		f.State = "active"
	}
	if f.State != "" && !slices.Contains([]string{"candidate", "active", "paused", "unavailable", "address_pending"}, f.State) {
		return f, "", errors.New("无效仓库状态")
	}
	for _, slug := range c.QueryArray("category") {
		valid := false
		for _, category := range categoriesFor(c) {
			valid = valid || category.Slug == slug
		}
		if !valid {
			return f, "", errors.New("未知分类")
		}
		f.Category = append(f.Category, slug)
	}
	slices.Sort(f.Category)
	f.Category = slices.Compact(f.Category)
	sort := c.DefaultQuery("sort", "discovered")
	if !slices.Contains([]string{"discovered", "stars", "followed", "hotness", "growth24h", "growth7d"}, sort) || (sort == "followed" && !f.Followed) {
		return f, "", errors.New("无效排序")
	}
	return f, sort, nil
}

func metricSort(sort string) bool {
	return sort == "hotness" || sort == "growth24h" || sort == "growth7d"
}

// filterCategories 使用本次请求的数据库映射，多选分类取并集。
func (a *collectionAPI) filterCategories(c *gin.Context, db *gorm.DB, selected []string) *gorm.DB {
	if len(selected) == 0 {
		return db
	}
	var clauses []string
	var args []any
	for _, slug := range selected {
		var topics []string
		for _, category := range categoriesFor(c) {
			if category.Slug == slug || slug == "other" {
				topics = append(topics, category.AnyTopics...)
			}
		}
		if len(topics) == 0 {
			if slug == "other" {
				clauses = append(clauses, "1=1")
			}
			continue
		}
		expression := "EXISTS (SELECT 1 FROM json_each(repositories.topics_json) topic WHERE lower(topic.value) IN ?)"
		if slug == "other" {
			expression = "NOT " + expression
		}
		clauses, args = append(clauses, expression), append(args, topics)
	}
	return db.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

func decodeCursor(raw, sort string, filters repoFilters) (repoCursor, error) {
	var cursor repoCursor
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return cursor, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || cursor.Version != 1 || cursor.Last.RepoID <= 0 ||
		cursor.Sort != sort || !reflect.DeepEqual(cursor.Filters, filters) {
		return cursor, errors.New("游标与当前查询不匹配")
	}
	return cursor, nil
}
