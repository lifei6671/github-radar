package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/summary"
)

func cachedReadme(c *gin.Context, db *gorm.DB, repo storage.Repository) (summary.ReadmeView, error) {
	if repo.Blocked || repo.Disabled || !repo.NameIsCurrent || repo.FirstNotFoundAt != nil || repo.State == consts.StatusUnavailable {
		return summary.ReadmeView{State: consts.StatusUnavailable}, nil
	}
	view, err := summary.CachedReadme(db, repo.RepoID)
	if err != nil || c.GetBool(publicReadKey) || view.State != consts.StatusPending ||
		view.Language == "" {
		return view, err
	}
	var job storage.Job
	cols := storage.JobColumns
	statuses := []string{consts.StatusPending, consts.StatusRunning, consts.StatusFailed, consts.StatusUnknown}
	if err := db.Select(cols.ID).Where(cols.RepoID+"=? AND "+cols.Type+"=? AND "+cols.Status+" IN ?", repo.RepoID,
		summary.TranslationJob, statuses).Order(cols.ID + " DESC").Limit(1).Find(&job).Error; err != nil {
		return view, err
	}
	if job.ID != 0 {
		view.TranslationJobID = &job.ID
	}
	return view, nil
}

func (a *collectionAPI) readme(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	db := a.db.WithContext(c.Request.Context())
	var repo storage.Repository
	if err := db.Where(readableRepoCondition(c)).Take(&repo, storage.RepositoryColumns.RepoID+"=?", id).Error; err != nil {
		a.fail(c, err)
		return
	}
	view, err := cachedReadme(c, db, repo)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}
