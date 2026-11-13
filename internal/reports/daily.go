package reports

import (
	"context"
	"time"
)

const catchUpDays = 7

var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)

type Window struct {
	Date        string
	Start, End  time.Time
	GeneratedAt time.Time
}

type PublishedWindow struct {
	Date string
	End  time.Time
}

type Store interface {
	Windows(ctx context.Context, since string) ([]PublishedWindow, error)
	Publish(ctx context.Context, window Window) error
}

// DailyCatchUpStart 限定自动补刊和旧版窗口校正的日期范围。
func DailyCatchUpStart(now time.Time) time.Time {
	latest, _, _ := Daily.Bounds(Daily.LatestDueKey(now))
	return latest.AddDate(0, 0, 1-catchUpDays)
}

// ClosedDailyWindow 检查已保存的窗口是否以对应北京时间自然日结束。
func ClosedDailyWindow(date string, end int64) bool {
	_, boundary, valid := Daily.Bounds(date)
	return valid && boundary.UnixMilli() == end
}

// Generate 每期使用北京时间上一自然日的观测，次日 08:00 起出刊。
// 重启补查最近缺刊，并重新取证仍使用旧时间窗的期次。
func Generate(ctx context.Context, store Store, now time.Time) error {
	latest, _, _ := Daily.Bounds(Daily.LatestDueKey(now))
	earliest := DailyCatchUpStart(now)
	windows, err := store.Windows(ctx, earliest.Format(time.DateOnly))
	if err != nil {
		return err
	}
	published := make(map[string]time.Time, len(windows))
	for _, w := range windows {
		published[w.Date] = w.End
	}
	for start := earliest; !start.After(latest); start = start.AddDate(0, 0, 1) {
		date, end := start.Format(time.DateOnly), start.AddDate(0, 0, 1)
		if published[date].Equal(end) {
			continue
		}
		if err := store.Publish(ctx, Window{Date: date, Start: start, End: end, GeneratedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
