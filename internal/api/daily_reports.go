package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/reports"
	"github.com/lifei6671/github-radar/internal/storage/reportstore"
)

func (a *collectionAPI) dailyReport(c *gin.Context) {
	const pageSize = 24
	kind := reports.Kind(c.DefaultQuery("kind", string(reports.Daily)))
	if kind != reports.Daily && kind != reports.Weekly && kind != reports.Monthly {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidFilter})
		return
	}
	date, before := c.Query("date"), c.Query("before")
	for _, key := range []string{date, before} {
		if key != "" {
			if _, _, valid := kind.Bounds(key); !valid {
				c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidFilter})
				return
			}
		}
	}
	store := reportstore.Store{DB: a.db}
	published, err := store.ReadEditorials(c.Request.Context(), kind, date, before, pageSize+1)
	if err != nil {
		a.fail(c, err)
		return
	}
	archive := published.Archive
	var next *string
	if len(archive) > pageSize {
		archive = archive[:pageSize]
		next = &archive[pageSize-1].IssueDate
	}
	dates := make([]gin.H, 0, len(archive))
	for _, issue := range archive {
		entry := gin.H{"date": issue.IssueDate, "count": issue.Count, "headline": issue.Headline}
		if kind != reports.Daily {
			start, _, _ := kind.Bounds(issue.IssueDate)
			entry["periodFrom"] = start.Format(time.DateOnly)
		}
		dates = append(dates, entry)
	}
	var report gin.H
	if edition := published.Edition; edition != nil {
		var stories []reports.Story
		if err := json.Unmarshal([]byte(edition.StoriesJSON), &stories); err != nil {
			a.fail(c, err)
			return
		}
		report = gin.H{"date": edition.IssueKey, "number": edition.IssueNumber, "observedFrom": iso(edition.ObservedFrom), "observedTo": iso(edition.ObservedTo),
			"generatedAt": iso(edition.GeneratedAt), "headline": edition.Headline, "lead": edition.Lead, "stories": stories, "items": published.Citations}
		if kind != reports.Daily {
			start, end, _ := kind.Bounds(edition.IssueKey)
			_, required := kind.PublicationPolicy(edition.IssueKey, runtimeFor(c).Config.ReportPublication)
			report["periodFrom"] = start.Format(time.DateOnly)
			report["periodTo"] = end.AddDate(0, 0, -1).Format(time.DateOnly)
			report["sourceIssues"] = edition.SourceIssues
			report["publicationStatus"] = edition.PublicationStatus
			report["coverageDays"] = edition.CoverageDays
			report["requiredDays"] = required
			report["inProgress"] = time.Now().Before(end)
		}
	}
	c.JSON(http.StatusOK, gin.H{"report": report, "archive": dates, "nextBefore": next, "total": published.Total, "calendar": published.Calendar})
}
