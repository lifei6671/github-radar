// Package aibudget 提供所有模型调用共用的 UTC 日预算和一次性发送凭证。
package aibudget

import (
	"context"
	"crypto/rand"
	"errors"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/admission"
	"github.com/lifei6671/github-radar/internal/storage"
)

const (
	AutoLimit   = config.MaxAutoDailyCalls
	ManualLimit = config.MaxManualDailyCalls
	TotalLimit  = AutoLimit + ManualLimit
)

var (
	ErrExhausted = errors.New("当日模型调用额度已用完")
	ErrUncertain = errors.New("同一模型输入已有未确认请求")
	ErrNoPermit  = errors.New("缺少有效模型发送凭证")
)

// Limits 可将硬上限调低；自动通道在 UTC 中午后共用自动余量。
type Limits struct {
	Auto, Manual       int
	SoftCost, HardCost float64
}

type Service struct {
	DB     *gorm.DB
	Limits Limits
}

// ForConfig 将进程设置映射到共享账本；新旧配置都由发送服务钳制硬上限。
func ForConfig(db *gorm.DB, cfg config.Summary) Service {
	return Service{DB: db, Limits: Limits{Auto: cfg.AutoDailyCalls, Manual: cfg.ManualDailyCalls,
		SoftCost: cfg.DailySoftCost, HardCost: cfg.DailyHardCost}}
}

// Request 描述一个发送尝试；LogicalKey 只用于无任务租约的 Embedding 去重。
type Request struct {
	Key, LogicalKey, Lane, Operation, Provider, Model string
	Manual                                            bool
	HeldCost                                          float64
}

type permitKey struct{}
type permit struct {
	used atomic.Bool
	day  int64
}

// Consume 在实际 HTTP 发送处消耗一次性凭证，拒绝重复使用和跨 UTC 日的旧凭证。
func Consume(ctx context.Context) error {
	p, ok := ctx.Value(permitKey{}).(*permit)
	if !ok || p.day != admission.DayStart(time.Now()).UnixMilli() || !p.used.CompareAndSwap(false, true) {
		return ErrNoPermit
	}
	return nil
}

// Unused 只在调用方能证明未进入 HTTP 发送时释放。账本保留原因状态。
func Unused(tx *gorm.DB, key string, now time.Time) error {
	return tx.Model(&storage.AIBudgetReservation{}).Where("reservation_key = ? AND status = ?", key, consts.StatusSending).
		Updates(map[string]any{"status": "unused", "sending_at": nil, "settled_at": now.UnixMilli()}).Error
}

// Day 查询当天真正发送或可能发送的记录。已证实未发送的预留不占实际额度。
func Day(db *gorm.DB, now time.Time) *gorm.DB {
	start := admission.DayStart(now)
	return db.Model(&storage.AIBudgetReservation{}).Where("sending_at >= ? AND sending_at < ?", start.UnixMilli(), start.Add(24*time.Hour).UnixMilli())
}

// Remaining 给调度器提供近似空闲槽；最终结果以发送事务为准。
func (s Service) Remaining(db *gorm.DB, lane string, now time.Time) (int, error) {
	used, err := s.usage(db, now)
	if err != nil {
		return 0, err
	}
	return s.remaining(used, lane, now), nil
}

type usageRow struct {
	Origin, Lane string
	Count        int
	Cost         float64
}

// usage 汇总 UTC 日内的次数及已知费用；不确定结果保留估算上界。
func (s Service) usage(db *gorm.DB, now time.Time) ([]usageRow, error) {
	var rows []usageRow
	err := Day(db, now).Select("origin, lane, COUNT(*) AS count, SUM(COALESCE(estimated_cost, held_cost)) AS cost").
		Group("origin, lane").Scan(&rows).Error
	return rows, err
}

func (s Service) remaining(rows []usageRow, lane string, now time.Time) int {
	var total, auto, manual, inLane int
	var cost float64
	for _, row := range rows {
		total += row.Count
		cost += row.Cost
		if row.Origin == consts.OriginManual {
			manual += row.Count
		} else {
			auto += row.Count
			if row.Lane == lane {
				inLane += row.Count
			}
		}
	}
	if s.Limits.HardCost > 0 && cost >= s.Limits.HardCost {
		return 0
	}
	if lane == admission.Manual {
		return max(0, min(TotalLimit-total, min(ManualLimit, s.Limits.Manual)-manual))
	}
	if s.Limits.SoftCost > 0 && cost >= s.Limits.SoftCost {
		return 0
	}
	limit := min(AutoLimit, s.Limits.Auto)
	left := min(TotalLimit-total, limit-auto)
	if now.UTC().Hour() < 12 && lane != "" {
		share := 1
		switch lane {
		case admission.Growth:
			share = 5
		case admission.Exploration:
			share = 3
		}
		left = min(left, AutoLimit*share/10-inLane)
	}
	return max(0, left)
}

// Start 在 SQLite 的 IMMEDIATE 写事务中计数并标记 sending。事务返回前不得调用上游。
func (s Service) Start(ctx context.Context, req Request, now time.Time, before func(*gorm.DB, *Request) error) (context.Context, storage.AIBudgetReservation, error) {
	if s.DB == nil {
		return ctx, storage.AIBudgetReservation{}, ErrNoPermit
	}
	if req.Key == "" {
		req.Key = rand.Text()
	}
	at := now.UnixMilli()
	var row storage.AIBudgetReservation
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if before != nil {
			if err := before(tx, &req); err != nil {
				return err
			}
		}
		origin := consts.OriginAuto
		if req.Manual {
			origin, req.Lane = consts.OriginManual, admission.Manual
		}
		row = storage.AIBudgetReservation{ReservationKey: req.Key, LogicalKey: req.LogicalKey, Origin: origin,
			Lane: req.Lane, Operation: req.Operation, Provider: req.Provider, Model: req.Model, ReservedAt: at,
			SendingAt: &at, Status: consts.StatusSending, HeldCost: req.HeldCost}
		if req.LogicalKey != "" {
			var duplicate int64
			if err := tx.Model(&storage.AIBudgetReservation{}).Where("logical_key = ? AND status IN ?", req.LogicalKey,
				[]string{consts.StatusSending, consts.StatusUnknown}).Count(&duplicate).Error; err != nil {
				return err
			}
			if duplicate != 0 {
				return ErrUncertain
			}
		}
		rows, err := s.usage(tx, now)
		if err != nil {
			return err
		}
		if s.remaining(rows, req.Lane, now) == 0 {
			return ErrExhausted
		}
		var cost float64
		for _, group := range rows {
			cost += group.Cost
		}
		if s.Limits.HardCost > 0 && cost+req.HeldCost > s.Limits.HardCost {
			return ErrExhausted
		}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "reservation_key"}},
			DoUpdates: clause.AssignmentColumns([]string{"sending_at", "status", "lane", "model", "operation", "held_cost"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: "ai_budget_reservations.status", Value: consts.StatusReserved}}},
		}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUncertain
		}
		return nil
	})
	if err != nil {
		return ctx, storage.AIBudgetReservation{}, err
	}
	return context.WithValue(ctx, permitKey{}, &permit{day: admission.DayStart(now).UnixMilli()}), row, nil
}

// Settle 只更新已经记账的发送；成功、失败和未知都保留当天的占用。
func Settle(tx *gorm.DB, key, status string, input, output *int64, cost *float64, duration time.Duration, now time.Time) error {
	return tx.Model(&storage.AIBudgetReservation{}).Where("reservation_key = ? AND status = ?", key, consts.StatusSending).
		Updates(map[string]any{"status": status, "settled_at": now.UnixMilli(), "input_tokens": input,
			"output_tokens": output, "estimated_cost": cost, "duration_ms": duration.Milliseconds()}).Error
}
