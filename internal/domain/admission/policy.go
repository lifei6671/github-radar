// Package admission 提供采集与模型任务共用的纯准入规则。
package admission

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"
	"unicode"
)

const (
	Manual      = "manual"
	Growth      = "growth"
	Exploration = "exploration"
	Revival     = "revival"
	Maintenance = "maintenance"

	Pass   = "pass"
	Defer  = "defer"
	Reject = "reject"
)

// Evidence 区分本地净增、官方历史和榜单；只有精确观测能提供净增。
type Evidence struct {
	Exact24h *int64 `json:"exact24h,omitempty"`
	StartAt  int64  `json:"startAt,omitempty"`
	EndAt    int64  `json:"endAt,omitempty"`
	BurstAt  int64  `json:"burstAt,omitempty"`
	Trending int64  `json:"trendingAt,omitempty"`
	SharedAt int64  `json:"sharedAt,omitempty"`
	Source   string `json:"burstSource,omitempty"`
}

// Input 使用公开身份、内容及证据；不读取发布资格。
type Input struct {
	ID                          int64
	Stars                       int
	CreatedAt, PushedAt         *int64
	Public, Followed, Manual    bool
	Valuable, HasSummary, Cache bool
	Derivative                  bool
	Description, Readme, Name   string
	Evidence                    Evidence
}

// Decision 记录稳定原因码，允许调用方在下一次检查前跳过昂贵工作。
type Decision struct {
	State, Reason, Lane string
	Priority            int
	RetryAt             int64
}

// AgeDays 以 UTC 日期差计算年龄；缺失或未来时间不能进入常规年龄档。
func AgeDays(created *int64, now time.Time) (int, bool) {
	if created == nil || *created <= 0 || *created > now.UnixMilli() {
		return 0, false
	}
	return int(now.UTC().Truncate(24*time.Hour).Sub(time.UnixMilli(*created).UTC().Truncate(24*time.Hour)) / (24 * time.Hour)), true
}

// DayStart 用 UTC 作为模型额度和探索抽样的日界。
func DayStart(now time.Time) time.Time { return now.UTC().Truncate(24 * time.Hour) }

// FreshGrowth 拒绝过期或不完整的精确增长。
func (e Evidence) FreshGrowth(now time.Time) int64 {
	if e.Exact24h != nil && e.StartAt > 0 && e.StartAt < e.EndAt &&
		e.EndAt <= now.UnixMilli() && e.EndAt > now.Add(-24*time.Hour).UnixMilli() {
		return *e.Exact24h
	}
	return 0
}

// Strong 使用近期榜单或可核实的爆发，保留它们各自的计量口径。
func (e Evidence) Strong(now time.Time) bool {
	at := max(e.BurstAt, e.Trending)
	return at <= now.UnixMilli() && at > now.Add(-48*time.Hour).UnixMilli()
}

// Readable 排除单独标题、链接和模板占位；简短的用途说明和命令仍可通过。
func Readable(value, name string) bool {
	value = strings.TrimSpace(value)
	name = strings.ToLower(strings.TrimSpace(name))
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		lower := strings.ToLower(line)
		if lower == "" || lower == name || strings.HasPrefix(lower, "http") ||
			strings.HasPrefix(lower, "![") || strings.HasPrefix(lower, "[![") ||
			strings.HasPrefix(lower, "<!--") || strings.HasPrefix(lower, "lorem ipsum") {
			continue
		}
		switch strings.Trim(lower, " .!？。_-") {
		case "test", "demo", "todo", "readme", "my repo", "my repository", "coming soon",
			"hello world", "test repository", "测试", "测试项目", "暂无":
			continue
		}
		words, ideographs := 0, 0
		for _, word := range strings.Fields(line) {
			if strings.ContainsFunc(word, unicode.IsLetter) {
				words++
			}
		}
		for _, r := range line {
			if unicode.Is(unicode.Han, r) {
				ideographs++
			}
		}
		if words >= 3 || ideographs >= 5 || strings.HasPrefix(lower, "pip install ") ||
			strings.HasPrefix(lower, "npm install ") || strings.HasPrefix(lower, "cargo install ") {
			return true
		}
	}
	return false
}

// Preflight 是无网络、无模型的廉价检查。强信号可安排一次来源复核。
func Preflight(in Input, now time.Time) Decision {
	if !in.Public {
		return Decision{State: Reject, Reason: "not_public"}
	}
	if in.Manual || in.Followed {
		return Decision{State: Pass, Reason: "manual_priority"}
	}
	if Readable(in.Description, in.Name) || Readable(in.Readme, in.Name) {
		return Decision{State: Pass, Reason: "readable_source"}
	}
	if in.Evidence.Strong(now) || in.Evidence.FreshGrowth(now) >= 50 {
		return Decision{State: Pass, Reason: "verify_strong_signal"}
	}
	return Decision{State: Defer, Reason: "no_readable_source", RetryAt: now.Add(7 * 24 * time.Hour).UnixMilli()}
}

// AIEligible 为首次清洗和维护任务划分通道，最终调用仍须通过预算服务。
func AIEligible(in Input, operation string, now time.Time) Decision {
	d := Preflight(in, now)
	if d.State != Pass {
		return d
	}
	if in.Cache && !in.Manual {
		return Decision{State: Defer, Reason: "fingerprint_cached", RetryAt: now.Add(24 * time.Hour).UnixMilli()}
	}
	if in.Manual {
		return Decision{State: Pass, Reason: "manual_request", Lane: Manual, Priority: 200}
	}
	age, known := AgeDays(in.CreatedAt, now)
	growth := in.Evidence.FreshGrowth(now)
	strong := in.Evidence.Strong(now)
	if operation != "summary" && d.Reason == "verify_strong_signal" {
		return Decision{State: Defer, Reason: "no_readable_source", RetryAt: now.Add(7 * 24 * time.Hour).UnixMilli()}
	}
	switch {
	case in.Followed:
		// 管理员重点可绕过内容门槛；后台任务仍使用自动池。
		return Decision{State: Pass, Reason: "manual_follow", Lane: Growth, Priority: 160}
	case operation != "summary" && (in.Valuable || strong || growth >= 50):
		return Decision{State: Pass, Reason: "valuable_maintenance", Lane: Maintenance, Priority: 45}
	case operation == "summary" && known && age > 30 && (growth >= 50 || strong):
		return Decision{State: Pass, Reason: "recent_revival", Lane: Revival, Priority: 135}
	case operation == "summary" && (growth >= 100 || strong):
		return Decision{State: Pass, Reason: "strong_growth", Lane: Growth, Priority: 150}
	case operation == "summary" && known && age <= 30 && (in.Stars >= 20 || growth >= 15 ||
		in.Evidence.SharedAt > now.Add(-7*24*time.Hour).UnixMilli() && in.Evidence.SharedAt <= now.UnixMilli()):
		priority := 110
		if in.Derivative {
			priority -= 5
		}
		return Decision{State: Pass, Reason: "early_exploration", Lane: Exploration, Priority: priority}
	case operation == "summary" && (growth >= 15 || (in.HasSummary && in.Valuable)):
		return Decision{State: Pass, Reason: "valuable_maintenance", Lane: Maintenance, Priority: 55}
	default:
		return Decision{State: Defer, Reason: "no_value_signal", RetryAt: now.Add(24 * time.Hour).UnixMilli()}
	}
}

// DailySample 在同一 UTC 日按仓库数字 ID 得到可复现的随机顺序。
func DailySample(id int64, now time.Time) uint64 {
	var data [16]byte
	binary.LittleEndian.PutUint64(data[:8], uint64(id))
	binary.LittleEndian.PutUint64(data[8:], uint64(DayStart(now).Unix()/86400))
	hash := sha256.Sum256(data[:])
	return binary.LittleEndian.Uint64(hash[:8])
}
