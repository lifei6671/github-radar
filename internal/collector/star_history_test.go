package collector

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

type historyFixture func(context.Context, int64, int, int, gh.Policy) (gh.StarHistoryPage, error)

func (f historyFixture) StarHistory(ctx context.Context, id int64, page, size int, policy gh.Policy) (gh.StarHistoryPage, error) {
	return f(ctx, id, page, size, policy)
}

// 重跑同一页覆盖旧值，限制翻页次数，404 保留缓存且不改本地采样计划。
func TestStarHistoryJobIsIndependentAndIdempotent(t *testing.T) {
	e := newEngine(t)
	e.Queue.StarHistory = true
	const id = 69
	discover(t, e, source(id, "old-name", 90))
	week := time.Now().UTC().Truncate(24*time.Hour).UnixMilli() - metrics.Day
	var called []int
	value := int64(5)
	failure := false
	private := false
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
		// 上游同一个数字 ID 已改名；历史仍属于原仓库。
		repo := source(id, "renamed", 90)
		repo.Private = &private
		return gh.RepositoryResult{Repository: repo}, nil
	}}
	e.History = historyFixture(func(_ context.Context, repo int64, page, size int, policy gh.Policy) (gh.StarHistoryPage, error) {
		if repo != id || !policy.StarHistory || size != metrics.StarHistoryPageSize {
			t.Fatalf("回补没有沿用数字身份和独立预算: %d %d %+v", repo, size, policy)
		}
		called = append(called, page)
		if failure {
			return gh.StarHistoryPage{}, &gh.Failure{Code: consts.CodeGitHubNotFound, Status: 404}
		}
		return gh.StarHistoryPage{Weeks: []metrics.StarHistoryWeekData{{Week: week - int64(page-1)*metrics.StarHistoryWeek,
			Days: []*int64{&value}, FetchedAt: time.Now().UnixMilli()}}, NextPage: page + 1}, nil
	})
	request := func(key string) storage.Job {
		t.Helper()
		job, err := e.Queue.RequestStarHistory(t.Context(), id, key, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	first := request("first")
	if repeated := request("first"); repeated.ID != first.ID {
		t.Fatalf("重复请求生成了不同任务: %+v %+v", first, repeated)
	}
	for range 3 {
		step(t, e)
	}
	if !reflect.DeepEqual(called, []int{1, 2, 3}) {
		t.Fatalf("回补页次不符: %v", called)
	}
	if err := e.Queue.DB.Model(&storage.Repository{}).Where("repo_id = ?", id).Update("full_name", "owner/renamed").Error; err != nil {
		t.Fatal(err)
	}
	value = 12
	request("overwrite")
	for range 3 {
		step(t, e)
	}
	var rows []storage.RepoStarHistoryWeek
	if err := e.Queue.DB.Where("repo_id = ?", id).Order("week_timestamp DESC").Find(&rows).Error; err != nil || len(rows) != 3 || rows[0].DaysJSON != `[12]` {
		t.Fatalf("重复回补必须更新每周值且不新增重复行: %+v %v", rows, err)
	}
	before, err := e.Queue.Repository(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	failure = true
	request("unavailable")
	step(t, e)
	after, err := e.Queue.Repository(t.Context(), id)
	if err != nil || before.NextFetchAt != after.NextFetchAt {
		t.Fatalf("历史失败不得延迟本地采样: %d %d %v", before.NextFetchAt, after.NextFetchAt, err)
	}
	var state storage.RepoStarHistoryStatus
	if err := e.Queue.DB.Take(&state, "repo_id = ?", id).Error; err != nil || state.FetchedAt == 0 ||
		state.LastErrorCode == nil || *state.LastErrorCode != consts.CodeGitHubNotFound {
		t.Fatalf("失败应保留先前成功时间: %+v %v", state, err)
	}
	var snapshots int64
	if err := e.Queue.DB.Model(&storage.RepoSnapshot{}).Where("repo_id = ?", id).Count(&snapshots).Error; err != nil || snapshots != 0 {
		t.Fatalf("官方历史不得写入本地快照: %d %v", snapshots, err)
	}
	failure, private = false, true
	request("private")
	step(t, e)
	withdrawn, err := e.Queue.Repository(t.Context(), id)
	if err != nil || withdrawn.State != consts.StatusUnavailable || withdrawn.FirstNotFoundAt == nil {
		t.Fatalf("数字身份确认转私后应撤回仓库: %+v %v", withdrawn, err)
	}
}
