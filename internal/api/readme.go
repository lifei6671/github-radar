package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

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
	if err := a.db.WithContext(ctx).Where(publicRepoCondition).Take(&repo, "repo_id=?", id).Error; err != nil {
		a.fail(c, err)
		return
	}
	if a.readmes == nil {
		c.JSON(http.StatusOK, summary.ReadmeView{State: "unavailable"})
		return
	}
	view, err := a.readmes.ReadmeView(ctx, repo)
	switch {
	case err == nil:
		if !c.GetBool(publicReadKey) && view.OriginalHTML != "" {
			var job storage.Job
			if err := a.db.WithContext(ctx).Select("id").Where(
				"repo_id=? AND type=? AND status IN ('pending','running','failed','unknown')", id, summary.TranslationJob).Find(&job).Error; err != nil {
				a.fail(c, err)
				return
			}
			if job.ID != 0 {
				view.TranslationJobID = &job.ID
			}
		}
		c.JSON(http.StatusOK, view)
	case errors.Is(err, summary.ErrRepoUnavailable), errors.Is(err, storage.ErrRepositoryBlocked):
		c.JSON(http.StatusNotFound, gin.H{"code": "not_found"})
	default:
		var upstream *gh.Failure
		if errors.As(err, &upstream) || errors.Is(err, context.DeadlineExceeded) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": "readme_unavailable"})
			return
		}
		a.fail(c, err)
	}
}
