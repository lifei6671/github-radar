package reports

import (
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
)

func TestPublicationRequiresEndedPeriodAndDistinctObservationDays(t *testing.T) {
	for _, test := range []struct {
		kind     Kind
		key      string
		cutoff   string
		required int
	}{
		{Daily, "2026-11-12", "2026-11-13", 1},
		{Weekly, "2026-W46", "2026-11-16", 5},
		{Monthly, "2026-11", "2026-12-01", 23},
		{Monthly, "2026-12", "2027-01-01", 24},
		{Monthly, "2028-02", "2028-03-01", 22},
		{Monthly, "2027-02", "2027-03-01", 21},
	} {
		want, _ := time.Parse(time.DateOnly, test.cutoff) // 00:00 UTC，即北京时间 08:00。
		cutoff, required := test.kind.PublicationPolicy(test.key, config.DefaultReportPublication())
		if !cutoff.Equal(want) || required != test.required {
			t.Fatalf("%s 规则不符: %v %d", test.key, cutoff, required)
		}
		if test.kind.PublicationStatus(test.key, required, cutoff.Add(-time.Second), config.DefaultReportPublication()) != Preview ||
			test.kind.PublicationStatus(test.key, required-1, cutoff.AddDate(0, 0, 5), config.DefaultReportPublication()) != Preview ||
			test.kind.PublicationStatus(test.key, required, cutoff, config.DefaultReportPublication()) != Published {
			t.Fatalf("%s 正式出刊必须同时满足时间和覆盖", test.key)
		}
		if test.kind.LatestDueKey(cutoff) != test.key || test.kind.LatestDueKey(cutoff.Add(-time.Second)) >= test.key {
			t.Fatalf("%s 不能在收口之前进入组稿: %s", test.kind, test.key)
		}
	}
}
