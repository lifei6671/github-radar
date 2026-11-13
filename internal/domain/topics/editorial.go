package topics

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

const ScoreVersion = 2

// Brief 是既有导读模型同一次请求返回的可选选题材料。
type Brief struct {
	ReaderBenefit string   `json:"readerBenefit,omitempty"`
	Demo          string   `json:"demo,omitempty"`
	Angle         string   `json:"angle,omitempty"`
	Steps         []string `json:"steps,omitempty"`
	Risks         []string `json:"risks,omitempty"`
}

// Review 保存人工决定的依据和复现结果，独立于自动建议。
type Review struct {
	Verification string `json:"verification,omitempty"`
	Evidence     string `json:"evidence,omitempty"`
	Override     string `json:"override,omitempty"`
}

// Dimension 保存分项的证据来源、数值及置信状态。版本由评估统一标记。
type Dimension struct {
	Key        string  `json:"key"`
	Points     float64 `json:"points"`
	Maximum    int     `json:"maximum"`
	Source     string  `json:"source"`
	Confidence string  `json:"confidence"`
	Evidence   string  `json:"evidence"`
}

type Assessment struct {
	Dimensions []Dimension `json:"dimensions"`
	Brief      Brief       `json:"brief"`
	Revision   string      `json:"revision,omitempty"`
}

func (a Assessment) Total() float64 {
	var total float64
	for _, part := range a.Dimensions {
		total += part.Points
	}
	return math.Round(total*100) / 100
}

// SetAssessment 同步保存自动评分和用于解释它的证据快照。
func (c *Candidate) SetAssessment(a Assessment) {
	data, _ := json.Marshal(a)
	c.ScoreVersion, c.ScoreDetailsJSON, c.TopicScore = ScoreVersion, string(data), a.Total()
}

// Rescore 同步证据版本、材料和分数，保留选题身份和人工状态。
func (c *Candidate) Rescore(row Signals, review Review, now time.Time) {
	if row.Fingerprint != nil || c.SourceFingerprint == nil {
		c.SourceFingerprint, c.ReaderValue = row.Fingerprint, readerValue(row)
		c.SourceURL = row.HTMLURL
		if row.SourceURL != nil {
			c.SourceURL = *row.SourceURL
		}
	}
	c.HotnessScore, c.Growth24h, c.ReleaseKey = row.Hotness, row.Growth, row.ReleaseKey
	c.SetAssessment(Assess(row, review, now))
}

func (c Candidate) Assessment() Assessment {
	var a Assessment
	_ = json.Unmarshal([]byte(c.ScoreDetailsJSON), &a)
	return a
}

func (c Candidate) Review() Review {
	var r Review
	_ = json.Unmarshal([]byte(c.EditorialReviewJSON), &r)
	return r
}

func Suggestion(score float64) string {
	switch {
	case score >= 80:
		return "prepare"
	case score >= 65:
		return "candidate"
	default:
		return "observe"
	}
}

// Assess 按五维规则计算分数。模型只提供可复核的文字材料，实测分由管理员给出。
func Assess(row Signals, review Review, now time.Time) Assessment {
	brief := row.Brief
	reader := Dimension{Key: "reader", Maximum: 30, Source: "仓库简介", Confidence: "inferred", Evidence: readerValue(row)}
	if reader.Evidence != "" {
		reader.Points = 8
	}
	if len(row.Features) > 0 || len(row.Audience) > 0 {
		reader.Source = "已有导读"
		reader.Points += float64(min(3, len(row.Features)) * 3)
		if len(row.Audience) > 0 {
			reader.Points += 5
			reader.Evidence = strings.Trim(strings.Join([]string{reader.Evidence, strings.Join(row.Audience, "、")}, "；"), "；")
		}
	}
	if brief.ReaderBenefit != "" {
		reader.Source, reader.Evidence = "导读提炼", brief.ReaderBenefit
		reader.Points += 8
	}
	reader.Points = min(30, reader.Points)
	demo := Dimension{Key: "demo", Maximum: 20, Source: "仓库文档", Confidence: "pending", Evidence: "待确认可演示场景"}
	if brief.Demo != "" {
		demo.Source, demo.Confidence, demo.Evidence, demo.Points = "导读提炼", "inferred", brief.Demo, 12
		if len(brief.Steps) > 0 {
			demo.Points = 16
		}
	}
	reproduce := Reproduction(review, brief)
	if review.Verification == "verified" && brief.Demo != "" {
		demo.Points, demo.Confidence, demo.Source = 20, "verified", "人工实测"
	}
	heat := Dimension{Key: "heat", Maximum: 20, Source: "本地 Star 采样", Confidence: "pending", Evidence: "暂无有效增长证据"}
	if row.Hotness != nil {
		heat.Points, heat.Confidence, heat.Evidence = math.Round(max(0, min(*row.Hotness, 100))*2)/10, "observed", "全局热度按 20 分折算"
	} else if row.Growth != nil {
		heat.Points, heat.Confidence, heat.Evidence = min(20, float64(max(0, *row.Growth))/10), "observed", "本地 24 小时精确净增"
	} else if row.TrendingRank != nil {
		heat.Points, heat.Source, heat.Confidence, heat.Evidence = 8, "GitHub Trending", "observed", "近期进入榜单"
	}
	fresh := Dimension{Key: "freshness", Maximum: 10, Source: "GitHub 仓库", Confidence: "observed", Evidence: "近期缺少新建、更新或发布证据"}
	if row.GitHubCreatedAt != nil && recent(*row.GitHubCreatedAt, now, 30) {
		fresh.Points, fresh.Evidence = 8, "GitHub 仓库在近 30 天创建"
	} else if row.GitHubPushedAt != nil && recent(*row.GitHubPushedAt, now, 14) {
		fresh.Points, fresh.Evidence = 5, "GitHub 仓库在近 14 天有推送"
	} else if recent(row.DiscoveredAt, now, 7) {
		fresh.Points, fresh.Source, fresh.Evidence = 3, "首次发现记录", "近期首次收录"
	}
	if row.ReleaseKey != "" && recent(row.ReleaseObservedAt, now, 30) {
		fresh.Points, fresh.Source, fresh.Evidence = 10, "人工记录的 GitHub 发布链接", row.ReleaseKey
	}
	return Assessment{Dimensions: []Dimension{reader, demo, heat, reproduce, fresh}, Brief: brief, Revision: EditorialRevision(row)}
}

// EditorialRevision 绑定当前 README、发布记录和供管理员核验的材料。
func EditorialRevision(row Signals) string {
	brief := row.Brief
	brief.Risks = slices.Clone(brief.Risks)
	slices.Sort(brief.Risks)
	fingerprint := row.ObservedFingerprint
	if fingerprint == nil {
		fingerprint = row.Fingerprint
	}
	raw, _ := json.Marshal(struct {
		ObservedFingerprint *string
		ReadyFingerprint    *string
		Release             string
		Brief               Brief
	}{fingerprint, row.Fingerprint, row.ReleaseKey, brief})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func recent(at int64, now time.Time, days int) bool {
	return at >= now.AddDate(0, 0, -days).UnixMilli() && at <= now.UnixMilli()
}

// Reproduction 仅在人工填写实测依据后计分。
func Reproduction(review Review, brief Brief) Dimension {
	part := Dimension{Key: "reproduction", Maximum: 20, Source: "人工实测", Confidence: "pending", Evidence: "待验证"}
	if len(brief.Steps) > 0 {
		part.Evidence = "已有建议步骤，等待人工实测"
	}
	switch review.Verification {
	case "verified":
		part.Points, part.Confidence, part.Evidence = 20, "verified", review.Evidence
	case "failed":
		part.Confidence, part.Evidence = "failed", review.Evidence
	}
	return part
}

// WithReview 只调整人工核验影响的分项，保留原始自动评分证据。
func (a Assessment) WithReview(review Review) Assessment {
	for i := range a.Dimensions {
		switch a.Dimensions[i].Key {
		case "reproduction":
			a.Dimensions[i] = Reproduction(review, a.Brief)
		case "demo":
			if a.Brief.Demo == "" {
				continue
			}
			part := &a.Dimensions[i]
			part.Points, part.Confidence, part.Source = 12, "inferred", "导读提炼"
			if len(a.Brief.Steps) > 0 {
				part.Points = 16
			}
			if review.Verification == "verified" {
				part.Points, part.Confidence, part.Source = 20, "verified", "人工实测"
			}
		}
	}
	return a
}
