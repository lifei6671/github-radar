package metrics

const (
	BreakoutLocal        = "local_net_24h"
	BreakoutGitHub       = "github_daily_stars"
	BreakoutMinimum      = 100
	BreakoutDirect       = 500
	BreakoutMinimumRatio = 2.5
	BreakoutMinimumDays  = 5
	breakoutSmoothing    = 10
)

// Breakout 保存判定时实际采用的证据与阈值，不与热度评分混用。
type Breakout struct {
	Source       string   `json:"source"`
	Value        int64    `json:"value"`
	At           int64    `json:"at"`
	StartAt      int64    `json:"startAt"`
	CoveredDays  int      `json:"coveredDays"`
	Baseline     *float64 `json:"baseline"`
	Ratio        *float64 `json:"ratio"`
	Absolute     bool     `json:"absolute"`
	Minimum      int      `json:"minimum"`
	Direct       int      `json:"direct"`
	MinimumRatio float64  `json:"minimumRatio"`
	MinimumDays  int      `json:"minimumDays"`
	Smoothing    int      `json:"smoothing"`
}

// OverlapsOfficialDay 判断本地窗口与官方自然日是否有交集，保留两种来源各自的计算结果。
func (b Breakout) OverlapsOfficialDay(end int64) bool {
	return b.Source == BreakoutLocal && end > b.StartAt && end-Day < b.At
}

// EvaluateBreakout 只比较同一来源的相邻独立日窗。nil 表示缺测，负净增按零计入基线。
func EvaluateBreakout(source string, value, at int64, previous []*int64) *Breakout {
	var total float64
	var covered int
	for _, delta := range previous {
		if delta != nil {
			total += float64(max(*delta, 0))
			covered++
		}
	}
	result := &Breakout{Source: source, Value: value, At: at, StartAt: at - Day, CoveredDays: covered,
		Minimum: BreakoutMinimum, Direct: BreakoutDirect, MinimumRatio: BreakoutMinimumRatio, MinimumDays: BreakoutMinimumDays, Smoothing: breakoutSmoothing}
	result.Absolute = value >= BreakoutDirect
	if covered >= BreakoutMinimumDays {
		baseline := total / float64(covered)
		ratio := (float64(value) + breakoutSmoothing) / (baseline + breakoutSmoothing)
		result.Baseline, result.Ratio = &baseline, &ratio
	}
	if result.Absolute || value >= BreakoutMinimum && result.Ratio != nil && *result.Ratio >= BreakoutMinimumRatio {
		return result
	}
	return nil
}
