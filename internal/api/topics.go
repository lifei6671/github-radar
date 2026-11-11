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
	Day    string `json:"day,omitempty"`
	At     int64  `json:"at"`
	ID     int64  `json:"id"`
}

type topicDay struct {
	Day   string `json:"day"`
	Title string `json:"title"`
	Count int    `json:"count"`
}

type topicDaySpan struct {
	start time.Time
	end   time.Time
}

// topicDaySpans 将民用日期与各段时区偏移相交，覆盖午夜跳时或重复的时刻。
func topicDaySpans(day time.Time, loc *time.Location) []topicDaySpan {
	nextDay := day.AddDate(0, 0, 1)
	windowEnd := nextDay.Add(24 * time.Hour)
	var spans []topicDaySpan
	for zoneStart := day.Add(-24 * time.Hour); zoneStart.Before(windowEnd); {
		probe := zoneStart.In(loc)
		_, offset := probe.Zone()
		_, zoneEnd := probe.ZoneBounds()
		if !zoneEnd.IsZero() && !zoneEnd.After(zoneStart) {
			// 外推规则可能以 365 天计算闰年年尾，推进到下一个 UTC 年界。
			zoneEnd = time.Date(zoneStart.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		if zoneEnd.IsZero() || zoneEnd.After(windowEnd) {
			zoneEnd = windowEnd
		}
		shift := -time.Duration(offset) * time.Second
		start, end := day.Add(shift), nextDay.Add(shift)
		if start.Before(zoneStart) {
			start = zoneStart
		}
		if end.After(zoneEnd) {
			end = zoneEnd
		}
		if start.Before(end) {
			spans = append(spans, topicDaySpan{start, end})
		}
		zoneStart = zoneEnd
	}
	return spans
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
	day := c.Query("day")
	orderColumn := "t.updated_at"
	if day != "" {
		loc, err := time.LoadLocation(a.cfg.Recommendations.Timezone)
		if err != nil {
			a.fail(c, err)
			return
		}
		date, err := time.Parse(time.DateOnly, day)
		if err != nil || date.Format(time.DateOnly) != day {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_topic_day"})
			return
		}
		// 用入池日期和稳定的创建顺序生成版面，编辑反馈不会移动条目。
		var clauses []string
		var bounds []any
		for _, span := range topicDaySpans(date, loc) {
			clauses = append(clauses, "(t.created_at>=? AND t.created_at<?)")
			bounds = append(bounds, span.start.UnixMilli(), span.end.UnixMilli())
		}
		if len(clauses) == 0 {
			db = db.Where("1=0")
		} else {
			db = db.Where("("+strings.Join(clauses, " OR ")+")", bounds...)
		}
		orderColumn = "t.created_at"
	}
	if raw, exists := c.GetQuery("cursor"); exists {
		var cursor topicCursor
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Status != status || cursor.Day != day || cursor.ID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_cursor"})
			return
		}
		db = db.Where("("+orderColumn+",t.id)<(?,?)", cursor.At, cursor.ID)
	}
	var rows []struct {
		storage.TopicCandidate
		FullName      string
		SummaryZh     *string
		ValueTagsJSON *string
	}
	if err := db.Select("t.*,r.full_name,s.summary_zh,s.value_tags_json").
		Joins("JOIN repositories r ON r.repo_id=t.repo_id LEFT JOIN ai_summaries s ON s.repo_id=t.repo_id").
		Order(orderColumn + " DESC,t.id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		a.fail(c, err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		at := last.UpdatedAt
		if day != "" {
			at = last.CreatedAt
		}
		data, _ := json.Marshal(topicCursor{Status: status, Day: day, At: at, ID: last.ID})
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

// topicDays 使用与日期过滤相同的选题时区和创建顺序生成往期索引。
func (a *collectionAPI) topicDays(c *gin.Context) {
	status := c.Query("status")
	if status != "" && !validTopicStatus(status) {
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_topic_status"})
		return
	}
	loc, err := time.LoadLocation(a.cfg.Recommendations.Timezone)
	if err != nil {
		a.fail(c, err)
		return
	}
	var rows []storage.TopicCandidate
	db := a.db.WithContext(c.Request.Context()).Select("created_at", "title_zh").Order("created_at DESC,id DESC")
	if status != "" {
		db = db.Where("status=?", status)
	}
	if err := db.Find(&rows).Error; err != nil {
		a.fail(c, err)
		return
	}
	days := []topicDay{}
	index := make(map[string]int)
	for _, row := range rows {
		day := time.UnixMilli(row.CreatedAt).In(loc).Format(time.DateOnly)
		i, exists := index[day]
		if !exists {
			i = len(days)
			index[day] = i
			days = append(days, topicDay{Day: day, Title: row.TitleZh})
		}
		days[i].Count++
	}
	c.JSON(http.StatusOK, gin.H{"items": days})
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
