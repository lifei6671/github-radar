package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/summary"
)

// readme 只读取当前可公开的 README 和匹配该版本的译文，不触发模型调用。
func (a *collectionAPI) readme(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(a.cfg.Server.WriteTimeoutSeconds)*time.Second)
	defer cancel()
	var repo storage.Repository
	if err := a.db.WithContext(ctx).Where(publicRepoCondition).Take(&repo, storage.RepositoryColumns.RepoID+"=?", id).Error; err != nil {
		a.fail(c, err)
		return
	}
	if a.readmes == nil {
		c.JSON(http.StatusOK, summary.ReadmeView{State: consts.StatusUnavailable})
		return
	}
	view, err := a.readmes.ReadmeView(ctx, repo)
	switch {
	case err == nil:
		if !c.GetBool(publicReadKey) && view.OriginalHTML != "" {
			var job storage.Job
			if err := a.db.WithContext(ctx).Select(storage.JobColumns.ID).Where(
				storage.JobColumns.RepoID+"=? AND "+storage.JobColumns.Type+"=? AND "+storage.JobColumns.Status+" IN ('"+consts.StatusPending+"','"+consts.StatusRunning+"','"+consts.StatusFailed+"','"+consts.StatusUnknown+"')", id, summary.TranslationJob).Find(&job).Error; err != nil {
				a.fail(c, err)
				return
			}
			if job.ID != 0 {
				view.TranslationJobID = &job.ID
			}
		}
		c.JSON(http.StatusOK, view)
	case errors.Is(err, summary.ErrRepoUnavailable), errors.Is(err, storage.ErrRepositoryBlocked):
		c.JSON(http.StatusNotFound, gin.H{"code": consts.CodeNotFound})
	default:
		var upstream *gh.Failure
		if errors.As(err, &upstream) || errors.Is(err, context.DeadlineExceeded) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": consts.CodeReadmeUnavailable})
			return
		}
		a.fail(c, err)
	}
}
