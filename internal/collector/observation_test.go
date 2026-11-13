package collector

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	sdk "github.com/google/go-github/v88/github"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/storage/metricstore"
)

// 两套分档都保留低 Star 搜索召回；探测的真实 24h 增长可以绕过累计门槛。
func TestAgeAdmissionAndFiniteProbes(t *testing.T) {
	for _, pool := range []bool{false, true} {
		name := "旧分档"
		if pool {
			name = "观察池"
		}
		t.Run(name, func(t *testing.T) {
			e := newEngine(t)
			if pool {
				e.Queue.Cfg = e.Queue.Cfg.ObservationTrial()
			}
			e.Queue.Cfg.LegacyMonitoring = false
			e.Cfg.Scheduler = e.Queue.Cfg
			at := time.Now().UTC()
			repo := source(850, "small-probe", 40)
			repo.CreatedAt = &sdk.Timestamp{Time: at.Add(-48 * time.Hour)}
			for range 3 {
				discover(t, e, repo)
			}
			start, err := e.Queue.Repository(t.Context(), 850)
			if err != nil || start.State != consts.StatusCandidate {
				t.Fatalf("40 Star 仓库未保留为候选: %+v %v", start, err)
			}
			// 固定实际观察时间，让 24h 证据和重复发现可精确复现。
			for i, stars := range []int{40, 90} {
				observed := at.Add(time.Duration(i)*24*time.Hour + time.Minute)
				if err := e.Queue.Schedule(t.Context(), observed); err != nil {
					t.Fatal(err)
				}
				job, err := e.Queue.Claim(t.Context(), observed)
				if err != nil || job == nil || job.RepoID == nil || *job.RepoID != 850 {
					t.Fatalf("探测 %d 未领取: %+v %v", i, job, err)
				}
				var p collection.Payload
				if err := json.Unmarshal([]byte(job.PayloadJSON), &p); err != nil || !p.ObservationProbe || p.ObservationFollowup != (i == 1) {
					t.Fatalf("探测计划不符合首采和复采顺序: %+v %v", p, err)
				}
				baseline, err := e.Queue.Repository(t.Context(), 850)
				if err != nil {
					t.Fatal(err)
				}
				result := source(850, "small-probe", stars)
				result.CreatedAt = repo.CreatedAt
				if err := e.write(func(w port.Writer) error {
					return w.SaveRefresh(port.Refresh{Baseline: baseline, Plan: p, StartedAt: observed, ObservedAt: observed,
						Result: gh.RepositoryResult{Repository: result, ETag: "probe"}})
				}); err != nil {
					t.Fatal(err)
				}
				if err := e.Queue.DB.Model(&storage.Job{}).Where("id = ?", job.ID).Update("status", consts.StatusSucceeded).Error; err != nil {
					t.Fatal(err)
				}
			}
			if pool {
				if err := metrics.Compute(t.Context(), metricstore.Store{DB: e.Queue.DB}, e.Cfg, at.Add(25*time.Hour), true); err != nil {
					t.Fatal(err)
				}
			}
			got, err := e.Queue.Repository(t.Context(), 850)
			if err != nil || got.State != consts.StatusActive || got.ObservationProbeDueAt != 0 {
				t.Fatalf("精确 50 增长未晋级: %+v %v", got, err)
			}
			discover(t, e, repo)
			if err := e.Queue.Schedule(t.Context(), at.Add(48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := e.Queue.DB.Model(&storage.Job{}).Where("repo_id = ? AND dedup_key LIKE ?", 850, "%:observation:%").Count(&count).Error; err != nil || count != 2 {
				t.Fatalf("重复 Search 产生多次探测: %d %v", count, err)
			}
			report, err := storage.ReadAdmissionAudit(t.Context(), e.Queue.DB, at)
			if err != nil || report.ProbeSamples != 1 {
				t.Fatalf("每日计入一次实际探测: %+v %v", report, err)
			}
		})
	}
}

// 无采样的候选先建立基线，官方爆发保持待确认，直到第二个本地样本出现。
func TestObservationProbeToLocalBreakoutAcrossWorkerRestart(t *testing.T) {
	e := newEngine(t)
	e.Queue.Cfg = e.Queue.Cfg.ObservationTrial()
	e.Cfg.Scheduler = e.Queue.Cfg
	discover(t, e, source(751, "early-probe", 21))
	now := time.Now()
	officialAt := now.Add(-2 * time.Hour).UnixMilli()
	if err := e.Queue.DB.Create(&storage.RepoStarHistoryStatus{RepoID: 751, BreakoutAt: &officialAt}).Error; err != nil {
		t.Fatal(err)
	}
	run := func(at time.Time) {
		t.Helper()
		if err := metrics.Compute(t.Context(), metricstore.Store{DB: e.Queue.DB}, e.Cfg, at, true); err != nil {
			t.Fatal(err)
		}
	}
	repository := func() storage.Repository {
		t.Helper()
		repo, err := e.Queue.Repository(t.Context(), 751)
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}
	run(now)
	if repo := repository(); repo.State != consts.StatusCandidate || repo.LastSuccessAt != nil {
		t.Fatalf("官方证据不能代替实测: %+v", repo)
	}
	if err := e.Queue.Schedule(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
		return gh.RepositoryResult{Repository: source(751, "early-probe", 21), ETag: "observed"}, nil
	}}
	step(t, e)
	repo := repository()
	if repo.State != consts.StatusCandidate || repo.LastSuccessAt == nil || repo.ObservationProbeDueAt != *repo.LastSuccessAt+24*time.Hour.Milliseconds() {
		t.Fatalf("首采应建立下次采样约定而不升档: %+v", repo)
	}
	// 模拟切换 Worker 后下一日的计划与真实响应。无须等待墙钟。
	reopened := *e.Queue
	e.Queue, e.Engine.Queue = &reopened, &reopened
	followup := time.UnixMilli(repo.ObservationProbeDueAt)
	if err := e.Queue.Schedule(t.Context(), followup); err != nil {
		t.Fatal(err)
	}
	job, err := e.Queue.Claim(t.Context(), followup)
	if err != nil || job == nil {
		t.Fatalf("复采计划未持久化: %+v %v", job, err)
	}
	var payload collection.Payload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || !payload.ObservationFollowup {
		t.Fatalf("应领取复采任务: %+v %v", payload, err)
	}
	change, err := e.refresh(t.Context(), *job, payload)
	if err != nil || change == nil {
		t.Fatalf("候选复采应进入正常请求路径: %v", err)
	}
	if err := e.write(func(w port.Writer) error {
		return w.SaveRefresh(port.Refresh{Baseline: repo, Plan: payload, StartedAt: followup, ObservedAt: followup,
			Result: gh.RepositoryResult{Repository: source(751, "early-probe", 521), ETag: "grown"}})
	}); err != nil {
		t.Fatal(err)
	}
	// 计算在 3 小时之后仍可复用实际复采时间的精确日窗。
	run(followup.Add(3 * time.Hour))
	run(followup.Add(3*time.Hour + time.Minute))
	var events []storage.ObservationTransition
	if err := e.Queue.DB.Where("repo_id = ?", repo.RepoID).Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if repo := repository(); repo.State != consts.StatusActive || repo.Grade != consts.GradeS ||
		len(events) != 1 || events[0].Reason != "breakout" || events[0].FromState != consts.StatusCandidate {
		t.Fatalf("需要精确本地爆发，并且跨重算只记录一次: %+v %+v", repo, events)
	}
}

func TestObservationProbeRechecksFirst404(t *testing.T) {
	for _, tc := range []struct {
		name          string
		manual, block bool
		result        int
	}{
		{name: "探测后恢复", result: http.StatusOK},
		{name: "探测后确认下线", result: http.StatusNotFound},
		{name: "人工刷新后恢复", manual: true, result: http.StatusOK},
		{name: "解除屏蔽后恢复", block: true, result: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEngine(t)
			e.Queue.Cfg = e.Queue.Cfg.ObservationTrial()
			discover(t, e, source(752, "recheck", 40))
			first := time.Now()
			if tc.manual {
				manual(t, e, 752, "observation-first-404", first)
			} else {
				if err := e.Queue.Schedule(t.Context(), first); err != nil {
					t.Fatal(err)
				}
			}
			e.GitHub = fixtureGitHub{get: func() (gh.RepositoryResult, error) {
				return gh.RepositoryResult{}, &gh.Failure{Status: 404}
			}}
			step(t, e)
			repo, err := e.Queue.Repository(t.Context(), 752)
			if err != nil || repo.State != consts.StatusCandidate || repo.FirstNotFoundAt == nil ||
				repo.ObservationProbeDueAt != *repo.FirstNotFoundAt+time.Hour.Milliseconds() {
				t.Fatalf("首个 404 应等待复核: %+v %v", repo, err)
			}
			at := time.UnixMilli(repo.ObservationProbeDueAt)
			if tc.block {
				if err := e.Queue.SetBlocked(t.Context(), 752, true, at.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				if err := e.Queue.SetBlocked(t.Context(), 752, false, at); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Queue.Schedule(t.Context(), at); err != nil {
				t.Fatal(err)
			}
			job, err := e.Queue.Claim(t.Context(), at)
			if err != nil || job == nil {
				t.Fatalf("未安排复核: %+v %v", job, err)
			}
			var payload collection.Payload
			if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || !payload.ObservationRecheck {
				t.Fatalf("任务应按数字 ID 复核: %+v %v", payload, err)
			}
			observation := port.Refresh{Baseline: repo, Plan: payload, StartedAt: at, ObservedAt: at}
			if tc.result == http.StatusOK {
				observation.Result.Repository = source(752, "recheck", 41)
			} else {
				observation.FailureStatus = 404
			}
			if err := e.write(func(w port.Writer) error { return w.SaveRefresh(observation) }); err != nil {
				t.Fatal(err)
			}
			repo, err = e.Queue.Repository(t.Context(), 752)
			if err != nil {
				t.Fatal(err)
			}
			if tc.result == http.StatusOK && (repo.FirstNotFoundAt != nil || repo.LastSuccessAt == nil || repo.ObservationProbeDueAt == 0) ||
				tc.result == http.StatusNotFound && (repo.State != consts.StatusUnavailable || repo.ObservationProbeDueAt != 0) {
				t.Fatalf("复核后身份状态错误: %+v", repo)
			}
		})
	}
}

func TestObservationManualFollowPreservesCapacity(t *testing.T) {
	e := newEngine(t)
	discover(t, e, source(753, "victim", 80))
	discover(t, e, source(754, "other", 90))
	e.Queue.Cfg = e.Queue.Cfg.ObservationTrial()
	e.Queue.Cfg.MaxHot, e.Queue.Cfg.MaxActive, e.Queue.Cfg.MaxNormal, e.Queue.Cfg.MaxCooling, e.Queue.Cfg.MaxMonitoredRepos = 0, 1, 0, 0, 1
	discover(t, e, source(755, "manual-low-stars", 1))
	if err := e.Queue.Follow(t.Context(), 755, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	var active int64
	if err := e.Queue.DB.Model(&storage.Repository{}).Where("state = ?", consts.StatusActive).Count(&active).Error; err != nil || active != 2 {
		t.Fatalf("首轮演算前不能单独暂停现有仓库: %d %v", active, err)
	}
	e.Cfg.Scheduler = e.Queue.Cfg
	if err := metrics.Compute(t.Context(), metricstore.Store{DB: e.Queue.DB}, e.Cfg, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	repo, err := e.Queue.Repository(t.Context(), 755)
	if err != nil || repo.State != consts.StatusActive || repo.Grade != consts.GradeA {
		t.Fatalf("人工重点应越过 Star 门槛并优先占用名额: %+v %v", repo, err)
	}
}
