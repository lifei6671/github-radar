package topicstore

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/consts"
	domain "github.com/lifei6671/github-radar/internal/domain/topics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/topics"
)

type Store struct{ DB *gorm.DB }

type transaction struct{ db *gorm.DB }

func (s Store) Transaction(ctx context.Context, work func(topics.Transaction) error) error {
	return s.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		return work(transaction{db: db})
	})
}

func (t transaction) Marker(key string) (string, int64, bool, error) {
	var row storage.MaintenanceState
	err := t.db.Find(&row, storage.MaintenanceStateColumns.Key+"=?", key).Error
	return row.Value, row.UpdatedAt, row.Key != "", err
}

func (t transaction) SaveMarker(key, day string, at int64) error {
	marker := storage.MaintenanceState{Key: key, Value: day, UpdatedAt: at}
	return t.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
}

type input struct {
	storage.Repository
	Hotness      *float64
	Growth       *int64
	TrendingRank *int
	Persistence  *float64
	TitleZh      string
	Summary      string
	Features     string
	Fingerprint  *string
	SourceURL    *string
}

func (row input) signals() domain.Signals {
	var features []string
	_ = json.Unmarshal([]byte(row.Features), &features)
	return domain.Signals{
		RepoID: row.RepoID, FullName: row.FullName, HTMLURL: row.HTMLURL, StarredByUser: row.StarredByUser,
		Description: row.Description, LicenseSPDXID: row.LicenseSPDXID, DiscoveredAt: row.DiscoveredAt, GitHubPushedAt: row.GitHubPushedAt,
		Hotness: row.Hotness, Persistence: row.Persistence, Growth: row.Growth, TrendingRank: row.TrendingRank,
		TitleZh: row.TitleZh, Summary: row.Summary, Features: features, Fingerprint: row.Fingerprint, SourceURL: row.SourceURL,
	}
}

func (t transaction) Recommendable(minSignalAt int64) ([]domain.Signals, map[int64]domain.Candidate, error) {
	tx := t.db
	var rows []input
	if err := tx.Raw(repoSignalsSQL+fmt.Sprintf(`
WHERE r.%[1]s='%[2]s' AND r.%[3]s=0 AND r.%[4]s=0 AND r.%[5]s=1
  AND (r.%[6]s=1 OR trim(coalesce(r.%[7]s,''))<>'' OR s.%[8]s IS NOT NULL)
  AND (r.%[6]s=1 OR m.%[9]s>0 OR m.%[10]s>0 OR m.%[11]s>0)`,
		storage.RepositoryColumns.State, consts.StatusActive, storage.RepositoryColumns.Disabled, storage.RepositoryColumns.Archived, storage.RepositoryColumns.NameIsCurrent, storage.RepositoryColumns.StarredByUser, storage.RepositoryColumns.Description, storage.AISummaryColumns.RepoID, storage.LatestMetricColumns.Delta24h, storage.LatestMetricColumns.TrendingScore, storage.LatestMetricColumns.PersistenceScore), minSignalAt).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	var existing []storage.TopicCandidate
	if err := tx.Where(storage.TopicCandidateColumns.AngleKey + "='" + consts.TopicAngleProject + "'").Find(&existing).Error; err != nil {
		return nil, nil, err
	}
	byRepo := make(map[int64]domain.Candidate, len(existing))
	for _, row := range existing {
		byRepo[row.RepoID] = domain.Candidate(row)
	}

	signals := make([]domain.Signals, 0, len(rows))
	for _, row := range rows {
		signals = append(signals, row.signals())
	}
	return signals, byRepo, nil
}

func (t transaction) Previous(id int64) (domain.Candidate, error) {
	var row storage.TopicCandidate
	err := t.db.Find(&row, storage.TopicCandidateColumns.RepoID+"=? AND "+storage.TopicCandidateColumns.AngleKey+"=?", id, consts.TopicAngleProject).Error
	return domain.Candidate(row), err
}

func (t transaction) LoadSignals(id, minSignalAt int64) (domain.Signals, error) {
	var row input
	result := t.db.Raw(repoSignalsSQL+"WHERE r."+storage.RepositoryColumns.RepoID+"=?", minSignalAt, id).Scan(&row)
	if result.Error != nil {
		return domain.Signals{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.Signals{}, gorm.ErrRecordNotFound
	}
	return row.signals(), nil
}

func (t transaction) Save(candidate domain.Candidate) (domain.Candidate, error) {
	row := storage.TopicCandidate(candidate)
	err := t.db.Save(&row).Error
	return domain.Candidate(row), err
}

func (t transaction) MarkReviewed(id, at int64) error {
	return t.db.Model(&storage.TopicCandidate{}).Where(storage.TopicCandidateColumns.ID+"=?", id).
		Update(storage.TopicCandidateColumns.ReviewedAt, at).Error
}

var repoSignalsSQL = fmt.Sprintf(`
SELECT r.*, m.%[1]s AS hotness,
 CASE WHEN json_extract(m.%[2]s,'$."%[21]s".quality')='%[3]s' THEN m.%[4]s END AS growth,
 CASE WHEN m.%[5]s>0 THEN m.%[6]s END AS trending_rank,m.%[7]s AS persistence,
 s.%[22]s AS title_zh,s.%[8]s AS summary,s.%[9]s AS features,s.%[10]s AS fingerprint,s.%[11]s AS source_url
FROM %[12]s r LEFT JOIN %[13]s m ON m.%[14]s=r.%[15]s AND m.%[16]s>=?
LEFT JOIN %[17]s s ON s.%[18]s=r.%[15]s AND s.%[19]s='%[20]s'
`,
	storage.LatestMetricColumns.HotnessScore, storage.LatestMetricColumns.WindowDetailsJSON, consts.QualityExact, storage.LatestMetricColumns.Delta24h,
	storage.LatestMetricColumns.TrendingScore, storage.LatestMetricColumns.TrendingRank, storage.LatestMetricColumns.PersistenceScore,
	storage.AISummaryColumns.SummaryZh, storage.AISummaryColumns.FeaturesJSON, storage.AISummaryColumns.SourceFingerprint, storage.AISummaryColumns.SourceReadmeURL,
	storage.RepositoryTable, storage.LatestMetricTable, storage.LatestMetricColumns.RepoID, storage.RepositoryColumns.RepoID, storage.LatestMetricColumns.CalculatedAt,
	storage.AISummaryTable, storage.AISummaryColumns.RepoID, storage.AISummaryColumns.SummaryStatus, consts.StatusReady, consts.Window24h,
	storage.AISummaryColumns.TitleZh)
