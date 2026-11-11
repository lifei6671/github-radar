package topics

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lifei6671/github-radar/internal/config"
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
		if err := tx.Find(&computed, "key=?", metrics.ComputeKey).Error; err != nil ||
			computed.Key == "" || computed.UpdatedAt < now.Add(-signalAge).UnixMilli() {
			return err
		}
		var marker storage.MaintenanceState
		if err := tx.Find(&marker, "key=?", dayKey).Error; err != nil ||
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
	if err := tx.Raw(repoSignalsSQL+`
WHERE r.state='active' AND r.disabled=0 AND r.archived=0 AND r.name_is_current=1
  AND (r.starred_by_user=1 OR trim(coalesce(r.description,''))<>'' OR s.repo_id IS NOT NULL)
  AND (r.starred_by_user=1 OR m.delta_24h>0 OR m.trending_score>0 OR m.persistence_score>0)`, now.Add(-signalAge).UnixMilli()).Scan(&rows).Error; err != nil {
		return nil, err
	}
	var existing []storage.TopicCandidate
	if err := tx.Where("angle_key='project'").Find(&existing).Error; err != nil {
		return nil, err
	}
	byRepo := make(map[int64]storage.TopicCandidate, len(existing))
	for _, row := range existing {
		byRepo[row.RepoID] = row
	}
	options := make([]option, 0, len(rows))
	for _, row := range rows {
		prev := byRepo[row.RepoID]
		if prev.ID != 0 && prev.Status != "candidate" && !materialChange(prev, row, cfg.ReentryGrowth) {
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

const repoSignalsSQL = `
SELECT r.*, m.hotness_score AS hotness,
 CASE WHEN json_extract(m.window_details_json,'$."24h".quality')='exact' THEN m.delta_24h END AS growth,
 CASE WHEN m.trending_score>0 THEN m.trending_rank END AS trending_rank,m.persistence_score AS persistence,
 s.summary_zh AS summary,s.features_json AS features,s.source_fingerprint AS fingerprint,s.source_readme_url AS source_url
FROM repositories r LEFT JOIN latest_metrics m ON m.repo_id=r.repo_id AND m.calculated_at>=?
LEFT JOIN ai_summaries s ON s.repo_id=r.repo_id AND s.summary_status='ready'
`

func materialChange(previous storage.TopicCandidate, row input, threshold int) bool {
	// 已处理项目需要已确认的 README 变动或精确净增的明显抬升。
	if previous.SourceFingerprint != nil && row.Fingerprint != nil &&
		strings.HasPrefix(*previous.SourceFingerprint, "readme:") && strings.HasPrefix(*row.Fingerprint, "readme:") &&
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
		candidate.RepoID, candidate.AngleKey, candidate.CreatedAt = row.RepoID, "project", now
	}
	candidate.Status, candidate.UpdatedAt = "candidate", now
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
