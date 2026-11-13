package metrics

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

const (
	ObservationMinStars      = 20
	ObservationProbesPerHour = 20
	observationGrowth        = 15
	focusGrowth              = 50
)

// ObservationEvidence 保存真正参与本次状态转换的观测。
type ObservationEvidence struct {
	Stars          int       `json:"stars"`
	Exact24h       *int64    `json:"exact24h,omitempty"`
	WindowStartAt  *int64    `json:"windowStartAt,omitempty"`
	WindowEndAt    *int64    `json:"windowEndAt,omitempty"`
	Previous24h    *int64    `json:"previous24h,omitempty"`
	PeerMedians    []int64   `json:"peerMedians,omitempty"`
	LowDays        int       `json:"lowDays,omitempty"`
	QuietDays      int       `json:"quietDays,omitempty"`
	LastSignalAt   int64     `json:"lastSignalAt,omitempty"`
	RequestedGrade string    `json:"requestedGrade,omitempty"`
	Breakout       *Breakout `json:"breakout,omitempty"`
}

// ObservationPolicy 使用有效快照划分观察档位。来源信号只负责保留或安排复核。
type ObservationPolicy struct {
	Capacity  [4]int
	Total     int
	Intervals [4]int64
}

type observationChoice struct {
	index, level, strength int
	reason                 string
	evidence               ObservationEvidence
}

// AllocateObservation 先保留人工关注，再按爆发、精确增长、热度和稳定 ID 分配名额。
func AllocateObservation(policy ObservationPolicy, repos []Repository, rows []Calculation, asOf int64) []GradeChange {
	choices := make([]observationChoice, 0, len(repos))
	cohorts := observationCohorts(repos, rows)
	for i, repo := range repos {
		level, reason, evidence, strength := observationTarget(repo, rows[i], cohorts, asOf)
		choices = append(choices, observationChoice{i, level, strength, reason, evidence})
	}
	slices.SortFunc(choices, func(a, b observationChoice) int {
		left, right := repos[a.index], repos[b.index]
		if n := cmp.Compare(exactInt(right.Followed), exactInt(left.Followed)); n != 0 {
			return n
		}
		if n := cmp.Compare(a.level, b.level); n != 0 {
			return n
		}
		if n := cmp.Compare(b.strength, a.strength); n != 0 {
			return n
		}
		if n := cmp.Compare(growth(b.evidence.Exact24h), growth(a.evidence.Exact24h)); n != 0 {
			return n
		}
		if n := cmp.Compare(score(rows[b.index].HotnessScore), score(rows[a.index].HotnessScore)); n != 0 {
			return n
		}
		if n := cmp.Compare(right.LastSignalAt, left.LastSignalAt); n != 0 {
			return n
		}
		if n := cmp.Compare(right.Stars, left.Stars); n != 0 {
			return n
		}
		return cmp.Compare(left.ID, right.ID)
	})

	var changes []GradeChange
	for _, item := range choices {
		repo := repos[item.index]
		level, reason, evidence := item.level, item.reason, item.evidence
		if level < len(gradeNames) {
			evidence.RequestedGrade = gradeNames[level]
			if policy.Total == 0 {
				level = len(gradeNames)
			}
			for level < len(gradeNames) && policy.Capacity[level] <= 0 {
				level++
			}
			// 高档空位也可承接人工重点，保证人工优先级和总容量同时生效。
			if level == len(gradeNames) && repo.Followed && policy.Total > 0 {
				for i := item.level - 1; i >= 0; i-- {
					if policy.Capacity[i] > 0 {
						level = i
						break
					}
				}
			}
			if level != item.level {
				reason = "capacity"
			}
		}
		state, grade := repo.State, repo.Grade
		var next *int64
		if level < len(gradeNames) {
			policy.Total--
			policy.Capacity[level]--
			state, grade = consts.StatusActive, gradeNames[level]
			if state != repo.State || level < slices.Index(gradeNames, repo.Grade) {
				next = new(min(asOf, repo.NextFetchAt))
			} else if grade != repo.Grade && repo.LastSuccessAt != nil {
				next = new(max(asOf, *repo.LastSuccessAt+policy.Intervals[level]))
			}
			if repo.FirstNotFoundAt != nil {
				next = nil
			}
		} else if state == consts.StatusActive {
			state = consts.StatusPaused
		}
		if state == repo.State && grade == repo.Grade {
			continue
		}
		change := GradeChange{RepoID: repo.ID, State: state, Grade: grade, Reason: reason, Evidence: evidence, NextFetchAt: next}
		if level == 0 {
			change.Breakout = evidence.Breakout
		}
		changes = append(changes, change)
	}
	return changes
}

func observationTarget(repo Repository, row Calculation, cohorts map[observationCohort][]int64, asOf int64) (int, string, ObservationEvidence, int) {
	signal := row.Breakout
	if signal == nil {
		signal = row.ObservationBreakout
	}
	evidence := ObservationEvidence{Stars: repo.Stars, LastSignalAt: repo.LastSignalAt, Breakout: signal}
	window := row.Windows[consts.Window24h]
	if window.Quality != Exact || signal != nil && signal == row.ObservationBreakout {
		window = row.ObservationGrowth
	}
	if window.Quality == Exact {
		evidence.Exact24h = window.Delta
		evidence.WindowStartAt, evidence.WindowEndAt = window.StartAt, window.EndAt
	}
	delta := growth(evidence.Exact24h)
	current := slices.Index(gradeNames, repo.Grade)
	if signal != nil && repo.FirstNotFoundAt == nil && (repo.State != consts.StatusPaused || repo.PausedAt == 0 || signal.At > repo.PausedAt) {
		strength := 3
		if signal.Absolute {
			strength = 4
		}
		return 0, "breakout", evidence, strength
	}
	if repo.Followed {
		return 1, "manual_follow", evidence, 2
	}
	if repo.State != consts.StatusActive {
		if repo.State == consts.StatusCandidate && repo.Stars >= ObservationMinStars && delta >= observationGrowth {
			if delta >= focusGrowth {
				return 1, "exact_growth_50", evidence, 2
			}
			return 2, "exact_growth_15", evidence, 2
		}
		// 已暂停项目需新的爆发证据，或由管理员重点监控。
		return len(gradeNames), "", evidence, 0
	}
	if current <= 1 {
		if current == 1 && delta >= focusGrowth {
			return 1, "exact_growth_50", evidence, 2
		}
		previous := row.Windows[consts.WindowPrevious24h]
		threshold := int64(focusGrowth)
		if current == 0 {
			threshold = BreakoutMinimum
		}
		if evidence.Exact24h != nil && previous.Quality == Exact && previous.Delta != nil &&
			delta < threshold && *previous.Delta < threshold {
			evidence.LowDays = 2
			evidence.Previous24h = previous.Delta
			if delta >= focusGrowth || *previous.Delta >= focusGrowth {
				return 1, "slowed_48h", evidence, 2
			}
			return 2, "slowed_48h", evidence, 1
		}
		// S 档缺少完整的相邻窗口时保留档位，较强的精确证据在容量排序中优先。
		return max(0, current), "retained", evidence, 0
	}
	if delta >= focusGrowth {
		return 1, "exact_growth_50", evidence, 2
	}
	if delta >= observationGrowth {
		return 2, "exact_growth_15", evidence, 2
	}
	if row.ObservationQuiet && repo.LastSignalAt <= asOf-14*Day {
		evidence.QuietDays = 14
		return len(gradeNames), "quiet_14d", evidence, 0
	}
	if current == 2 {
		if medians := observationMedians(repo, row, cohorts); len(medians) == 7 {
			evidence.LowDays, evidence.PeerMedians = 7, medians
			return 3, "below_peers_7d", evidence, 1
		}
	}
	return max(2, current), "retained", evidence, exactInt(evidence.Exact24h != nil)
}

// 同语言和量级的项目按日比较，至少要有五个其他项目的精确样本。
type observationCohort struct {
	language string
	band     int
	day      int64
}

func cohortKey(repo Repository, window Window) observationCohort {
	band := 0
	for stars := repo.Stars; stars >= 100; stars /= 10 {
		band++
	}
	// 以窗口中点的 UTC 日期归组，避免迟一日的样本与隔日项目直接比较。
	day := (*window.EndAt - Day/2) / Day
	return observationCohort{strings.ToLower(repo.Language), band, day}
}

func observationCohorts(repos []Repository, rows []Calculation) map[observationCohort][]int64 {
	result := make(map[observationCohort][]int64)
	for i, repo := range repos {
		if repo.State != consts.StatusActive {
			continue
		}
		for _, window := range rows[i].ObservationDays {
			if window.Quality == Exact && window.Delta != nil && window.EndAt != nil {
				key := cohortKey(repo, window)
				result[key] = append(result[key], max(0, *window.Delta))
			}
		}
	}
	for _, values := range result {
		slices.Sort(values)
	}
	return result
}

func observationMedians(repo Repository, row Calculation, cohorts map[observationCohort][]int64) []int64 {
	if len(row.ObservationDays) != 7 {
		return nil
	}
	medians := make([]int64, 0, 7)
	for _, window := range row.ObservationDays {
		if window.Quality != Exact || window.Delta == nil || window.EndAt == nil {
			return nil
		}
		peers := cohorts[cohortKey(repo, window)]
		if len(peers) < 6 {
			return nil
		}
		value := max(0, *window.Delta)
		// 本项目低于这个位置时，去掉它后，该位置对应其余项目中位数的下界。
		if value >= peers[len(peers)/2] {
			return nil
		}
		medians = append(medians, peers[len(peers)/2])
	}
	return medians
}

// QuietObservation 使用真实采样时间校验连续覆盖。冷却档按 72 小时采样，也能积累 14 天证据。
func QuietObservation(points []Point, asOf, interval int64) bool {
	tolerance := 6 * time.Hour.Milliseconds()
	// 观测跨度最多三天，配置更稀疏时不会凭少量点判断连续低增长。
	maxGap := min(interval, 3*Day) + tolerance
	if len(points) < 2 || asOf-points[0].At > maxGap {
		return false
	}
	last := points[0]
	for _, point := range points[1:] {
		if last.At-point.At > maxGap || last.Stars > point.Stars {
			return false
		}
		if points[0].At-point.At >= 14*Day {
			return true
		}
		last = point
	}
	return false
}

// ObservationPeerDays 按本项目最近观测的时刻对齐日窗，避免每日采样因时区或调度偏移全部落空。
func ObservationPeerDays(points []Point, asOf, interval int64, tolerance BoundaryToleranceMinutes) []Window {
	if len(points) == 0 || asOf-points[0].At > min(interval, Day)+6*Hour {
		return nil
	}
	days := make([]Window, 7)
	anchor := points[0].At
	limit := int64(tolerance.Exact) * minute
	end := &points[0]
	for i := range days {
		to := anchor - int64(i)*Day
		start := nearest(points, to-Day, limit)
		days[i] = Between(start, end, to-Day, to, tolerance)
		if days[i].Quality == Exact {
			// 保留标准化边界供同日对照，实际采样只用于核验精确性。
			days[i].EndAt = new(to)
		}
		end = start
	}
	return days
}

// ObservationProbeGrowth 以实际复采时刻计算准入用的精确日窗，避免后台计算时刻错过证据。
func ObservationProbeGrowth(points []Point, asOf int64, tolerance BoundaryToleranceMinutes) (Window, *Breakout) {
	if len(points) == 0 || asOf-points[0].At >= Day {
		return Window{}, nil
	}
	anchor := points[0].At
	limit := int64(tolerance.Exact) * minute
	start := nearest(points, anchor-Day, limit)
	current := Between(start, &points[0], anchor-Day, anchor, tolerance)
	if current.Quality != Exact || *current.Delta < BreakoutMinimum {
		return current, nil
	}
	var previous [7]*int64
	end := start
	for i := range previous {
		to := anchor - int64(i+1)*Day
		start := nearest(points, to-Day, limit)
		if w := Between(start, end, to-Day, to, tolerance); w.Quality == Exact {
			previous[i] = w.Delta
		}
		end = start
	}
	signal := EvaluateBreakout(BreakoutLocal, *current.Delta, anchor, previous[:])
	if signal != nil {
		signal.StartAt = *current.StartAt
	}
	return current, signal
}
