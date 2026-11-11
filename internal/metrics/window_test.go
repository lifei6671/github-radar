package metrics

import (
	"testing"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

func TestBoundaryCoverage(t *testing.T) {
	tolerance := config.DefaultMetrics().Hour
	from, to := 2*Hour, 3*Hour
	points := []Point{
		{At: from + 5*minute, Stars: 120, Slot: 2, Resolution: "raw"},
		{At: from - 5*minute, Stars: 100, Slot: 1, Resolution: "raw"},
	}
	start := nearest(points, from, Hour)
	if start == nil || start.Stars != 100 {
		t.Fatal("等距应选较早端点")
	}
	end := Point{At: to - 20*minute, Stars: 80, Resolution: "hourly"}
	w := Between(start, &end, from, to, tolerance)
	if w.Delta == nil || *w.Delta != -20 || w.Quality != Estimated || *w.Resolution != "mixed" {
		t.Fatalf("估算负增量或实际粒度错误: %+v", w)
	}
	end.At = to - 31*minute
	w = Between(start, &end, from, to, tolerance)
	if w.Delta != nil || w.Quality != Insufficient {
		t.Fatalf("超出容差不可推算: %+v", w)
	}
	w = Bucket(storage.MetricAnchors{OpenObservedAt: from + minute, CloseObservedAt: from + minute, SampleCount: 1}, from, Hour, tolerance)
	if w.Delta != nil || w.Quality != Insufficient {
		t.Fatalf("单点桶不能计算零增长: %+v", w)
	}
}
