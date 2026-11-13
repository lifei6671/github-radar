package metrics

// BoundaryToleranceMinutes 定义窗口两端的实际采样容差。
type BoundaryToleranceMinutes struct {
	Exact int `json:"exact"`
	Max   int `json:"max"`
}

type Policy struct {
	Hour                BoundaryToleranceMinutes `json:"hour"`
	Day                 BoundaryToleranceMinutes `json:"day"`
	Week                BoundaryToleranceMinutes `json:"week"`
	Month               BoundaryToleranceMinutes `json:"month"`
	HotGrowth           int                      `json:"hotGrowth"`
	ActiveGrowth        int                      `json:"activeGrowth"`
	HotDemotionHours    int                      `json:"hotDemotionHours"`
	ActiveDemotionHours int                      `json:"activeDemotionHours"`
	NormalDemotionHours int                      `json:"normalDemotionHours"`
	TopGrowthRepos      int                      `json:"topGrowthRepos"`
	RelativeStarFloor   int                      `json:"relativeStarFloor"`
	Velocity24Cap       int                      `json:"velocity24Cap"`
	Velocity7Cap        int                      `json:"velocity7Cap"`
	AccelerationCap     int                      `json:"accelerationCap"`
	MinPersistenceDays  int                      `json:"minPersistenceDays"`
	// 权重顺序为 24h 增长、7d 增长、加速度、持续性和 Trending。
	Weights [5]float64 `json:"weights"`
}
