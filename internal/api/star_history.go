package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/collectionstore"
)

// starHistory 公开端只读独立的历史日计数；失败原因和任务 ID 只在后台返回。
func (a *collectionAPI) starHistory(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	days, err := strconv.Atoi(c.DefaultQuery("days", "30"))
	if err != nil || days < 1 || days > 630 {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidRange})
		return
	}
	public := c.GetBool(publicReadKey)
	var result gin.H
	err = a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var repo storage.Repository
		if err := tx.Where(readableRepoCondition(c)).Take(&repo, "repo_id = ?", id).Error; err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		var rows []storage.RepoStarHistoryWeek
		if err := tx.Where("repo_id = ? AND week_timestamp >= ?", id, now-int64(days+7)*metrics.Day).
			Order("week_timestamp DESC").Find(&rows).Error; err != nil {
			return err
		}
		weeks := make([]metrics.StarHistoryWeekData, 0, len(rows))
		for _, row := range rows {
			week, err := row.Decode()
			if err != nil {
				return err
			}
			weeks = append(weeks, week)
		}
		var status storage.RepoStarHistoryStatus
		if err := tx.Find(&status, "repo_id = ?", id).Error; err != nil {
			return err
		}
		var local storage.LatestMetric
		if err := tx.Select("breakout_json").Find(&local, "repo_id = ?", id).Error; err != nil {
			return err
		}
		var breakout *metrics.Breakout
		if local.BreakoutJSON != "" {
			if err := json.Unmarshal([]byte(local.BreakoutJSON), &breakout); err != nil {
				return err
			}
		} else if status.BreakoutAt != nil && *status.BreakoutAt >= now-metrics.StarHistorySignalAge &&
			(status.ConfirmedAt == nil || *status.ConfirmedAt != *status.BreakoutAt) {
			// 两种日界不同，只有本地自身命中爆发才确认。未确认的官方信号保留到时效结束。
			if err := json.Unmarshal([]byte(status.BreakoutJSON), &breakout); err != nil {
				return err
			}
		}
		points := metrics.StarHistoryDays(weeks, now, days, repo.GitHubCreatedAt)
		result = gin.H{"days": points, "breakout": breakout}
		if status.FetchedAt > 0 && len(points) > 0 {
			result["fetchedAt"] = iso(status.FetchedAt)
		}
		if !public {
			result["errorCode"] = status.LastErrorCode
			if repo.BreakoutGradeJSON != "" {
				result["gradeEvidence"] = json.RawMessage(repo.BreakoutGradeJSON)
			}
			var job storage.Job
			if err := tx.Select("id", "payload_json").Where("repo_id = ? AND type = ? AND status IN ?", id, consts.JobStarHistory,
				[]string{consts.StatusPending, consts.StatusRunning, consts.StatusFailed}).Order("id DESC").Limit(1).Find(&job).Error; err != nil {
				return err
			}
			if job.ID > 0 && job.IsManual() {
				result["jobId"] = job.ID
			}
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// requestStarHistory 只在后台创建有界回补任务，公开详情访问不会请求 GitHub。
func (a *collectionAPI) requestStarHistory(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		RequestID string `json:"requestId"`
	}
	if c.ShouldBindJSON(&input) != nil || !submissionID.MatchString(input.RequestID) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidRequestID})
		return
	}
	cfg := runtimeFor(c).Config
	if cfg.GitHubToken() == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": consts.CodeGitHubNotConfigured})
		return
	}
	job, err := (collectionstore.Queue{DB: a.db, Cfg: cfg.Scheduler}).RequestStarHistory(c.Request.Context(), id, input.RequestID, time.Now())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"jobId": job.ID, "statusUrl": "/api/v1/jobs/" + strconv.FormatInt(job.ID, 10)})
}
