package collector

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/google/go-github/v88/github"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/collection"
	gh "github.com/lifei6671/github-radar/internal/github"
	"github.com/lifei6671/github-radar/internal/storage"
)

// 分片、重试和补扫保留各自的计划，同轮和跨来源都按数字仓库 ID 去重。
func TestEarlyDiscoveryRunCountsShardsAndRecoversLateIndex(t *testing.T) {
	e := newEngine(t)
	now := time.Now().UTC()
	slot := collection.Slot(now.UnixMilli())
	rule := storage.DiscoveryRule{SourceType: "search", SourceKey: "rolling", AgeWindow: "0-3", Query: "archived:false",
		MinStars: 10, Enabled: true, IntervalMinutes: 120, NextRunAt: slot, Priority: 170}
	if err := e.Queue.DB.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Schedule(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	from, until, _ := collection.CreationWindow(rule.AgeWindow, now, collection.DiscoveryPrimary)
	oldFrom, oldUntil, _ := collection.CreationWindow(rule.AgeWindow, now, collection.DiscoveryBackfill)
	bound := func(from, until int64) string {
		return "created:" + time.UnixMilli(from).UTC().Format(time.RFC3339) + ".." + time.UnixMilli(until).UTC().Format(time.RFC3339)
	}
	project := func(id int64, stars int, created int64) *sdk.Repository {
		repo := source(id, fmt.Sprintf("early-%d", id), stars)
		repo.CreatedAt = &sdk.Timestamp{Time: time.UnixMilli(created)}
		return repo
	}
	incomplete := true
	e.GitHub = fixtureGitHub{search: func(query string, page int) (gh.SearchPage, error) {
		if strings.Contains(query, bound(oldFrom, oldUntil)) {
			return gh.SearchPage{Total: 2, Repositories: []*sdk.Repository{project(1, 19, from), project(2, 15, oldFrom)}}, nil
		}
		if !strings.Contains(query, bound(from, until)) {
			t.Fatalf("主扫描查询改动了计划窗口: %s", query)
		}
		switch {
		case strings.Contains(query, "stars:>=10"):
			return gh.SearchPage{Total: 1100, Repositories: []*sdk.Repository{project(1, 19, from)}}, nil
		case strings.Contains(query, "stars:15..19"):
			if incomplete {
				incomplete = false
				return gh.SearchPage{}, &gh.Failure{Code: consts.CodeGitHubIncompleteSearch, Status: 200, SearchTotal: new(900)}
			}
			return gh.SearchPage{Total: 2, Repositories: []*sdk.Repository{project(1, 19, from), project(9, 19, oldFrom)}}, nil
		default:
			next := 0
			if page == 1 {
				next = 2
			}
			return gh.SearchPage{Total: 2, Repositories: []*sdk.Repository{project(1, 14, from)}, NextPage: next}, nil
		}
	}}
	read := func(pass string) storage.DiscoveryRuleRun {
		t.Helper()
		var run storage.DiscoveryRuleRun
		if err := e.Queue.DB.Take(&run, "rule_id = ? AND pass = ?", rule.ID, pass).Error; err != nil {
			t.Fatal(err)
		}
		return run
	}
	for range 4 {
		step(t, e)
	}
	primary := read(collection.DiscoveryPrimary)
	if primary.Status != consts.StatusFailed || primary.UpstreamTotal == nil || *primary.UpstreamTotal != 1100 ||
		primary.PlannedShards != 2 || primary.CompletedShards != 1 || primary.ErrorCode == nil || *primary.ErrorCode != consts.CodeGitHubIncompleteSearch {
		t.Fatalf("分片和失败状态错误: %+v", primary)
	}
	// 模拟第二天重新领取。任务里的边界保持在原计划日期。
	var retry storage.Job
	if err := e.Queue.DB.Take(&retry, "discovery_run_id = ? AND status = ?", primary.ID, consts.StatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	available := time.Now().Add(24 * time.Hour)
	var payload collection.Payload
	if err := json.Unmarshal([]byte(retry.PayloadJSON), &payload); err != nil || payload.CreatedFrom != from || payload.CreatedUntil != until {
		t.Fatalf("跨日重试丢失计划: %+v %v", payload, err)
	}
	if err := e.Queue.DB.Model(&retry).Update("available_at", available.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	job, err := e.Queue.Claim(t.Context(), available)
	if err != nil || job == nil || job.ID != retry.ID {
		t.Fatalf("未领取跨日重试: %+v %v", job, err)
	}
	apply, err := e.execute(t.Context(), *job)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Complete(t.Context(), *job, apply); err != nil {
		t.Fatal(err)
	}
	step(t, e)
	primary, backfill := read(collection.DiscoveryPrimary), read(collection.DiscoveryBackfill)
	if primary.Status != consts.StatusSucceeded || primary.CompletedShards != 2 || primary.ErrorCode != nil ||
		backfill.Status != consts.StatusSucceeded || backfill.CreatedFrom != oldFrom {
		t.Fatalf("最终主扫与补扫状态错误: %+v %+v", primary, backfill)
	}
	var hits []storage.DiscoveryRuleRunRepo
	if err := e.Queue.DB.Order("run_id, repo_id").Find(&hits).Error; err != nil || len(hits) != 3 ||
		hits[0].RepoID != 1 || !hits[0].NewlyDiscovered || hits[1].RepoID != 1 || hits[1].NewlyDiscovered ||
		hits[2].RepoID != 2 || !hits[2].NewlyDiscovered {
		t.Fatalf("补扫应补捞迟到仓库而不重复记为新入库: %+v %v", hits, err)
	}
}

// 前一天到期的任务延后排入队列时，主扫仍使用原计划的 UTC 日期。
func TestOverdueEarlyDiscoveryKeepsScheduledDate(t *testing.T) {
	e := newEngine(t)
	due := time.Date(2026, 11, 12, 23, 55, 0, 0, time.UTC)
	now := due.Add(20 * time.Minute)
	rule := storage.DiscoveryRule{SourceType: "search", SourceKey: "overdue", AgeWindow: "4-14", Query: "archived:false",
		Enabled: true, IntervalMinutes: 240, NextRunAt: due.UnixMilli()}
	if err := e.Queue.DB.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Schedule(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var run storage.DiscoveryRuleRun
	if err := e.Queue.DB.Take(&run, "rule_id = ? AND pass = ?", rule.ID, collection.DiscoveryPrimary).Error; err != nil {
		t.Fatal(err)
	}
	from, until, _ := collection.CreationWindow(rule.AgeWindow, due, collection.DiscoveryPrimary)
	if run.PlannedAt != due.UnixMilli() || run.CreatedFrom != from || run.CreatedUntil != until {
		t.Fatalf("延迟入队不应改变主扫计划: %+v", run)
	}
}
