package metrics

import "testing"

func TestBoundaryCoverage(t *testing.T) {
	tolerance := BoundaryToleranceMinutes{Exact: 10, Max: 30}
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
	only := Point{At: from + minute}
	w = Between(&only, &only, from, to, tolerance)
	if w.Delta != nil || w.Quality != Insufficient {
		t.Fatalf("单点桶不能计算零增长: %+v", w)
	}
}
