package reports

import (
	"context"
	"testing"
	"time"
)

type windowFixture struct {
	published []PublishedWindow
	planned   []Window
}

func (s *windowFixture) Windows(context.Context, string) ([]PublishedWindow, error) {
	return s.published, nil
}

func (s *windowFixture) Publish(_ context.Context, window Window) error {
	s.planned = append(s.planned, window)
	return nil
}

func TestDailyCatchUpContinuesAnEarlyFirstIssue(t *testing.T) {
	firstEnd := time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC) // 北京时间 11 月 12 日 07:00
	store := &windowFixture{}
	if err := Generate(t.Context(), store, firstEnd); err != nil {
		t.Fatal(err)
	}
	if len(store.planned) != 1 || store.planned[0].Date != "2026-11-12" || !store.planned[0].End.Equal(firstEnd) {
		t.Fatalf("首期应以北京时间记期: %+v", store.planned)
	}
	store.published = []PublishedWindow{{Date: "2026-11-12", End: firstEnd}}
	store.planned = nil
	cutoff := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if err := Generate(t.Context(), store, cutoff); err != nil {
		t.Fatal(err)
	}
	if len(store.planned) != 3 || !store.planned[0].Start.Equal(firstEnd) || !store.planned[2].End.Equal(cutoff) {
		t.Fatalf("恢复后应连续补查三期，接续首期实际截止时间: %+v", store.planned)
	}
	for i := 1; i < len(store.planned); i++ {
		if !store.planned[i-1].End.Equal(store.planned[i].Start) {
			t.Fatalf("日报窗口之间出现缺口: %+v", store.planned)
		}
	}
}
