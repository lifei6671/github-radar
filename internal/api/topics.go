package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/topics"
)

func validTopicStatus(status string) bool {
	return slices.Contains([]string{"candidate", "shortlisted", "accepted", "ignored", "published"}, status)
}

type topicCursor struct {
	Status string `json:"status"`
	At     int64  `json:"at"`
	ID     int64  `json:"id"`
}

func (a *collectionAPI) addTopic(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	row, err := topics.Add(c.Request.Context(), a.db, id, time.Now())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, topicView(row))
}

func (a *collectionAPI) topics(c *gin.Context) {
	limit, ok := pageLimit(c)
	if !ok {
		return
	}
	status := c.Query("status")
	if status != "" && !validTopicStatus(status) {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_topic_status"})
		return
	}
	db := a.db.WithContext(c.Request.Context()).Table("topic_candidates t")
	if status != "" {
		db = db.Where("t.status=?", status)
	}
	if raw, exists := c.GetQuery("cursor"); exists {
		var cursor topicCursor
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Status != status || cursor.ID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_cursor"})
			return
		}
		db = db.Where("(t.updated_at,t.id)<(?,?)", cursor.At, cursor.ID)
	}
	var rows []struct {
		storage.TopicCandidate
		FullName      string
		SummaryZh     *string
		ValueTagsJSON *string
	}
	if err := db.Select("t.*,r.full_name,s.summary_zh,s.value_tags_json").
		Joins("JOIN repositories r ON r.repo_id=t.repo_id LEFT JOIN ai_summaries s ON s.repo_id=t.repo_id").
		Order("t.updated_at DESC,t.id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		a.fail(c, err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		data, _ := json.Marshal(topicCursor{status, last.UpdatedAt, last.ID})
		value := base64.RawURLEncoding.EncodeToString(data)
		next = &value
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		view := topicView(row.TopicCandidate)
		view["fullName"], view["summaryZh"] = row.FullName, row.SummaryZh
		tags := []string{}
		if row.ValueTagsJSON != nil {
			_ = json.Unmarshal([]byte(*row.ValueTagsJSON), &tags)
		}
		view["valueTags"] = tags
		items = append(items, view)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "nextCursor": next, "updatedAt": iso(time.Now().UnixMilli())})
}

func (a *collectionAPI) updateTopic(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		Status   *string `json:"status"`
		Feedback *string `json:"feedback"`
	}
	if c.ShouldBindJSON(&input) != nil || (input.Status == nil && input.Feedback == nil) ||
		(input.Status != nil && !validTopicStatus(*input.Status)) ||
		(input.Feedback != nil && utf8.RuneCountInString(*input.Feedback) > 500) {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_topic_update"})
		return
	}
	now := time.Now().UnixMilli()
	values := map[string]any{"updated_at": now, "reviewed_at": now}
	if input.Status != nil {
		values["status"] = *input.Status
	}
	if input.Feedback != nil {
		values["feedback"] = textPtr(strings.TrimSpace(*input.Feedback))
	}
	var row storage.TopicCandidate
	err := a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&storage.TopicCandidate{}).Where("id=?", id).Updates(values)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Take(&row, "id=?", id).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": "not_found"})
			return
		}
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, topicView(row))
}

func topicView(row storage.TopicCandidate) gin.H {
	var score *float64
	if row.HotnessScore != nil {
		value := 0.4**row.HotnessScore + 0.6*row.TopicScore
		score = &value
	}
	return gin.H{"id": row.ID, "repoId": row.RepoID, "titleZh": row.TitleZh, "readerValue": row.ReaderValue,
		"sourceUrl": row.SourceURL, "reason": row.Reason, "topicScore": row.TopicScore, "hotnessScore": row.HotnessScore,
		"recommendationScore": score, "growth24h": row.Growth24h, "status": row.Status, "feedback": row.Feedback,
		"reviewedAt": optionalISO(row.ReviewedAt), "createdAt": iso(row.CreatedAt), "updatedAt": iso(row.UpdatedAt)}
}
