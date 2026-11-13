package metricstore

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	domain "github.com/lifei6671/github-radar/internal/domain/metrics"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
)

type Store struct{ DB *gorm.DB }

type transaction struct{ db *gorm.DB }

func (s Store) Transaction(ctx context.Context, work func(metrics.Transaction) error) error {
	return s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		return work(transaction{db: db})
	})
}

func (r transaction) ComputeMarker() (string, int64, bool, error) {
	var marker storage.MaintenanceState
	err := r.db.Find(&marker, storage.MaintenanceStateColumns.Key+" = ?", metrics.ComputeKey).Error
	return marker.Value, marker.UpdatedAt, marker.Key != "", err
}

func (r transaction) ActiveRepositories() ([]domain.Repository, error) {
	c := storage.RepositoryColumns
	var rows []storage.Repository
	err := r.db.Select(c.RepoID, c.Grade, c.BelowGradeSince, c.FirstNotFoundAt, c.NextFetchAt).
		Where(c.State+" = ?", consts.StatusActive).Order(c.RepoID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	repos := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repos = append(repos, domain.Repository{ID: row.RepoID, Grade: row.Grade, BelowGradeSince: row.BelowGradeSince,
			FirstNotFoundAt: row.FirstNotFoundAt, NextFetchAt: row.NextFetchAt})
	}
	return repos, nil
}

func (r transaction) Publish(slot string, asOf int64, calculations []domain.Calculation, grades []domain.GradeChange) error {
	rows := make([]storage.LatestMetric, 0, len(calculations))
	for _, result := range calculations {
		windows, err := json.Marshal(result.Windows)
		if err != nil {
			return err
		}
		rows = append(rows, storage.LatestMetric{
			RepoID: result.RepoID, CalculatedAt: result.CalculatedAt, WindowDetailsJSON: string(windows),
			Delta1h: result.Delta1h, Delta24h: result.Delta24h, Delta7d: result.Delta7d, Delta30d: result.Delta30d,
			RelativeGrowth: result.RelativeGrowth, Acceleration: result.Acceleration, PersistenceScore: result.PersistenceScore,
			DailyConcentration: result.DailyConcentration, HotnessScore: result.HotnessScore, CandidateBurst: result.CandidateBurst,
			TrendingFrequency: result.TrendingFrequency, TrendingStreak: result.TrendingStreak, TrendingRank: result.TrendingRank,
			TrendingScore: result.TrendingScore, DataQuality: result.DataQuality,
		})
	}
	if len(rows) > 0 {
		if err := r.db.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(&rows, 100).Error; err != nil {
			return err
		}
	}
	c := storage.RepositoryColumns
	for _, grade := range grades {
		updates := map[string]any{c.Grade: grade.Grade, c.BelowGradeSince: grade.BelowGradeSince, c.UpdatedAt: asOf}
		if grade.NextFetchAt != nil {
			updates[c.NextFetchAt] = *grade.NextFetchAt
		}
		if err := r.db.Model(&storage.Repository{}).Where(c.RepoID+" = ?", grade.RepoID).Updates(updates).Error; err != nil {
			return err
		}
	}
	inactive := r.db.Model(&storage.Repository{}).Select(c.RepoID).Where(c.State+" <> ?", consts.StatusActive)
	if err := r.db.Where(storage.LatestMetricColumns.RepoID+" IN (?)", inactive).Delete(&storage.LatestMetric{}).Error; err != nil {
		return err
	}
	marker := storage.MaintenanceState{Key: metrics.ComputeKey, Value: slot, UpdatedAt: asOf}
	return r.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
}
