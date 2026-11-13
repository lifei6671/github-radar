package metrics

import (
	"testing"

	"github.com/lifei6671/github-radar/internal/consts"
)

func observationWindow(delta int64) Window { return Window{Delta: &delta, Quality: Exact} }

// 验证人工优先、绝对爆发优先、容量向下分配及落选候选保留。
func TestObservationAllocation(t *testing.T) {
	now := 100 * Day
	repos := []Repository{
		{ID: 1, State: consts.StatusCandidate, Grade: "C", Stars: 3, Followed: true},
		{ID: 2, State: consts.StatusPaused, Grade: "C", Stars: 700},
		{ID: 3, State: consts.StatusCandidate, Grade: "C", Stars: 130},
		{ID: 4, State: consts.StatusCandidate, Grade: "C", Stars: 20},
		{ID: 5, State: consts.StatusCandidate, Grade: "C", Stars: 200},
		{ID: 6, State: consts.StatusCandidate, Grade: "C", Stars: 19},
	}
	rows := []Calculation{
		{},
		{Breakout: &Breakout{Absolute: true}, Windows: map[string]Window{"24h": observationWindow(500)}},
		{Breakout: &Breakout{}, Windows: map[string]Window{"24h": observationWindow(100)}},
		{Windows: map[string]Window{"24h": observationWindow(15)}},
		{Windows: map[string]Window{"24h": {Delta: new(int64(80)), Quality: Estimated}}},
		{Windows: map[string]Window{"24h": observationWindow(49)}},
	}
	policy := ObservationPolicy{Capacity: [4]int{1, 1, 2, 0}, Total: 4}
	changes := AllocateObservation(policy, repos, rows, now)
	if len(changes) != 4 {
		t.Fatalf("只有四个项目获得监控名额: %+v", changes)
	}
	for i, grade := range []string{"A", "S", "B", "B"} {
		change := changes[i]
		if change.RepoID != int64(i+1) || change.Grade != grade || change.State != consts.StatusActive {
			t.Fatalf("档位或优先级错误: %+v", changes)
		}
	}
	if changes[0].Reason != "manual_follow" || changes[1].Reason != "breakout" ||
		changes[2].Reason != "capacity" || changes[3].Reason != "exact_growth_15" {
		t.Fatalf("入档证据不正确: %+v", changes)
	}
}

// 验证连续低增长和传播信号分别影响降档与暂停，缺测阻止连续判定。
func TestObservationCoolingAndMissingEvidence(t *testing.T) {
	now := 100 * Day
	policy := ObservationPolicy{Capacity: [4]int{2, 3, 10, 2}, Total: 17}
	repos := []Repository{
		{ID: 1, State: "active", Grade: "A"},
		{ID: 2, State: "active", Grade: "A"},
		{ID: 3, State: "active", Grade: "B", Stars: 40, Language: "Go"},
		{ID: 4, State: "active", Grade: "C"},
		{ID: 5, State: "active", Grade: "C", LastSignalAt: now - Day},
	}
	low := make([]Window, 7)
	high := make([]Window, 7)
	for i := range low {
		low[i], high[i] = observationWindow(0), observationWindow(5)
		low[i].EndAt, high[i].EndAt = new(now-int64(i)*Day), new(now-int64(i)*Day)
	}
	rows := []Calculation{
		{Windows: map[string]Window{consts.Window24h: observationWindow(20), consts.WindowPrevious24h: observationWindow(30)}},
		{Windows: map[string]Window{consts.Window24h: observationWindow(20)}},
		{ObservationDays: low},
		{ObservationQuiet: true},
		{ObservationQuiet: true},
	}
	for i := range 5 {
		repos = append(repos, Repository{ID: int64(6 + i), State: "active", Grade: "B", Stars: 55, Language: "go"})
		rows = append(rows, Calculation{ObservationDays: high})
	}
	result := func() map[int64]GradeChange {
		got := map[int64]GradeChange{}
		for _, change := range AllocateObservation(policy, repos, rows, now) {
			got[change.RepoID] = change
		}
		return got
	}
	got := result()
	if got[1].Reason != "slowed_48h" || got[1].Grade != "B" || got[2].Reason != "" ||
		got[3].Reason != "below_peers_7d" || got[3].Grade != "C" || got[4].Reason != "quiet_14d" ||
		got[4].State != "paused" || got[5].Reason != "" {
		t.Fatalf("降档或暂停条件有误: %+v", got)
	}
	low[3] = Window{Quality: Insufficient}
	if got := result(); got[3].Reason != "" {
		t.Fatalf("缺一天时不能冷却: %+v", got[3])
	}
}

func TestQuietObservationRequiresCoverageWithoutGrowth(t *testing.T) {
	now := 100 * Day
	// 每三天有实测，六个锚点覆盖十五天。
	var points []Point
	for i := range 6 {
		points = append(points, Point{At: now - int64(i)*3*Day, Stars: 80})
	}
	if !QuietObservation(points, now, 3*Day) {
		t.Fatal("有效的冷却档采样应能证明 14 天无增长")
	}
	if QuietObservation(append(append([]Point{}, points[:2]...), points[3:]...), now, 3*Day) {
		t.Fatal("缺测不能证明连续无增长")
	}
	points[1].Stars = 79
	if QuietObservation(points, now, 3*Day) {
		t.Fatal("窗口内真实增长不能被其他日的下降抵消")
	}
}
