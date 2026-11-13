package storage

// MetricWindowSQL 读取别名 m 的指标窗口，调用方使用固定窗口名和字段。
func MetricWindowSQL(window, attribute string) string {
	return `json_extract(m.` + LatestMetricColumns.WindowDetailsJSON + `,'$."` + window + `".` + attribute + `')`
}

// MetricAnchors 保存非空 UTC 桶的首末实际观测以及样本数。
type MetricAnchors struct {
	StarsOpen       int64 `gorm:"column:stars_open"`
	StarsClose      int64 `gorm:"column:stars_close"`
	OpenObservedAt  int64 `gorm:"column:open_observed_at"`
	CloseObservedAt int64 `gorm:"column:close_observed_at"`
	OpenSlot        int64 `gorm:"column:open_slot"`
	CloseSlot       int64 `gorm:"column:close_slot"`
	SampleCount     int64 `gorm:"column:sample_count"`
}

type HourlyMetric struct {
	RepoID        int64 `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	HourStart     int64 `gorm:"column:hour_start;primaryKey;autoIncrement:false"`
	MetricAnchors `gorm:"embedded"`
	Delta         *int64 `gorm:"column:delta"`
	Quality       string `gorm:"column:quality"`
}

func (HourlyMetric) TableName() string { return HourlyMetricTable }

type DailyMetric struct {
	RepoID        int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	Day           string `gorm:"column:day;primaryKey"`
	MetricAnchors `gorm:"embedded"`
	Delta         *int64   `gorm:"column:delta"`
	Quality       string   `gorm:"column:quality"`
	HotnessScore  *float64 `gorm:"column:hotness_score"`
	RankInDay     *int     `gorm:"column:rank_in_day"`
}

func (DailyMetric) TableName() string { return DailyMetricTable }

type PendingMetric struct {
	RepoID        int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	Resolution    string `gorm:"column:resolution;primaryKey"`
	BucketStart   int64  `gorm:"column:bucket_start;primaryKey;autoIncrement:false"`
	MetricAnchors `gorm:"embedded"`
}

func (PendingMetric) TableName() string { return PendingMetricTable }

// LatestMetric 记录同一轮 asOf 计算的窗口、榜单和热度，缺失数值以 NULL 保存。
type LatestMetric struct {
	RepoID             int64    `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	Delta1h            *int64   `gorm:"column:delta_1h"`
	Delta24h           *int64   `gorm:"column:delta_24h"`
	Delta7d            *int64   `gorm:"column:delta_7d"`
	Delta30d           *int64   `gorm:"column:delta_30d"`
	RelativeGrowth     *float64 `gorm:"column:relative_growth"`
	Acceleration       *float64 `gorm:"column:acceleration"`
	PersistenceScore   *float64 `gorm:"column:persistence_score"`
	DailyConcentration *float64 `gorm:"column:daily_concentration"`
	TrendingFrequency  int      `gorm:"column:trending_frequency"`
	TrendingStreak     int      `gorm:"column:trending_streak"`
	TrendingRank       *int     `gorm:"column:trending_rank"`
	TrendingScore      *float64 `gorm:"column:trending_score"`
	HotnessScore       *float64 `gorm:"column:hotness_score"`
	CandidateBurst     bool     `gorm:"column:candidate_burst"`
	BreakoutJSON       string   `gorm:"column:breakout_json"`
	DataQuality        string   `gorm:"column:data_quality"`
	WindowDetailsJSON  string   `gorm:"column:window_details_json"`
	CalculatedAt       int64    `gorm:"column:calculated_at"`
}

func (LatestMetric) TableName() string { return LatestMetricTable }

type TrendingBatchRecord struct {
	SourceKey    string `gorm:"column:source_key;primaryKey"`
	Window       string `gorm:"column:window;primaryKey"`
	ObservedSlot int64  `gorm:"column:observed_slot;primaryKey;autoIncrement:false"`
	ObservedAt   int64  `gorm:"column:observed_at"`
}

func (TrendingBatchRecord) TableName() string { return TrendingBatchRecordTable }
