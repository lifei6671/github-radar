package aibudget_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lifei6671/github-radar/internal/aibudget"
	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/migrations"
)

// budgetDB 用真实磁盘 SQLite 和多个连接验证跨连接预算事务。
func budgetDB(t *testing.T) (aibudget.Service, func(), string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.db")
	pool, err := storage.Open(t.Context(), config.Storage{Path: path, MaxOpenConns: 4, BusyTimeoutMS: 30000, WAL: true})
	if err != nil {
		t.Fatal(err)
	}
	closeDB := func() { _ = pool.Close() }
	t.Cleanup(closeDB)
	if _, err := migrations.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	return aibudget.ForConfig(db, config.DefaultSummary()), closeDB, path
}

// 多连接并行竞争固定账日，验证 4500+500、未知保留、重启和 UTC 日界。
func TestConcurrentDailyLimitAndRestart(t *testing.T) {
	s, closeDB, path := budgetDB(t)
	at := time.Date(2026, 11, 13, 23, 58, 0, 0, time.UTC)
	// 北京时间已进入次日，调用依然计入 UTC 13 日。
	at = at.In(time.FixedZone("UTC+8", 8*3600))
	var auto, manual atomic.Int32
	var wg sync.WaitGroup
	for worker := range 12 {
		wg.Go(func() {
			for i := range 500 {
				isManual := worker < 2
				_, row, err := s.Start(t.Context(), aibudget.Request{Key: fmt.Sprintf("%d/%d", worker, i),
					Lane: admission.Growth, Operation: "summary", Manual: isManual}, at, nil)
				if errors.Is(err, aibudget.ErrExhausted) {
					continue
				}
				if err != nil {
					t.Errorf("预留出错: %v", err)
					return
				}
				if isManual {
					manual.Add(1)
				} else {
					auto.Add(1)
				}
				if err := aibudget.Settle(s.DB, row.ReservationKey, "unknown", nil, nil, nil, time.Millisecond, at); err != nil {
					t.Errorf("结算出错: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	if auto.Load() != 4500 || manual.Load() != 500 {
		t.Fatalf("实际发送自动=%d 人工=%d", auto.Load(), manual.Load())
	}
	closeDB()
	pool, err := storage.OpenBootstrap(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s.DB, err = storage.OpenGORM(pool)
	if err != nil {
		t.Fatal(err)
	}
	req := aibudget.Request{Operation: "embedding_query", Lane: admission.Maintenance}
	if _, _, err := s.Start(t.Context(), req, at.Add(time.Minute), nil); !errors.Is(err, aibudget.ErrExhausted) {
		t.Fatalf("重启后第 5001 次发送: %v", err)
	}
	if _, _, err := s.Start(t.Context(), req, at.Add(2*time.Minute), nil); err != nil {
		t.Fatalf("UTC 次日没有恢复额度: %v", err)
	}
}

// 不同模型链路争用最后一个额度；重复键和发送未决的相同输入不能免费重发。
func TestLastSlotAndLogicalUncertainty(t *testing.T) {
	s, _, _ := budgetDB(t)
	s.Limits.Auto = 1
	at := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	var wg sync.WaitGroup
	var winners atomic.Int32
	for _, operation := range []string{"summary", "embedding_index"} {
		wg.Go(func() {
			_, row, err := s.Start(t.Context(), aibudget.Request{Key: operation, LogicalKey: operation, Operation: operation, Lane: admission.Maintenance}, at, nil)
			if err == nil {
				winners.Add(1)
				if err := aibudget.Settle(s.DB, row.ReservationKey, "unknown", nil, nil, nil, time.Millisecond, at); err != nil {
					t.Error(err)
				}
			} else if !errors.Is(err, aibudget.ErrExhausted) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("最后一个槽位发送 %d 次", winners.Load())
	}
	var won storage.AIBudgetReservation
	if err := s.DB.Take(&won).Error; err != nil {
		t.Fatal(err)
	}
	nextDay := at.Add(24 * time.Hour)
	for _, req := range []aibudget.Request{{LogicalKey: won.LogicalKey}, {Key: won.ReservationKey}} {
		req.Lane = admission.Maintenance
		if _, _, err := s.Start(t.Context(), req, nextDay, nil); !errors.Is(err, aibudget.ErrUncertain) {
			t.Fatalf("跨日不能重用未决输入或发送键: %v", err)
		}
	}
}

// 软配额只在前半天保留。金额硬上限在网络动作之前比较本次费用上界。
func TestLaneBorrowingAndCost(t *testing.T) {
	s, _, _ := budgetDB(t)
	at := time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC)
	for lane, limit := range map[string]int{admission.Growth: 2250, admission.Exploration: 1350, admission.Revival: 450, admission.Maintenance: 450} {
		left, err := s.Remaining(s.DB, lane, at)
		if err != nil || left != limit {
			t.Fatalf("通道 %s 余量=%d err=%v", lane, left, err)
		}
	}
	s.Limits.SoftCost, s.Limits.HardCost = 1, 2
	req := aibudget.Request{Lane: admission.Maintenance, HeldCost: 1.1}
	if _, _, err := s.Start(t.Context(), req, at, nil); err != nil {
		t.Fatal(err)
	}
	if left, err := s.Remaining(s.DB, admission.Maintenance, at); err != nil || left != 0 {
		t.Fatalf("自动金额软上限无效: %d %v", left, err)
	}
	req.Manual = true
	if _, _, err := s.Start(t.Context(), req, at, nil); !errors.Is(err, aibudget.ErrExhausted) {
		t.Fatalf("人工绕过金额硬上限: %v", err)
	}
	s.Limits.SoftCost, s.Limits.HardCost = 0, 0
	if left, err := s.Remaining(s.DB, admission.Maintenance, at.Add(time.Hour)); err != nil || left != 4499 {
		t.Fatalf("UTC 午后未借用自动余额: %d %v", left, err)
	}
}

// BenchmarkReservation 量测真实磁盘账本的写入吞吐，不接触任何外部模型。
func BenchmarkReservation(b *testing.B) {
	pool, err := storage.OpenBootstrap(b.Context(), filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	if _, err := migrations.Up(b.Context(), pool); err != nil {
		b.Fatal(err)
	}
	db, err := storage.OpenGORM(pool)
	if err != nil {
		b.Fatal(err)
	}
	s := aibudget.ForConfig(db, config.DefaultSummary())
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := range b.N {
		now := at.AddDate(0, 0, i/aibudget.AutoLimit)
		_, row, err := s.Start(context.Background(), aibudget.Request{Lane: admission.Growth}, now, nil)
		if err != nil {
			b.Fatal(err)
		}
		if err := aibudget.Settle(db, row.ReservationKey, "succeeded", nil, nil, nil, time.Millisecond, now); err != nil {
			b.Fatal(err)
		}
	}
}
