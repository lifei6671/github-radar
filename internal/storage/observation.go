package storage

import (
	"encoding/json"

	"gorm.io/gorm"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/domain/metrics"
)

// ObservationTransition 保存档位或观察状态变更的不可变记录。
type ObservationTransition struct {
	ID           int64  `gorm:"column:id;primaryKey"`
	RepoID       int64  `gorm:"column:repo_id"`
	OccurredAt   int64  `gorm:"column:occurred_at"`
	FromState    string `gorm:"column:from_state"`
	ToState      string `gorm:"column:to_state"`
	FromGrade    string `gorm:"column:from_grade"`
	ToGrade      string `gorm:"column:to_grade"`
	Reason       string `gorm:"column:reason"`
	EvidenceJSON string `gorm:"column:evidence_json"`
}

func (ObservationTransition) TableName() string { return "repo_observation_transitions" }

// RecordObservation 与仓库更新同事务提交。同一档位和状态不重复写入。
func RecordObservation(tx *gorm.DB, repo Repository, state, grade, reason string, at int64, evidence metrics.ObservationEvidence) error {
	if state == repo.State && grade == repo.Grade {
		return nil
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return tx.Create(&ObservationTransition{RepoID: repo.RepoID, OccurredAt: at,
		FromState: repo.State, ToState: state, FromGrade: repo.Grade, ToGrade: grade,
		Reason: reason, EvidenceJSON: string(data)}).Error
}

// UpdateObservationJobs 撤销不再适用的自动采样，或同步已排队任务的档位优先级。
func UpdateObservationJobs(tx *gorm.DB, id, at int64, cancel bool, priority int) error {
	jobs := tx.Model(&Job{}).Where("repo_id = ? AND type = ? AND dedup_key LIKE ?", id, consts.JobRefreshRepo, consts.AutoJobPattern).
		Where("(status IN ? OR (status = ? AND lease_until <= ?))",
			[]string{consts.StatusPending, consts.StatusFailed}, consts.StatusRunning, at)
	if cancel {
		return jobs.Updates(map[string]any{"status": consts.StatusCanceled, "last_error_code": "observation_changed",
			"updated_at": at, "lease_token": nil, "lease_until": nil}).Error
	}
	return jobs.Update("priority", priority).Error
}

// SamplingPriority 保持定时采样及排队任务的优先级口径一致。
func SamplingPriority(grade string) int {
	switch grade {
	case consts.GradeS:
		return 100
	case consts.GradeA:
		return 80
	case consts.GradeB:
		return 40
	case consts.GradeC:
		return 20
	}
	return 0
}
