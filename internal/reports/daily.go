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

// Generate 首期使用当前已有观测，后续北京时间 08:00 截稿。重启补查最近已到期的缺刊。
func Generate(ctx context.Context, store Store, now time.Time) error {
	cutoff := now.UTC().Truncate(24 * time.Hour)
	earliest := cutoff.AddDate(0, 0, 1-catchUpDays)
	windows, err := store.Windows(ctx, earliest.Format(time.DateOnly))
	if err != nil {
		return err
	}
	if len(windows) == 0 {
		return store.Publish(ctx, Window{Date: now.In(beijing).Format(time.DateOnly),
			Start: now.Add(-24 * time.Hour), End: now, GeneratedAt: now})
	}
	// 保留上线后的归档，跳过首期之前的日期。
	first, err := time.Parse(time.DateOnly, windows[0].Date)
	if err != nil {
		return err
	}
	if first.After(earliest) {
		earliest = first
	}
	published := make(map[string]time.Time, len(windows))
	for _, w := range windows {
		published[w.Date] = w.End
	}
	for end := earliest; !end.After(cutoff); end = end.AddDate(0, 0, 1) {
		date := end.Format(time.DateOnly)
		if _, exists := published[date]; exists {
			continue
		}
		start := end.AddDate(0, 0, -1)
		// 首期可能在 08:00 前后任意时刻出刊，下一期接着首期实际截止时间。
		if previous, exists := published[start.Format(time.DateOnly)]; exists {
			start = previous
		}
		if err := store.Publish(ctx, Window{Date: date, Start: start, End: end, GeneratedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
