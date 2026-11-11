package metrics

import (
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/storage"
)

const (
	Exact        = "exact"
	Estimated    = "estimated"
	Insufficient = "insufficient"
	Hour         = int64(time.Hour / time.Millisecond)
	Day          = 24 * Hour
	minute       = int64(time.Minute / time.Millisecond)
)

type Point struct {
	At         int64
	Stars      int64
	Slot       int64
	Resolution string
}

type Window struct {
	Delta      *int64  `json:"delta"`
	Quality    string  `json:"quality"`
	StartAt    *int64  `json:"startAt"`
	EndAt      *int64  `json:"endAt"`
	Resolution *string `json:"resolution"`
}

func distance(a, b int64) int64 { return max(a-b, b-a) }

// nearest 按真实观测时间选边界，等距时优先更早的观测及采样槽。
func nearest(points []Point, target, tolerance int64) *Point {
	var best *Point
	for i := range points {
		p := &points[i]
		d := distance(p.At, target)
		if d <= tolerance && (best == nil || d < distance(best.At, target) ||
			(d == distance(best.At, target) && (p.At < best.At || (p.At == best.At && p.Slot < best.Slot)))) {
			best = p
		}
	}
	return best
}

// Between 只使用不同时间的实际观测，保留负增长及覆盖时间。
func Between(start, end *Point, from, to int64, tolerance config.BoundaryToleranceMinutes) Window {
	w := Window{Quality: Insufficient}
	if start == nil || end == nil || start.At >= end.At {
		return w
	}
	offset := max(distance(start.At, from), distance(end.At, to))
	if offset > int64(tolerance.Max)*minute {
		return w
	}
	delta := end.Stars - start.Stars
	resolution := end.Resolution
	if start.Resolution != end.Resolution {
		resolution = "mixed"
	}
	w = Window{Delta: &delta, Quality: Estimated, StartAt: &start.At, EndAt: &end.At, Resolution: &resolution}
	if offset <= int64(tolerance.Exact)*minute {
		w.Quality = Exact
	}
	return w
}

// Bucket 计算桶内首末样本的变化，单样本桶仍可提供锚点。
func Bucket(a storage.MetricAnchors, from, span int64, tolerance config.BoundaryToleranceMinutes) Window {
	return Between(&Point{At: a.OpenObservedAt, Stars: a.StarsOpen},
		&Point{At: a.CloseObservedAt, Stars: a.StarsClose}, from, from+span, tolerance)
}
