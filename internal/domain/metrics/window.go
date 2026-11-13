package metrics

import (
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

const (
	Exact        = consts.QualityExact
	Estimated    = consts.QualityEstimated
	Insufficient = consts.QualityInsufficient
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
func Between(start, end *Point, from, to int64, tolerance BoundaryToleranceMinutes) Window {
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
		resolution = consts.ResolutionMixed
	}
	w = Window{Delta: &delta, Quality: Estimated, StartAt: &start.At, EndAt: &end.At, Resolution: &resolution}
	if offset <= int64(tolerance.Exact)*minute {
		w.Quality = Exact
	}
	return w
}

type Anchors struct {
	Raw, Hourly map[int64][]Point
}

func (a Anchors) Pick(id, target int64, tolerance BoundaryToleranceMinutes, hourlyOnly bool) *Point {
	limit := int64(tolerance.Max) * minute
	if !hourlyOnly {
		if p := nearest(a.Raw[id], target, limit); p != nil {
			return p
		}
	}
	return nearest(a.Hourly[id], target, limit)
}
