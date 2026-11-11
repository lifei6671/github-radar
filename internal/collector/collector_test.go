package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/google/go-github/v88/github"
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/config"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/trending"
	"github.com/lifei6671/github-radar/migrations"
)

type fixtureGitHub struct {
	get    func() (gh.RepositoryResult, error)
	search func(string, int) (gh.SearchPage, error)
}

func (f fixtureGitHub) Repository(context.Context, string, string, string, gh.Policy) (gh.RepositoryResult, error) {
	return f.get()
}
func (f fixtureGitHub) RepositoryByID(context.Context, int64, gh.Policy) (gh.RepositoryResult, error) {
	return f.get()
}
func (f fixtureGitHub) Search(_ context.Context, query string, page int) (gh.SearchPage, error) {
	return f.search(query, page)
}

type fixtureTrending struct {
	entries []trending.Entry
	err     error
}

func (f fixtureTrending) Fetch(context.Context, string, string) ([]trending.Entry, error) {
	return f.entries, f.err
}

func newEngine(t *testing.T) *Engine {
	t.Helper()
	return newEngineWithBusyTimeout(t, 5000)
}

func newEngineWithBusyTimeout(t *testing.T, timeout int) *Engine {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Path = filepath.Join(t.TempDir(), "collector.db")
	cfg.Storage.BusyTimeoutMS = timeout
	cfg.Discovery.TrendingEnabled, cfg.Discovery.SearchRules = false, nil
	pool, err := storage.Open(t.Context(), cfg.Storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if _, err := migrations.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (settings.Store{DB: db}).Initialize(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	return &Engine{Queue: Queue{DB: db, Cfg: cfg.Scheduler}, Cfg: cfg,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func source(id int64, name string, stars int) *sdk.Repository {
	return &sdk.Repository{
		ID: &id, Owner: &sdk.User{Login: sdk.Ptr("owner")}, Name: &name,
		FullName: sdk.Ptr("owner/" + name), HTMLURL: sdk.Ptr("https://github.com/owner/" + name),
		StargazersCount: &stars, ForksCount: sdk.Ptr(10), Topics: []string{"mcp"},
	}
}

func discover(t *testing.T, e *Engine, repo *sdk.Repository) {
	t.Helper()
	err := e.Queue.DB.Transaction(func(tx *gorm.DB) error { return e.Queue.discover(tx, repo, time.Now().UnixMilli()) })
	if err != nil {
		t.Fatal(err)
	}
}

func manual(t *testing.T, e *Engine, id int64, request string, planned time.Time) storage.Job {
	t.Helper()
	job, err := e.Queue.Refresh(t.Context(), id, request, planned)
	if err != nil {
		t.Fatal(err)
	}
	// 测试未来的计划槽时仍让任务当前可领取。
	if err := e.Queue.DB.Model(&job).Update("available_at", time.Now().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func step(t *testing.T, e *Engine) {
	t.Helper()
	if processed, err := e.Step(t.Context()); err != nil || !processed {
		t.Fatalf("任务未执行: processed=%v err=%v", processed, err)
	}
}

func repository(t *testing.T, e *Engine, id int64) storage.Repository {
	t.Helper()
	var repo storage.Repository
	if err := e.Queue.DB.Take(&repo, "repo_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestSnapshotBaseline304AndSameSlot(t *testing.T) {
	e := newEngine(t)
	original := source(1, "repo", 10000)
	discover(t, e, original)
	var samples int64
	e.Queue.DB.Model(&storage.RepoSnapshot{}).Count(&samples)
	if samples != 0 {
		t.Fatal("发现结果不能写入 Star 样本")
	}
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
		time.Sleep(2 * time.Millisecond)
		return gh.RepositoryResult{Repository: original, ETag: `"v1"`}, nil
	}}
	planned := time.Now()
	first := manual(t, e, 1, "first", planned)
	step(t, e)
	// 同一手动 requestId 跨槽重试仍返回原任务。
	repeated, err := e.Queue.Refresh(t.Context(), 1, "first", planned.Add(10*time.Minute))
	if err != nil || repeated.ID != first.ID {
		t.Fatalf("手动请求幂等失败: %+v %v", repeated, err)
	}
	original.StargazersCount = sdk.Ptr(10020)
	manual(t, e, 1, "second", planned)
	step(t, e)
	var snapshot storage.RepoSnapshot
	if err := e.Queue.DB.Take(&snapshot, "repo_id = ?", 1).Error; err != nil {
		t.Fatal(err)
	}
	if snapshot.Stars != 10000 || repository(t, e, 1).Stars != 10020 {
		t.Fatalf("同槽应保留第一条样本，元数据可以更新: sample=%+v", snapshot)
	}
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
		time.Sleep(2 * time.Millisecond)
		return gh.RepositoryResult{NotModified: true}, nil
	}}
	manual(t, e, 1, "third", planned.Add(5*time.Minute))
	step(t, e)
	var all []storage.RepoSnapshot
	if err := e.Queue.DB.Order("sample_slot").Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[1].Stars != 10020 || all[1].SampleSlot != Slot(planned.Add(5*time.Minute).UnixMilli()) {
		t.Fatalf("认证 304 应沿可靠基线采样: %+v", all)
	}
}

func TestRenameAndNameReusePreserveIdentity(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "old", 100))
	if err := e.Queue.Follow(t.Context(), 1, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	result := source(1, "new", 110)
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
		time.Sleep(2 * time.Millisecond)
		return gh.RepositoryResult{Repository: result, ETag: "new"}, nil
	}}
	manual(t, e, 1, "rename", time.Now())
	step(t, e)
	renamed := repository(t, e, 1)
	if renamed.FullName != "owner/new" || !renamed.StarredByUser || renamed.DiscoveredAt == 0 {
		t.Fatalf("改名丢失身份: %+v", renamed)
	}
	discover(t, e, source(2, "old", 1))
	// 新地址随后返回另一个数字 ID，应暂停旧 ID，保留原历史与关注。
	result = source(3, "new", 999)
	manual(t, e, 1, "reuse", time.Now())
	step(t, e)
	old := repository(t, e, 1)
	if old.State != "address_pending" || old.NameIsCurrent || !old.StarredByUser || old.Stars != 110 {
		t.Fatalf("名称复用污染旧身份: %+v", old)
	}
	if current := repository(t, e, 3); current.FullName != "owner/new" || !current.NameIsCurrent {
		t.Fatalf("新身份未正确建档: %+v", current)
	}
	var snapshots []storage.RepoSnapshot
	e.Queue.DB.Where("repo_id = ?", 1).Find(&snapshots)
	if len(snapshots) != 1 || snapshots[0].Stars != 110 {
		t.Fatalf("旧身份的采样历史发生变化: %+v", snapshots)
	}
}

func TestNotFoundConfirmationAndFailureNoSamples(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "repo", 100))
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
		return gh.RepositoryResult{}, &gh.Failure{Code: "github_not_found", Status: 404}
	}}
	manual(t, e, 1, "404-first", time.Now())
	step(t, e)
	first := repository(t, e, 1)
	if first.State != "active" || first.FirstNotFoundAt == nil || first.LastSuccessAt != nil ||
		first.NextFetchAt < time.Now().Add(59*time.Minute).UnixMilli() {
		t.Fatalf("首次 404 不应立即失效: %+v", first)
	}
	value := time.Now().Add(-time.Hour - time.Second).UnixMilli()
	e.Queue.DB.Model(&first).Update("first_not_found_at", value)
	manual(t, e, 1, "404-confirm", time.Now())
	step(t, e)
	if repository(t, e, 1).State != "unavailable" {
		t.Fatal("一小时后重复 404 应确认失效")
	}
	var count int64
	e.Queue.DB.Model(&storage.RepoSnapshot{}).Count(&count)
	if count != 0 {
		t.Fatal("失败请求不应生成样本")
	}
}

func TestAtomicClaimAndLeaseFence(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "repo", 1))
	manual(t, e, 1, "lease", time.Now())
	var group sync.WaitGroup
	results := make(chan *storage.Job, 2)
	errs := make(chan error, 2)
	for range 2 {
		group.Go(func() {
			job, err := e.Queue.Claim(t.Context(), time.Now())
			results <- job
			errs <- err
		})
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *storage.Job
	count := 0
	for job := range results {
		if job != nil {
			count++
			first = job
		}
	}
	if count != 1 {
		t.Fatalf("同一任务被并发领取 %d 次", count)
	}
	e.Queue.DB.Model(first).Update("lease_until", time.Now().Add(-time.Second).UnixMilli())
	second, err := e.Queue.Claim(t.Context(), time.Now())
	if err != nil || second == nil || second.ID != first.ID || *second.LeaseToken == *first.LeaseToken {
		t.Fatalf("过期租约未被新 token 接管: %+v %v", second, err)
	}
	wrote := false
	if err := e.Queue.Complete(t.Context(), *first, func(tx *gorm.DB) error { wrote = true; return nil }); !errors.Is(err, ErrLeaseLost) || wrote {
		t.Fatalf("旧执行者不应提交: wrote=%v err=%v", wrote, err)
	}
	if err := e.Queue.Complete(t.Context(), *second, func(tx *gorm.DB) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestTrendingFailureKeepsCompleteBatch(t *testing.T) {
	e := newEngine(t)
	e.Trending = fixtureTrending{entries: []trending.Entry{{Repository: source(1, "repo", 100), Rank: 1}}}
	p := Payload{PlannedAt: time.Now().UnixMilli(), SourceKey: "all", Period: "daily"}
	apply, err := e.discoverTrending(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.DB.Transaction(apply); err != nil {
		t.Fatal(err)
	}
	var before storage.MaintenanceState
	e.Queue.DB.Take(&before, "key = ?", BatchKey("all", "daily"))
	e.Trending = fixtureTrending{err: errors.New("页面结构变化")}
	p.PlannedAt += 300000
	if _, err := e.discoverTrending(t.Context(), p); err == nil {
		t.Fatal("抓取失败必须上报")
	}
	var after storage.MaintenanceState
	e.Queue.DB.Take(&after, "key = ?", before.Key)
	if before.Value != after.Value || before.UpdatedAt != after.UpdatedAt {
		t.Fatal("失败不能改写旧批次或伪造新鲜时间")
	}
}

func TestSearchDedupAndBoundedShards(t *testing.T) {
	e := newEngine(t)
	p := Payload{PlannedAt: time.Now().UnixMilli(), SourceKey: "mcp", RuleID: 1, Query: "topic:mcp", Page: 1}
	e.GitHub = fixtureGitHub{search: func(query string, page int) (gh.SearchPage, error) {
		return gh.SearchPage{Repositories: []*sdk.Repository{source(1, "repo", 100), source(1, "repo", 100)}, Total: 2}, nil
	}}
	job := storage.Job{Priority: 20}
	apply, err := e.search(t.Context(), job, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.DB.Transaction(apply); err != nil {
		t.Fatal(err)
	}
	var count int64
	e.Queue.DB.Model(&storage.Repository{}).Count(&count)
	if count != 1 {
		t.Fatalf("重复发现生成了 %d 个实体", count)
	}
	p.StarRange, p.MinStars, p.MaxStars = true, 0, 1
	children, err := splitSearch(p, []*sdk.Repository{source(1, "repo", 1)})
	if err != nil || len(children) != 2 || !children[0].StarRange || children[0].MaxStars != 0 || children[1].MinStars != 1 {
		t.Fatalf("零 Star 分片必须有明确上界: %+v %v", children, err)
	}
	encoded, _ := json.Marshal(children[0])
	var restored Payload
	json.Unmarshal(encoded, &restored)
	if !restored.StarRange || restored.MaxStars != 0 {
		t.Fatal("零上界在任务序列化后丢失")
	}
}

func TestCapacityAndFollowTime(t *testing.T) {
	e := newEngine(t)
	e.Queue.Cfg.MaxMonitoredRepos = 1
	discover(t, e, source(1, "one", 10))
	discover(t, e, source(2, "two", 20))
	if repository(t, e, 2).State != "candidate" {
		t.Fatal("容量满时新仓库应保留 candidate")
	}
	first := time.Now()
	if err := e.Queue.Follow(t.Context(), 1, true, first); err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Follow(t.Context(), 1, true, first.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if *repository(t, e, 1).FollowedAt != first.UnixMilli() {
		t.Fatal("重复关注不得改写首次关注时间")
	}
	if err := e.Queue.Follow(t.Context(), 2, true, first); !errors.Is(err, ErrCapacity) {
		t.Fatalf("全部容量已被关注项目占用时应返回容量错误: %v", err)
	}
	if repository(t, e, 2).StarredByUser {
		t.Fatal("失败的关注请求未整体回滚")
	}
}

func TestCompletionRollsBackWhenLeaseExpiresDuringWrite(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "repo", 10))
	manual(t, e, 1, "transaction-fence", time.Now())
	job, err := e.Queue.Claim(t.Context(), time.Now())
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %+v %v", job, err)
	}
	err = e.Queue.Complete(t.Context(), *job, func(tx *gorm.DB) error {
		if err := tx.Model(&storage.Repository{}).Where("repo_id=1").Update("stars", 999).Error; err != nil {
			return err
		}
		return tx.Model(job).Update("lease_until", time.Now().Add(-time.Second).UnixMilli()).Error
	})
	if !errors.Is(err, ErrLeaseLost) || repository(t, e, 1).Stars != 10 {
		t.Fatalf("提交前失效应回滚全部业务结果: %v", err)
	}
}

func TestEngineWaitsForInitializationAndCollects(t *testing.T) {
	e := newEngine(t)
	store := settings.Store{DB: e.Queue.DB}
	snapshot, err := store.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveDiscoveryRules(t.Context(), snapshot.Revision, []settings.Rule{{
		SourceType: "search", SourceKey: "fixture", Query: "topic:mcp", Enabled: true, Priority: 20, IntervalMinutes: 30,
	}}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	e.GitHub = fixtureGitHub{
		search: func(string, int) (gh.SearchPage, error) {
			requests.Add(1)
			return gh.SearchPage{Repositories: []*sdk.Repository{source(1, "repo", 10000)}, Total: 1}, nil
		},
		get: func() (gh.RepositoryResult, error) {
			requests.Add(1)
			return gh.RepositoryResult{Repository: source(1, "repo", 10000), ETag: "baseline"}, nil
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Worker 未按取消信号退出: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Worker 退出超时")
		}
	})
	time.Sleep(20 * time.Millisecond)
	if requests.Load() != 0 {
		t.Fatal("未初始化时不能启动业务采集")
	}
	if err := storage.InitializeEmpty(t.Context(), e.Queue.DB); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var samples int64
		if err := e.Queue.DB.Model(&storage.RepoSnapshot{}).Count(&samples).Error; err != nil {
			t.Fatal(err)
		}
		if samples == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("初始化后未完成发现和首次采样")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requests.Load() != 2 {
		t.Fatalf("应先搜索发现再单独采集详情: %d", requests.Load())
	}
}

func TestOutOfOrderSuccessPreservesEachSlot(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "repo", 100))
	stars := 101
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
		return gh.RepositoryResult{Repository: source(1, "repo", stars), ETag: "value"}, nil
	}}
	id := int64(1)
	oldPlan := time.Now().Add(-5 * time.Minute).UnixMilli()
	older, err := e.refresh(t.Context(), storage.Job{RepoID: &id}, Payload{PlannedAt: oldPlan, Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	stars = 102
	newer, err := e.refresh(t.Context(), storage.Job{RepoID: &id}, Payload{PlannedAt: oldPlan + 300000, Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, apply := range []func(*gorm.DB) error{newer, older} {
		if err := e.Queue.DB.Transaction(apply); err != nil {
			t.Fatal(err)
		}
	}
	var snapshots []storage.RepoSnapshot
	if err := e.Queue.DB.Order("sample_slot").Find(&snapshots).Error; err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[0].Stars != 101 || snapshots[1].Stars != 102 || repository(t, e, 1).Stars != 102 {
		t.Fatalf("旧元数据不能覆盖新值，每个成功槽都应保留: %+v", snapshots)
	}
}

func TestCapacityReductionRejectsExcessFollowedRepos(t *testing.T) {
	e := newEngine(t)
	for _, repo := range []*sdk.Repository{source(1, "one", 1), source(2, "two", 2)} {
		discover(t, e, repo)
		if err := e.Queue.Follow(t.Context(), repo.GetID(), true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	e.Queue.Cfg.MaxMonitoredRepos = 1
	if err := e.Queue.Rebalance(t.Context(), time.Now()); err == nil {
		t.Fatal("不能采用小于已关注活跃项目数量的监控容量")
	}
}

func TestChangedCapacityReevaluatesSameDay(t *testing.T) {
	e := newEngine(t)
	e.Queue.Cfg.MaxMonitoredRepos = 2
	discover(t, e, source(1, "one", 1))
	discover(t, e, source(2, "two", 2))
	if err := e.Queue.Rebalance(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	e.Queue.Cfg.MaxMonitoredRepos = 1
	if err := e.Queue.Rebalance(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	var active int64
	if err := e.Queue.DB.Model(&storage.Repository{}).Where("state='active'").Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("同日重启时新的容量也必须生效: %d", active)
	}
}

func TestLocalDeferralAndRemoteLimitUseDifferentAttemptCounts(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "repo", 1))
	pending := manual(t, e, 1, "budget", time.Now())
	for index, status := range []int{0, 429} {
		if err := e.Queue.DB.Model(&pending).Update("available_at", time.Now().UnixMilli()).Error; err != nil {
			t.Fatal(err)
		}
		job, err := e.Queue.Claim(t.Context(), time.Now())
		if err != nil || job == nil {
			t.Fatalf("领取失败: %+v %v", job, err)
		}
		failure := &gh.Failure{Code: "github_secondary_limit", Status: status, Until: time.Now().Add(time.Minute).UnixMilli()}
		if err := e.Queue.Fail(t.Context(), *job, failure, time.Now()); err != nil {
			t.Fatal(err)
		}
		var stored storage.Job
		if err := e.Queue.DB.Take(&stored, pending.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Attempts != index {
			t.Fatalf("本地等待不计次数，真实限流计一次: status=%d attempts=%d", status, stored.Attempts)
		}
	}
}

func TestRuleChangeCancelsOldLeaseAndSchedulesNewQuery(t *testing.T) {
	e := newEngine(t)
	store := settings.Store{DB: e.Queue.DB}
	snapshot, err := store.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rules := []settings.Rule{{SourceType: "search", SourceKey: "editable", Query: "topic:old", Enabled: true, IntervalMinutes: 60}}
	revision, _, err := store.SaveDiscoveryRules(t.Context(), snapshot.Revision, rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Schedule(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	oldJob, err := e.Queue.Claim(t.Context(), time.Now())
	if err != nil || oldJob == nil || oldJob.Type != "discover_search" {
		t.Fatalf("未领取旧规则任务: %+v %v", oldJob, err)
	}
	var oldRule storage.DiscoveryRule
	if err := e.Queue.DB.Take(&oldRule, "source_key='editable'").Error; err != nil {
		t.Fatal(err)
	}
	rules[0].Query = "topic:new"
	if _, _, err := store.SaveDiscoveryRules(t.Context(), revision, rules); err != nil {
		t.Fatal(err)
	}
	wrote := false
	err = e.Queue.Complete(t.Context(), *oldJob, func(*gorm.DB) error { wrote = true; return nil })
	if !errors.Is(err, ErrLeaseLost) || wrote {
		t.Fatalf("旧定义的在途任务不能提交结果: wrote=%v err=%v", wrote, err)
	}
	var current storage.DiscoveryRule
	if err := e.Queue.DB.Take(&current, "source_key='editable'").Error; err != nil || current.ID != oldRule.ID {
		t.Fatalf("编辑保留原规则 ID: %+v %v", current, err)
	}
	var oldPayload Payload
	if err := json.Unmarshal([]byte(oldJob.PayloadJSON), &oldPayload); err != nil {
		t.Fatal(err)
	}
	if Slot(current.NextRunAt) == Slot(oldPayload.PlannedAt) {
		if err := e.Queue.Schedule(t.Context(), time.UnixMilli(current.NextRunAt)); err != nil {
			t.Fatal(err)
		}
		if err := e.Queue.DB.Take(&current, current.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.NextRunAt != Slot(oldPayload.PlannedAt)+300000 {
			t.Fatal("同槽撤销后应在下一槽安排新查询")
		}
	}
	next := time.UnixMilli(current.NextRunAt)
	if err := e.Queue.Schedule(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	job, err := e.Queue.Claim(t.Context(), next)
	if err != nil || job == nil {
		t.Fatalf("未安排新规则任务: %+v %v", job, err)
	}
	var payload Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.Query != "topic:new" {
		t.Fatalf("新任务应使用数据库里的新查询: %+v %v", payload, err)
	}
}

func TestPendingLowerCapacityProtectsRestart(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "first", 10))
	discover(t, e, source(2, "second", 20))
	store := settings.Store{DB: e.Queue.DB}
	snapshot, err := store.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Settings.Scheduler.MaxMonitoredRepos = 1
	if _, err := store.SaveRuntime(t.Context(), snapshot.Revision, snapshot.Settings); err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Follow(t.Context(), 1, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Follow(t.Context(), 2, true, time.Now()); !errors.Is(err, ErrCapacity) {
		t.Fatalf("新增关注不能导致已保存的下次启动容量失效: %v", err)
	}
}

func TestRebalanceFillsCurrentCapacityWhileLowerLimitIsPending(t *testing.T) {
	e := newEngine(t)
	e.Queue.Cfg.MaxMonitoredRepos = 2
	for id := int64(1); id <= 3; id++ {
		discover(t, e, source(id, fmt.Sprintf("repo-%d", id), 10))
	}
	if err := e.Queue.DB.Model(&storage.Repository{}).Where("repo_id=3").Update("state", "unavailable").Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 3} {
		if err := e.Queue.Follow(t.Context(), id, true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	store := settings.Store{DB: e.Queue.DB}
	snapshot, err := store.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Settings.Scheduler.MaxMonitoredRepos = 1
	if _, err := store.SaveRuntime(t.Context(), snapshot.Revision, snapshot.Settings); err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.DB.Model(&storage.Repository{}).Where("repo_id=3").Update("state", "paused").Error; err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Rebalance(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if repository(t, e, 2).State != "active" || repository(t, e, 3).State != "paused" {
		t.Fatal("不可恢复的关注项目不能挤掉仍可监控的仓库")
	}
}

func TestEngineStaysRunningThroughSQLiteBusy(t *testing.T) {
	e := newEngineWithBusyTimeout(t, 0)
	e.Cfg.GitHub.WorkerConcurrency = 3
	if err := storage.InitializeEmpty(t.Context(), e.Queue.DB); err != nil {
		t.Fatal(err)
	}
	pool, err := e.Queue.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	exited := make(chan struct{})
	var runErr error
	go func() {
		runErr = e.Run(ctx)
		close(exited)
	}()
	defer func() {
		cancel()
		<-exited
	}()
	select {
	case <-exited:
		t.Fatalf("临时 SQLite 写锁不应终止采集服务: %v", runErr)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	locked = false
	deadline := time.Now().Add(4 * time.Second)
	for {
		var count int64
		if err := e.Queue.DB.Model(&storage.MaintenanceState{}).Where("key='admission.day'").Count(&count).Error; err != nil && !storage.IsBusy(err) {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("写锁释放后未恢复调度")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-exited:
		t.Fatalf("工作池争锁不应终止采集服务: %v", runErr)
	default:
	}
}
