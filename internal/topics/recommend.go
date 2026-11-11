package topics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
	"github.com/lifei6671/github-radar/internal/consts"
	"github.com/lifei6671/github-radar/internal/metrics"
	"github.com/lifei6671/github-radar/internal/storage"
	"github.com/lifei6671/github-radar/internal/summary"
)

const dayKey = "topics.recommendation_day"
const signalAge = 24 * time.Hour

type input struct {
	storage.Repository
	Hotness      *float64
	Growth       *int64
	TrendingRank *int
	Persistence  *float64
	Summary      string
	Features     string
	Fingerprint  *string
	SourceURL    *string
}

type option struct {
	input
	previous storage.TopicCandidate
	score    float64
	groups   [4]bool
}

// Generate 每个配置时区的自然日只发布一轮选题，来源不同的候选轮流入池。
func Generate(ctx context.Context, db *gorm.DB, cfg config.Recommendations, now time.Time) error {
	if !cfg.Enabled {
		return nil
	}
	location, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return err
	}
	local := now.In(location)
	if local.Hour()*60+local.Minute() < cfg.Hour*60+cfg.Minute {
		return nil
	}
	day := cfg.Timezone + "/" + local.Format(time.DateOnly)
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var computed storage.MaintenanceState
		// 停机恢复时等计算 Worker 发布有效信号，再写当天的选题完成水位。
		if err := tx.Find(&computed, storage.MaintenanceStateColumns.Key+"=?", metrics.ComputeKey).Error; err != nil ||
			computed.Key == "" || computed.UpdatedAt < now.Add(-signalAge).UnixMilli() {
			return err
		}
		var marker storage.MaintenanceState
		if err := tx.Find(&marker, storage.MaintenanceStateColumns.Key+"=?", dayKey).Error; err != nil ||
			(strings.HasPrefix(marker.Value, cfg.Timezone+"/") && marker.Value >= day) {
			return err
		}
		rows, err := recommendable(tx, cfg, now)
		if err != nil {
			return err
		}
		choose := selectTopics(rows, cfg.Limit)
		for _, pick := range choose {
			if err := save(tx, rows[pick.index], pick.group, now.UnixMilli()); err != nil {
				return err
			}
		}
		marker = storage.MaintenanceState{Key: dayKey, Value: day, UpdatedAt: now.UnixMilli()}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&marker).Error
	})
}

func recommendable(tx *gorm.DB, cfg config.Recommendations, now time.Time) ([]option, error) {
	var rows []input
	if err := tx.Raw(repoSignalsSQL+fmt.Sprintf(`
WHERE r.%[1]s='%[2]s' AND r.%[3]s=0 AND r.%[4]s=0 AND r.%[5]s=1
  AND (r.%[6]s=1 OR trim(coalesce(r.%[7]s,''))<>'' OR s.%[8]s IS NOT NULL)
  AND (r.%[6]s=1 OR m.%[9]s>0 OR m.%[10]s>0 OR m.%[11]s>0)`,
		storage.RepositoryColumns.State, consts.StatusActive, storage.RepositoryColumns.Disabled, storage.RepositoryColumns.Archived, storage.RepositoryColumns.NameIsCurrent, storage.RepositoryColumns.StarredByUser, storage.RepositoryColumns.Description, storage.AISummaryColumns.RepoID, storage.LatestMetricColumns.Delta24h, storage.LatestMetricColumns.TrendingScore, storage.LatestMetricColumns.PersistenceScore), now.Add(-signalAge).UnixMilli()).Scan(&rows).Error; err != nil {
		return nil, err
	}
	var existing []storage.TopicCandidate
	if err := tx.Where(storage.TopicCandidateColumns.AngleKey + "='" + consts.TopicAngleProject + "'").Find(&existing).Error; err != nil {
		return nil, err
	}
	byRepo := make(map[int64]storage.TopicCandidate, len(existing))
	for _, row := range existing {
		byRepo[row.RepoID] = row
	}
	options := make([]option, 0, len(rows))
	for _, row := range rows {
		prev := byRepo[row.RepoID]
		if prev.ID != 0 && prev.Status != consts.StatusCandidate && !materialChange(prev, row, cfg.ReentryGrowth) {
			continue
		}
		groups := [4]bool{row.Growth != nil && *row.Growth > 0, row.TrendingRank != nil,
			row.Persistence != nil && *row.Persistence > 0, row.StarredByUser}
		if !slices.Contains(groups[:], true) {
			continue
		}
		options = append(options, option{row, prev, topicScore(row, now), groups})
	}
	return options, nil
}

var repoSignalsSQL = fmt.Sprintf(`
SELECT r.*, m.%[1]s AS hotness,
 CASE WHEN json_extract(m.%[2]s,'$."%[21]s".quality')='%[3]s' THEN m.%[4]s END AS growth,
 CASE WHEN m.%[5]s>0 THEN m.%[6]s END AS trending_rank,m.%[7]s AS persistence,
 s.%[8]s AS summary,s.%[9]s AS features,s.%[10]s AS fingerprint,s.%[11]s AS source_url
FROM %[12]s r LEFT JOIN %[13]s m ON m.%[14]s=r.%[15]s AND m.%[16]s>=?
LEFT JOIN %[17]s s ON s.%[18]s=r.%[15]s AND s.%[19]s='%[20]s'
`,
	storage.LatestMetricColumns.HotnessScore, storage.LatestMetricColumns.WindowDetailsJSON, consts.QualityExact, storage.LatestMetricColumns.Delta24h,
	storage.LatestMetricColumns.TrendingScore, storage.LatestMetricColumns.TrendingRank, storage.LatestMetricColumns.PersistenceScore,
	storage.AISummaryColumns.SummaryZh, storage.AISummaryColumns.FeaturesJSON, storage.AISummaryColumns.SourceFingerprint, storage.AISummaryColumns.SourceReadmeURL,
	storage.RepositoryTable, storage.LatestMetricTable, storage.LatestMetricColumns.RepoID, storage.RepositoryColumns.RepoID, storage.LatestMetricColumns.CalculatedAt,
	storage.AISummaryTable, storage.AISummaryColumns.RepoID, storage.AISummaryColumns.SummaryStatus, consts.StatusReady, consts.Window24h)

func materialChange(previous storage.TopicCandidate, row input, threshold int) bool {
	// 已处理项目需要已确认的 README 变动或精确净增的明显抬升。
	if previous.SourceFingerprint != nil && row.Fingerprint != nil &&
		strings.HasPrefix(*previous.SourceFingerprint, consts.ReadmeFingerprintPrefix) && strings.HasPrefix(*row.Fingerprint, consts.ReadmeFingerprintPrefix) &&
		*row.Fingerprint != *previous.SourceFingerprint && readerValue(row) != previous.ReaderValue {
		return true
	}
	before := int64(0)
	if previous.Growth24h != nil {
		before = max(*previous.Growth24h, 0)
	}
	return row.Growth != nil && *row.Growth-before >= int64(threshold)
}

func topicScore(row input, now time.Time) float64 {
	var features []string
	_ = json.Unmarshal([]byte(row.Features), &features)
	// 评分基于可见证据的启发式信号，缺少实测时保持中性值。
	value := 25.0 + float64(min(len(features), 5))*10
	if row.StarredByUser {
		value += 20
	}
	demo, repeat, evidence, novel := 25.0, 20.0, 20.0, 30.0
	if row.SourceURL != nil {
		demo += 25
		repeat += 20
		evidence += 35
	}
	if row.LicenseSPDXID != nil && *row.LicenseSPDXID != "" && *row.LicenseSPDXID != "NOASSERTION" {
		repeat += 25
	}
	if row.Summary != "" {
		demo += 20
		evidence += 25
	}
	if row.Description != nil && strings.TrimSpace(*row.Description) != "" {
		value += 15
		evidence += 10
	}
	if row.DiscoveredAt >= now.AddDate(0, 0, -7).UnixMilli() {
		novel += 35
	}
	if row.GitHubPushedAt != nil && *row.GitHubPushedAt >= now.AddDate(0, 0, -30).UnixMilli() {
		novel += 25
	}
	return .35*min(value, 100) + .25*demo + .20*repeat + .10*novel + .10*evidence
}

type selection struct{ index, group int }

func selectTopics(rows []option, limit int) []selection {
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		left, right := combined(rows[a]), combined(rows[b])
		if left > right {
			return -1
		}
		if left < right {
			return 1
		}
		if rows[a].RepoID < rows[b].RepoID {
			return -1
		}
		if rows[a].RepoID > rows[b].RepoID {
			return 1
		}
		return 0
	})
	picks := make([]selection, 0, limit)
	seen := make(map[int]bool)
	next := [4]int{}
	for len(picks) < limit {
		before := len(picks)
		for group := range next {
			for next[group] < len(order) {
				index := order[next[group]]
				next[group]++
				if seen[index] || !rows[index].groups[group] {
					continue
				}
				seen[index] = true
				picks = append(picks, selection{index, group})
				break
			}
			if len(picks) == limit {
				break
			}
		}
		if before == len(picks) {
			break
		}
	}
	return picks
}

func combined(row option) float64 {
	if row.Hotness == nil {
		return row.score
	}
	hotness := *row.Hotness
	return .4*hotness + .6*row.score
}

func save(tx *gorm.DB, row option, group int, now int64) error {
	candidate := row.previous
	if candidate.ID == 0 {
		candidate.RepoID, candidate.AngleKey, candidate.CreatedAt = row.RepoID, consts.TopicAngleProject, now
	}
	candidate.Status, candidate.UpdatedAt = consts.StatusCandidate, now
	// 摘要待更新时保留上一组来源证据，后续重入判断需要比较同一版本的指纹和功能。
	if row.Fingerprint != nil || candidate.SourceFingerprint == nil {
		candidate.SourceFingerprint = row.Fingerprint
		candidate.TitleZh = summary.Title(row.Summary)
		if candidate.TitleZh == "" {
			candidate.TitleZh = "开源项目 " + row.FullName
		}
		candidate.SourceURL = row.HTMLURL
		if row.SourceURL != nil {
			candidate.SourceURL = *row.SourceURL
		}
		candidate.ReaderValue = readerValue(row.input)
	}
	candidate.Reason = []string{"24 小时精确净增长", "近期进入 GitHub Trending", "近期有可确认的日增长", "管理员已关注", "人工加入选题池"}[group]
	candidate.TopicScore = math.Round(row.score*100) / 100
	candidate.HotnessScore, candidate.Growth24h = row.Hotness, row.Growth
	return tx.Save(&candidate).Error
}

func readerValue(row input) string {
	var features []string
	_ = json.Unmarshal([]byte(row.Features), &features)
	if len(features) > 0 {
		slices.Sort(features)
		return strings.Join(features, "、")
	} else if row.Description != nil {
		runes := []rune(strings.TrimSpace(*row.Description))
		return string(runes[:min(160, len(runes))])
	}
	return ""
}
