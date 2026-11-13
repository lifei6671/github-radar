package collection

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 保留短语和带引号的限定值，正文中的 "stars:..." 不参与数值筛选。
var searchTokens = regexp.MustCompile(`(?:[^\s"]+|"(?:[^"\\]|\\.)*")+`)

// SearchQuery 返回空查询时，现有规则与门槛的交集为空。
func (p Payload) SearchQuery() (string, error) {
	query := p.Query
	if p.AgeWindow != "" {
		for _, token := range searchTokens.FindAllString(query, -1) {
			if name, _, ok := strings.Cut(token, ":"); ok && strings.EqualFold(strings.TrimLeft(name, "+-"), "created") {
				return "", fmt.Errorf("启用滚动年龄窗口时，查询中不能再设置 created 限定词")
			}
		}
	}
	if p.StarFloor > 0 || p.StarRange {
		floor, ceiling := p.StarFloor, math.MaxInt
		if p.StarRange {
			floor, ceiling = max(floor, p.MinStars), p.MaxStars
		}
		if ceiling < floor {
			return "", nil
		}
		var tokens []string
		explicit, matched := false, false
		for _, token := range searchTokens.FindAllString(query, -1) {
			name, raw, qualifier := strings.Cut(token, ":")
			if !qualifier || !strings.EqualFold(name, "stars") {
				tokens = append(tokens, token)
				continue
			}
			explicit = true
			low, high, err := searchStarBounds(raw)
			if err != nil {
				return "", fmt.Errorf("Search 的限定条件 %q 无效: %w", token, err)
			}
			low, high = max(low, floor), min(high, ceiling)
			if low <= high {
				// GitHub 对同名正向限定词取并集，需分别收紧每个已有区间。
				matched = true
				tokens = append(tokens, starQualifier(low, high))
			}
		}
		if explicit && !matched {
			return "", nil
		}
		if !explicit {
			tokens = append(tokens, starQualifier(floor, ceiling))
		}
		query = strings.Join(tokens, " ")
	}
	query += " is:public"
	if !strings.Contains(query, "pushed:") {
		query += " pushed:>=" + time.UnixMilli(p.PlannedAt).UTC().AddDate(0, 0, -90).Format(time.DateOnly)
	}
	if p.CreatedFrom > 0 {
		query += " created:" + time.UnixMilli(p.CreatedFrom).UTC().Format(time.RFC3339) +
			".." + time.UnixMilli(p.CreatedUntil).UTC().Format(time.RFC3339)
	}
	return query, nil
}

func starQualifier(low, high int) string {
	if high == math.MaxInt {
		return fmt.Sprintf("stars:>=%d", low)
	}
	return fmt.Sprintf("stars:%d..%d", low, high)
}

func searchStarBounds(raw string) (int, int, error) {
	raw = strings.Trim(raw, `"`)
	parse := func(value string) (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("Star 数必须为非负整数")
		}
		return n, nil
	}
	if start, end, ok := strings.Cut(raw, ".."); ok {
		low, high := 0, math.MaxInt
		var err error
		if start != "*" {
			low, err = parse(start)
			if err != nil {
				return 0, 0, err
			}
		}
		if end != "*" {
			high, err = parse(end)
		}
		return low, high, err
	}
	operand := strings.TrimLeft(raw, "<>=")
	n, err := parse(operand)
	if err != nil {
		return 0, 0, err
	}
	switch raw[:len(raw)-len(operand)] {
	case ">=":
		return n, math.MaxInt, nil
	case ">":
		if n == math.MaxInt {
			return 1, 0, nil
		}
		return n + 1, math.MaxInt, nil
	case "<=":
		return 0, n, nil
	case "<":
		return 0, n - 1, nil
	case "", "=":
		return n, n, nil
	default:
		return 0, 0, fmt.Errorf("无效的 Star 比较运算符")
	}
}
