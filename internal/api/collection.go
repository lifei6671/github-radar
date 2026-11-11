package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/collector"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

type collectionAPI struct {
	db     *gorm.DB
	cfg    config.Config
	logger *slog.Logger
}

func (a *collectionAPI) register(v1 *gin.RouterGroup) {
	v1.GET("/repos", a.loadCategories, a.repos)
	v1.GET("/repos/:id", a.loadCategories, a.repo)
	v1.GET("/repos/:id/trend", a.trend)
	v1.POST("/repos/:id/refresh", a.refresh)
	v1.POST("/repos/:id/summary", a.requestSummary)
	v1.POST("/repos/:id/follow", a.follow)
	v1.POST("/repos/:id/topic", a.addTopic)
	v1.GET("/topics", a.topics)
	v1.PATCH("/topics/:id", a.updateTopic)
	v1.GET("/categories", a.loadCategories, a.categoryList)
	v1.GET("/trending", a.loadCategories, a.trending)
	v1.GET("/jobs/:id", a.job)
	v1.POST("/jobs/:id/retry", a.retrySummary)
	v1.GET("/system/jobs", a.jobs)
	v1.GET("/system/limits", a.limits)
	v1.GET("/system/storage", a.storageStatus)
	v1.POST("/system/maintenance/run", a.requestMaintenance)
}

const categoriesKey = "repositoryCategories"

// loadCategories 在同一个 HTTP 请求内复用已保存的分类映射。
func (a *collectionAPI) loadCategories(c *gin.Context) {
	items, err := (settings.Store{DB: a.db}).Categories(c.Request.Context())
	if err != nil {
		a.fail(c, err)
		c.Abort()
		return
	}
	c.Set(categoriesKey, append(items, config.Category{Slug: "other", Name: "其他"}))
	c.Next()
}

func categoriesFor(c *gin.Context) []config.Category {
	return c.MustGet(categoriesKey).([]config.Category)
}

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

func optionalISO(ms *int64) *string {
	if ms == nil {
		return nil
	}
	value := iso(*ms)
	return &value
}

// card 组合仓库元数据及本轮成功发布的增长和热度。
func (a *collectionAPI) card(c *gin.Context, repo storage.Repository, entries map[int64][]gin.H, view metricView, summary summaryView) gin.H {
	var topics []string
	_ = json.Unmarshal([]byte(repo.TopicsJSON), &topics)
	trending := entries[repo.RepoID]
	if trending == nil {
		trending = []gin.H{}
	}
	var metricsUpdatedAt *string
	if view.row.RepoID != 0 {
		metricsUpdatedAt = optionalISO(&view.row.CalculatedAt)
	}
	quality := view.row.DataQuality
	if quality == "" {
		quality = "insufficient"
	}
	return gin.H{
		"id": repo.RepoID, "fullName": repo.FullName, "htmlUrl": repo.HTMLURL,
		"description": repo.Description, "titleZh": summary.title(), "language": repo.PrimaryLanguage,
		"categories": config.Classify(categoriesFor(c), topics), "state": repo.State,
		"stars": repo.Stars, "forks": repo.Forks, "grade": repo.Grade,
		"growth24h": view.window("24h"), "growth7d": view.window("7d"),
		"hotnessScore": view.row.HotnessScore, "hotnessRank": view.rank, "trendingEntries": trending,
		"dataQuality": quality, "metricsUpdatedAt": metricsUpdatedAt, "candidateBurst": view.row.CandidateBurst,
		"sparkline": view.sparkline, "summaryPreview": summary.preview(), "summaryStatus": summary.state(),
		"summarySourceQuality": textPtr(summary.row.SourceQuality), "summaryJobId": summary.jobID,
		"followed": repo.StarredByUser, "followedAt": optionalISO(repo.FollowedAt),
		"lastSuccessAt": optionalISO(repo.LastSuccessAt),
		"discoveredAt":  iso(repo.DiscoveredAt), "relativeGrowth": view.row.RelativeGrowth,
	}
}

func (a *collectionAPI) fail(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"code": "not_found"})
		return
	}
	a.logger.Error("采集接口失败", "route", c.FullPath(), "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"code": "internal_error"})
}

func parseID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_id"})
		return 0, false
	}
	return id, true
}

func (a *collectionAPI) repo(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var repo storage.Repository
	db := a.db.WithContext(c.Request.Context())
	if err := db.Take(&repo, "repo_id = ?", id).Error; err != nil {
		a.fail(c, err)
		return
	}
	entries, err := a.latestEntries(db)
	if err != nil {
		a.fail(c, err)
		return
	}
	views, err := a.metricViews(db, []storage.Repository{repo})
	if err != nil {
		a.fail(c, err)
		return
	}
	view := views[id]
	summaries, err := summaryViews(db, []storage.Repository{repo})
	if err != nil {
		a.fail(c, err)
		return
	}
	summary := summaries[id]
	body := a.card(c, repo, entries, view, summary)
	body["license"] = nil
	if repo.LicenseSPDXID != nil || repo.LicenseName != nil {
		body["license"] = gin.H{"spdxId": repo.LicenseSPDXID, "name": repo.LicenseName}
	}
	body["summaryZh"], body["sourceReadmeUrl"], body["summaryModel"] = textPtr(summary.row.SummaryZh), summary.row.SourceReadmeURL, textPtr(summary.row.ModelName)
	body["summaryGeneratedAt"] = nil
	if summary.row.RepoID != 0 {
		body["summaryGeneratedAt"] = iso(summary.row.GeneratedAt)
	}
	body["summarySourceLanguage"] = textPtr(summary.row.SourceLanguage)
	for name, raw := range map[string]string{"features": summary.row.FeaturesJSON, "audience": summary.row.AudienceJSON, "valueTags": summary.row.ValueTagsJSON} {
		tags := []string{}
		_ = json.Unmarshal([]byte(raw), &tags)
		body[name] = tags
	}
	body["growth1h"], body["growth30d"] = view.window("1h"), view.window("30d")
	body["acceleration"], body["relativeGrowth"] = view.row.Acceleration, view.row.RelativeGrowth
	body["persistenceScore"], body["dailyConcentration"] = view.row.PersistenceScore, view.row.DailyConcentration
	body["trendingFrequency"], body["trendingStreak"], body["trendingRank"] = view.row.TrendingFrequency, view.row.TrendingStreak, view.row.TrendingRank
	c.JSON(http.StatusOK, body)
}

func (a *collectionAPI) refresh(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		RequestID string `json:"requestId"`
	}
	if c.ShouldBindJSON(&input) != nil || strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request_id"})
		return
	}
	if strings.TrimSpace(os.Getenv(a.cfg.GitHub.TokenEnv)) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "github_not_configured"})
		return
	}
	job, err := (collector.Queue{DB: a.db, Cfg: a.cfg.Scheduler}).Refresh(c.Request.Context(), id, input.RequestID, time.Now())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"jobId": job.ID, "statusUrl": "/api/v1/jobs/" + strconv.FormatInt(job.ID, 10)})
}

func (a *collectionAPI) follow(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		Follow *bool `json:"follow"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Follow == nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_follow"})
		return
	}
	err := (collector.Queue{DB: a.db, Cfg: a.cfg.Scheduler}).Follow(c.Request.Context(), id, *input.Follow, time.Now())
	if errors.Is(err, collector.ErrCapacity) {
		c.JSON(http.StatusConflict, gin.H{"code": "monitoring_capacity_full"})
		return
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"followed": *input.Follow})
}

func jobView(job storage.Job) gin.H {
	return gin.H{
		"id": job.ID, "type": job.Type, "status": job.Status, "attempts": job.Attempts,
		"maxAttempts": job.MaxAttempts, "errorCode": job.LastErrorCode,
		"createdAt": iso(job.CreatedAt), "updatedAt": iso(job.UpdatedAt), "repoId": job.RepoID,
	}
}

func (a *collectionAPI) job(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var job storage.Job
	if err := a.db.WithContext(c.Request.Context()).Take(&job, "id = ?", id).Error; err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, jobView(job))
}

func pageLimit(c *gin.Context) (int, bool) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "30"))
	if err != nil || limit < 1 || limit > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_limit"})
		return 0, false
	}
	return limit, true
}

func (a *collectionAPI) jobs(c *gin.Context) {
	limit, ok := pageLimit(c)
	if !ok {
		return
	}
	var jobs []storage.Job
	if err := a.db.WithContext(c.Request.Context()).Order("id DESC").Limit(limit).Find(&jobs).Error; err != nil {
		a.fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, jobView(job))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (a *collectionAPI) limits(c *gin.Context) {
	var states []storage.RateLimit
	db := a.db.WithContext(c.Request.Context())
	if err := db.Order("resource").Find(&states).Error; err != nil {
		a.fail(c, err)
		return
	}
	var waiting int64
	if err := db.Model(&storage.Job{}).Where("status IN ('pending','failed','running')").Count(&waiting).Error; err != nil {
		a.fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(states))
	for _, state := range states {
		items = append(items, gin.H{
			"resource": state.Resource, "limit": state.LimitTotal, "remaining": state.Remaining,
			"resetAt": iso(state.ResetAt), "pausedUntil": optionalISO(state.PausedUntil),
			"localRequests": state.LocalRequests, "localWindowAt": iso(state.LocalWindow),
			"rateLimitedResponses": state.LimitedCount,
		})
	}
	ai, err := a.aiLimits(db, time.Now())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"githubConfigured": strings.TrimSpace(os.Getenv(a.cfg.GitHub.TokenEnv)) != "",
		"items": items, "waitingTaskCount": waiting, "ai": ai})
}

func (a *collectionAPI) categoryList(c *gin.Context) {
	categories := categoriesFor(c)
	var repos []storage.Repository
	if err := a.db.WithContext(c.Request.Context()).Select("topics_json").Where("state='active'").Find(&repos).Error; err != nil {
		a.fail(c, err)
		return
	}
	counts := make(map[string]int)
	for _, repo := range repos {
		var topics []string
		if err := json.Unmarshal([]byte(repo.TopicsJSON), &topics); err != nil {
			a.fail(c, err)
			return
		}
		for _, category := range config.Classify(categories, topics) {
			counts[category]++
		}
	}
	items := make([]gin.H, 0, len(categories))
	for _, category := range categories {
		items = append(items, gin.H{"slug": category.Slug, "name": category.Name, "count": counts[category.Slug]})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (a *collectionAPI) latestEntries(db *gorm.DB) (map[int64][]gin.H, error) {
	result := make(map[int64][]gin.H)
	var markers []storage.MaintenanceState
	if err := db.Where("key LIKE 'trending:%' AND EXISTS (SELECT 1 FROM discovery_rules r WHERE r.source_type='trending' AND 'trending:'||r.source_key=maintenance_state.key)").
		Find(&markers).Error; err != nil {
		return nil, err
	}
	for _, marker := range markers {
		parts := strings.Split(marker.Key, ":")
		period := parts[len(parts)-1]
		source := strings.TrimSuffix(strings.TrimPrefix(marker.Key, "trending:"), ":"+period)
		var batch collector.TrendingBatch
		if err := json.Unmarshal([]byte(marker.Value), &batch); err != nil {
			return nil, err
		}
		var entries []storage.TrendingObservation
		if err := db.Where("source_key=? AND window=? AND observed_slot=?", source, period, batch.ObservedSlot).
			Order("rank, repo_id").Find(&entries).Error; err != nil {
			return nil, err
		}
		for _, entry := range entries {
			result[entry.RepoID] = append(result[entry.RepoID], gin.H{
				"sourceKey": source, "period": period, "rank": entry.Rank, "observedAt": iso(batch.ObservedAt),
			})
		}
	}
	return result, nil
}

func (a *collectionAPI) trending(c *gin.Context) {
	period, source := c.DefaultQuery("period", "daily"), c.DefaultQuery("sourceKey", "all")
	db := a.db.WithContext(c.Request.Context())
	var count int64
	if err := db.Model(&storage.DiscoveryRule{}).Where("source_type='trending' AND source_key=?", source+":"+period).Count(&count).Error; err != nil {
		a.fail(c, err)
		return
	}
	if count == 0 || (period != "daily" && period != "weekly" && period != "monthly") {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_trending_source"})
		return
	}
	var marker storage.MaintenanceState
	err := db.Take(&marker, "key = ?", collector.BatchKey(source, period)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusOK, gin.H{"sourceKey": source, "period": period, "batch": nil, "items": []any{}, "updatedAt": nil})
		return
	}
	if err != nil {
		a.fail(c, err)
		return
	}
	var batch collector.TrendingBatch
	if err := json.Unmarshal([]byte(marker.Value), &batch); err != nil {
		a.fail(c, err)
		return
	}
	var members []struct {
		storage.Repository
		BatchRank int
	}
	if err := db.Model(&storage.Repository{}).Select("repositories.*, t.rank AS batch_rank").
		Joins("JOIN trending_observations t ON t.repo_id = repositories.repo_id").
		Where("t.source_key=? AND t.window=? AND t.observed_slot=?", source, period, batch.ObservedSlot).
		Order("t.rank, repositories.repo_id").Find(&members).Error; err != nil {
		a.fail(c, err)
		return
	}
	// 维护可能在两次读取之间整批清理过期榜单。
	if len(members) != batch.Size {
		c.JSON(http.StatusOK, gin.H{"sourceKey": source, "period": period, "batch": nil, "items": []any{}, "updatedAt": nil})
		return
	}
	entries, err := a.latestEntries(db)
	if err != nil {
		a.fail(c, err)
		return
	}
	repos := make([]storage.Repository, 0, len(members))
	// 目标来源的名次固定使用已选择批次，期间发布新榜单也不会混用槽。
	for _, member := range members {
		repos = append(repos, member.Repository)
		var retained []gin.H
		for _, entry := range entries[member.RepoID] {
			if entry["sourceKey"] != source || entry["period"] != period {
				retained = append(retained, entry)
			}
		}
		entries[member.RepoID] = append(retained, gin.H{
			"sourceKey": source, "period": period, "rank": member.BatchRank, "observedAt": iso(batch.ObservedAt),
		})
	}
	views, err := a.metricViews(db, repos)
	if err != nil {
		a.fail(c, err)
		return
	}
	summaries, err := summaryViews(db, repos)
	if err != nil {
		a.fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(repos))
	for _, repo := range repos {
		items = append(items, a.card(c, repo, entries, views[repo.RepoID], summaries[repo.RepoID]))
	}
	c.JSON(http.StatusOK, gin.H{
		"sourceKey": source, "period": period, "batch": gin.H{"observedSlot": batch.ObservedSlot, "observedAt": iso(batch.ObservedAt)},
		"items": items, "updatedAt": iso(batch.ObservedAt),
	})
}
