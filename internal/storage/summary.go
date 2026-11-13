package storage

// AISummary 记录最近一次成功摘要，原始文档和模型响应仅存在于 Worker 内存。
type AISummary struct {
	RepoID               int64   `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	ReadmeSHA            *string `gorm:"column:readme_sha"`
	SourceFingerprint    string  `gorm:"column:source_fingerprint"`
	SourceQuality        string  `gorm:"column:source_quality"`
	SourceLanguage       string  `gorm:"column:source_language"`
	TitleZh              *string `gorm:"column:title_zh"`
	SummaryZh            string  `gorm:"column:summary_zh"`
	RecommendationReason *string `gorm:"column:recommendation_reason"`
	FeaturesJSON         string  `gorm:"column:features_json"`
	AudienceJSON         string  `gorm:"column:audience_json"`
	ValueTagsJSON        string  `gorm:"column:value_tags_json"`
	EditorialBriefJSON   string  `gorm:"column:editorial_brief_json"`
	SourceReadmeURL      *string `gorm:"column:source_readme_url"`
	ModelProvider        string  `gorm:"column:model_provider"`
	ModelName            string  `gorm:"column:model_name"`
	PromptVersion        int     `gorm:"column:prompt_version"`
	SummaryStatus        string  `gorm:"column:summary_status"`
	GeneratedAt          int64   `gorm:"column:generated_at"`
}

func (AISummary) TableName() string { return AISummaryTable }

// RepoAITopics 保存模型提取的主题词及其生成依据，空数组表示已分析但未提取到主题。
type RepoAITopics struct {
	ThemeSlugsJSON    string `gorm:"column:theme_slugs_json;default:'[]'"`
	ThemeCatalogKey   string `gorm:"column:theme_catalog_key"`
	RepoID            int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	TopicsJSON        string `gorm:"column:topics_json"`
	SourceFingerprint string `gorm:"column:source_fingerprint"`
	ModelProvider     string `gorm:"column:model_provider"`
	ModelName         string `gorm:"column:model_name"`
	PromptVersion     int    `gorm:"column:prompt_version"`
	GeneratedAt       int64  `gorm:"column:generated_at"`
}

func (RepoAITopics) TableName() string { return RepoAITopicsTable }

// RepoReadmeTranslation 按 README 版本缓存译文片段，已保存的批次可在重试时继续使用。
type RepoReadmeTranslation struct {
	RepoID      int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	ReadmeSHA   string `gorm:"column:readme_sha"`
	ReadmeBytes int    `gorm:"column:readme_bytes"`
	ModelKey    string `gorm:"column:model_key"`
	PartsJSON   string `gorm:"column:parts_json"`
	Complete    bool   `gorm:"column:complete"`
}

func (RepoReadmeTranslation) TableName() string { return RepoReadmeTranslationTable }

// RepoReadmeDocument 保存当前已验证版本的中文正文和后台续译使用的有界原文。空 SHA 表示确认缺失。
type RepoReadmeDocument struct {
	RepoID         int64  `gorm:"column:repo_id;primaryKey;autoIncrement:false"`
	ReadmeSHA      string `gorm:"column:readme_sha"`
	ReadmeBytes    int    `gorm:"column:readme_bytes"`
	Language       string `gorm:"column:language"`
	SourceURL      string `gorm:"column:source_url"`
	ChineseHTML    string `gorm:"column:chinese_html"`
	Complete       bool   `gorm:"column:complete"`
	Truncated      bool   `gorm:"column:truncated"`
	SourceMarkdown []byte `gorm:"column:source_markdown" json:"-"`
	SourceRepoName string `gorm:"column:source_repo_name" json:"-"`
	SourceETag     string `gorm:"column:source_etag" json:"-"`
	SourcePath     string `gorm:"column:source_path" json:"-"`
	SourceJobID    int64  `gorm:"column:source_job_id" json:"-"`
}

func (RepoReadmeDocument) TableName() string { return RepoReadmeDocumentTable }

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

func (SummaryCheck) TableName() string { return SummaryCheckTable }

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

func (AICall) TableName() string { return AICallTable }

type TopicCandidate struct {
	ID                  int64    `gorm:"column:id;primaryKey;autoIncrement"`
	RepoID              int64    `gorm:"column:repo_id"`
	AngleKey            string   `gorm:"column:angle_key"`
	TitleZh             string   `gorm:"column:title_zh"`
	ReaderValue         string   `gorm:"column:reader_value"`
	SourceURL           string   `gorm:"column:source_url"`
	SourceFingerprint   *string  `gorm:"column:source_fingerprint"`
	Reason              string   `gorm:"column:reason"`
	TopicScore          float64  `gorm:"column:topic_score"`
	HotnessScore        *float64 `gorm:"column:hotness_score"`
	Growth24h           *int64   `gorm:"column:growth_24h"`
	Status              string   `gorm:"column:status"`
	Feedback            *string  `gorm:"column:feedback"`
	ReviewedAt          *int64   `gorm:"column:reviewed_at"`
	CreatedAt           int64    `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt           int64    `gorm:"column:updated_at;autoUpdateTime:false"`
	ScoreVersion        int      `gorm:"column:score_version;default:1"`
	ScoreDetailsJSON    string   `gorm:"column:score_details_json"`
	EditorialReviewJSON string   `gorm:"column:editorial_review_json"`
	ReleaseKey          string   `gorm:"column:release_key"`
}

func (TopicCandidate) TableName() string { return TopicCandidateTable }

// TopicReviewHistory 保存重入前的状态、评分和人工复核。
type TopicReviewHistory struct {
	ID           int64 `gorm:"primaryKey"`
	TopicID      int64
	ArchivedAt   int64
	SnapshotJSON string
}

func (TopicReviewHistory) TableName() string { return "topic_review_history" }
