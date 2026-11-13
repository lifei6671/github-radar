package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/metricstore"
)

// observationReplay 只返回试运行前后数量，不更新仓库、队列或配置。
func (a *collectionAPI) observationReplay(c *gin.Context) {
	cfg := runtimeFor(c).Config
	report, err := metrics.ObservationReplay(c.Request.Context(), metricstore.Store{DB: a.db}, cfg, time.Now())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"report": report, "scheduler": cfg.Scheduler.ObservationTrial()})
}

// repoObservationHistory 仅为后台仓库详情返回最近 20 条状态或档位变更。
func repoObservationHistory(db *gorm.DB, repoID int64) ([]gin.H, error) {
	var rows []storage.ObservationTransition
	if err := db.Where("repo_id = ?", repoID).Order("occurred_at DESC, id DESC").Limit(20).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		result = append(result, gin.H{"id": row.ID, "at": iso(row.OccurredAt),
			"fromState": row.FromState, "toState": row.ToState, "fromGrade": row.FromGrade, "toGrade": row.ToGrade,
			"reason": row.Reason, "evidence": json.RawMessage(row.EvidenceJSON)})
	}
	return result, nil
}
