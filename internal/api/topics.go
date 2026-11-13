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
	domain "github.com/lifei6671/github-radar/internal/domain/topics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/topicstore"
	"github.com/lifei6671/github-radar/internal/topics"
)

func validTopicStatus(status string) bool {
	return slices.Contains([]string{consts.StatusCandidate, consts.StatusShortlisted, consts.StatusAccepted, consts.StatusIgnored, consts.StatusPublished}, status)
}

type topicCursor struct {
	Status string   `json:"status"`
	Day    string   `json:"day,omitempty"`
	At     int64    `json:"at"`
	ID     int64    `json:"id"`
	Score  *float64 `json:"score,omitempty"`
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
	byScore := c.Query("sort") == "score"
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
	if byScore {
		orderColumn = "t." + storage.TopicCandidateColumns.TopicScore
	}
	if raw, exists := c.GetQuery("cursor"); exists {
		var cursor topicCursor
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Status != status || cursor.Day != day || cursor.ID <= 0 || byScore != (cursor.Score != nil) {
			c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidCursor})
			return
		}
		var boundary any = cursor.At
		if byScore {
			boundary = *cursor.Score
		}
		db = db.Where("("+orderColumn+",t."+storage.TopicCandidateColumns.ID+")<(?,?)", boundary, cursor.ID)
	}
	var rows []struct {
		storage.TopicCandidate
		FullName          string
		NameIsCurrent     bool `gorm:"column:name_is_current"`
		SummaryZh         *string
		ValueTagSlugsJSON *string
	}
	if err := db.Select("t.*,r." + storage.RepositoryColumns.FullName + ",r." + storage.RepositoryColumns.NameIsCurrent + ",s." + storage.AISummaryColumns.SummaryZh + ",s." + storage.AISummaryColumns.ValueTagSlugsJSON).
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
		cursor := topicCursor{Status: status, Day: day, At: at, ID: last.ID}
		if byScore {
			cursor.Score = &last.TopicScore
		}
		data, _ := json.Marshal(cursor)
		value := base64.RawURLEncoding.EncodeToString(data)
		next = &value
	}
	var vocabulary storage.TechnicalTags
	if len(rows) > 0 {
		var err error
		vocabulary, err = storage.ReadTechnicalTags(a.db.WithContext(c.Request.Context()))
		if err != nil {
			a.fail(c, err)
			return
		}
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		view := topicView(row.TopicCandidate)
		view["fullName"], view["summaryZh"] = row.FullName, row.SummaryZh
		view["nameIsCurrent"] = row.NameIsCurrent
		tags := []string{}
		if row.ValueTagSlugsJSON != nil {
			tags = vocabulary.Labels(*row.ValueTagSlugsJSON)
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
		Status               *string `json:"status"`
		Feedback             *string `json:"feedback"`
		Verification         *string `json:"verification"`
		VerificationEvidence *string `json:"verificationEvidence"`
		OverrideReason       *string `json:"overrideReason"`
		AssessmentRevision   string  `json:"assessmentRevision"`
		ExpectedUpdatedAt    *string `json:"expectedUpdatedAt"`
		Rescore              bool    `json:"rescore"`
	}
	if c.ShouldBindJSON(&input) != nil || (!input.Rescore && input.Status == nil && input.Feedback == nil &&
		input.Verification == nil && input.VerificationEvidence == nil && input.OverrideReason == nil) ||
		(input.Status != nil && !validTopicStatus(*input.Status)) ||
		!validReviewText(input.Feedback) || !validReviewText(input.VerificationEvidence) || !validReviewText(input.OverrideReason) ||
		(input.Verification != nil && !slices.Contains([]string{"", "pending", "verified", "failed"}, *input.Verification)) {
		c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidTopicUpdate})
		return
	}
	now := time.Now()
	var row storage.TopicCandidate
	err := a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Take(&row, "id = ?", id).Error; err != nil {
			return err
		}
		entry := domain.Candidate(row)
		// 浏览器提交携带打开表单时的记录版本。重新入池或另一轮人工决定会使旧表单失效。
		if input.ExpectedUpdatedAt != nil && *input.ExpectedUpdatedAt != iso(row.UpdatedAt) {
			return errTopicEditStale
		}
		oldReview := entry.Review()
		review := oldReview
		reopened := input.Status != nil && *input.Status == consts.StatusCandidate && entry.Status != consts.StatusCandidate
		if reopened {
			review = domain.Review{}
		}
		if input.Verification != nil && !reopened {
			review.Verification = *input.Verification
		}
		if input.VerificationEvidence != nil && !reopened {
			review.Evidence = strings.TrimSpace(*input.VerificationEvidence)
		}
		if input.OverrideReason != nil && !reopened {
			review.Override = strings.TrimSpace(*input.OverrideReason)
		}
		if (review.Verification == "verified" || review.Verification == "failed") && review.Evidence == "" {
			return errTopicVerificationEvidence
		}
		assessment := entry.Assessment()
		accepting := input.Status != nil && (*input.Status == consts.StatusAccepted || *input.Status == consts.StatusPublished)
		reviewChanged := review.Verification != oldReview.Verification || review.Evidence != oldReview.Evidence
		checkSource := input.Rescore || accepting || reopened || reviewChanged
		if checkSource {
			if input.Rescore && entry.Status == consts.StatusPublished {
				return errTopicRescoreState
			}
			signals, err := topicstore.Signals(tx, entry.RepoID, now.Add(-24*time.Hour).UnixMilli())
			if err != nil {
				return err
			}
			// 新导读未就绪时，继续保留已有风险标记。自动更新资料不能撤销风险。
			for _, risk := range assessment.Brief.Risks {
				if !slices.Contains(signals.Brief.Risks, risk) {
					signals.Brief.Risks = append(signals.Brief.Risks, risk)
				}
			}
			slices.Sort(signals.Brief.Risks)
			revision := domain.EditorialRevision(signals)
			changed := assessment.Revision != revision
			if reviewChanged && (review.Verification == "verified" || review.Verification == "failed") &&
				(input.ExpectedUpdatedAt == nil || input.AssessmentRevision == "" ||
					input.AssessmentRevision != revision || input.AssessmentRevision != assessment.Revision) {
				return errTopicReviewStale
			}
			if changed {
				review.Verification, review.Evidence = "", ""
			}
			if accepting && len(signals.Brief.Risks) > 0 && review.Verification != "verified" {
				return errTopicVerificationRequired
			}
			if input.Rescore || reopened {
				entry.Rescore(signals, review, now)
				assessment = entry.Assessment()
			} else if changed && (assessment.Revision != "" || len(signals.Brief.Risks) > 0) {
				return errTopicReviewStale
			}
		}
		if input.Verification != nil || input.VerificationEvidence != nil {
			assessment = assessment.WithReview(review)
		}
		if input.Status != nil {
			entry.Status = *input.Status
		}
		if (input.Status != nil || reviewChanged || input.Rescore) && (entry.Status == consts.StatusAccepted || entry.Status == consts.StatusPublished) &&
			len(assessment.Brief.Risks) > 0 && review.Verification != "verified" {
			return errTopicVerificationRequired
		}
		if len(assessment.Dimensions) > 0 {
			entry.SetAssessment(assessment)
		}
		if input.Feedback != nil {
			entry.Feedback = textPtr(strings.TrimSpace(*input.Feedback))
		}
		at := max(now.UnixMilli(), row.UpdatedAt+1)
		entry.UpdatedAt = at
		if reopened {
			entry.CreatedAt, entry.ReviewedAt, entry.Reason = at, nil, "人工重新打开选题"
			if input.Feedback == nil || (row.Feedback != nil && strings.TrimSpace(*input.Feedback) == *row.Feedback) {
				entry.Feedback = nil
			}
		}
		if reopened || row.Status == consts.StatusPublished && entry.Status != row.Status ||
			row.ScoreVersion < entry.ScoreVersion || input.Rescore && (oldReview.Verification != "" && review.Verification == "" || row.ReleaseKey != entry.ReleaseKey) {
			if err := topicstore.Archive(tx, row, at); err != nil {
				return err
			}
		}
		if !reopened && (input.Status != nil || input.Feedback != nil || input.Verification != nil || input.VerificationEvidence != nil || input.OverrideReason != nil) {
			entry.ReviewedAt = &at
		}
		if review != oldReview {
			raw, _ := json.Marshal(review)
			entry.EditorialReviewJSON = string(raw)
		}
		row = storage.TopicCandidate(entry)
		return tx.Save(&row).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"code": consts.CodeNotFound})
			return
		}
		if errors.Is(err, errTopicVerificationEvidence) || errors.Is(err, errTopicVerificationRequired) || errors.Is(err, errTopicRescoreState) || errors.Is(err, errTopicReviewStale) {
			c.JSON(http.StatusBadRequest, gin.H{"code": consts.CodeInvalidTopicUpdate, "message": err.Error()})
			return
		}
		if errors.Is(err, errTopicEditStale) {
			c.JSON(http.StatusConflict, gin.H{"code": "topic_edit_conflict", "message": err.Error()})
			return
		}
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, topicView(row))
}

func topicView(row storage.TopicCandidate) gin.H {
	var score *float64
	if row.ScoreVersion < domain.ScoreVersion && row.HotnessScore != nil {
		value := 0.4**row.HotnessScore + 0.6*row.TopicScore
		score = &value
	}
	body := gin.H{"id": row.ID, "repoId": row.RepoID, "titleZh": row.TitleZh, "readerValue": row.ReaderValue,
		"sourceUrl": row.SourceURL, "reason": row.Reason, "topicScore": row.TopicScore, "hotnessScore": row.HotnessScore,
		"recommendationScore": score, "growth24h": row.Growth24h, "status": row.Status, "feedback": row.Feedback,
		"reviewedAt": optionalISO(row.ReviewedAt), "createdAt": iso(row.CreatedAt), "updatedAt": iso(row.UpdatedAt)}
	entry := domain.Candidate(row)
	body["scoreVersion"], body["review"] = max(1, row.ScoreVersion), entry.Review()
	if row.ScoreVersion >= domain.ScoreVersion {
		body["assessment"], body["suggestion"] = entry.Assessment(), domain.Suggestion(row.TopicScore)
	}
	return body
}

func validReviewText(value *string) bool {
	return value == nil || utf8.RuneCountInString(*value) <= 500
}

var (
	errTopicVerificationEvidence = errors.New("请填写人工实测结果或证据")
	errTopicVerificationRequired = errors.New("此选题涉及执行代码、系统权限或个人数据，采用或发布前需通过人工核验")
	errTopicRescoreState         = errors.New("已发布选题需重新打开后才能评分")
	errTopicReviewStale          = errors.New("选题资料版本已变化，请重新评分后核验")
	errTopicEditStale            = errors.New("选题已更新，请重新打开复核表单")
)
