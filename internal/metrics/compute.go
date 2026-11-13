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
}

type Transaction interface {
	ComputeMarker() (slot string, updatedAt int64, found bool, err error)
	ActiveRepositories() ([]domain.Repository, error)
	LoadAnchors(target, limit, asOf int64, hourlyOnly bool) (domain.Anchors, error)
	LoadTrending(asOf int64) ([]domain.TrendingBatch, error)
	Publish(slot string, asOf int64, rows []domain.Calculation, grades []domain.GradeChange) error
}

// Compute 原子发布同一 asOf 的全量指标和等级；恢复时只计算当前窗口。
func Compute(ctx context.Context, store Store, cfg config.Config, now time.Time, force bool) error {
	asOf := now.UnixMilli()
	slot := strconv.FormatInt(now.UTC().Truncate(time.Duration(cfg.Scheduler.MetricsIntervalMinutes)*time.Minute).UnixMilli(), 10)
	return store.Transaction(ctx, func(tx Transaction) error {
		lastSlot, updatedAt, found, err := tx.ComputeMarker()
		if err != nil {
			return err
		}
		if found && (updatedAt > asOf || (!force && lastSlot == slot)) {
			return nil
		}
		repos, err := tx.ActiveRepositories()
		if err != nil {
			return err
		}
		rows, err := calculate(tx, cfg.Metrics, repos, now)
		if err != nil {
			return err
		}
		capacity := [3]int{cfg.Scheduler.MaxHot, cfg.Scheduler.MaxActive, cfg.Scheduler.MaxNormal}
		grades := domain.AllocateGrades(cfg.Metrics, capacity, repos, rows, asOf)
		return tx.Publish(slot, asOf, rows, grades)
	})
}

type windowSpec struct {
	key        string
	span       int64
	tolerance  domain.BoundaryToleranceMinutes
	hourlyOnly bool
}

func calculate(tx Transaction, cfg domain.Policy, repos []domain.Repository, now time.Time) ([]domain.Calculation, error) {
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
		return tx.LoadAnchors(target, int64(tolerance.Max)*time.Minute.Milliseconds(), asOf, hourlyOnly)
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
		rows = append(rows, domain.Summarize(cfg, repo, asOf, w, dayWindows, trending[id], fresh, start7))
	}
	return rows, nil
}
