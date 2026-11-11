package storage

// AISummary 记录最近一次成功摘要，原始文档和模型响应仅存在于 Worker 内存。
type AISummary struct {
	RepoID            int64   `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	ReadmeSHA         *string `gorm:"column:readme_sha"`
	SourceFingerprint string  `gorm:"column:source_fingerprint"`
	SourceQuality     string  `gorm:"column:source_quality"`
	SourceLanguage    string  `gorm:"column:source_language"`
	SummaryZh         string  `gorm:"column:summary_zh"`
	FeaturesJSON      string  `gorm:"column:features_json"`
	AudienceJSON      string  `gorm:"column:audience_json"`
	ValueTagsJSON     string  `gorm:"column:value_tags_json"`
	SourceReadmeURL   *string `gorm:"column:source_readme_url"`
	ModelProvider     string  `gorm:"column:model_provider"`
	ModelName         string  `gorm:"column:model_name"`
	PromptVersion     int     `gorm:"column:prompt_version"`
	SummaryStatus     string  `gorm:"column:summary_status"`
	GeneratedAt       int64   `gorm:"column:generated_at"`
}

func (AISummary) TableName() string { return "ai_summaries" }

type SummaryCheck struct {
	RepoID            int64   `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	ReadmeETag        *string `gorm:"column:readme_etag"`
	LastSeenReadmeSHA *string `gorm:"column:last_seen_readme_sha"`
	SourceFingerprint string  `gorm:"column:source_fingerprint"`
	SourceLanguage    string  `gorm:"column:source_language"`
	LastCheckedAt     int64   `gorm:"column:last_checked_at"`
	NextCheckAt       int64   `gorm:"column:next_check_at"`
	LastErrorCode     *string `gorm:"column:last_error_code"`
}

func (SummaryCheck) TableName() string { return "summary_checks" }

// AICall 保存费用边界和可核实的计量元数据。
type AICall struct {
	CallKey           string   `gorm:"column:call_key;primaryKey"`
	JobID             int64    `gorm:"column:job_id"`
	Provider          string   `gorm:"column:provider"`
	ProviderRequestID *string  `gorm:"column:provider_request_id"`
	Origin            string   `gorm:"column:origin"`
	Status            string   `gorm:"column:status"`
	ReservedAt        int64    `gorm:"column:reserved_at"`
	UpdatedAt         int64    `gorm:"column:updated_at;autoUpdateTime:false"`
	InputTokens       *int64   `gorm:"column:input_tokens"`
	OutputTokens      *int64   `gorm:"column:output_tokens"`
	EstimatedCost     *float64 `gorm:"column:estimated_cost"`
	DurationMS        *int64   `gorm:"column:duration_ms"`
	ErrorCode         *string  `gorm:"column:error_code"`
}

func (AICall) TableName() string { return "ai_calls" }

type TopicCandidate struct {
	ID                int64    `gorm:"column:id;primaryKey;autoIncrement"`
	RepoID            int64    `gorm:"column:repo_id"`
	AngleKey          string   `gorm:"column:angle_key"`
	TitleZh           string   `gorm:"column:title_zh"`
	ReaderValue       string   `gorm:"column:reader_value"`
	SourceURL         string   `gorm:"column:source_url"`
	SourceFingerprint *string  `gorm:"column:source_fingerprint"`
	Reason            string   `gorm:"column:reason"`
	TopicScore        float64  `gorm:"column:topic_score"`
	HotnessScore      *float64 `gorm:"column:hotness_score"`
	Growth24h         *int64   `gorm:"column:growth_24h"`
	Status            string   `gorm:"column:status"`
	Feedback          *string  `gorm:"column:feedback"`
	ReviewedAt        *int64   `gorm:"column:reviewed_at"`
	CreatedAt         int64    `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt         int64    `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (TopicCandidate) TableName() string { return "topic_candidates" }
