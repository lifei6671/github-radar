package collection

import "time"

const (
	DiscoveryPrimary  = "primary"
	DiscoveryBackfill = "backfill"
)

// CreationWindow 以计划槽的 UTC 日期计算闭区间，精确到 GitHub 接口支持的秒。
func CreationWindow(window string, planned time.Time, pass string) (from, until int64, ok bool) {
	var youngest, oldest int
	switch window {
	case "0-3":
		oldest = 3
	case "4-14":
		youngest, oldest = 4, 14
	case "15-30":
		youngest, oldest = 15, 30
	default:
		return 0, 0, false
	}
	day := planned.UTC().Truncate(24 * time.Hour)
	if pass == DiscoveryBackfill {
		// 重跑前一天的年龄窗口，补捞已跨过窗口边界的迟到索引。
		day = day.AddDate(0, 0, -1)
	}
	return day.AddDate(0, 0, -oldest).UnixMilli(),
		day.AddDate(0, 0, 1-youngest).Add(-time.Second).UnixMilli(), true
}
