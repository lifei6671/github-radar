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

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/topicstore"
	"github.com/lifei6671/github-radar/internal/topics"
)

func validTopicStatus(status string) bool {
	return slices.Contains([]string{consts.StatusCandidate, consts.StatusShortlisted, consts.StatusAccepted, consts.StatusIgnored, consts.StatusPublished}, status)
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
	row, err := topics.Add(c.Request.Context(), topicstore.Store{DB: a.db}, id, time.Now())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, topicView(storage.TopicCandidate(row)))
}

func (a *collectionAPI) topics(c *gin.Context) {
	limit, ok := pageLimit(c)
	if !ok {
		return
	}
	status := c.Query("status")
	if status != "" && !validTopicStatus(status) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidTopicStatus})
		return
	}
	db := a.db.WithContext(c.Request.Context()).Table(storage.TopicCandidateTable + " t")
	if status != "" {
		db = db.Where("t."+storage.TopicCandidateColumns.Status+"=?", status)
	}
	day := c.Query("day")
	orderColumn := "t." + storage.TopicCandidateColumns.UpdatedAt
	if day != "" {
		loc, err := time.LoadLocation(runtimeFor(c).Config.Recommendations.Timezone)
		if err != nil {
			a.fail(c, err)
			return
		}
		date, err := time.Parse(time.DateOnly, day)
		if err != nil || date.Format(time.DateOnly) != day {
			c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidTopicDay})
			return
		}
		// 用入池日期和稳定的创建顺序生成版面，编辑反馈不会移动条目。
		var clauses []string
		var bounds []any
		for _, span := range topicDaySpans(date, loc) {
			createdAt := "t." + storage.TopicCandidateColumns.CreatedAt
			clauses = append(clauses, "("+createdAt+">=? AND "+createdAt+"<?)")
			bounds = append(bounds, span.start.UnixMilli(), span.end.UnixMilli())
		}
		if len(clauses) == 0 {
			db = db.Where("1=0")
		} else {
			db = db.Where("("+strings.Join(clauses, " OR ")+")", bounds...)
		}
		orderColumn = "t." + storage.TopicCandidateColumns.CreatedAt
	}
	if raw, exists := c.GetQuery("cursor"); exists {
		var cursor topicCursor
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Status != status || cursor.Day != day || cursor.ID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidCursor})
			return
		}
		db = db.Where("("+orderColumn+",t."+storage.TopicCandidateColumns.ID+")<(?,?)", cursor.At, cursor.ID)
	}
	var rows []struct {
		storage.TopicCandidate
		FullName      string
		NameIsCurrent bool `gorm:"column:name_is_current"`
		SummaryZh     *string
		ValueTagsJSON *string
	}
	if err := db.Select("t.*,r." + storage.RepositoryColumns.FullName + ",r." + storage.RepositoryColumns.NameIsCurrent + ",s." + storage.AISummaryColumns.SummaryZh + ",s." + storage.AISummaryColumns.ValueTagsJSON).
		Joins("JOIN " + storage.RepositoryTable + " r ON r." + storage.RepositoryColumns.RepoID + "=t." + storage.TopicCandidateColumns.RepoID + " LEFT JOIN " + storage.AISummaryTable + " s ON s." + storage.AISummaryColumns.RepoID + "=t." + storage.TopicCandidateColumns.RepoID).
		Order(orderColumn + " DESC,t." + storage.TopicCandidateColumns.ID + " DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
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
		view["nameIsCurrent"] = row.NameIsCurrent
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
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidTopicStatus})
		return
	}
	loc, err := time.LoadLocation(runtimeFor(c).Config.Recommendations.Timezone)
	if err != nil {
		a.fail(c, err)
		return
	}
	var rows []storage.TopicCandidate
	db := a.db.WithContext(c.Request.Context()).Select(storage.TopicCandidateColumns.CreatedAt, storage.TopicCandidateColumns.TitleZh).Order(storage.TopicCandidateColumns.CreatedAt + " DESC," + storage.TopicCandidateColumns.ID + " DESC")
	if status != "" {
		db = db.Where(storage.TopicCandidateColumns.Status+"=?", status)
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
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidTopicUpdate})
		return
	}
	now := time.Now().UnixMilli()
	values := map[string]any{storage.TopicCandidateColumns.UpdatedAt: now, storage.TopicCandidateColumns.ReviewedAt: now}
	if input.Status != nil {
		values[storage.TopicCandidateColumns.Status] = *input.Status
	}
	if input.Feedback != nil {
		values[storage.TopicCandidateColumns.Feedback] = textPtr(strings.TrimSpace(*input.Feedback))
	}
	var row storage.TopicCandidate
	err := a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&storage.TopicCandidate{}).Where(storage.TopicCandidateColumns.ID+"=?", id).Updates(values)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Take(&row, storage.TopicCandidateColumns.ID+"=?", id).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": consts.CodeNotFound})
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
