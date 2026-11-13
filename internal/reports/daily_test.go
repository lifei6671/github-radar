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

func TestDailyCatchUpUsesClosedBeijingDates(t *testing.T) {
	firstEnd := time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC) // 北京时间 11 月 12 日 07:00
	store := &windowFixture{}
	if err := Generate(t.Context(), store, firstEnd); err != nil {
		t.Fatal(err)
	}
	if len(store.planned) != catchUpDays || store.planned[catchUpDays-1].Date != "2026-11-10" {
		t.Fatalf("早于 08:00 时昨天尚未到出刊点: %+v", store.planned)
	}
	store.published = []PublishedWindow{{Date: "2026-11-12", End: firstEnd}, {Date: "2026-11-13", End: time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)}}
	store.planned = nil
	cutoff := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	if err := Generate(t.Context(), store, cutoff); err != nil {
		t.Fatal(err)
	}
	if len(store.planned) != catchUpDays-1 || store.planned[len(store.planned)-2].Date != "2026-11-12" ||
		store.planned[len(store.planned)-1].Date != "2026-11-14" {
		t.Fatalf("应重建旧窗口、跳过已收口期次，并生成昨天的日报: %+v", store.planned)
	}
	for _, window := range store.planned {
		if window.Start.Hour() != 0 || window.End.Hour() != 0 || window.End.Sub(window.Start) != 24*time.Hour {
			t.Fatalf("日报必须覆盖北京时间的完整自然日: %+v", window)
		}
	}
}
