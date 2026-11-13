package metrics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	domain "github.com/lifei6671/github-radar/internal/domain/metrics"
)

const ComputeKey = "metrics.compute"

type Store interface {
	Transaction(context.Context, func(Transaction) error) error
	ReadTransaction(context.Context, func(Transaction) error) error
}

type Transaction interface {
	ComputeMarker() (slot string, updatedAt int64, found bool, err error)
	Repositories(asOf int64, observation bool) ([]domain.Repository, error)
	LoadAnchors(target, limit, asOf int64, hourlyOnly, observation bool) (domain.Anchors, error)
	LoadObservationPoints(from, to int64) (map[int64][]domain.Point, error)
	LoadTrending(asOf int64) ([]domain.TrendingBatch, error)
	Publish(slot string, asOf int64, rows []domain.Calculation, grades []domain.GradeChange) error
}

// ObservationReplay 以同一份实测数据比较当前配置与 P0.4 试运行规则，期间不发布指标或变更。
func ObservationReplay(ctx context.Context, store Store, cfg config.Config, now time.Time) (map[string]any, error) {
	var result map[string]any
	err := store.ReadTransaction(ctx, func(tx Transaction) error {
		asOf := now.UnixMilli()
		repos, err := tx.Repositories(asOf, true)
		if err != nil {
			return err
		}
		trial := cfg
		trial.Scheduler = cfg.Scheduler.ObservationTrial()
		rows, err := calculate(tx, trial, repos, now)
		if err != nil {
			return err
		}
		count := func(repos []domain.Repository, changes []domain.GradeChange) map[string]int {
			index := make(map[int64]domain.GradeChange, len(changes))
			for _, change := range changes {
				index[change.RepoID] = change
			}
			totals := map[string]int{}
			for _, repo := range repos {
				state, grade := repo.State, repo.Grade
				if change, ok := index[repo.ID]; ok {
					grade = change.Grade
					if change.State != "" {
						state = change.State
					}
				}
				if state == consts.StatusActive {
					totals[grade]++
				} else {
					totals[state]++
				}
			}
			return totals
		}
		var oldRepos []domain.Repository
		var oldRows []domain.Calculation
		for i, repo := range repos {
			if repo.State == consts.StatusActive {
				oldRepos, oldRows = append(oldRepos, repo), append(oldRows, rows[i])
			}
		}
		legacy := cfg
		legacy.Scheduler.ObservationPool = false
		changes := allocate(trial, repos, rows, asOf)
		reasons := make(map[string]int)
		for _, change := range changes {
			reasons[change.Reason]++
		}
		result = map[string]any{"asOf": now.UTC(), "legacyActiveGrades": count(oldRepos, allocate(legacy, oldRepos, oldRows, asOf)),
			"trial": count(repos, changes), "trialChangesByReason": reasons}
		return nil
	})
	return result, err
}

// Compute 原子发布同一 asOf 的全量指标和等级；恢复时只计算当前窗口。
func Compute(ctx context.Context, store Store, cfg config.Config, now time.Time, force bool) error {
	asOf := now.UnixMilli()
	slot := strconv.FormatInt(now.UTC().Truncate(time.Duration(cfg.Scheduler.MetricsIntervalMinutes)*time.Minute).UnixMilli(), 10)
	// 同一时槽内切换规则或容量时也要立刻按新配置分配。
	if cfg.Scheduler.ObservationPool {
		s := cfg.Scheduler
		slot = fmt.Sprintf("%s/observation/%d/%v/%d/%d/%d/%d", slot, s.MaxMonitoredRepos, s.ObservationCapacities(),
			s.IntervalsMinutes[consts.GradeS], s.IntervalsMinutes[consts.GradeA], s.IntervalsMinutes[consts.GradeB], s.IntervalsMinutes[consts.GradeC])
	}
	return store.Transaction(ctx, func(tx Transaction) error {
		lastSlot, updatedAt, found, err := tx.ComputeMarker()
		if err != nil {
			return err
		}
		if found && (updatedAt > asOf || (!force && lastSlot == slot)) {
			return nil
		}
		repos, err := tx.Repositories(asOf, cfg.Scheduler.ObservationPool)
		if err != nil {
			return err
		}
		rows, err := calculate(tx, cfg, repos, now)
		if err != nil {
			return err
		}
		grades := allocate(cfg, repos, rows, asOf)
		return tx.Publish(slot, asOf, rows, grades)
	})
}

func allocate(cfg config.Config, repos []domain.Repository, rows []domain.Calculation, asOf int64) []domain.GradeChange {
	s := cfg.Scheduler
	if !s.ObservationPool {
		return domain.AllocateGrades(cfg.Metrics, [3]int{s.MaxHot, s.MaxActive, s.MaxNormal}, repos, rows, asOf)
	}
	var intervals [4]int64
	for i, grade := range []string{consts.GradeS, consts.GradeA, consts.GradeB, consts.GradeC} {
		intervals[i] = s.Interval(grade).Milliseconds()
	}
	return domain.AllocateObservation(domain.ObservationPolicy{
		Capacity: s.ObservationCapacities(), Total: s.MaxMonitoredRepos, Intervals: intervals,
	}, repos, rows, asOf)
}

type windowSpec struct {
	key        string
	span       int64
	tolerance  domain.BoundaryToleranceMinutes
	hourlyOnly bool
}

func calculate(tx Transaction, app config.Config, repos []domain.Repository, now time.Time) ([]domain.Calculation, error) {
	cfg := app.Metrics
	rows := make([]domain.Calculation, 0, len(repos))
	if len(repos) == 0 {
		return rows, nil
	}
	asOf := now.UnixMilli()
	windows := []windowSpec{
		{consts.Window1h, domain.Hour, cfg.Hour, false}, {consts.Window24h, domain.Day, cfg.Day, false}, {consts.Window7d, 7 * domain.Day, cfg.Week, false},
		{consts.Window30d, 30 * domain.Day, cfg.Month, true}, {consts.WindowPrevious24h, 2 * domain.Day, cfg.Day, false},
	}
	load := func(target int64, tolerance domain.BoundaryToleranceMinutes, hourlyOnly bool) (domain.Anchors, error) {
		return tx.LoadAnchors(target, int64(tolerance.Max)*time.Minute.Milliseconds(), asOf, hourlyOnly, app.Scheduler.ObservationPool)
	}
	starts := make([]domain.Anchors, len(windows))
	for i, w := range windows {
		a, err := load(asOf-w.span, w.tolerance, w.hourlyOnly)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 采样边界: %w", w.key, err)
		}
		starts[i] = a
	}
	end, err := load(asOf, domain.BoundaryToleranceMinutes{Max: max(cfg.Hour.Max, cfg.Day.Max, cfg.Week.Max, cfg.Month.Max)}, false)
	if err != nil {
		return nil, err
	}
	// 爆发基线锚定滚动 24 小时窗口的起点，使用此前七个不重叠的独立日窗。
	var breakoutDays [8]domain.Anchors
	breakoutCandidate := make(map[int64]bool)
	for _, repo := range repos {
		from := asOf - domain.Day
		window := domain.Between(starts[1].Pick(repo.ID, from, cfg.Day, false), end.Pick(repo.ID, asOf, cfg.Day, false), from, asOf, cfg.Day)
		if window.Quality == domain.Exact && *window.Delta >= domain.BreakoutMinimum {
			breakoutCandidate[repo.ID] = true
		}
	}
	if len(breakoutCandidate) > 0 {
		breakoutDays[0], breakoutDays[1] = starts[1], starts[4]
		for i := 2; i < len(breakoutDays); i++ {
			breakoutDays[i], err = load(asOf-int64(i+1)*domain.Day, cfg.Day, false)
			if err != nil {
				return nil, err
			}
		}
	}
	midnight := now.UTC().Truncate(24 * time.Hour).UnixMilli()
	days := make([]domain.Anchors, 8)
	for i := range days {
		a, err := load(midnight-int64(i)*domain.Day, cfg.Day, false)
		if err != nil {
			return nil, err
		}
		days[i] = a
	}
	batches, err := tx.LoadTrending(asOf)
	if err != nil {
		return nil, err
	}
	trending, fresh := domain.TrendingSignals(asOf, batches)
	var observation map[int64][]domain.Point
	if app.Scheduler.ObservationPool {
		// 额外保留一个冷却间隔，用最早的合格锚点证明完整的 14 天。
		observation, err = tx.LoadObservationPoints(asOf-21*domain.Day, asOf)
		if err != nil {
			return nil, err
		}
	}
	for _, repo := range repos {
		id := repo.ID
		w := make(map[string]domain.Window, len(windows))
		var start7 *domain.Point
		for i, spec := range windows {
			from, to := asOf-spec.span, asOf
			start := starts[i].Pick(id, from, spec.tolerance, spec.hourlyOnly)
			finish := end.Pick(id, to, spec.tolerance, false)
			if spec.key == consts.WindowPrevious24h {
				to -= domain.Day
				finish = starts[1].Pick(id, to, spec.tolerance, false)
			}
			w[spec.key] = domain.Between(start, finish, from, to, spec.tolerance)
			if spec.key == consts.Window7d {
				start7 = start
			}
		}
		dayWindows := make([]domain.Window, 7)
		for i := range dayWindows {
			to := midnight - int64(i)*domain.Day
			dayWindows[i] = domain.Between(days[i+1].Pick(id, to-domain.Day, cfg.Day, false), days[i].Pick(id, to, cfg.Day, false), to-domain.Day, to, cfg.Day)
		}
		row := domain.Summarize(cfg, repo, asOf, w, dayWindows, trending[id], fresh, start7)
		if points := observation[id]; len(points) > 0 {
			interval := app.Scheduler.Interval(repo.Grade).Milliseconds()
			row.ObservationQuiet = domain.QuietObservation(points, asOf, interval)
			row.ObservationDays = domain.ObservationPeerDays(points, asOf, interval, cfg.Day)
			if repo.State != consts.StatusActive || repo.Grade == consts.GradeC {
				row.ObservationGrowth, row.ObservationBreakout = domain.ObservationProbeGrowth(points, asOf, cfg.Day)
			}
		}
		if current := w[consts.Window24h]; breakoutCandidate[id] {
			var previous [7]*int64
			for i := range previous {
				to := asOf - int64(i+1)*domain.Day
				from := to - domain.Day
				window := domain.Between(breakoutDays[i+1].Pick(id, from, cfg.Day, false), breakoutDays[i].Pick(id, to, cfg.Day, false), from, to, cfg.Day)
				if window.Quality == domain.Exact {
					previous[i] = window.Delta
				}
			}
			row.Breakout = domain.EvaluateBreakout(domain.BreakoutLocal, *current.Delta, asOf, previous[:])
			if row.Breakout != nil {
				row.Breakout.StartAt, row.Breakout.At = *current.StartAt, *current.EndAt
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
