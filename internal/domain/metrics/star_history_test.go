package metrics

import (
	"testing"
	"time"
)

// 零、缺测和今日未完成必须在同一响应里保持可区分。
func TestStarHistoryDaysCoverage(t *testing.T) {
	week := time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC).UnixMilli()
	now := week + 3*Day + Hour
	rows := []StarHistoryWeekData{{Week: week, FetchedAt: now,
		Days: []*int64{new(int64(0)), nil, new(int64(12)), new(int64(0)), new(int64(0))}}}
	got := StarHistoryDays(rows, now, 4, nil)
	if len(got) != 4 || got[0].Date != "2026-11-08" || got[0].State != "complete" || *got[0].Count != 0 ||
		got[1].State != "missing" || got[1].Count != nil || got[2].State != "complete" || *got[2].Count != 12 ||
		got[3].State != "partial" || *got[3].Count != 0 {
		t.Fatalf("覆盖与计数不符: %+v", got)
	}
	later := StarHistoryDays(rows, now+Day, 2, nil)
	if later[0].Count != nil || later[1].Count != nil {
		t.Fatalf("旧的部分值和未来占位不能补成零: %+v", later)
	}
}

func TestBreakoutUsesOnlyCoveredBaselineOrAbsoluteThreshold(t *testing.T) {
	zero, negative, forty := int64(0), int64(-100), int64(40)
	previous := []*int64{&zero, nil, &zero, &negative, &zero, nil, &zero}
	result := EvaluateBreakout(BreakoutLocal, 100, 10, previous)
	if result == nil || result.CoveredDays != 5 || *result.Baseline != 0 || *result.Ratio != 11 || result.Absolute {
		t.Fatalf("应使用五个非负基线: %+v", result)
	}
	if EvaluateBreakout(BreakoutLocal, 499, 10, previous[:6]) != nil || EvaluateBreakout(BreakoutLocal, 99, 10, previous) != nil ||
		EvaluateBreakout(BreakoutLocal, 100, 10, []*int64{&forty, &forty, &forty, &forty, &forty}) != nil {
		t.Fatal("覆盖、最小计数和倍数均须达标")
	}
	if signal := EvaluateBreakout(BreakoutLocal, 500, 10, nil); signal == nil || !signal.Absolute || signal.CoveredDays != 0 || signal.Ratio != nil {
		t.Fatalf("精确净增 500 可绕过基线: %+v", signal)
	}
}

func TestOfficialBreakoutIgnoresUnfinishedDay(t *testing.T) {
	week := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC).UnixMilli()
	now := week + 4*Day + Hour
	zero, yesterday, today := int64(0), int64(120), int64(700)
	rows := []StarHistoryWeekData{{Week: week, FetchedAt: now, Days: []*int64{&zero, &zero, &zero, &yesterday, &today}},
		{Week: week - StarHistoryWeek, FetchedAt: now, Days: []*int64{&zero, &zero, &zero, &zero, &zero, &zero, &zero}}}
	if result := OfficialBreakout(rows, now, nil); result == nil || result.Source != BreakoutGitHub || result.Value != 120 || result.CoveredDays != 7 {
		t.Fatalf("只有已结束的官方日计数可参加判定: %+v", result)
	}
}
