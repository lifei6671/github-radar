package metrics

import "time"

const (
	StarHistoryWeek      = 7 * Day
	StarHistoryAutoWeeks = 5
	StarHistoryPageSize  = 30
	StarHistoryMaxPages  = 3
	StarHistoryBudget    = 120
	// 仅最近已结束的上游自然日可作为待确认爆发，不能把旧信号无限延续。
	StarHistorySignalAge = 2 * Day
)

// StarHistoryWeekData 的时间戳沿用 GitHub 周首边界；nil 表示上游未返回该日。
type StarHistoryWeekData struct {
	Week      int64
	Days      []*int64
	FetchedAt int64
}

type StarHistoryDay struct {
	Date  string `json:"date"`
	Count *int64 `json:"count"`
	State string `json:"state"`
}

// StarHistoryDays 以查询范围生成连续日期。判断完成度使用该周抓取时刻，避免过期的当天值被误认成已完成。
func StarHistoryDays(weeks []StarHistoryWeekData, now int64, days int, createdAt *int64) []StarHistoryDay {
	if len(weeks) == 0 {
		return []StarHistoryDay{}
	}
	latest := weeks[0].Week
	for _, week := range weeks {
		latest = max(latest, week.Week)
	}
	today := latest + (now-latest)/Day*Day
	byDay := make(map[int64]StarHistoryDay, len(weeks)*7)
	for _, week := range weeks {
		// 周首可能带时区偏移，按与最新周的周序对齐标签，仍用该周的真实边界判断完成度。
		aligned := latest - (latest-week.Week+StarHistoryWeek/2)/StarHistoryWeek*StarHistoryWeek
		for i, count := range week.Days {
			at := aligned + int64(i)*Day
			actual := week.Week + int64(i)*Day
			if count == nil || at > today || createdAt != nil && actual+Day <= *createdAt {
				continue
			}
			state := "complete"
			if week.FetchedAt < actual+Day {
				if week.FetchedAt < actual || at != today {
					continue
				}
				state = "partial"
			}
			byDay[at] = StarHistoryDay{Count: count, State: state}
		}
	}
	result := make([]StarHistoryDay, 0, days)
	for i := days - 1; i >= 0; i-- {
		at := today - int64(i)*Day
		day, ok := byDay[at]
		if !ok {
			day.State = "missing"
		}
		day.Date = time.UnixMilli(at).UTC().Format(time.DateOnly)
		result = append(result, day)
	}
	return result
}

// OfficialBreakout 只取上一个已完成自然日，与更早七日的官方数据比较。
func OfficialBreakout(weeks []StarHistoryWeekData, now int64, createdAt *int64) *Breakout {
	days := StarHistoryDays(weeks, now, 9, createdAt)
	if len(days) != 9 || days[7].State != "complete" {
		return nil
	}
	var baseline []*int64
	for _, day := range days[:7] {
		baseline = append(baseline, day.Count)
	}
	latest := weeks[0].Week
	for _, week := range weeks {
		latest = max(latest, week.Week)
	}
	return EvaluateBreakout(BreakoutGitHub, *days[7].Count, latest+(now-latest)/Day*Day, baseline)
}
