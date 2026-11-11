package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/lifecycle"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/trending"
)

// GitHubSource 让必要的采集回归可以使用本地 HTTP 固定响应。
type GitHubSource interface {
	Repository(context.Context, string, string, string, gh.Policy) (gh.RepositoryResult, error)
	RepositoryByID(context.Context, int64, gh.Policy) (gh.RepositoryResult, error)
	Search(context.Context, string, int) (gh.SearchPage, error)
}

type TrendingSource interface {
	Fetch(context.Context, string, string) ([]trending.Entry, error)
}

type Engine struct {
	Queue    Queue
	GitHub   GitHubSource
	Trending TrendingSource
	Cfg      config.Config
	Logger   *slog.Logger
}

// Run 仅在空库初始化完成后启动业务；退出时取消并等待全部 Worker。
func (e *Engine) Run(ctx context.Context) error {
	for {
		initialized, err := storage.Initialized(ctx, e.Queue.DB)
		if err != nil && !storage.IsBusy(err) {
			return err
		}
		if initialized {
			break
		}
		if err := wait(ctx, time.Second); err != nil {
			return err
		}
	}
	for {
		err := e.scan(ctx, time.Now())
		if !storage.IsBusy(err) {
			if err != nil {
				return err
			}
			break
		}
		if err := wait(ctx, time.Second); err != nil {
			return err
		}
	}
	group, workerCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		ticker := time.NewTicker(time.Duration(e.Cfg.Scheduler.TickMinutes) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return workerCtx.Err()
			case now := <-ticker.C:
				if err := e.scan(workerCtx, now); err != nil && !storage.IsBusy(err) {
					return err
				}
			}
		}
	})
	for range e.Cfg.GitHub.WorkerConcurrency {
		group.Go(func() error {
			for {
				processed, err := e.Step(workerCtx)
				if err != nil && !storage.IsBusy(err) {
					return err
				}
				if !processed || err != nil {
					if err := wait(workerCtx, time.Second); err != nil {
						return err
					}
				}
			}
		})
	}
	return group.Wait()
}

func (e *Engine) scan(ctx context.Context, now time.Time) error {
	if err := e.Queue.Rebalance(ctx, now); err != nil {
		return err
	}
	return e.Queue.Schedule(ctx, now)
}

// CollectOnce 最多处理一百个当前可领取任务，限流任务留在 SQLite 等待后续运行。
func (e *Engine) CollectOnce(ctx context.Context) error {
	initialized, err := storage.Initialized(ctx, e.Queue.DB)
	if err != nil {
		return err
	}
	if !initialized {
		return errors.New("请先通过已认证的 POST /api/v1/setup/initialize 初始化空库")
	}
	if err := e.scan(ctx, time.Now()); err != nil {
		return err
	}
	count := 0
	for count < 100 {
		processed, err := e.Step(ctx)
		if err != nil {
			return err
		}
		if !processed {
			break
		}
		count++
	}
	e.Logger.Info("单轮采集完成", "processedJobs", count)
	return nil
}

// Step 执行一个任务，租约续期失败会立即取消上游请求。
func (e *Engine) Step(ctx context.Context) (bool, error) {
	job, err := e.Queue.Claim(ctx, time.Now())
	if err != nil || job == nil {
		return false, err
	}
	settled := false
	defer func() {
		if !settled {
			if final, release, draining := lifecycle.Settlement(ctx); draining {
				defer release()
				lifecycle.RecordSettlement(ctx, storage.InterruptJob(final, e.Queue.DB, *job))
			}
		}
	}()
	taskCtx, cancel := context.WithCancel(ctx)
	renewed := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Duration(e.Cfg.Scheduler.LeaseSeconds) * time.Second / 3)
		defer ticker.Stop()
		for {
			select {
			case <-taskCtx.Done():
				renewed <- nil
				return
			case now := <-ticker.C:
				if err := e.Queue.Renew(taskCtx, *job, now); err != nil {
					cancel()
					renewed <- err
					return
				}
			}
		}
	}()
	apply, operationErr := e.execute(taskCtx, *job)
	if operationErr == nil {
		operationErr = e.Queue.Complete(taskCtx, *job, apply)
		settled = operationErr == nil
	}
	cancel()
	renewErr := <-renewed
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if renewErr != nil {
		if errors.Is(renewErr, ErrLeaseLost) {
			return true, nil
		}
		return true, renewErr
	}
	if errors.Is(operationErr, ErrLeaseLost) {
		return true, nil
	}
	if operationErr != nil {
		e.Logger.Warn("采集任务失败", "jobId", job.ID, "type", job.Type, "error", operationErr)
		err := e.Queue.Fail(ctx, *job, operationErr, time.Now())
		settled = err == nil
		if errors.Is(err, ErrLeaseLost) {
			return true, nil
		}
		return true, err
	}
	if job.Type != consts.JobRefreshRepo {
		// 新发现的仓库立即安排首采，无需等待下一个五分钟扫描。
		return true, e.Queue.Schedule(ctx, time.Now())
	}
	return true, nil
}

func (e *Engine) execute(ctx context.Context, job storage.Job) (func(*gorm.DB) error, error) {
	var payload Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("任务 payload 无效: %w", err)
	}
	switch job.Type {
	case consts.JobRefreshRepo:
		return e.refresh(ctx, job, payload)
	case consts.JobDiscoverSearch:
		return e.search(ctx, job, payload)
	case consts.JobDiscoverTrending:
		return e.discoverTrending(ctx, payload)
	default:
		return nil, errors.New("未支持的采集任务")
	}
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
