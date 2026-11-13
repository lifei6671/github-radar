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

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/discoverystore"
)

var errInvalidCursor = errors.New("无效分页游标")

// repoFilters 与游标内的过滤快照共用同一种表示。
type repoFilters struct {
	Q              string   `json:"q"`
	Language       string   `json:"language"`
	State          string   `json:"state"`
	Followed       bool     `json:"followed"`
	Category       []string `json:"category"`
	Grade          string   `json:"grade,omitempty"`
	Tag            string   `json:"tag,omitempty"`
	Blocked        string   `json:"blocked,omitempty"`
	IncludeBlocked bool     `json:"includeBlocked,omitempty"`
	Theme          string   `json:"theme,omitempty"`
}

type repoCursor struct {
	Version int         `json:"v"`
	Sort    string      `json:"sort"`
	Filters repoFilters `json:"filters"`
	Vector  string      `json:"vector,omitempty"`
	Last    struct {
		Value  any   `json:"value"`
		RepoID int64 `json:"repoId"`
		Rank   *int  `json:"rank,omitempty"`
	} `json:"last"`
}

func (a *collectionAPI) repos(c *gin.Context) {
	limit, ok := pageLimit(c)
	if !ok {
		return
	}
	filters, sort, err := a.filters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidFilter})
		return
	}
	if c.GetBool(publicReadKey) && (filters.Followed || filters.Blocked != "") {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidFilter})
		return
	}
	vector, err := a.prepareRepoSearch(c, filters, sort)
	var page gin.H
	db := a.db.WithContext(c.Request.Context())
	if err == nil {
		// 排序键、全局名次及计算时间来自同一读快照，外部模型请求已在事务外结束。
		err = db.Transaction(func(tx *gorm.DB) error {
			var err error
			if sort == consts.SortRelevance {
				page, err = a.relevanceRepoPage(c, tx, filters, vector, limit)
			} else {
				page, err = a.repoPage(c, tx, filters, sort, limit)
			}
			return err
		}, &sql.TxOptions{ReadOnly: true})
	}
	if errors.Is(err, errInvalidCursor) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidCursor})
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
		consts.SortDiscovered: storage.RepositoryTable + "." + storage.RepositoryColumns.DiscoveredAt,
		consts.SortStars:      storage.RepositoryTable + "." + storage.RepositoryColumns.Stars,
		consts.SortFollowed:   storage.RepositoryTable + "." + storage.RepositoryColumns.FollowedAt,
		consts.SortHotness:    "m." + storage.LatestMetricColumns.HotnessScore,
		consts.SortGrowth24h:  exactGrowthSQL(consts.Window24h, storage.LatestMetricColumns.Delta24h),
		consts.SortGrowth7d:   exactGrowthSQL(consts.Window7d, storage.LatestMetricColumns.Delta7d),
	}
	column := columns[sort]
	db := publicRepos(c, tx.Model(&storage.Repository{}))
	if metricSort(sort) {
		db = db.Joins("LEFT JOIN " + storage.LatestMetricTable + " m ON m." + storage.LatestMetricColumns.RepoID + "=" + storage.RepositoryTable + "." + storage.RepositoryColumns.RepoID + " AND " + storage.RepositoryTable + "." + storage.RepositoryColumns.State + "='" + consts.StatusActive + "'")
	}
	db, err := a.filterRepoList(c, tx, db, filters)
	if err != nil {
		return nil, err
	}
	if filters.Q != "" {
		db = db.Where(literalRepoSearch, filters.Q, filters.Q)
	}
	if raw, exists := c.GetQuery("cursor"); exists {
		cursor, err := decodeCursor(raw, sort, filters)
		if err != nil {
			return nil, errInvalidCursor
		}
		if cursor.Last.Value == nil {
			db = db.Where(column+" IS NULL").Where(storage.RepositoryTable+"."+storage.RepositoryColumns.RepoID+" > ?", cursor.Last.RepoID)
		} else {
			value, err := cursor.sortValue()
			if err != nil {
				return nil, errInvalidCursor
			}
			db = db.Where("("+column+" < ? OR ("+column+" = ? AND "+storage.RepositoryTable+"."+storage.RepositoryColumns.RepoID+" > ?) OR "+column+" IS NULL)",
				value, value, cursor.Last.RepoID)
		}
	}
	type row struct {
		storage.Repository
		IntegerKey *int64
		RealKey    *float64
	}
	keyAlias := "integer_key"
	if sort == consts.SortHotness {
		keyAlias = "real_key"
	}
	var rows []row
	if err := db.Select(storage.RepositoryTable + ".*, " + column + " AS " + keyAlias).
		Order(column + " DESC").Order(storage.RepositoryTable + "." + storage.RepositoryColumns.RepoID).Limit(limit + 1).Find(&rows).Error; err != nil {
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
	if err != nil {
		return nil, err
	}
	now := iso(time.Now().UnixMilli())
	updatedAt := &now
	if metricSort(sort) {
		var marker storage.MaintenanceState
		if err := tx.Find(&marker, storage.MaintenanceStateColumns.Key+"=?", metrics.ComputeKey).Error; err != nil {
			return nil, err
		}
		updatedAt = nil
		if marker.Key != "" {
			updatedAt = optionalISO(&marker.UpdatedAt)
		}
	}
	return gin.H{"items": items, "nextCursor": next, "updatedAt": updatedAt}, nil
}

func (a *collectionAPI) filterRepoList(c *gin.Context, tx, db *gorm.DB, filters repoFilters) (*gorm.DB, error) {
	if filters.IncludeBlocked {
		db = db.Where("("+storage.RepositoryColumns.State+" = ? OR "+storage.RepositoryColumns.Blocked+"=1)", filters.State)
	} else if filters.State != "" {
		db = db.Where(storage.RepositoryColumns.State+" = ?", filters.State)
	}
	if filters.Blocked != "" {
		db = db.Where(storage.RepositoryColumns.Blocked+"=?", filters.Blocked == "true")
	}
	if filters.Followed {
		db = db.Where(storage.RepositoryColumns.StarredByUser + " = 1")
	}
	if filters.Language != "" {
		db = db.Where(storage.RepositoryColumns.PrimaryLanguage+" = ?", filters.Language)
	}
	if filters.Grade != "" {
		db = db.Where(storage.RepositoryColumns.Grade+"=?", filters.Grade)
	}
	if filters.Tag != "" {
		db = db.Where(`EXISTS (SELECT 1 FROM `+storage.AISummaryTable+` s JOIN json_each(s.`+storage.AISummaryColumns.ValueTagsJSON+`) tag
WHERE s.`+storage.AISummaryColumns.RepoID+`=`+storage.RepositoryTable+`.`+storage.RepositoryColumns.RepoID+` AND tag.value=?)`, filters.Tag)
	}
	return filterProjectTheme(tx, a.filterCategories(c, db, filters.Category), filters.Theme)
}

// exactGrowthSQL 只将精确窗口的增长值用于排序和游标。
func exactGrowthSQL(window, deltaColumn string) string {
	return "CASE WHEN " + storage.MetricWindowSQL(window, "quality") + "='" + consts.QualityExact + "' THEN m." + deltaColumn + " END"
}

// repoCards 复用仓库列表的公开卡片字段、指标和摘要。
func (a *collectionAPI) repoCards(c *gin.Context, tx *gorm.DB, repos []storage.Repository) ([]gin.H, error) {
	items := make([]gin.H, 0, len(repos))
	if len(repos) == 0 {
		return items, nil
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
	for _, repo := range repos {
		items = append(items, a.card(c, repo, entries, views[repo.RepoID], summaries[repo.RepoID]))
	}
	return items, nil
}

func (a *collectionAPI) repoListCards(c *gin.Context, tx *gorm.DB, repos []storage.Repository) ([]gin.H, error) {
	items, err := a.repoCards(c, tx, repos)
	if err != nil || len(items) == 0 || !c.GetBool(publicReadKey) {
		return items, err
	}
	ids := make([]int64, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.RepoID)
	}
	matches, _, err := discoverystore.Matches(tx, time.Now())
	if err != nil {
		return nil, err
	}
	matches = matches.Where(storage.RepositoryTable+"."+storage.RepositoryColumns.RepoID+" IN ?", ids)
	var selectedIDs []int64
	if err := tx.Table("(?) AS matches", matches).
		Where(discoverystore.TimeSQL+" > 0").Pluck(storage.RepositoryColumns.RepoID, &selectedIDs).Error; err != nil {
		return nil, err
	}
	selected := make(map[int64]bool, len(selectedIDs))
	for _, id := range selectedIDs {
		selected[id] = true
	}
	for i, repo := range repos {
		isSelected := selected[repo.RepoID]
		items[i]["selected"] = isSelected
		if !isSelected {
			items[i]["recommendationReason"] = nil
		}
	}
	return items, nil
}

// sortValue 保留整数键的精度，同时按原浮点值比较热度和相关度游标。
func (c repoCursor) sortValue() (any, error) {
	number, ok := c.Last.Value.(json.Number)
	if !ok {
		return nil, errors.New("排序游标必须为数值")
	}
	if c.Sort != consts.SortHotness && c.Vector == "" {
		return number.Int64()
	}
	value, err := number.Float64()
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return nil, errors.New("浮点排序游标无效")
	}
	return value, nil
}

func (a *collectionAPI) filters(c *gin.Context) (repoFilters, string, error) {
	f := repoFilters{Q: strings.TrimSpace(c.Query("q")), Language: strings.TrimSpace(c.Query("language")), Category: []string{}}
	f.Theme = c.Query("theme")
	if f.Theme != "" && !c.GetBool(publicReadKey) {
		return f, "", errors.New("专题筛选用于公开项目列表")
	}
	f.Grade, f.Tag = c.Query("grade"), strings.TrimSpace(c.Query("tag"))
	if f.Grade != "" && !slices.Contains([]string{consts.GradeS, consts.GradeA, consts.GradeB, consts.GradeC}, f.Grade) {
		return f, "", errors.New("无效采样等级")
	}
	followed := c.DefaultQuery(consts.SortFollowed, "false")
	if followed != "true" && followed != "false" {
		return f, "", errors.New("followed 必须为布尔值")
	}
	f.Followed = followed == "true"
	f.Blocked = c.Query("blocked")
	if f.Blocked != "" && f.Blocked != "true" && f.Blocked != "false" {
		return f, "", errors.New("无效屏蔽筛选")
	}
	f.State = c.Query("state")
	if f.State == "" && !f.Followed && f.Blocked != "true" {
		f.State = consts.StatusActive
		// 默认后台列表仍能找到已屏蔽仓库以便解除。
		f.IncludeBlocked = !c.GetBool(publicReadKey) && f.Blocked == ""
	}
	if f.State != "" && !slices.Contains([]string{consts.StatusCandidate, consts.StatusActive, consts.StatusPaused, consts.StatusUnavailable, consts.StatusAddressPending}, f.State) {
		return f, "", errors.New("无效仓库状态")
	}
	var err error
	f.Category, err = categoryFilters(c)
	if err != nil {
		return f, "", err
	}
	defaultSort := consts.SortDiscovered
	if f.Q != "" {
		defaultSort = consts.SortRelevance
	}
	sort := c.DefaultQuery("sort", defaultSort)
	if !slices.Contains([]string{consts.SortDiscovered, consts.SortRelevance, consts.SortStars, consts.SortFollowed, consts.SortHotness, consts.SortGrowth24h, consts.SortGrowth7d}, sort) ||
		(sort == consts.SortFollowed && !f.Followed) || (sort == consts.SortRelevance && f.Q == "") {
		return f, "", errors.New("无效排序")
	}
	return f, sort, nil
}

func categoryFilters(c *gin.Context) ([]string, error) {
	categories := []string{}
	for _, slug := range c.QueryArray("category") {
		valid := false
		for _, category := range categoriesFor(c) {
			valid = valid || category.Slug == slug
		}
		if !valid {
			return nil, errors.New("未知分类")
		}
		categories = append(categories, slug)
	}
	slices.Sort(categories)
	return slices.Compact(categories), nil
}

func metricSort(sort string) bool {
	return sort == consts.SortHotness || sort == consts.SortGrowth24h || sort == consts.SortGrowth7d
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
			if category.Slug == slug || slug == consts.CategoryOther {
				topics = append(topics, category.AnyTopics...)
			}
		}
		if len(topics) == 0 {
			if slug == consts.CategoryOther {
				clauses = append(clauses, "1=1")
			}
			continue
		}
		expression := "EXISTS (SELECT 1 FROM json_each(" + effectiveTopicsSQL + ") topic WHERE lower(topic.value) IN ?)"
		if slug == consts.CategoryOther {
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
	if sort == consts.SortRelevance {
		if cursor.Last.Rank == nil || *cursor.Last.Rank < 0 || *cursor.Last.Rank > 3 ||
			(cursor.Vector == "" && *cursor.Last.Rank == 3) {
			return cursor, errInvalidCursor
		}
		if _, err := cursor.sortValue(); err != nil {
			return cursor, errInvalidCursor
		}
	} else if cursor.Vector != "" || cursor.Last.Rank != nil {
		return cursor, errInvalidCursor
	}
	return cursor, nil
}

func encodeRepoCursor(cursor repoCursor) (*string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return nil, err
	}
	value := base64.RawURLEncoding.EncodeToString(data)
	return &value, nil
}
