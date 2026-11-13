package topics

import (
	"context"
	"time"

	domain "github.com/lifei6671/github-radar/internal/domain/topics"
)

// Add 幂等加入选题池。已有选题保留人工状态，重新打开由选题状态接口处理。
func Add(ctx context.Context, store Store, id int64, now time.Time) (domain.Candidate, error) {
	var candidate domain.Candidate
	err := store.Transaction(ctx, func(tx Transaction) error {
		existing, err := tx.Previous(id)
		if err != nil {
			return err
		}
		if existing.ID != 0 {
			candidate = existing
			if candidate.ReviewedAt != nil {
				return nil
			}
			at := now.UnixMilli()
			candidate.ReviewedAt = &at
			return tx.MarkReviewed(candidate.ID, at)
		}
		signals, err := tx.LoadSignals(id, now.Add(-signalAge).UnixMilli())
		if err != nil {
			return err
		}
		candidate, err = tx.Save(domain.Manual(signals, now))
		return err
	})
	return candidate, err
}
