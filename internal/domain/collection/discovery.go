package collection

import (
	"errors"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

var ErrLeaseLost = errors.New("任务租约已失效")
var ErrCapacity = errors.New("监控容量已满")

// Payload 固化计划槽，跨窗口重试也不会重算槽。
type Payload struct {
	PlannedAt           int64  `json:"plannedAt"`
	RuleID              int64  `json:"ruleId,omitempty"`
	RunID               *int64 `json:"runId,omitempty"`
	AgeWindow           string `json:"ageWindow,omitempty"`
	RunRoot             bool   `json:"runRoot,omitempty"`
	SourceKey           string `json:"sourceKey,omitempty"`
	Query               string `json:"query,omitempty"`
	Language            string `json:"language,omitempty"`
	Period              string `json:"period,omitempty"`
	Manual              bool   `json:"manual,omitempty"`
	ObservationProbe    bool   `json:"observationProbe,omitempty"`
	ObservationFollowup bool   `json:"observationFollowup,omitempty"`
	ObservationSignalAt int64  `json:"observationSignalAt,omitempty"`
	ObservationRecheck  bool   `json:"observationRecheck,omitempty"`
	Repository          string `json:"repository,omitempty"`
	SubmissionKey       string `json:"submissionKey,omitempty"`
	SourceURL           string `json:"sourceUrl,omitempty"`
	Note                string `json:"note,omitempty"`
	Page                int    `json:"page,omitempty"`
	HistoryBefore       int64  `json:"historyBefore,omitempty"`
	StarFloor           int    `json:"starFloor,omitempty"`
	MinStars            int    `json:"minStars,omitempty"`
	MaxStars            int    `json:"maxStars,omitempty"`
	StarRange           bool   `json:"starRange,omitempty"`
	CreatedFrom         int64  `json:"createdFrom,omitempty"`
	CreatedUntil        int64  `json:"createdUntil,omitempty"`
}

func Slot(ms int64) int64 { return ms / 300000 * 300000 }

// TrendingBatch 保存真实观测时间，空榜也有批次；失败时该记录保持原值。
type TrendingBatch struct {
	ObservedSlot int64 `json:"observedSlot"`
	ObservedAt   int64 `json:"observedAt"`
	Size         int   `json:"size"`
}

func BatchKey(source, period string) string {
	return consts.TrendingStatePrefix + source + ":" + period
}

// SplitSearch 先按 Star 数拆分，再按创建时间拆分，避免把搜索的一千条上限当成完整结果。
func SplitSearch(p Payload, highestStars *int) ([]Payload, error) {
	low, high := max(p.MinStars, p.StarFloor), p.MaxStars
	if !p.StarRange {
		if highestStars == nil {
			return nil, errors.New("Search 超过一千条但缺少分片所需的 Star 观测")
		}
		high = *highestStars
	}
	if high < low {
		return nil, errors.New("Search 返回的 Star 观测低于当前分片门槛")
	}
	left, right := p, p
	left.Page, right.Page = 1, 1
	left.StarRange, right.StarRange = true, true
	left.MinStars, left.MaxStars = low, high
	right.MinStars, right.MaxStars = low, high
	if high > low {
		mid := low + (high-low)/2
		left.MinStars, left.MaxStars = low, mid
		right.MinStars, right.MaxStars = mid+1, high
		return []Payload{left, right}, nil
	}
	from, until := p.CreatedFrom, p.CreatedUntil
	if from == 0 {
		from = time.Date(2008, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		until = p.PlannedAt / 1000 * 1000
	}
	if until-from < 1000 {
		return nil, errors.New("Search 分片仍超过一千条，请缩小该来源查询")
	}
	mid := (from + (until-from)/2) / 1000 * 1000
	left.CreatedFrom, left.CreatedUntil = from, mid
	right.CreatedFrom, right.CreatedUntil = mid+1000, until
	return []Payload{left, right}, nil
}
