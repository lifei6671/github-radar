package lifecycle

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Drain 作为取消原因传递截止时间，并收集 Worker 的租约收尾失败。
type Drain struct {
	deadline time.Time
	mu       sync.Mutex
	err      error
}

func (*Drain) Error() string { return "恢复前暂停业务任务" }

// Settlement 使用独立且受排空截止时间限制的上下文完成任务状态落库。
func Settlement(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	var drain *Drain
	if !errors.As(context.Cause(ctx), &drain) {
		return nil, nil, false
	}
	done, cancel := context.WithDeadline(context.WithoutCancel(ctx), drain.deadline)
	return done, cancel, true
}

func RecordSettlement(ctx context.Context, err error) {
	var drain *Drain
	if err != nil && errors.As(context.Cause(ctx), &drain) {
		drain.mu.Lock()
		drain.err = errors.Join(drain.err, err)
		drain.mu.Unlock()
	}
}

type cycle struct {
	cancel   context.CancelCauseFunc
	done     chan struct{}
	drain    *Drain
	stop     chan struct{}
	retiring bool
}

type stopSchedulingKey struct{}

// SchedulingDone 停止领取新任务，正在执行的任务继续使用未取消的上下文。
func SchedulingDone(ctx context.Context) <-chan struct{} {
	done, _ := ctx.Value(stopSchedulingKey{}).(<-chan struct{})
	return done
}

func Stopping(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-SchedulingDone(ctx):
		return true
	default:
		return false
	}
}

// Wait 在到达下次执行时间时返回 true，配置切换和服务退出均可唤醒空闲 Worker。
func Wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-SchedulingDone(ctx):
		return false
	case <-timer.C:
		return !Stopping(ctx)
	}
}

// Workers 切换后台任务组，前一组退出后才会启动下一组。
type Workers struct {
	root     context.Context
	run      func(context.Context) error
	mu       sync.Mutex
	current  *cycle
	wanted   bool
	wg       sync.WaitGroup
	failures chan error
}

func NewWorkers(root context.Context, run func(context.Context) error) *Workers {
	return &Workers{root: root, run: run, failures: make(chan error, 1)}
}

func (w *Workers) Resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.wanted = true
	w.startLocked()
}

// Refresh 让当前任务组自然收尾，然后使用最新配置启动下一组。
func (w *Workers) Refresh() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if running := w.current; running != nil && !running.retiring {
		running.retiring = true
		close(running.stop)
	}
}

func (w *Workers) startLocked() {
	if !w.wanted || w.current != nil || w.root.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancelCause(w.root)
	running := &cycle{cancel: cancel, done: make(chan struct{}), stop: make(chan struct{})}
	ctx = context.WithValue(ctx, stopSchedulingKey{}, (<-chan struct{})(running.stop))
	w.current = running
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		err := w.run(ctx)
		var draining *Drain
		paused := errors.As(context.Cause(ctx), &draining)
		cancel(nil)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.current = nil
		close(running.done)
		if paused || (running.retiring && err == nil) {
			w.startLocked()
		} else if w.root.Err() == nil && err != nil {
			w.failures <- err
		}
	}()
}

func (w *Workers) Pause(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("排空需要截止时间")
	}
	w.mu.Lock()
	w.wanted = false
	running := w.current
	if running != nil && running.drain == nil {
		running.drain = &Drain{deadline: deadline}
		running.cancel(running.drain)
	}
	w.mu.Unlock()
	if running == nil {
		return nil
	}
	select {
	case <-running.done:
		drain := running.drain
		drain.mu.Lock()
		defer drain.mu.Unlock()
		return drain.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Workers) Failure() <-chan error { return w.failures }

// Wait 在根上下文已取消后等待所有 Worker 退出。
func (w *Workers) Wait() { w.wg.Wait() }
