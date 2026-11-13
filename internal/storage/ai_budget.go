package storage

import (
	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
)

// AIBudgetReservation 保存一次物理发送的独立计量信息，不保存模型输入或输出正文。
type AIBudgetReservation struct {
	ReservationKey string `gorm:"primaryKey"`
	LogicalKey     string
	Origin         string
	Lane           string
	Operation      string
	Provider       string
	Model          string
	ReservedAt     int64
	SendingAt      *int64
	SettledAt      *int64
	Status         string
	InputTokens    *int64
	OutputTokens   *int64
	EstimatedCost  *float64
	HeldCost       float64
	DurationMS     *int64
}

func (AIBudgetReservation) TableName() string { return "ai_budget_reservations" }

// MarkBudgetUnknown 与已有任务台账同步，将可能发出的调用保留到人工确认。
func MarkBudgetUnknown(tx *gorm.DB, calls *gorm.DB, at int64) error {
	return tx.Model(&AIBudgetReservation{}).Where("reservation_key IN (?) AND status = ?", calls, consts.StatusSending).
		Updates(map[string]any{"status": consts.StatusUnknown, "settled_at": at}).Error
}
