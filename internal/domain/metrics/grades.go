package metrics

import (
	"cmp"
	"slices"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

var gradeNames = []string{consts.GradeS, consts.GradeA, consts.GradeB, consts.GradeC}

type Repository struct {
	ID              int64
	Grade           string
	State           string
	Stars           int
	Language        string
	Followed        bool
	LastSuccessAt   *int64
	LastSignalAt    int64
	PausedAt        int64
	BelowGradeSince *int64
	FirstNotFoundAt *int64
	NextFetchAt     int64
}

type GradeChange struct {
	RepoID          int64
	Grade           string
	BelowGradeSince *int64
	NextFetchAt     *int64
	Breakout        *Breakout
	State           string
	Reason          string
	Evidence        ObservationEvidence
}

func AllocateGrades(cfg Policy, capacity [3]int, repos []Repository, rows []Calculation, asOf int64) []GradeChange {
	exact := func(i int) bool { return rows[i].Windows[consts.Window24h].Quality == Exact }
	var changes []GradeChange
	order := make([]int, len(repos))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		if n := cmp.Compare(exactInt(exact(b)), exactInt(exact(a))); n != 0 {
			return n
		}
		if n := cmp.Compare(growth(rows[b].Delta24h), growth(rows[a].Delta24h)); n != 0 {
			return n
		}
		return cmp.Compare(repos[a].ID, repos[b].ID)
	})
	type allocation struct {
		index, desired int
		since          *int64
	}
	list := make([]allocation, 0, len(repos))
	delays := []int{cfg.HotDemotionHours, cfg.ActiveDemotionHours, cfg.NormalDemotionHours}
	for rank, i := range order {
		repo, row := repos[i], rows[i]
		current := slices.Index(gradeNames, repo.Grade)
		desired := 3
		delta := growth(row.Delta24h)
		switch {
		case row.Breakout != nil || exact(i) && (delta >= int64(cfg.HotGrowth) || (rank < cfg.TopGrowthRepos && delta > 0)):
			desired = 0
		case exact(i) && delta >= int64(cfg.ActiveGrowth), row.CandidateBurst:
			desired = 1
		case delta > 0 || growth(row.Delta7d) > 0:
			desired = 2
		}
		var since *int64
		// S/A 的降档以可确认的 24h 证据为准；空窗打断连续低于阈值的计时。
		if desired > current && current < 3 {
			if !exact(i) && (current < 2 || row.Delta7d == nil) {
				desired = current
			} else {
				since = repo.BelowGradeSince
				if since == nil {
					since = &asOf
				}
				if asOf-*since < int64(time.Duration(delays[current])*time.Hour/time.Millisecond) {
					desired = current
				} else {
					desired = current + 1
				}
			}
		}
		list = append(list, allocation{i, desired, since})
	}
	// 容量不足时先保留高热度仓库，同分比较精确增长和稳定 ID。
	slices.SortStableFunc(list, func(a, b allocation) int {
		if n := cmp.Compare(a.desired, b.desired); n != 0 {
			return n
		}
		if n := cmp.Compare(exactInt(rows[b.index].Breakout != nil), exactInt(rows[a.index].Breakout != nil)); n != 0 {
			return n
		}
		return cmp.Compare(score(rows[b.index].HotnessScore), score(rows[a.index].HotnessScore))
	})
	for _, item := range list {
		repo := repos[item.index]
		level := item.desired
		for level < 3 && capacity[level] == 0 {
			level++
		}
		if level < 3 {
			capacity[level]--
		}
		grade := gradeNames[level]
		if grade != repo.Grade {
			item.since = nil
		}
		if grade == repo.Grade && sameTime(item.since, repo.BelowGradeSince) {
			continue
		}
		change := GradeChange{RepoID: repo.ID, Grade: grade, BelowGradeSince: item.since}
		if grade == consts.GradeS && repo.Grade != consts.GradeS {
			change.Breakout = rows[item.index].Breakout
		}
		if level < slices.Index(gradeNames, repo.Grade) && repo.FirstNotFoundAt == nil {
			next := min(asOf, repo.NextFetchAt)
			change.NextFetchAt = &next
		}
		changes = append(changes, change)
	}
	return changes
}

func sameTime(a, b *int64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
func growth(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func score(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}
func exactInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
