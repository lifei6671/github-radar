package api

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/collectionstore"
)

const maxSubmissionRows = 30

var submissionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var githubRepoName = regexp.MustCompile(`^[a-zA-Z0-9-]{1,100}/[a-zA-Z0-9_.-]{1,100}$`)

func submissionRepoName(input string) string {
	name := input
	if strings.Contains(input, "://") {
		u, err := url.Parse(input)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
			!strings.EqualFold(u.Host, "github.com") || u.User != nil || strings.Contains(u.EscapedPath(), "%") {
			return ""
		}
		name = strings.TrimPrefix(strings.TrimSuffix(u.Path, "/"), "/")
		if strings.HasSuffix(strings.ToLower(name), ".git") {
			name = name[:len(name)-4]
		}
	}
	_, repo, _ := strings.Cut(name, "/")
	if !githubRepoName.MatchString(name) || repo == "." || repo == ".." {
		return ""
	}
	return strings.ToLower(name)
}

func normalizeSubmissionSource(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), true
}

type submissionItem struct {
	Line      int    `json:"line"`
	Input     string `json:"input"`
	JobID     int64  `json:"jobId,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}

func (a *collectionAPI) submitRepositories(c *gin.Context) {
	var input struct {
		RequestID    string `json:"requestId"`
		Repositories string `json:"repositories"`
		SourceURL    string `json:"sourceUrl"`
		Note         string `json:"note"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 48<<10)
	if c.ShouldBindJSON(&input) != nil || !submissionID.MatchString(input.RequestID) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidRequestID})
		return
	}
	items := make([]submissionItem, 0)
	names := make([]string, 0)
	valid := make([]string, 0)
	for i, raw := range strings.Split(input.Repositories, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if len(items) == maxSubmissionRows {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_submission_count"})
			return
		}
		item := submissionItem{Line: i + 1, Input: line}
		name := submissionRepoName(line)
		if name == "" {
			item.ErrorCode = "invalid_submission_repo"
		} else {
			valid = append(valid, name)
		}
		items, names = append(items, item), append(names, name)
	}
	if len(items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_submission_count"})
		return
	}
	source, ok := normalizeSubmissionSource(strings.TrimSpace(input.SourceURL))
	if !ok || len(source) > 2048 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_submission_source"})
		return
	}
	note := strings.TrimSpace(input.Note)
	if len([]rune(note)) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_submission_note"})
		return
	}
	if len(valid) > 0 {
		cfg := runtimeFor(c).Config
		if cfg.GitHubToken() == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": consts.CodeGitHubNotConfigured})
			return
		}
		jobs, err := (collectionstore.Queue{DB: a.db, Cfg: cfg.Scheduler}).SubmitRepositories(
			c.Request.Context(), input.RequestID, valid, source, note, time.Now())
		if errors.Is(err, collectionstore.ErrSubmissionConflict) {
			c.JSON(http.StatusConflict, gin.H{"code": "submission_conflict"})
			return
		}
		if err != nil {
			a.fail(c, err)
			return
		}
		for i, name := range names {
			items[i].JobID = jobs[name].ID
		}
	}
	c.JSON(http.StatusAccepted, gin.H{"items": items})
}

func (a *collectionAPI) repoSubmissionJobs(c *gin.Context) {
	id := c.Param("requestId")
	if !submissionID.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidRequestID})
		return
	}
	prefix := collectionstore.ManualDiscoveryPrefix(id)
	var jobs []struct {
		storage.Job
		RepoFullName *string `gorm:"column:repo_full_name"`
	}
	db := a.db.WithContext(c.Request.Context())
	if err := db.Model(&storage.Job{}).
		Select("jobs.*, repositories.full_name AS repo_full_name").
		Joins("LEFT JOIN repositories ON repositories.repo_id = jobs.repo_id").
		Where("jobs.dedup_key GLOB ? AND jobs.type = ?", prefix+"*", consts.JobDiscoverManual).
		Order("jobs.id").Find(&jobs).Error; err != nil {
		a.fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(jobs))
	for _, job := range jobs {
		view := jobView(job.Job)
		view["repoFullName"] = job.RepoFullName
		items = append(items, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func discoveryEvents(db *gorm.DB, id int64) ([]gin.H, error) {
	var events []struct {
		SourceType      string
		SourceKey       string
		SourceURL       string
		FirstObservedAt int64
		Note            string
	}
	if err := db.Table("repo_discovery_events").Where("repo_id = ?", id).
		Order("first_observed_at DESC, source_type, source_key").Find(&events).Error; err != nil {
		return nil, err
	}
	items := make([]gin.H, 0, len(events))
	for _, event := range events {
		items = append(items, gin.H{"sourceType": event.SourceType, "sourceKey": event.SourceKey, "sourceUrl": event.SourceURL,
			"firstObservedAt": iso(event.FirstObservedAt), "note": event.Note})
	}
	return items, nil
}
