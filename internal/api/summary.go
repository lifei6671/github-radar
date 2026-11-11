package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
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
	a.requestAI(c, false)
}

func (a *collectionAPI) requestTranslation(c *gin.Context) {
	a.requestAI(c, true)
}

func (a *collectionAPI) requestAI(c *gin.Context, translate bool) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		RequestID string `json:"requestId"`
		Force     bool   `json:"force"`
	}
	if c.ShouldBindJSON(&input) != nil || !validRequestID(input.RequestID) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidRequestID})
		return
	}
	queue, now := a.summaryQueue(), time.Now()
	var decision summary.Decision
	var err error
	if translate {
		decision, err = queue.ManualTranslation(c.Request.Context(), id, input.RequestID, a.summaryAvailable(), now)
	} else {
		decision, err = queue.Manual(c.Request.Context(), id, input.RequestID, input.Force, a.summaryAvailable(), now)
	}
	if err != nil {
		a.summaryError(c, err)
		return
	}
	switch {
	case decision.Conflict:
		c.JSON(http.StatusConflict, gin.H{"code": consts.CodeSummaryInProgress, "existingJobId": decision.Job.ID, "statusUrl": jobStatusURL(decision.Job.ID)})
	case decision.Cached:
		c.JSON(http.StatusOK, gin.H{"cached": true, "summaryStatus": consts.StatusReady})
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
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidRequestID})
		return
	}
	if !input.AcceptPossibleCharge {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodePossibleChargeNotAccepted})
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
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": consts.CodeSummaryUnavailable})
	case errors.Is(err, summary.ErrRepoUnavailable):
		c.JSON(http.StatusConflict, gin.H{"code": consts.CodeSummaryRepoUnavailable})
	case errors.Is(err, summary.ErrNotUnknown):
		c.JSON(http.StatusConflict, gin.H{"code": consts.CodeSummaryNotUnknown})
	default:
		a.fail(c, err)
	}
}

type summaryView struct {
	row      storage.AISummary
	aiTopics []string
	jobID    *int64
	status   string
}

func (v summaryView) state() string {
	if v.status != "" {
		return v.status
	}
	if v.row.SummaryStatus != "" {
		return v.row.SummaryStatus
	}
	return consts.StatusMissing
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

func (v summaryView) reason() *string {
	if v.row.SummaryStatus != consts.StatusReady || v.row.RecommendationReason == nil {
		return nil
	}
	return textPtr(*v.row.RecommendationReason)
}

// summaryViews 批量读取摘要、AI Topics 和当前任务，避免列表逐仓库查询。
func summaryViews(db *gorm.DB, repos []storage.Repository, includeJobs bool) (map[int64]summaryView, error) {
	views := make(map[int64]summaryView, len(repos))
	if len(repos) == 0 {
		return views, nil
	}
	ids := make([]int64, 0, len(repos))
	var missingTopics []int64
	for _, r := range repos {
		ids = append(ids, r.RepoID)
		if len(r.GitHubTopics()) == 0 {
			missingTopics = append(missingTopics, r.RepoID)
		}
	}
	var rows []storage.AISummary
	if err := db.Where(storage.AISummaryColumns.RepoID+" IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		views[row.RepoID] = summaryView{row: row}
	}
	if len(missingTopics) > 0 {
		var rows []storage.RepoAITopics
		if err := db.Where(storage.RepoAITopicsColumns.RepoID+" IN ?", missingTopics).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			v := views[row.RepoID]
			if err := json.Unmarshal([]byte(row.TopicsJSON), &v.aiTopics); err != nil {
				return nil, err
			}
			views[row.RepoID] = v
		}
	}
	if !includeJobs {
		return views, nil
	}
	var jobs []struct {
		ID, RepoID int64
		Status     string
	}
	if err := db.Raw(fmt.Sprintf(`
SELECT %[1]s,%[2]s,%[3]s FROM (
 SELECT %[1]s,%[2]s,%[3]s,ROW_NUMBER() OVER(PARTITION BY %[2]s ORDER BY
  CASE WHEN %[3]s IN ('%[4]s','%[5]s','%[6]s','%[7]s') THEN 0 ELSE 1 END,%[1]s DESC) AS latest
 FROM %[8]s WHERE %[9]s=? AND %[2]s IN ?
) WHERE latest=1`,
		storage.JobColumns.ID, storage.JobColumns.RepoID, storage.JobColumns.Status, consts.StatusPending, consts.StatusRunning, consts.StatusFailed, consts.StatusUnknown, storage.JobTable, storage.JobColumns.Type), summary.JobType, ids).Scan(&jobs).Error; err != nil {
		return nil, err
	}
	for _, job := range jobs {
		v := views[job.RepoID]
		switch job.Status {
		case consts.StatusPending:
			v.status = consts.StatusQueued
		case consts.StatusRunning, consts.StatusUnknown:
			v.status = job.Status
		case consts.StatusFailed, consts.StatusDead:
			v.status = consts.StatusFailed
			if v.row.RepoID != 0 {
				v.status = consts.StatusStale
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
	if err := db.Model(&storage.AICall{}).Select(storage.AICallColumns.Origin+",COUNT(*) AS calls").
		Where(storage.AICallColumns.ReservedAt+">=? AND "+storage.AICallColumns.ReservedAt+"<?", day.UnixMilli(), reset.UnixMilli()).Group(storage.AICallColumns.Origin).Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64)
	for _, row := range rows {
		counts[row.Origin] = row.Calls
	}
	return gin.H{
		consts.OriginAuto:   gin.H{"used": counts[consts.OriginAuto], "limit": a.cfg.AI.Summary.AutoDailyCalls},
		consts.OriginManual: gin.H{"used": counts[consts.OriginManual], "limit": a.cfg.AI.Summary.ManualDailyCalls},
		"resetAt":           iso(reset.UnixMilli()),
	}, nil
}
