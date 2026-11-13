package collection

import (
	"testing"
	"time"
)

// 跨 UTC 日界和月界时，三个主窗口仍保持互斥且连续。
func TestCreationWindowsUseUTCDateAndInclusiveSeconds(t *testing.T) {
	plan := time.Date(2026, 11, 1, 7, 55, 0, 0, time.FixedZone("CST", 8*3600))
	for _, test := range []struct{ window, from, until string }{
		{"0-3", "2026-10-28T00:00:00Z", "2026-10-31T23:59:59Z"},
		{"4-14", "2026-10-17T00:00:00Z", "2026-10-27T23:59:59Z"},
		{"15-30", "2026-10-01T00:00:00Z", "2026-10-16T23:59:59Z"},
	} {
		from, until, ok := CreationWindow(test.window, plan, DiscoveryPrimary)
		if !ok || time.UnixMilli(from).UTC().Format(time.RFC3339) != test.from || time.UnixMilli(until).UTC().Format(time.RFC3339) != test.until {
			t.Fatalf("%s 窗口错误: %d %d", test.window, from, until)
		}
		a, b, _ := CreationWindow(test.window, plan, DiscoveryBackfill)
		if from-a != (24*time.Hour).Milliseconds() || until-b != (24*time.Hour).Milliseconds() {
			t.Fatal("补扫应使用前一个 UTC 日期锚点")
		}
	}
	if _, err := (Payload{AgeWindow: "0-3", Query: "topic:mcp created:>2026-01-01"}).SearchQuery(); err == nil {
		t.Fatal("重复 created 限定词不能放宽滚动窗口")
	}
}
