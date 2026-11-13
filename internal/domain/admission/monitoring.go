package admission

import "time"

// RegularMonitoring 判断常规监控资格。累计 Star 不参与轻量发现的筛选。
func RegularMonitoring(in Input, now time.Time) (string, bool) {
	age, ok := AgeDays(in.CreatedAt, now)
	if !ok {
		return "invalid_creation_time", false
	}
	switch {
	case age <= 7:
		return "age_7d_stars_200", in.Stars >= 200
	case age <= 14:
		return "age_14d_stars_500", in.Stars >= 500
	case age <= 30:
		return "age_30d_stars_1000", in.Stars >= 1000
	default:
		return "established_active", in.Stars >= 1000 && in.PushedAt != nil &&
			*in.PushedAt <= now.UnixMilli() && *in.PushedAt >= now.Add(-15*24*time.Hour).UnixMilli()
	}
}

// CrossAgeGrace 保护已达旧档门槛的活跃项目，窗口从跨档的 UTC 日界开始。
func CrossAgeGrace(in Input, now time.Time) (int64, bool) {
	age, ok := AgeDays(in.CreatedAt, now)
	if !ok {
		return 0, false
	}
	day := DayStart(time.UnixMilli(*in.CreatedAt))
	for _, edge := range []struct{ start, minimum int }{{8, 200}, {15, 500}, {31, 1000}} {
		if age >= edge.start && age < edge.start+3 && in.Stars >= edge.minimum {
			return day.Add(time.Duration(edge.start+3) * 24 * time.Hour).UnixMilli(), true
		}
	}
	return 0, false
}

// Monitoring 将正常资格与例外分开，保留可解释的原因。
func Monitoring(in Input, now time.Time) Decision {
	if !in.Public {
		return Decision{State: Reject, Reason: "not_public"}
	}
	switch n := in.Evidence.FreshGrowth(now); {
	case in.Followed:
		return Decision{State: Pass, Reason: "manual_follow", Priority: 80}
	case n >= 100 || in.Evidence.Strong(now):
		return Decision{State: Pass, Reason: "verified_burst", Priority: 100}
	case n >= 50:
		return Decision{State: Pass, Reason: "exact_growth_50", Priority: 80}
	case n >= 15:
		return Decision{State: Pass, Reason: "exact_growth_15", Priority: 40}
	}
	if reason, ok := RegularMonitoring(in, now); ok {
		return Decision{State: Pass, Reason: reason, Priority: 20}
	}
	return Decision{State: Defer, Reason: "below_monitoring_threshold", RetryAt: DayStart(now).Add(24 * time.Hour).UnixMilli()}
}
