package reportstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/reports"
	"github.com/lifei6671/github-radar/internal/settings"
	"github.com/lifei6671/github-radar/internal/storage"
)

type Editorial struct {
	Kind              reports.Kind `gorm:"primaryKey"`
	IssueKey          string       `gorm:"primaryKey"`
	IssueNumber       *int
	PublicationStatus string
	CoverageDays      int
	Fingerprint       string
	Headline          string
	Lead              string
	StoriesJSON       string
	RepoIDsJSON       string // 所有送给模型的证据，用于公开可见性校验。
	SourceIssues      int
	ObservedFrom      int64
	ObservedTo        int64
	GeneratedAt       int64
}

func (Editorial) TableName() string { return "report_editorials" }

var ErrEvidenceChanged = errors.New("组稿证据已变化")

type evidenceKey struct {
	IssueDate  string
	RepoID     int64
	ObservedAt int64
}

func evidenceKeys(db *gorm.DB) *gorm.DB {
	return publicItems(db).Select("daily_report_items.issue_date, daily_report_items.repo_id, daily_report_items.observed_at").
		Order("daily_report_items.issue_date DESC, daily_report_items.repo_id")
}

func addEvidence(digest hash.Hash, item evidenceKey) {
	fmt.Fprintf(digest, "%s:%d\n", item.IssueDate, item.RepoID)
}

func editorialDigest() hash.Hash {
	digest := sha256.New()
	fmt.Fprintf(digest, "%d\n", reports.EditorVersion)
	return digest
}

type evidenceWindow struct {
	Rows []evidenceKey
	Days int
}

func publicationRules(ctx context.Context, db *gorm.DB) (config.ReportPublication, error) {
	snapshot, err := (settings.Store{DB: db}).Get(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return config.DefaultReportPublication(), nil
	}
	return snapshot.Settings.ReportPublication, err
}

func makePlan(kind reports.Kind, key string, rows []evidenceKey, now time.Time, rules config.ReportPublication) (reports.Plan, evidenceWindow) {
	digest := editorialDigest()
	dates := make(map[string]struct{})
	for _, item := range rows {
		addEvidence(digest, item)
		dates[reports.ObservationDate(item.ObservedAt)] = struct{}{}
	}
	plan := reports.Plan{Kind: kind, Key: key}
	if kind != reports.Daily {
		plan.Status = kind.PublicationStatus(key, len(dates), now, rules)
		fmt.Fprintf(digest, "period-v2:%s\n", plan.Status)
	}
	plan.Fingerprint = fmt.Sprintf("%x", digest.Sum(nil))
	return plan, evidenceWindow{Rows: rows, Days: len(dates)}
}

// Plans 按实际观测时间划分周/月，刊物状态也是证据版本的一部分。
func (s Store) Plans(ctx context.Context, now time.Time) ([]reports.Plan, error) {
	var keys []evidenceKey
	if err := evidenceKeys(s.DB.WithContext(ctx)).Where("daily_report_items.observed_at <= ?", now.UnixMilli()).Scan(&keys).Error; err != nil {
		return nil, err
	}
	rules, err := publicationRules(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	type period struct {
		Kind reports.Kind
		Key  string
	}
	windows := make(map[period][]evidenceKey)
	order := make([]period, 0)
	for _, item := range keys {
		for _, kind := range []reports.Kind{reports.Daily, reports.Weekly, reports.Monthly} {
			key := period{kind, item.IssueDate}
			if kind != reports.Daily {
				key.Key = kind.Key(time.UnixMilli(item.ObservedAt))
			}
			if len(windows[key]) == 0 {
				order = append(order, key)
			}
			windows[key] = append(windows[key], item)
		}
	}
	plans := make([]reports.Plan, 0, len(order))
	for _, key := range order {
		plan, _ := makePlan(key.Kind, key.Key, windows[key], now, rules)
		plans = append(plans, plan)
	}
	return plans, nil
}

func (s Store) matchEvidence(ctx context.Context, plan reports.Plan, now time.Time) (evidenceWindow, error) {
	from, to, valid := plan.Kind.Bounds(plan.Key)
	if !valid || plan.Fingerprint == "" {
		return evidenceWindow{}, ErrEvidenceChanged
	}
	var keys []evidenceKey
	query := evidenceKeys(s.DB.WithContext(ctx)).Where("daily_report_items.observed_at <= ?", now.UnixMilli())
	if plan.Kind == reports.Daily {
		query = query.Where("daily_report_items.issue_date=?", plan.Key)
	} else {
		query = query.Where("daily_report_items.observed_at >= ? AND daily_report_items.observed_at < ?", from.UnixMilli(), to.UnixMilli())
	}
	if err := query.Scan(&keys).Error; err != nil {
		return evidenceWindow{}, err
	}
	var rules config.ReportPublication
	if plan.Kind != reports.Daily {
		var err error
		rules, err = publicationRules(ctx, s.DB)
		if err != nil {
			return evidenceWindow{}, err
		}
	}
	actual, window := makePlan(plan.Kind, plan.Key, keys, now, rules)
	if len(keys) == 0 || actual != plan {
		return evidenceWindow{}, ErrEvidenceChanged
	}
	return window, nil
}

type ManuscriptSource struct {
	Edition
	SourceIssues, CoverageDays int
	Repositories               []reports.Evidence
}

func (s Store) Evidence(ctx context.Context, plan reports.Plan, now time.Time) (ManuscriptSource, error) {
	var source ManuscriptSource
	var items []PublicItem
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		snapshot := Store{DB: tx}
		window, err := snapshot.matchEvidence(ctx, plan, now)
		if err != nil {
			return err
		}
		source.CoverageDays = window.Days
		if plan.Kind == reports.Daily {
			var edition *Edition
			edition, items, _, _, err = snapshot.Read(ctx, plan.Key, "", 0)
			if err == nil && edition != nil {
				source.Edition, source.SourceIssues = *edition, 1
			}
			return err
		}
		source.ObservedFrom, source.ObservedTo = window.Rows[0].ObservedAt, window.Rows[0].ObservedAt
		issues := make(map[string]struct{})
		for _, item := range window.Rows {
			issues[item.IssueDate] = struct{}{}
			source.ObservedFrom = min(source.ObservedFrom, item.ObservedAt)
			source.ObservedTo = max(source.ObservedTo, item.ObservedAt)
		}
		source.SourceIssues = len(issues)
		items, err = periodProjects(tx, plan.Kind, plan.Key, now)
		return err
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ManuscriptSource{}, err
	}
	for _, item := range items {
		entry := reports.Evidence{ID: item.RepoID, Project: item.FullName, Guide: item.SummaryZh, Rank: item.Rank}
		if plan.Kind != reports.Daily {
			entry.Date = reports.ObservationDate(item.ObservedAt)
		}
		if item.TitleZh != nil {
			entry.Title = *item.TitleZh
		}
		source.Repositories = append(source.Repositories, entry)
	}
	return source, nil
}

// SaveEditorial 在调用台账事务中确认本轮证据仍然有效，保留已有刊号。
func (s Store) SaveEditorial(ctx context.Context, plan reports.Plan, source ManuscriptSource, edition reports.Editorial, sent []reports.Evidence, now time.Time) error {
	_, err := s.matchEvidence(ctx, plan, now)
	if err != nil {
		return err
	}
	db := s.DB.WithContext(ctx)
	stories, err := json.Marshal(edition.Stories)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(sent))
	for _, item := range sent {
		ids = append(ids, item.ID)
	}
	evidenceIDs, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	var previous Editorial
	if err := db.Find(&previous, "kind=? AND issue_key=?", plan.Kind, plan.Key).Error; err != nil {
		return err
	}
	status := plan.PublicationStatus()
	if status == reports.Preview && (!edition.SafePreview() || previous.PublicationStatus == reports.Published) {
		return ErrEvidenceChanged
	}
	number := previous.IssueNumber
	if status == reports.Published && number == nil {
		n := source.IssueNumber
		if plan.Kind != reports.Daily {
			if err := db.Model(&Editorial{}).Where("kind=? AND publication_status=?", plan.Kind, reports.Published).
				Select("COALESCE(max(issue_number),0)+1").Scan(&n).Error; err != nil {
				return err
			}
		}
		number = &n
	}
	row := Editorial{Kind: plan.Kind, IssueKey: plan.Key, IssueNumber: number, Fingerprint: plan.Fingerprint,
		Headline: edition.Headline, Lead: edition.Lead, StoriesJSON: string(stories), RepoIDsJSON: string(evidenceIDs),
		PublicationStatus: status, CoverageDays: source.CoverageDays, SourceIssues: source.SourceIssues,
		ObservedFrom: source.ObservedFrom, ObservedTo: source.ObservedTo, GeneratedAt: now.UnixMilli()}
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

type Citation struct {
	RepoID   int64  `json:"repoId"`
	FullName string `json:"fullName"`
	HTMLURL  string `json:"htmlUrl" gorm:"column:html_url"`
}

type Editorials struct {
	Edition   *Editorial
	Archive   []ArchiveEntry
	Calendar  []string
	Citations []Citation
	Total     int64
}

func (s Store) ReadEditorials(ctx context.Context, kind reports.Kind, date, before string, limit int) (Editorials, error) {
	result := Editorials{Calendar: []string{}}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		visible := func(statuses ...string) *gorm.DB {
			return tx.Model(&Editorial{}).Where("kind=? AND publication_status IN ?", kind, statuses).Where(`NOT EXISTS (
				SELECT 1 FROM json_each(repo_ids_json) AS cited WHERE NOT EXISTS (
					SELECT 1 FROM repositories WHERE repositories.repo_id=cited.value AND ` + storage.PublishableRepositoryCondition + `
				))`)
		}
		if err := visible(reports.Published).Count(&result.Total).Error; err != nil {
			return err
		}
		archive := visible(reports.Published).Select("issue_key AS issue_date, json_array_length(stories_json) AS count, headline").
			Order("issue_key DESC").Limit(limit)
		if before != "" {
			archive = archive.Where("issue_key < ?", before)
		}
		if err := archive.Scan(&result.Archive).Error; err != nil {
			return err
		}
		query := visible(reports.Published, reports.Preview).Order("issue_key DESC")
		if date != "" {
			query = query.Where("issue_key=?", date)
		}
		var edition Editorial
		if err := query.Take(&edition).Error; err != nil {
			if date == "" && errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		result.Edition = &edition
		group := strings.Split(edition.IssueKey, "-")[0]
		if kind == reports.Daily {
			group = edition.IssueKey[:7]
		}
		if err := visible(reports.Published).Where("issue_key LIKE ?", group+"-%").Order("issue_key").Pluck("issue_key", &result.Calendar).Error; err != nil {
			return err
		}
		return tx.Table("repositories").Select("repo_id, full_name, html_url").
			Where(`repo_id IN (SELECT cited.value FROM json_each(?) AS story,
				json_each(story.value, '$.repoIds') AS cited)`, edition.StoriesJSON).Scan(&result.Citations).Error
	}, &sql.TxOptions{ReadOnly: true})
	return result, err
}
