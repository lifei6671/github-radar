package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
)

// repoFilters 与游标内的过滤快照共用同一种表示。
type repoFilters struct {
	Q        string   `json:"q"`
	Language string   `json:"language"`
	State    string   `json:"state"`
	Followed bool     `json:"followed"`
	Category []string `json:"category"`
}

type repoCursor struct {
	Version int         `json:"v"`
	Sort    string      `json:"sort"`
	Filters repoFilters `json:"filters"`
	Last    struct {
		Value  *int64 `json:"value"`
		RepoID int64  `json:"repoId"`
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
	columns := map[string]string{
		"discovered": "discovered_at", "stars": "stars", "followed": "followed_at",
		"hotness": "", "growth24h": "",
	}
	column := columns[sort]
	db := a.db.WithContext(c.Request.Context()).Model(&storage.Repository{})
	if filters.State != "" {
		db = db.Where("state = ?", filters.State)
	}
	if filters.Followed {
		db = db.Where("starred_by_user = 1")
	}
	if filters.Language != "" {
		db = db.Where("primary_language = ?", filters.Language)
	}
	if filters.Q != "" {
		// instr 按字面搜索，用户输入中的 % 和 _ 不扩展为通配符。
		db = db.Where("instr(lower(full_name), lower(?)) > 0 OR instr(lower(coalesce(description,'')), lower(?)) > 0", filters.Q, filters.Q)
	}
	db = a.filterCategories(c, db, filters.Category)
	if raw, exists := c.GetQuery("cursor"); exists {
		cursor, err := decodeCursor(raw, sort, filters)
		if err != nil || (column == "" && cursor.Last.Value != nil) {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_cursor"})
			return
		}
		if cursor.Last.Value == nil {
			if column != "" {
				db = db.Where(column + " IS NULL")
			}
			db = db.Where("repo_id > ?", cursor.Last.RepoID)
		} else {
			db = db.Where("("+column+" < ? OR ("+column+" = ? AND repo_id > ?) OR "+column+" IS NULL)",
				*cursor.Last.Value, *cursor.Last.Value, cursor.Last.RepoID)
		}
	}
	if column != "" {
		db = db.Order(column + " DESC")
	}
	var repos []storage.Repository
	if err := db.Order("repo_id").Limit(limit + 1).Find(&repos).Error; err != nil {
		a.fail(c, err)
		return
	}
	var next *string
	if len(repos) > limit {
		repos = repos[:limit]
		last := repos[len(repos)-1]
		cursor := repoCursor{Version: 1, Sort: sort, Filters: filters}
		cursor.Last.RepoID = last.RepoID
		switch sort {
		case "discovered":
			cursor.Last.Value = &last.DiscoveredAt
		case "stars":
			value := int64(last.Stars)
			cursor.Last.Value = &value
		case "followed":
			cursor.Last.Value = last.FollowedAt
		}
		data, err := json.Marshal(cursor)
		if err != nil {
			a.fail(c, err)
			return
		}
		value := base64.RawURLEncoding.EncodeToString(data)
		next = &value
	}
	entries, err := a.latestEntries(c)
	if err != nil {
		a.fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(repos))
	for _, repo := range repos {
		items = append(items, a.card(c, repo, entries))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "nextCursor": next, "updatedAt": iso(time.Now().UnixMilli())})
}

func (a *collectionAPI) filters(c *gin.Context) (repoFilters, string, error) {
	f := repoFilters{Q: strings.TrimSpace(c.Query("q")), Language: strings.TrimSpace(c.Query("language")), Category: []string{}}
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
	if !slices.Contains([]string{"discovered", "stars", "followed", "hotness", "growth24h"}, sort) || (sort == "followed" && !f.Followed) {
		return f, "", errors.New("无效排序")
	}
	return f, sort, nil
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
