package topics

import (
	"slices"
	"strings"
	"time"

	"github.com/lifei6671/github-radar/internal/consts"
)

type Candidate struct {
	ID                  int64
	RepoID              int64
	AngleKey            string
	TitleZh             string
	ReaderValue         string
	SourceURL           string
	SourceFingerprint   *string
	Reason              string
	TopicScore          float64
	HotnessScore        *float64
	Growth24h           *int64
	Status              string
	Feedback            *string
	ReviewedAt          *int64
	CreatedAt           int64
	UpdatedAt           int64
	ScoreVersion        int
	ScoreDetailsJSON    string
	EditorialReviewJSON string
	ReleaseKey          string
}

type Signals struct {
	RepoID                 int64
	FullName, HTMLURL      string
	StarredByUser          bool
	Description            *string
	DiscoveredAt           int64
	GitHubPushedAt         *int64
	Hotness, Persistence   *float64
	Growth                 *int64
	TrendingRank           *int
	TitleZh                string
	Features               []string
	Audience               []string
	Brief                  Brief
	ReleaseKey             string
	ReleaseObservedAt      int64
	UnseenRelease          bool
	GitHubCreatedAt        *int64
	Fingerprint, SourceURL *string
	ObservedFingerprint    *string
}

type option struct {
	Signals
	previous   Candidate
	assessment Assessment
	groups     [4]bool
}

func Manual(row Signals, now time.Time) Candidate {
	candidate := candidateFor(option{Signals: row, assessment: Assess(row, Review{}, now)}, 4, now.UnixMilli())
	reviewedAt := now.UnixMilli()
	candidate.ReviewedAt = &reviewedAt
	return candidate
}

func Recommend(rows []Signals, byRepo map[int64]Candidate, limit, threshold int, now time.Time) []Candidate {
	options := make([]option, 0, len(rows))
	for _, row := range rows {
		prev := byRepo[row.RepoID]
		reentry := ReentryReason(prev, row, threshold)
		if prev.ID != 0 && prev.Status != consts.StatusCandidate && reentry == "" {
			continue
		}
		// 有人工核验的待选项目保留核验时的证据版本，直到它离开候选或管理员显式重新评分。
		if prev.ID != 0 && prev.Status == consts.StatusCandidate &&
			slices.Contains([]string{"verified", "failed"}, prev.Review().Verification) {
			continue
		}
		groups := [4]bool{row.Growth != nil && *row.Growth > 0, row.TrendingRank != nil,
			row.Persistence != nil && *row.Persistence > 0, row.StarredByUser}
		if !slices.Contains(groups[:], true) && reentry != "" {
			groups[0] = true
		}
		if prev.ID == 0 && row.ReleaseKey != "" {
			groups[0] = true
		}
		if !slices.Contains(groups[:], true) {
			continue
		}
		review := prev.Review()
		if prev.ID != 0 && prev.Status != consts.StatusCandidate {
			review = Review{}
		}
		options = append(options, option{row, prev, Assess(row, review, now), groups})
	}
	picks := selectTopics(options, limit)
	result := make([]Candidate, 0, len(picks))
	for _, pick := range picks {
		row := options[pick.index]
		candidate := candidateFor(row, pick.group, now.UnixMilli())
		if row.previous.ID != 0 && row.previous.Status != consts.StatusCandidate {
			candidate.Reason, candidate.EditorialReviewJSON = ReentryReason(row.previous, row.Signals, threshold), ""
			candidate.CreatedAt, candidate.Feedback, candidate.ReviewedAt = now.UnixMilli(), nil, nil
		} else if row.previous.ID == 0 && row.ReleaseKey != "" {
			candidate.Reason = "记录了 GitHub 发布链接"
		}
		if risks := row.previous.Assessment().Brief.Risks; len(risks) > 0 {
			// 即使新导读尚未生成，重入也保留已知风险及其对应的核验要求。
			for _, risk := range risks {
				if !slices.Contains(row.Brief.Risks, risk) {
					row.Brief.Risks = append(row.Brief.Risks, risk)
				}
			}
			slices.Sort(row.Brief.Risks)
			row.assessment.Brief.Risks = row.Brief.Risks
			row.assessment.Revision = EditorialRevision(row.Signals)
			candidate.SetAssessment(row.assessment)
		}
		result = append(result, candidate)
	}
	return result
}

// ReentryReason 保留这次再次入池的具体依据。
func ReentryReason(previous Candidate, row Signals, threshold int) string {
	if previous.ID == 0 {
		return ""
	}
	if row.UnseenRelease && row.ReleaseKey != "" && row.ReleaseKey != previous.ReleaseKey {
		return "发现新的 GitHub 发布记录"
	}
	// 已处理项目需要已确认的 README 变动或精确净增的明显抬升。
	if previous.SourceFingerprint != nil && row.Fingerprint != nil &&
		strings.HasPrefix(*previous.SourceFingerprint, consts.ReadmeFingerprintPrefix) && strings.HasPrefix(*row.Fingerprint, consts.ReadmeFingerprintPrefix) &&
		*row.Fingerprint != *previous.SourceFingerprint && readerValue(row) != previous.ReaderValue {
		return "README 与功能证据发生变化"
	}
	before := int64(0)
	if previous.Growth24h != nil {
		before = max(*previous.Growth24h, 0)
	}
	if row.Growth != nil && *row.Growth-before >= int64(threshold) {
		return "24 小时精确净增明显抬升"
	}
	return ""
}

type selection struct{ index, group int }

func selectTopics(rows []option, limit int) []selection {
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		left, right := rows[a].assessment.Total(), rows[b].assessment.Total()
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

func candidateFor(row option, group int, now int64) Candidate {
	candidate := row.previous
	if candidate.ID == 0 {
		candidate.RepoID, candidate.AngleKey, candidate.CreatedAt = row.RepoID, consts.TopicAngleProject, now
	}
	candidate.Status, candidate.UpdatedAt = consts.StatusCandidate, now
	if row.previous.ID != 0 {
		candidate.UpdatedAt = max(now, row.previous.UpdatedAt+1)
	}
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
	if row.ReleaseKey != "" {
		candidate.ReleaseKey = row.ReleaseKey
	}
	candidate.SetAssessment(row.assessment)
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
