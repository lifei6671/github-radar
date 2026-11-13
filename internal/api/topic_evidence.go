package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	domain "github.com/lifei6671/github-radar/internal/domain/topics"
	"github.com/lifei6671/github-radar/internal/storage"
)

// topicEvidence 按需读取传播证据和历史复核，避免列表为每张卡片重复查库。
func (a *collectionAPI) topicEvidence(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	db := a.db.WithContext(c.Request.Context())
	var topic storage.TopicCandidate
	if err := db.Take(&topic, "id = ?", id).Error; err != nil {
		a.fail(c, err)
		return
	}
	events, err := discoveryEvents(db, topic.RepoID)
	if err != nil {
		a.fail(c, err)
		return
	}
	var sightings []storage.TrendingObservation
	if err := db.Where("repo_id = ?", topic.RepoID).Order("observed_slot DESC, rank, source_key, window").
		Limit(5).Find(&sightings).Error; err != nil {
		a.fail(c, err)
		return
	}
	trending := make([]gin.H, 0, len(sightings))
	for _, row := range sightings {
		trending = append(trending, gin.H{"source": row.SourceKey, "period": row.Window, "rank": row.Rank, "at": iso(row.ObservedSlot)})
	}
	var history []storage.TopicReviewHistory
	if err := db.Where("topic_id = ?", id).Order("id DESC").Limit(10).Find(&history).Error; err != nil {
		a.fail(c, err)
		return
	}
	previous := make([]gin.H, 0, len(history))
	for _, row := range history {
		var entry domain.Candidate
		if err := json.Unmarshal([]byte(row.SnapshotJSON), &entry); err != nil {
			a.fail(c, err)
			return
		}
		view := topicView(storage.TopicCandidate(entry))
		view["id"], view["archivedAt"] = row.ID, iso(row.ArchivedAt)
		previous = append(previous, view)
	}
	c.JSON(http.StatusOK, gin.H{"events": events, "trending": trending, "history": previous})
}
