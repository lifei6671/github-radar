package reportstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/reports"
	"github.com/lifei6671/github-radar/internal/storage"
)

const sectionLimit = 6

const (
	discoveries = "discoveries"
	trending    = "trending"
)

type Store struct{ DB *gorm.DB }

type Edition struct {
	IssueDate    string `gorm:"primaryKey"`
	IssueNumber  int
	WindowEnd    int64
	ObservedFrom int64
	ObservedTo   int64
	GeneratedAt  int64
}

func (Edition) TableName() string { return "daily_reports" }

type Item struct {
	IssueDate    string `gorm:"primaryKey"`
	Position     int    `gorm:"primaryKey;autoIncrement:false"`
	RepoID       int64
	Section      string
	SummaryZh    string
	TitleZh      *string
	ObservedAt   int64
	Rank         *int
	PreviousRank *int
}

func (Item) TableName() string { return "daily_report_items" }

type PublicItem struct {
	Item
	FullName string
	HTMLURL  string `gorm:"column:html_url"`
}

type ArchiveEntry struct {
	IssueDate string
	Count     int
	Headline  string
}

func (s Store) Windows(ctx context.Context, since string) ([]reports.PublishedWindow, error) {
	var rows []Edition
	err := s.DB.WithContext(ctx).Select("issue_date", "window_end").
		Where("issue_date >= ? OR issue_date = (SELECT max(issue_date) FROM daily_reports WHERE issue_date < ?)", since, since).
		Order("issue_date").Find(&rows).Error
	windows := make([]reports.PublishedWindow, 0, len(rows))
	for _, row := range rows {
		windows = append(windows, reports.PublishedWindow{Date: row.IssueDate, End: time.UnixMilli(row.WindowEnd)})
	}
	return windows, err
}

// Publish 一次事务内选稿并保存快照，同一期的并发或重试保持幂等。
func (s Store) Publish(ctx context.Context, window reports.Window) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Edition
		if err := tx.Select("issue_date").Find(&existing, "issue_date = ?", window.Date).Error; err != nil || existing.IssueDate != "" {
			return err
		}
		from, to := window.Start.UnixMilli(), window.End.UnixMilli()
		official, err := trendingItems(tx, from, to)
		if err != nil {
			return err
		}
		seen := make([]int64, 0, len(official))
		for _, item := range official {
			seen = append(seen, item.RepoID)
		}
		newProjects := eligible(tx).Select("repositories.repo_id, ai.summary_zh, ai.title_zh, repositories.discovered_at AS observed_at").
			Where("repositories.discovered_at >= ? AND repositories.discovered_at < ?", from, to)
		if len(seen) > 0 {
			newProjects = newProjects.Where("repositories.repo_id NOT IN ?", seen)
		}
		var recent []Item
		if err := newProjects.Order("repositories.discovered_at DESC, repositories.stars DESC, repositories.repo_id").
			Limit(sectionLimit).Scan(&recent).Error; err != nil {
			return err
		}
		for i := range recent {
			recent[i].Section = discoveries
		}
		entries := append(official, recent...)
		if len(entries) == 0 {
			return nil
		}
		edition := Edition{IssueDate: window.Date, WindowEnd: to, GeneratedAt: window.GeneratedAt.UnixMilli(),
			ObservedFrom: entries[0].ObservedAt, ObservedTo: entries[0].ObservedAt}
		for i := range entries {
			entries[i].IssueDate, entries[i].Position = window.Date, i+1
			edition.ObservedFrom = min(edition.ObservedFrom, entries[i].ObservedAt)
			edition.ObservedTo = max(edition.ObservedTo, entries[i].ObservedAt)
		}
		var observed struct{ First, Last *int64 }
		if err := tx.Raw(`
SELECT min(observed_at) AS first, max(observed_at) AS last FROM (
 SELECT min(observed_at) AS observed_at FROM search_observations WHERE observed_at >= ? AND observed_at < ?
 UNION ALL SELECT max(observed_at) FROM search_observations WHERE observed_at >= ? AND observed_at < ?
 UNION ALL SELECT observed_at FROM trending_batches WHERE source_key = ? AND window = ? AND observed_at >= ? AND observed_at < ?
)`, from, to, from, to, consts.TrendingAll, consts.PeriodDaily, from, to).Scan(&observed).Error; err != nil {
			return err
		}
		if observed.First != nil && observed.Last != nil {
			edition.ObservedFrom = min(edition.ObservedFrom, *observed.First)
			edition.ObservedTo = max(edition.ObservedTo, *observed.Last)
		}
		if err := tx.Model(&Edition{}).Select("COALESCE(max(issue_number),0)+1").Scan(&edition.IssueNumber).Error; err != nil {
			return err
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&edition)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		return tx.Create(&entries).Error
	})
}

func eligible(db *gorm.DB) *gorm.DB {
	return db.Table(storage.RepositoryTable).Joins("JOIN ai_summaries ai ON ai.repo_id = repositories.repo_id").
		Where(storage.PublishableRepositoryCondition)
}

func trendingItems(tx *gorm.DB, from, to int64) ([]Item, error) {
	var batch storage.TrendingBatchRecord
	batches := func() *gorm.DB {
		return tx.Model(&storage.TrendingBatchRecord{}).Where("source_key = ? AND window = ?", consts.TrendingAll, consts.PeriodDaily).
			Order("observed_at DESC, observed_slot DESC").Limit(1)
	}
	if err := batches().Where("observed_at >= ? AND observed_at < ?", from, to).Find(&batch).Error; err != nil || batch.SourceKey == "" {
		return nil, err
	}
	priorSlot := batches().Select("observed_slot").Where("observed_at < ?", from)
	var items []Item
	err := eligible(tx).
		Select("repositories.repo_id, ai.summary_zh, ai.title_zh, ? AS section, ? AS observed_at, t.rank, prior.rank AS previous_rank", trending, batch.ObservedAt).
		Joins("JOIN trending_observations t ON t.repo_id = repositories.repo_id AND t.source_key = ? AND t.window = ? AND t.observed_slot = ?",
			batch.SourceKey, batch.Window, batch.ObservedSlot).
		Joins("LEFT JOIN trending_observations prior ON prior.repo_id = t.repo_id AND prior.source_key = t.source_key AND prior.window = t.window AND prior.observed_slot = (?)",
			priorSlot).
		Order("t.rank, repositories.repo_id").Limit(sectionLimit).Scan(&items).Error
	return items, err
}

func publicItems(db *gorm.DB) *gorm.DB {
	return db.Table("daily_report_items").Joins("JOIN repositories ON repositories.repo_id = daily_report_items.repo_id").
		Where(storage.PublishableRepositoryCondition)
}

// Read 在同一次读取中返回正文和往期索引，实时沿用公开仓库门槛。
func (s Store) Read(ctx context.Context, date, before string, limit int) (edition *Edition, items []PublicItem, archive []ArchiveEntry, total int64, err error) {
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := publicItems(tx).Distinct("daily_report_items.issue_date").Count(&total).Error; err != nil {
			return err
		}
		// SQLite 在单个 min 聚合旁选择同一行的列，position 即该期仍可见的第一条。
		index := publicItems(tx).Select("daily_report_items.issue_date, count(*) AS count, min(daily_report_items.position), COALESCE(NULLIF(daily_report_items.title_zh,''),repositories.full_name) AS headline").
			Group("daily_report_items.issue_date").Order("daily_report_items.issue_date DESC").Limit(limit)
		if before != "" {
			index = index.Where("daily_report_items.issue_date < ?", before)
		}
		if err := index.Scan(&archive).Error; err != nil {
			return err
		}
		visible := publicItems(tx).Select("1").Where("daily_report_items.issue_date = daily_reports.issue_date")
		query := tx.Where("EXISTS (?)", visible).Order("issue_date DESC")
		if date != "" {
			query = query.Where("issue_date = ?", date)
		}
		var saved Edition
		if err := query.Take(&saved).Error; err != nil {
			if date == "" && errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		edition = &saved
		return publicItems(tx).Select("daily_report_items.*, repositories.full_name, repositories.html_url").
			Where("daily_report_items.issue_date = ?", saved.IssueDate).Order("daily_report_items.position").Scan(&items).Error
	}, &sql.TxOptions{ReadOnly: true})
	return
}
