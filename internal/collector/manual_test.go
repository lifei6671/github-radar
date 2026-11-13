package collector

import (
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/collector/port"
	"github.com/lifei6671/github-radar/internal/domain/collection"
)

func TestLateManualDiscoveryKeepsNewerRepositoryIdentity(t *testing.T) {
	e := newEngine(t)
	ctx := t.Context()
	at := time.Now().Add(-time.Second)
	old, moved := source(1, "old", 1), source(1, "new", 1)
	for _, name := range []string{old.GetFullName(), moved.GetFullName()} {
		if _, err := e.Queue.SubmitRepositories(ctx, name, []string{name}, "", "", at); err != nil {
			t.Fatal(err)
		}
	}
	first, err := e.Queue.Claim(ctx, time.Now())
	if err != nil || first == nil {
		t.Fatalf("领取旧地址任务: %v", err)
	}
	second, err := e.Queue.Claim(ctx, time.Now())
	if err != nil || second == nil {
		t.Fatalf("领取新地址任务: %v", err)
	}
	if err := e.Queue.Complete(ctx, *second, func(w port.Writer) error {
		return w.SaveManualDiscovery(*second, collection.Payload{PlannedAt: at.Add(100 * time.Millisecond).UnixMilli(), SourceURL: "https://example.test/new"},
			moved, at.Add(200*time.Millisecond), at.Add(300*time.Millisecond))
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Queue.Complete(ctx, *first, func(w port.Writer) error {
		return w.SaveManualDiscovery(*first, collection.Payload{PlannedAt: at.UnixMilli(), SourceURL: "https://example.test/old"},
			old, at, at.Add(400*time.Millisecond))
	}); err != nil {
		t.Fatal(err)
	}
	if got := repository(t, e, 1); got.FullName != moved.GetFullName() || !got.NameIsCurrent {
		t.Fatalf("迟到响应覆盖了新地址: %+v", got)
	}
	var count int64
	if err := e.Queue.DB.Table("repo_discovery_events").Where("repo_id = ?", 1).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("同一数字 ID 的两条来源应保留: count=%d err=%v", count, err)
	}
}
