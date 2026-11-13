package admission

import (
	"testing"
	"time"
)

// 分档使用 UTC 日界，复核门槛、跨档保护和精确增长豁免。
func TestMonitoringBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 13, 8, 15, 0, 0, time.FixedZone("UTC+8", 8*3600))
	for _, c := range []struct {
		age, stars  int
		pass, grace bool
	}{{7, 199, false, false}, {7, 200, true, false}, {8, 200, false, true}, {11, 200, false, false},
		{14, 500, true, false}, {15, 500, false, true}, {30, 1000, true, false}, {31, 1000, false, true}} {
		created := DayStart(now).AddDate(0, 0, -c.age).Add(23 * time.Hour).UnixMilli()
		oldPush := now.Add(-16 * 24 * time.Hour).UnixMilli()
		in := Input{CreatedAt: &created, PushedAt: &oldPush, Stars: c.stars, Public: true}
		_, pass := RegularMonitoring(in, now)
		_, grace := CrossAgeGrace(in, now)
		if pass != c.pass || grace != c.grace {
			t.Errorf("年龄 %d 星标 %d：pass=%t grace=%t", c.age, c.stars, pass, grace)
		}
	}
	future := now.Add(time.Minute).UnixMilli()
	if _, ok := RegularMonitoring(Input{CreatedAt: &future, Stars: 10000}, now); ok {
		t.Fatal("未来创建时间取得了常规资格")
	}
	delta := int64(50)
	in := Input{Stars: 40, Public: true, Evidence: Evidence{Exact24h: &delta, StartAt: now.Add(-24 * time.Hour).UnixMilli(), EndAt: now.UnixMilli()}}
	if d := Monitoring(in, now); d.Reason != "exact_growth_50" {
		t.Fatalf("真实增长未获重点资格: %+v", d)
	}
	in.Evidence.Exact24h = nil
	if d := Monitoring(in, now); d.State != Defer {
		t.Fatalf("累计 Star 冒充增长: %+v", d)
	}
}

// 纯规则覆盖内容门槛、首次清洗、同指纹缓存及人工安全边界。
func TestAIEligible(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 15, 0, 0, time.UTC)
	created := now.Add(-48 * time.Hour).UnixMilli()
	in := Input{Public: true, CreatedAt: &created, Stars: 40, Description: "TODO"}
	if got := AIEligible(in, "topics", now); got.Reason != "no_readable_source" {
		t.Fatalf("模板说明不应触发主题任务: %+v", got)
	}
	in.Readme = "pip install fixture"
	if got := AIEligible(in, "summary", now); got.Lane != Exploration {
		t.Fatalf("短 README 没有进入首次清洗: %+v", got)
	}
	in.Cache = true
	if got := AIEligible(in, "summary", now); got.Reason != "fingerprint_cached" {
		t.Fatalf("缓存未复用: %+v", got)
	}
	in.Manual = true
	in.Public = false
	if got := AIEligible(in, "summary", now); got.State != Reject {
		t.Fatalf("人工绕过安全: %+v", got)
	}
}
