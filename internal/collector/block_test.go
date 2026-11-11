package collector

import (
	"errors"
	"testing"
	"time"

	sdk "github.com/google/go-github/v88/github"

	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/trending"
)

func TestRepositoryBlockStopsSamplingAndDiscovery(t *testing.T) {
	e := newEngine(t)
	ctx, db := t.Context(), e.Queue.DB
	discover(t, e, source(1, "repo", 100))
	requests := 0
	e.GitHub = fixtureGitHub{
		get: func() (gh.RepositoryResult, error) {
			requests++
			return gh.RepositoryResult{Repository: source(1, "repo", 150)}, nil
		},
		search: func(string, int) (gh.SearchPage, error) {
			return gh.SearchPage{Repositories: []*sdk.Repository{source(1, "renamed", 150)}, Total: 1}, nil
		},
	}
	job := manual(t, e, 1, "before-block", time.Now())
	claimed, err := e.Queue.Claim(ctx, time.Now())
	if err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("未领取采样任务: %+v %v", claimed, err)
	}
	write, err := e.refresh(ctx, *claimed, Payload{Manual: true, PlannedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.SetBlocked(ctx, 1, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Complete(ctx, *claimed, write); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("屏蔽前已发出的采样不能提交: %v", err)
	}
	if _, err := e.Queue.Refresh(ctx, 1, "blocked", time.Now()); !errors.Is(err, storage.ErrRepositoryBlocked) {
		t.Fatalf("屏蔽仓库不能手动采样: %v", err)
	}
	if err := e.Queue.Schedule(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if next, err := e.Queue.Claim(ctx, time.Now()); err != nil || next != nil {
		t.Fatalf("屏蔽仓库不能自动采样: %+v %v", next, err)
	}
	trendingRepo := source(0, "repo", 150)
	e.Trending = fixtureTrending{entries: []trending.Entry{{Repository: trendingRepo, Rank: 1}, {Repository: source(2, "other", 200), Rank: 2}}}
	p := Payload{SourceKey: "all", Period: "daily", PlannedAt: time.Now().UnixMilli()}
	trend, err := e.discoverTrending(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(trend); err != nil {
		t.Fatal(err)
	}
	search, err := e.search(ctx, storage.Job{}, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(search); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&storage.RepoSnapshot{}, &storage.SearchObservation{}, &storage.TrendingObservation{}} {
		var count int64
		if err := db.Model(model).Where("repo_id=1").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("屏蔽仓库产生新观测 %T: %d %v", model, count, err)
		}
	}
	var remaining storage.TrendingObservation
	if err := db.Take(&remaining, "repo_id=2").Error; err != nil || remaining.Rank != 2 {
		t.Fatalf("跳过屏蔽仓库后应保留其他仓库的原始名次: %+v %v", remaining, err)
	}
	if repo := repository(t, e, 1); !repo.Blocked || repo.FullName != "owner/repo" || repo.Stars != 100 || requests != 1 {
		t.Fatalf("屏蔽后不应更新元数据或补查详情: %+v requests=%d", repo, requests)
	}
	if err := e.Queue.SetBlocked(ctx, 1, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	manual(t, e, 1, "unblocked", time.Now())
	step(t, e)
	if repo := repository(t, e, 1); repo.Blocked || repo.State != "active" || repo.Stars != 150 {
		t.Fatalf("解除屏蔽应恢复采样: %+v", repo)
	}
}

func TestRepositoryBlockFencesModelJobsAndKeepsUncertainCharge(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "repo", 100))
	db, at := e.Queue.DB, time.Now().UnixMilli()
	sending := storage.Job{RepoID: new(int64(1)), Type: "translate_readme", DedupKey: "sending", Status: "running", LeaseToken: new("lease")}
	pending := storage.Job{RepoID: new(int64(1)), Type: "refresh_readme_summary", DedupKey: "reserved", Status: "pending"}
	for _, job := range []*storage.Job{&sending, &pending} {
		job.PayloadJSON, job.AvailableAt, job.MaxAttempts, job.CreatedAt, job.UpdatedAt = "{}", at, 3, at, at
		if err := db.Create(job).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range []storage.AICall{
		{CallKey: "sending", JobID: sending.ID, Provider: "fixture", Origin: "auto", Status: "sending", ReservedAt: at, UpdatedAt: at},
		{CallKey: "reserved", JobID: pending.ID, Provider: "fixture", Origin: "auto", Status: "reserved", ReservedAt: at, UpdatedAt: at},
	} {
		if err := db.Create(&call).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Queue.SetBlocked(t.Context(), 1, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id                     int64
		key, jobState, aiState string
	}{{sending.ID, "sending", "unknown", "unknown"}, {pending.ID, "reserved", "canceled", "failed"}} {
		var job storage.Job
		var call storage.AICall
		if err := db.Take(&job, "id=?", tc.id).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Take(&call, "call_key=?", tc.key).Error; err != nil {
			t.Fatal(err)
		}
		if job.Status != tc.jobState || job.LeaseToken != nil || call.Status != tc.aiState {
			t.Fatalf("屏蔽后任务和模型台账不符: %+v %+v", job, call)
		}
	}
}

func TestBlockedNameDoesNotHideDifferentGitHubID(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(1, "repo", 100))
	if err := e.Queue.SetBlocked(t.Context(), 1, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	reused := source(2, "repo", 200)
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) { return gh.RepositoryResult{Repository: reused}, nil }}
	e.Trending = fixtureTrending{entries: []trending.Entry{{Repository: reused, Rank: 1}}}
	write, err := e.discoverTrending(t.Context(), Payload{SourceKey: "all", Period: "daily", PlannedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.DB.Transaction(write); err != nil {
		t.Fatal(err)
	}
	if old, next := repository(t, e, 1), repository(t, e, 2); !old.Blocked || old.NameIsCurrent || next.Blocked || !next.NameIsCurrent {
		t.Fatalf("名称复用不能把屏蔽转移给新的数字身份: old=%+v new=%+v", old, next)
	}
}
