package storage

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type JobLane struct {
	Key      string
	Types    []string
	Gap      time.Duration
	Resource string
	Window   time.Duration
	Budget   int
	Burst    int
}

type JobPacing []JobLane

func (j Job) IsManual() bool {
	var payload struct {
		Manual bool `json:"manual"`
	}
	return json.Unmarshal([]byte(j.PayloadJSON), &payload) == nil && payload.Manual
}

func (l JobLane) stateKey() string { return "job.pacing." + l.Key }

func (l JobLane) waitingForBudget(rate RateLimit, now int64) bool {
	if l.Budget <= 0 || rate.LocalWindow == 0 || rate.LocalWindow+l.Window.Milliseconds() <= now {
		return false
	}
	if rate.LocalRequests >= l.Budget {
		return true
	}
	// 用已经实际发送的请求数确定下一次开工点，任务内部可以正常完成多步请求。
	elapsed := int64(float64(max(0, rate.LocalRequests-l.Burst)) * float64(l.Window.Milliseconds()) / float64(l.Budget))
	return rate.LocalWindow+elapsed > now
}

// Claim 将自动任务的开始时刻和原子领取放入同一事务。手动任务由调用方跳过调度间隔。
func (p JobPacing) Claim(db *gorm.DB, now time.Time, take func(*gorm.DB, []string) (*Job, error)) (*Job, error) {
	job, _, err := p.ClaimWithLocalFallback(db, now, take, nil)
	return job, err
}

// ClaimWithLocalFallback 先领取正常任务，再尝试不消耗上游额度的本地任务。本地任务不更新上游开工间隔。
func (p JobPacing) ClaimWithLocalFallback(db *gorm.DB, now time.Time, take, local func(*gorm.DB, []string) (*Job, error)) (*Job, bool, error) {
	if len(p) == 0 {
		job, err := take(db, nil)
		return job, false, err
	}
	var job *Job
	var usedLocal bool
	err := db.Transaction(func(tx *gorm.DB) error {
		keys := make([]string, 0, len(p))
		resources := make([]string, 0, len(p))
		for _, lane := range p {
			keys = append(keys, lane.stateKey())
			if lane.Resource != "" {
				resources = append(resources, lane.Resource)
			}
		}
		var starts []MaintenanceState
		if err := tx.Where("key IN ?", keys).Find(&starts).Error; err != nil {
			return err
		}
		last := make(map[string]int64, len(starts))
		for _, state := range starts {
			last[state.Key] = state.UpdatedAt
		}
		var rates []RateLimit
		if len(resources) > 0 {
			if err := tx.Where("resource IN ?", resources).Find(&rates).Error; err != nil {
				return err
			}
		}
		byResource := make(map[string]RateLimit, len(rates))
		for _, rate := range rates {
			byResource[rate.Resource] = rate
		}
		var deferred []string
		ms := now.UnixMilli()
		for _, lane := range p {
			started := last[lane.stateKey()]
			if (started != 0 && started+lane.Gap.Milliseconds() > ms) || lane.waitingForBudget(byResource[lane.Resource], ms) {
				deferred = append(deferred, lane.Types...)
			}
		}
		var err error
		job, err = take(tx, deferred)
		if err == nil && job == nil && local != nil {
			job, err = local(tx, deferred)
			usedLocal = job != nil
			return err
		}
		if err != nil || job == nil || job.IsManual() {
			return err
		}
		for _, lane := range p {
			for _, kind := range lane.Types {
				if kind == job.Type {
					row := MaintenanceState{Key: lane.stateKey(), UpdatedAt: ms}
					return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}},
						DoUpdates: clause.AssignmentColumns([]string{"updated_at"})}).Create(&row).Error
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return job, usedLocal, nil
}
