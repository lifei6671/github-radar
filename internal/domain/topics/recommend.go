package topics

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

type Candidate struct {
	ID                int64
	RepoID            int64
	AngleKey          string
	TitleZh           string
	ReaderValue       string
	SourceURL         string
	SourceFingerprint *string
	Reason            string
	TopicScore        float64
	HotnessScore      *float64
	Growth24h         *int64
	Status            string
	Feedback          *string
	ReviewedAt        *int64
	CreatedAt         int64
	UpdatedAt         int64
}

type Signals struct {
	RepoID                     int64
	FullName, HTMLURL          string
	StarredByUser              bool
	Description, LicenseSPDXID *string
	DiscoveredAt               int64
	GitHubPushedAt             *int64
	Hotness, Persistence       *float64
	Growth                     *int64
	TrendingRank               *int
	TitleZh, Summary           string
	Features                   []string
	Fingerprint, SourceURL     *string
}

type option struct {
	Signals
	previous Candidate
	score    float64
	groups   [4]bool
}

func Manual(row Signals, now time.Time) Candidate {
	candidate := candidateFor(option{Signals: row, score: topicScore(row, now)}, 4, now.UnixMilli())
	reviewedAt := now.UnixMilli()
	candidate.ReviewedAt = &reviewedAt
	return candidate
}

func Recommend(rows []Signals, byRepo map[int64]Candidate, limit, threshold int, now time.Time) []Candidate {
	options := make([]option, 0, len(rows))
	for _, row := range rows {
		prev := byRepo[row.RepoID]
		if prev.ID != 0 && prev.Status != consts.StatusCandidate && !materialChange(prev, row, threshold) {
			continue
		}
		groups := [4]bool{row.Growth != nil && *row.Growth > 0, row.TrendingRank != nil,
			row.Persistence != nil && *row.Persistence > 0, row.StarredByUser}
		if !slices.Contains(groups[:], true) {
			continue
		}
		options = append(options, option{row, prev, topicScore(row, now), groups})
	}
	picks := selectTopics(options, limit)
	result := make([]Candidate, 0, len(picks))
	for _, pick := range picks {
		result = append(result, candidateFor(options[pick.index], pick.group, now.UnixMilli()))
	}
	return result
}

func materialChange(previous Candidate, row Signals, threshold int) bool {
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

func topicScore(row Signals, now time.Time) float64 {
	features := row.Features
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

func candidateFor(row option, group int, now int64) Candidate {
	candidate := row.previous
	if candidate.ID == 0 {
		candidate.RepoID, candidate.AngleKey, candidate.CreatedAt = row.RepoID, consts.TopicAngleProject, now
	}
	candidate.Status, candidate.UpdatedAt = consts.StatusCandidate, now
	// 摘要待更新时保留上一组来源证据，后续重入判断需要比较同一版本的指纹和功能。
	if row.Fingerprint != nil || candidate.SourceFingerprint == nil {
		candidate.SourceFingerprint = row.Fingerprint
		candidate.TitleZh = row.TitleZh
		if candidate.TitleZh == "" {
			candidate.TitleZh = "开源项目 " + row.FullName
		}
		candidate.SourceURL = row.HTMLURL
		if row.SourceURL != nil {
			candidate.SourceURL = *row.SourceURL
		}
		candidate.ReaderValue = readerValue(row.Signals)
	}
	candidate.Reason = []string{"24 小时精确净增长", "近期进入 GitHub Trending", "近期有可确认的日增长", "管理员已关注", "人工加入选题池"}[group]
	candidate.TopicScore = math.Round(row.score*100) / 100
	candidate.HotnessScore, candidate.Growth24h = row.Hotness, row.Growth
	return candidate
}

func readerValue(row Signals) string {
	features := slices.Clone(row.Features)
	if len(features) > 0 {
		slices.Sort(features)
		return strings.Join(features, "、")
	} else if row.Description != nil {
		runes := []rune(strings.TrimSpace(*row.Description))
		return string(runes[:min(160, len(runes))])
	}
	return ""
}
