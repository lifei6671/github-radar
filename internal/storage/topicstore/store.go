package topicstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

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
	Hotness             *float64
	Growth              *int64
	TrendingRank        *int
	Persistence         *float64
	TitleZh             string
	Features            string
	Audience            string
	BriefJSON           string
	Fingerprint         *string
	ObservedFingerprint *string
	PreviousBriefJSON   string
	SourceURL           *string
	ReleaseKey          string
	ReleaseObservedAt   int64
	UnseenRelease       bool
}

func (row input) signals() domain.Signals {
	var features []string
	_ = json.Unmarshal([]byte(row.Features), &features)
	var audience []string
	_ = json.Unmarshal([]byte(row.Audience), &audience)
	var brief domain.Brief
	_ = json.Unmarshal([]byte(row.BriefJSON), &brief)
	// 新摘要尚未就绪时保留上次识别的风险，实测仍绑定最近观测到的来源。
	if row.BriefJSON == "" && row.PreviousBriefJSON != "" {
		var previous domain.Brief
		_ = json.Unmarshal([]byte(row.PreviousBriefJSON), &previous)
		brief.Risks = previous.Risks
	}
	return domain.Signals{
		RepoID: row.RepoID, FullName: row.FullName, HTMLURL: row.HTMLURL, StarredByUser: row.StarredByUser,
		Description: row.Description, DiscoveredAt: row.DiscoveredAt, GitHubPushedAt: row.GitHubPushedAt,
		Hotness: row.Hotness, Persistence: row.Persistence, Growth: row.Growth, TrendingRank: row.TrendingRank,
		TitleZh: row.TitleZh, Features: features, Audience: audience, Brief: brief,
		Fingerprint: row.Fingerprint, SourceURL: row.SourceURL, ReleaseKey: row.ReleaseKey, ReleaseObservedAt: row.ReleaseObservedAt,
		UnseenRelease: row.UnseenRelease, GitHubCreatedAt: row.GitHubCreatedAt, ObservedFingerprint: row.ObservedFingerprint,
	}
}

func (t transaction) Recommendable(minSignalAt int64) ([]domain.Signals, map[int64]domain.Candidate, error) {
	tx := t.db
	var rows []input
	releases, err := releaseEvidence(tx)
	if err != nil {
		return nil, nil, err
	}
	var withRelease []int64
	for id := range releases {
		withRelease = append(withRelease, id)
	}
	if err := tx.Raw("SELECT * FROM ("+repoSignalsSQL+fmt.Sprintf(`
WHERE r.%[1]s IN ('active','paused','candidate') AND r.%[3]s=0 AND r.%[4]s=0 AND r.%[5]s=1
  AND (r.%[6]s=1 OR trim(coalesce(r.%[7]s,''))<>'' OR s.%[8]s IS NOT NULL)
  AND r.blocked=0 AND r.first_not_found_at IS NULL
) AS candidates WHERE (state='%[2]s' AND (starred_by_user=1 OR growth>0 OR trending_rank IS NOT NULL OR persistence>0)) OR repo_id IN ?`,
		storage.RepositoryColumns.State, consts.StatusActive, storage.RepositoryColumns.Disabled, storage.RepositoryColumns.Archived, storage.RepositoryColumns.NameIsCurrent, storage.RepositoryColumns.StarredByUser, storage.RepositoryColumns.Description, storage.AISummaryColumns.RepoID), minSignalAt, withRelease).Scan(&rows).Error; err != nil {
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
	seen, err := reviewedReleases(tx)
	if err != nil {
		return nil, nil, err
	}

	signals := make([]domain.Signals, 0, len(rows))
	for _, row := range rows {
		previous := byRepo[row.RepoID]
		row.applyRelease(releases[row.RepoID], previous.ReleaseKey, seen[previous.ID])
		if row.State != consts.StatusActive && !row.UnseenRelease {
			continue
		}
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
	return Signals(t.db, id, minSignalAt)
}

// Signals 读取一次当前项目的已有摘要和可信增长证据。
func Signals(db *gorm.DB, id, minSignalAt int64) (domain.Signals, error) {
	var row input
	result := db.Raw(repoSignalsSQL+"WHERE r."+storage.RepositoryColumns.RepoID+"=?", minSignalAt, id).Scan(&row)
	if result.Error != nil {
		return domain.Signals{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.Signals{}, gorm.ErrRecordNotFound
	}
	releases, err := releaseEvidence(db.Where("e.repo_id = ?", id))
	if err != nil {
		return domain.Signals{}, err
	}
	var previous storage.TopicCandidate
	if err := db.Find(&previous, "repo_id = ? AND angle_key = ?", id, consts.TopicAngleProject).Error; err != nil {
		return domain.Signals{}, err
	}
	seen, err := reviewedReleases(db.Where("topic_id = ?", previous.ID))
	if err != nil {
		return domain.Signals{}, err
	}
	row.applyRelease(releases[id], previous.ReleaseKey, seen[previous.ID])
	return row.signals(), nil
}

type releaseSignal struct {
	key string
	at  int64
}

func (row *input) applyRelease(releases []releaseSignal, current string, seen map[string]bool) {
	for _, release := range releases {
		if release.key != current && seen[release.key] {
			continue
		}
		row.ReleaseKey, row.ReleaseObservedAt, row.UnseenRelease = release.key, release.at, release.key != current
		return
	}
	row.ReleaseKey = current
}

func reviewedReleases(db *gorm.DB) (map[int64]map[string]bool, error) {
	var rows []storage.TopicReviewHistory
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[int64]map[string]bool)
	for _, row := range rows {
		var entry domain.Candidate
		if err := json.Unmarshal([]byte(row.SnapshotJSON), &entry); err != nil {
			return nil, err
		}
		if entry.ReleaseKey == "" {
			continue
		}
		if result[row.TopicID] == nil {
			result[row.TopicID] = map[string]bool{}
		}
		result[row.TopicID][entry.ReleaseKey] = true
	}
	return result, nil
}

// releaseEvidence 按规范地址去重，保留最早观测日期。
func releaseEvidence(db *gorm.DB) (map[int64][]releaseSignal, error) {
	var rows []struct {
		RepoID              int64
		SourceURL, FullName string
		FirstObservedAt     int64
	}
	err := db.Table("repo_discovery_events e").Select("e.repo_id, e.source_url, r.full_name, e.first_observed_at").
		Joins("JOIN repositories r ON r.repo_id = e.repo_id").
		Where("e.source_url LIKE ?", "%/releases/tag/%").Order("e.first_observed_at, e.source_key").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make(map[int64][]releaseSignal)
	seen := make(map[int64]map[string]bool)
	for _, row := range rows {
		if release := releaseURL(row.FullName, row.SourceURL); release != "" {
			if seen[row.RepoID] == nil {
				seen[row.RepoID] = make(map[string]bool)
			}
			if seen[row.RepoID][release] {
				continue
			}
			seen[row.RepoID][release] = true
			result[row.RepoID] = append(result[row.RepoID], releaseSignal{release, row.FirstObservedAt})
		}
	}
	for _, releases := range result {
		slices.Reverse(releases)
	}
	return result, nil
}

func releaseURL(name, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Host, "github.com") || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	path := strings.SplitN(strings.Trim(strings.TrimSuffix(u.Path, "/"), "/"), "/", 5)
	if len(path) != 5 || !strings.EqualFold(path[0]+"/"+path[1], name) || path[2] != "releases" || path[3] != "tag" || path[4] == "" {
		return ""
	}
	return "https://github.com/" + strings.ToLower(name) + "/releases/tag/" + url.PathEscape(path[4])
}

func (t transaction) Save(candidate domain.Candidate) (domain.Candidate, error) {
	row := storage.TopicCandidate(candidate)
	if row.ID != 0 && row.Status == consts.StatusCandidate {
		var previous storage.TopicCandidate
		if err := t.db.Take(&previous, "id = ?", row.ID).Error; err != nil {
			return domain.Candidate{}, err
		}
		if previous.Status != consts.StatusCandidate || previous.ScoreVersion < row.ScoreVersion {
			if err := Archive(t.db, previous, row.UpdatedAt); err != nil {
				return domain.Candidate{}, err
			}
		}
	}
	err := t.db.Save(&row).Error
	return domain.Candidate(row), err
}

// Archive 在同一个写事务保留旧版评分、发布状态和复核结果。
func Archive(db *gorm.DB, previous storage.TopicCandidate, at int64) error {
	snapshot, err := json.Marshal(domain.Candidate(previous))
	if err != nil {
		return err
	}
	history := storage.TopicReviewHistory{TopicID: previous.ID, ArchivedAt: at, SnapshotJSON: string(snapshot)}
	return db.Create(&history).Error
}

func (t transaction) MarkReviewed(id, at int64) error {
	return t.db.Model(&storage.TopicCandidate{}).Where(storage.TopicCandidateColumns.ID+"=?", id).
		Update(storage.TopicCandidateColumns.ReviewedAt, at).Error
}

var repoSignalsSQL = fmt.Sprintf(`
SELECT r.*, m.%[1]s AS hotness,
 CASE WHEN json_extract(m.%[2]s,'$."%[21]s".quality')='%[3]s' THEN m.%[4]s END AS growth,
 CASE WHEN m.%[5]s>0 THEN m.%[6]s END AS trending_rank,m.%[7]s AS persistence,
 s.%[22]s AS title_zh,s.%[9]s AS features,s.%[10]s AS fingerprint,s.%[11]s AS source_url,
 s.audience_json AS audience, s.editorial_brief_json AS brief_json,
 NULLIF(checks.source_fingerprint, '') AS observed_fingerprint, previous.editorial_brief_json AS previous_brief_json
FROM %[12]s r LEFT JOIN %[13]s m ON m.%[14]s=r.%[15]s AND m.%[16]s>=?
LEFT JOIN %[17]s s ON s.%[18]s=r.%[15]s AND s.%[19]s='%[20]s'
LEFT JOIN %[17]s previous ON previous.%[18]s=r.%[15]s
LEFT JOIN summary_checks checks ON checks.repo_id=r.%[15]s
`,
	storage.LatestMetricColumns.HotnessScore, storage.LatestMetricColumns.WindowDetailsJSON, consts.QualityExact, storage.LatestMetricColumns.Delta24h,
	storage.LatestMetricColumns.TrendingScore, storage.LatestMetricColumns.TrendingRank, storage.LatestMetricColumns.PersistenceScore,
	storage.AISummaryColumns.RepoID, storage.AISummaryColumns.FeaturesJSON, storage.AISummaryColumns.SourceFingerprint, storage.AISummaryColumns.SourceReadmeURL,
	storage.RepositoryTable, storage.LatestMetricTable, storage.LatestMetricColumns.RepoID, storage.RepositoryColumns.RepoID, storage.LatestMetricColumns.CalculatedAt,
	storage.AISummaryTable, storage.AISummaryColumns.RepoID, storage.AISummaryColumns.SummaryStatus, consts.StatusReady, consts.Window24h,
	storage.AISummaryColumns.TitleZh)
