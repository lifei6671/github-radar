package metrics

import (
	"math"

	"github.com/lifei6671/github-radar/internal/consts"
)

type Calculation struct {
	RepoID              int64
	Delta1h             *int64
	Delta24h            *int64
	Delta7d             *int64
	Delta30d            *int64
	RelativeGrowth      *float64
	Acceleration        *float64
	PersistenceScore    *float64
	DailyConcentration  *float64
	TrendingFrequency   int
	TrendingStreak      int
	TrendingRank        *int
	TrendingScore       *float64
	HotnessScore        *float64
	CandidateBurst      bool
	Breakout            *Breakout
	DataQuality         string
	CalculatedAt        int64
	Windows             map[string]Window
	ObservationDays     []Window
	ObservationQuiet    bool
	ObservationGrowth   Window
	ObservationBreakout *Breakout
}

func ptr(value float64) *float64 { return &value }

func scale(value, cap float64) float64 {
	return 100 * min(1, math.Log1p(max(value, 0))/math.Log1p(cap))
}

func Summarize(cfg Policy, repo Repository, asOf int64, w map[string]Window, days []Window, signal TrendingSignal, fresh bool, start7 *Point) Calculation {
	current, week, prev := w[consts.Window24h], w[consts.Window7d], w[consts.WindowPrevious24h]
	row := Calculation{
		RepoID: repo.ID, CalculatedAt: asOf, Windows: w,
		CandidateBurst: (repo.Grade == consts.GradeB || repo.Grade == consts.GradeC) && current.Quality == Estimated && *current.Delta >= int64(cfg.HotGrowth),
		Delta1h:        w[consts.Window1h].Delta, Delta24h: current.Delta, Delta7d: week.Delta, Delta30d: w[consts.Window30d].Delta,
		TrendingFrequency: signal.Frequency, TrendingStreak: signal.Streak, TrendingRank: signal.Rank, TrendingScore: signal.Score,
		DataQuality: Insufficient,
	}
	if fresh && row.TrendingScore == nil {
		row.TrendingScore = ptr(0)
	}
	if week.Delta != nil {
		row.RelativeGrowth = ptr(float64(*week.Delta) / float64(max(start7.Stars, int64(cfg.RelativeStarFloor))))
	}
	if current.Delta != nil && prev.Delta != nil {
		row.Acceleration = ptr((float64(max(*current.Delta, 0)) + 1) / (float64(max(*prev.Delta, 0)) + 1))
	}
	var valid, positive int
	var peak, total int64
	for _, day := range days {
		if day.Quality != Exact {
			continue
		}
		valid++
		if delta := *day.Delta; delta > 0 {
			positive++
			total += delta
			peak = max(peak, delta)
		}
	}
	if valid >= cfg.MinPersistenceDays {
		row.PersistenceScore = ptr(100 * float64(positive) / float64(valid))
	}
	if total > 0 {
		row.DailyConcentration = ptr(float64(peak) / float64(total))
	}
	if current.Quality != Exact {
		return row
	}
	features := [5]*float64{ptr(scale(float64(*current.Delta), float64(cfg.Velocity24Cap))), nil, nil, row.PersistenceScore, row.TrendingScore}
	if week.Quality == Exact {
		features[1] = ptr(scale(float64(*week.Delta), float64(cfg.Velocity7Cap)))
	}
	if prev.Quality == Exact {
		features[2] = ptr(scale(*row.Acceleration-1, float64(cfg.AccelerationCap)))
	}
	var score, knownWeight float64
	var known int
	for i, value := range features {
		if value != nil {
			score += *value * cfg.Weights[i]
			knownWeight += cfg.Weights[i]
			known++
		}
	}
	if known >= 3 {
		row.HotnessScore = ptr(score / knownWeight)
		row.DataQuality = Estimated
		if known == len(features) {
			row.DataQuality = Exact
		}
	}
	return row
}
