package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/summary"
)

func (a *collectionAPI) summaryQueue() summary.Queue {
	return summary.Queue{DB: a.db, AI: a.cfg.AI, LeaseSeconds: a.cfg.Scheduler.LeaseSeconds}
}

func (a *collectionAPI) summaryAvailable() bool {
	return a.cfg.AI.Enabled && a.cfg.AI.Summary.ManualDailyCalls > 0 && len(a.cfg.MissingAISettings()) == 0 &&
		strings.TrimSpace(os.Getenv(a.cfg.GitHub.TokenEnv)) != ""
}

func validRequestID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 128
}

func jobStatusURL(id int64) string { return "/api/v1/jobs/" + strconv.FormatInt(id, 10) }

func (a *collectionAPI) requestSummary(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		RequestID string `json:"requestId"`
		Force     bool   `json:"force"`
	}
	if c.ShouldBindJSON(&input) != nil || !validRequestID(input.RequestID) {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request_id"})
		return
	}
	decision, err := a.summaryQueue().Manual(c.Request.Context(), id, input.RequestID, input.Force, a.summaryAvailable(), time.Now())
	if err != nil {
		a.summaryError(c, err)
		return
	}
	switch {
	case decision.Conflict:
		c.JSON(http.StatusConflict, gin.H{"code": "summary_in_progress", "existingJobId": decision.Job.ID, "statusUrl": jobStatusURL(decision.Job.ID)})
	case decision.Cached:
		c.JSON(http.StatusOK, gin.H{"cached": true, "summaryStatus": "ready"})
	default:
		c.JSON(http.StatusAccepted, gin.H{"jobId": decision.Job.ID, "statusUrl": jobStatusURL(decision.Job.ID)})
	}
}

func (a *collectionAPI) retrySummary(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		RequestID            string `json:"requestId"`
		AcceptPossibleCharge bool   `json:"acceptPossibleCharge"`
	}
	if c.ShouldBindJSON(&input) != nil || !validRequestID(input.RequestID) {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_request_id"})
		return
	}
	if !input.AcceptPossibleCharge {
		c.JSON(http.StatusBadRequest, gin.H{"code": "possible_charge_not_accepted"})
		return
	}
	job, err := a.summaryQueue().RetryUnknown(c.Request.Context(), id, input.RequestID, a.summaryAvailable(), time.Now())
	if err != nil {
		a.summaryError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"jobId": job.ID, "statusUrl": jobStatusURL(job.ID)})
}

func (a *collectionAPI) summaryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, summary.ErrUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "summary_unavailable"})
	case errors.Is(err, summary.ErrRepoUnavailable):
		c.JSON(http.StatusConflict, gin.H{"code": "summary_repo_unavailable"})
	case errors.Is(err, summary.ErrNotUnknown):
		c.JSON(http.StatusConflict, gin.H{"code": "summary_not_unknown"})
	default:
		a.fail(c, err)
	}
}

type summaryView struct {
	row    storage.AISummary
	jobID  *int64
	status string
}

func (v summaryView) state() string {
	if v.status != "" {
		return v.status
	}
	if v.row.SummaryStatus != "" {
		return v.row.SummaryStatus
	}
	return "missing"
}

func (v summaryView) preview() *string {
	if v.row.SummaryZh == "" {
		return nil
	}
	value := v.row.SummaryZh
	if utf8.RuneCountInString(value) > 120 {
		value = string([]rune(value)[:120]) + "…"
	}
	return &value
}

func textPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (v summaryView) title() *string { return textPtr(summary.Title(v.row.SummaryZh)) }

// summaryViews 批量读取最近成功摘要及当前任务状态，避免列表逐仓库查询。
func summaryViews(db *gorm.DB, repos []storage.Repository, includeJobs bool) (map[int64]summaryView, error) {
	views := make(map[int64]summaryView, len(repos))
	if len(repos) == 0 {
		return views, nil
	}
	ids := make([]int64, 0, len(repos))
	for _, r := range repos {
		ids = append(ids, r.RepoID)
	}
	var rows []storage.AISummary
	if err := db.Where("repo_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		views[row.RepoID] = summaryView{row: row}
	}
	if !includeJobs {
		return views, nil
	}
	var jobs []struct {
		ID, RepoID int64
		Status     string
	}
	if err := db.Raw(`
SELECT id,repo_id,status FROM (
 SELECT id,repo_id,status,ROW_NUMBER() OVER(PARTITION BY repo_id ORDER BY
  CASE WHEN status IN ('pending','running','failed','unknown') THEN 0 ELSE 1 END,id DESC) AS latest
 FROM jobs WHERE type=? AND repo_id IN ?
) WHERE latest=1`, summary.JobType, ids).Scan(&jobs).Error; err != nil {
		return nil, err
	}
	for _, job := range jobs {
		v := views[job.RepoID]
		switch job.Status {
		case "pending":
			v.status = "queued"
		case "running", "unknown":
			v.status = job.Status
		case "failed", "dead":
			v.status = "failed"
			if v.row.RepoID != 0 {
				v.status = "stale"
			}
		default:
			continue
		}
		v.jobID = &job.ID
		views[job.RepoID] = v
	}
	return views, nil
}

func (a *collectionAPI) aiLimits(db *gorm.DB, now time.Time) (gin.H, error) {
	type usage struct {
		Origin string
		Calls  int64
	}
	var rows []usage
	day := now.UTC().Truncate(24 * time.Hour)
	reset := day.Add(24 * time.Hour)
	if err := db.Model(&storage.AICall{}).Select("origin,COUNT(*) AS calls").
		Where("reserved_at>=? AND reserved_at<?", day.UnixMilli(), reset.UnixMilli()).Group("origin").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64)
	for _, row := range rows {
		counts[row.Origin] = row.Calls
	}
	return gin.H{
		"auto":    gin.H{"used": counts["auto"], "limit": a.cfg.AI.Summary.AutoDailyCalls},
		"manual":  gin.H{"used": counts["manual"], "limit": a.cfg.AI.Summary.ManualDailyCalls},
		"resetAt": iso(reset.UnixMilli()),
	}, nil
}
