package storage

// 表名和字段映射用于构造查询。GORM 标签和版本化迁移使用同一组实际名称。
const (
	AICallTable                = "ai_calls"
	AISummaryTable             = "ai_summaries"
	CategorySettingTable       = "categories"
	DailyMetricTable           = "metrics_daily"
	DiscoveryRuleTable         = "discovery_rules"
	HourlyMetricTable          = "metrics_hourly"
	JobTable                   = "jobs"
	LatestMetricTable          = "latest_metrics"
	MaintenanceStateTable      = "maintenance_state"
	PendingMetricTable         = "metric_rollup_pending"
	ProjectThemeTable          = "project_themes"
	ProjectThemeGroupTable     = "project_theme_groups"
	RateLimitTable             = "rate_limits"
	RepoAITopicsTable          = "repo_ai_topics"
	RepoReadmeTranslationTable = "repo_readme_translations"
	RepoReadmeDocumentTable    = "repo_readme_documents"
	RepoSnapshotTable          = "repo_snapshots"
	RepositoryTable            = "repositories"
	SearchObservationTable     = "search_observations"
	SummaryCheckTable          = "summary_checks"
	SystemSettingTable         = "system_settings"
	TopicCandidateTable        = "topic_candidates"
	TrendingBatchRecordTable   = "trending_batches"
	TrendingObservationTable   = "trending_observations"
)

// AICallColumns 对应 AICall 的 GORM 字段。
var AICallColumns = struct {
	CallKey           string
	JobID             string
	Provider          string
	ProviderRequestID string
	Origin            string
	Status            string
	ReservedAt        string
	UpdatedAt         string
	InputTokens       string
	OutputTokens      string
	EstimatedCost     string
	DurationMS        string
	ErrorCode         string
}{
	CallKey:           "call_key",
	JobID:             "job_id",
	Provider:          "provider",
	ProviderRequestID: "provider_request_id",
	Origin:            "origin",
	Status:            "status",
	ReservedAt:        "reserved_at",
	UpdatedAt:         "updated_at",
	InputTokens:       "input_tokens",
	OutputTokens:      "output_tokens",
	EstimatedCost:     "estimated_cost",
	DurationMS:        "duration_ms",
	ErrorCode:         "error_code",
}

// AISummaryColumns 对应 AISummary 的 GORM 字段。
var AISummaryColumns = struct {
	RepoID               string
	ReadmeSHA            string
	SourceFingerprint    string
	SourceQuality        string
	SourceLanguage       string
	TitleZh              string
	SummaryZh            string
	RecommendationReason string
	FeaturesJSON         string
	AudienceJSON         string
	ValueTagsJSON        string
	EditorialBriefJSON   string
	SourceReadmeURL      string
	ModelProvider        string
	ModelName            string
	PromptVersion        string
	SummaryStatus        string
	GeneratedAt          string
}{
	RepoID:               "repo_id",
	ReadmeSHA:            "readme_sha",
	SourceFingerprint:    "source_fingerprint",
	SourceQuality:        "source_quality",
	SourceLanguage:       "source_language",
	TitleZh:              "title_zh",
	SummaryZh:            "summary_zh",
	RecommendationReason: "recommendation_reason",
	FeaturesJSON:         "features_json",
	AudienceJSON:         "audience_json",
	ValueTagsJSON:        "value_tags_json",
	EditorialBriefJSON:   "editorial_brief_json",
	SourceReadmeURL:      "source_readme_url",
	ModelProvider:        "model_provider",
	ModelName:            "model_name",
	PromptVersion:        "prompt_version",
	SummaryStatus:        "summary_status",
	GeneratedAt:          "generated_at",
}

// CategorySettingColumns 对应 CategorySetting 的 GORM 字段。
var CategorySettingColumns = struct {
	Slug       string
	Name       string
	TopicsJSON string
	Position   string
}{
	Slug:       "slug",
	Name:       "name",
	TopicsJSON: "topics_json",
	Position:   "position",
}

// 指标表共用嵌入的 MetricAnchors，字段映射也随各表嵌入。
type metricAnchorColumns struct {
	StarsOpen       string
	StarsClose      string
	OpenObservedAt  string
	CloseObservedAt string
	OpenSlot        string
	CloseSlot       string
	SampleCount     string
}

var metricAnchors = metricAnchorColumns{
	StarsOpen:       "stars_open",
	StarsClose:      "stars_close",
	OpenObservedAt:  "open_observed_at",
	CloseObservedAt: "close_observed_at",
	OpenSlot:        "open_slot",
	CloseSlot:       "close_slot",
	SampleCount:     "sample_count",
}

// DailyMetricColumns 对应 DailyMetric 的 GORM 字段。
var DailyMetricColumns = struct {
	metricAnchorColumns
	RepoID       string
	Day          string
	Delta        string
	Quality      string
	HotnessScore string
	RankInDay    string
}{
	metricAnchorColumns: metricAnchors,
	RepoID:              "repo_id",
	Day:                 "day",
	Delta:               "delta",
	Quality:             "quality",
	HotnessScore:        "hotness_score",
	RankInDay:           "rank_in_day",
}

// DiscoveryRuleColumns 对应 DiscoveryRule 的 GORM 字段。
var DiscoveryRuleColumns = struct {
	ID              string
	SourceType      string
	SourceKey       string
	Query           string
	MinStars        string
	Enabled         string
	IntervalMinutes string
	NextRunAt       string
	Priority        string
	DisplayPosition string
	AgeWindow       string
}{
	ID:              "id",
	SourceType:      "source_type",
	SourceKey:       "source_key",
	Query:           "query",
	MinStars:        "min_stars",
	Enabled:         "enabled",
	IntervalMinutes: "interval_minutes",
	NextRunAt:       "next_run_at",
	Priority:        "priority",
	DisplayPosition: "display_position",
	AgeWindow:       "age_window",
}

// HourlyMetricColumns 对应 HourlyMetric 的 GORM 字段。
var HourlyMetricColumns = struct {
	metricAnchorColumns
	RepoID    string
	HourStart string
	Delta     string
	Quality   string
}{
	metricAnchorColumns: metricAnchors,
	RepoID:              "repo_id",
	HourStart:           "hour_start",
	Delta:               "delta",
	Quality:             "quality",
}

// JobColumns 对应 Job 的 GORM 字段。
var JobColumns = struct {
	ID             string
	Type           string
	RepoID         string
	DiscoveryRunID string
	DedupKey       string
	PayloadJSON    string
	Priority       string
	Status         string
	AvailableAt    string
	LeaseUntil     string
	LeaseToken     string
	Attempts       string
	MaxAttempts    string
	LastErrorCode  string
	CreatedAt      string
	UpdatedAt      string
}{
	ID:             "id",
	Type:           "type",
	RepoID:         "repo_id",
	DiscoveryRunID: "discovery_run_id",
	DedupKey:       "dedup_key",
	PayloadJSON:    "payload_json",
	Priority:       "priority",
	Status:         "status",
	AvailableAt:    "available_at",
	LeaseUntil:     "lease_until",
	LeaseToken:     "lease_token",
	Attempts:       "attempts",
	MaxAttempts:    "max_attempts",
	LastErrorCode:  "last_error_code",
	CreatedAt:      "created_at",
	UpdatedAt:      "updated_at",
}

// LatestMetricColumns 对应 LatestMetric 的 GORM 字段。
var LatestMetricColumns = struct {
	RepoID             string
	Delta1h            string
	Delta24h           string
	Delta7d            string
	Delta30d           string
	RelativeGrowth     string
	Acceleration       string
	PersistenceScore   string
	DailyConcentration string
	TrendingFrequency  string
	TrendingStreak     string
	TrendingRank       string
	TrendingScore      string
	HotnessScore       string
	CandidateBurst     string
	BreakoutJSON       string
	DataQuality        string
	WindowDetailsJSON  string
	CalculatedAt       string
}{
	RepoID:             "repo_id",
	Delta1h:            "delta_1h",
	Delta24h:           "delta_24h",
	Delta7d:            "delta_7d",
	Delta30d:           "delta_30d",
	RelativeGrowth:     "relative_growth",
	Acceleration:       "acceleration",
	PersistenceScore:   "persistence_score",
	DailyConcentration: "daily_concentration",
	TrendingFrequency:  "trending_frequency",
	TrendingStreak:     "trending_streak",
	TrendingRank:       "trending_rank",
	TrendingScore:      "trending_score",
	HotnessScore:       "hotness_score",
	CandidateBurst:     "candidate_burst",
	BreakoutJSON:       "breakout_json",
	DataQuality:        "data_quality",
	WindowDetailsJSON:  "window_details_json",
	CalculatedAt:       "calculated_at",
}

// PendingMetricColumns 对应 PendingMetric 的 GORM 字段。
var PendingMetricColumns = struct {
	metricAnchorColumns
	RepoID      string
	Resolution  string
	BucketStart string
}{
	metricAnchorColumns: metricAnchors,
	RepoID:              "repo_id",
	Resolution:          "resolution",
	BucketStart:         "bucket_start",
}

// ProjectThemeColumns 对应 ProjectTheme 的 GORM 字段。
var ProjectThemeColumns = struct {
	AliasesJSON string
	OwnersJSON  string
	GroupSlug   string
	Icon        string
	IconColor   string
	Slug        string
	Name        string
	Description string
	TopicsJSON  string
	Position    string
}{
	AliasesJSON: "aliases_json",
	OwnersJSON:  "owners_json",
	GroupSlug:   "group_slug",
	Icon:        "icon",
	IconColor:   "icon_color",
	Slug:        "slug",
	Name:        "name",
	Description: "description",
	TopicsJSON:  "topics_json",
	Position:    "position",
}

// ProjectThemeGroupColumns 对应 ProjectThemeGroup 的 GORM 字段。
var ProjectThemeGroupColumns = struct {
	Slug, Name, Description, Position string
}{
	Slug: "slug", Name: "name", Description: "description", Position: "position",
}

// RateLimitColumns 对应 RateLimit 的 GORM 字段。
var RateLimitColumns = struct {
	Resource        string
	LimitTotal      string
	Remaining       string
	ResetAt         string
	PausedUntil     string
	ObservedAt      string
	LocalWindow     string
	LocalRequests   string
	LimitedCount    string
	SecondaryStreak string
}{
	Resource:        "resource",
	LimitTotal:      "limit_total",
	Remaining:       "remaining",
	ResetAt:         "reset_at",
	PausedUntil:     "paused_until",
	ObservedAt:      "observed_at",
	LocalWindow:     "local_window",
	LocalRequests:   "local_requests",
	LimitedCount:    "limited_count",
	SecondaryStreak: "secondary_streak",
}

// RepoAITopicsColumns 对应 RepoAITopics 的 GORM 字段。
var RepoAITopicsColumns = struct {
	ThemeSlugsJSON    string
	ThemeCatalogKey   string
	RepoID            string
	TopicsJSON        string
	SourceFingerprint string
	ModelProvider     string
	ModelName         string
	PromptVersion     string
	GeneratedAt       string
}{
	ThemeSlugsJSON:    "theme_slugs_json",
	ThemeCatalogKey:   "theme_catalog_key",
	RepoID:            "repo_id",
	TopicsJSON:        "topics_json",
	SourceFingerprint: "source_fingerprint",
	ModelProvider:     "model_provider",
	ModelName:         "model_name",
	PromptVersion:     "prompt_version",
	GeneratedAt:       "generated_at",
}

// RepoReadmeTranslationColumns 对应 RepoReadmeTranslation 的 GORM 字段。
var RepoReadmeTranslationColumns = struct {
	RepoID      string
	ReadmeSHA   string
	ReadmeBytes string
	ModelKey    string
	PartsJSON   string
	Complete    string
}{
	RepoID:      "repo_id",
	ReadmeSHA:   "readme_sha",
	ReadmeBytes: "readme_bytes",
	ModelKey:    "model_key",
	PartsJSON:   "parts_json",
	Complete:    "complete",
}

// RepoSnapshotColumns 对应 RepoSnapshot 的 GORM 字段。
var RepoSnapshotColumns = struct {
	RepoID     string
	SampleSlot string
	ObservedAt string
	Stars      string
	Forks      string
}{
	RepoID:     "repo_id",
	SampleSlot: "sample_slot",
	ObservedAt: "observed_at",
	Stars:      "stars",
	Forks:      "forks",
}

// RepositoryColumns 对应 Repository 的 GORM 字段。
var RepositoryColumns = struct {
	RepoID                   string
	Owner                    string
	Name                     string
	FullName                 string
	NameIsCurrent            string
	HTMLURL                  string
	Description              string
	TopicsJSON               string
	PrimaryLanguage          string
	LicenseSPDXID            string
	LicenseName              string
	Stars                    string
	Forks                    string
	Archived                 string
	Disabled                 string
	Blocked                  string
	Grade                    string
	BelowGradeSince          string
	BreakoutGradeJSON        string
	ObservationProbeSignalAt string
	ObservationProbeDueAt    string
	StarredByUser            string
	FollowedAt               string
	State                    string
	GitHubCreatedAt          string
	GitHubPushedAt           string
	DiscoveredAt             string
	LastDiscoveredAt         string
	LastCheckedAt            string
	LastSuccessAt            string
	FirstNotFoundAt          string
	NextFetchAt              string
	MetadataETag             string
	UpdatedAt                string
}{
	RepoID:                   "repo_id",
	Owner:                    "owner",
	Name:                     "name",
	FullName:                 "full_name",
	NameIsCurrent:            "name_is_current",
	HTMLURL:                  "html_url",
	Description:              "description",
	TopicsJSON:               "topics_json",
	PrimaryLanguage:          "primary_language",
	LicenseSPDXID:            "license_spdx_id",
	LicenseName:              "license_name",
	Stars:                    "stars",
	Forks:                    "forks",
	Archived:                 "archived",
	Disabled:                 "disabled",
	Blocked:                  "blocked",
	Grade:                    "grade",
	BelowGradeSince:          "below_grade_since",
	BreakoutGradeJSON:        "breakout_grade_json",
	ObservationProbeSignalAt: "observation_probe_signal_at",
	ObservationProbeDueAt:    "observation_probe_due_at",
	StarredByUser:            "starred_by_user",
	FollowedAt:               "followed_at",
	State:                    "state",
	GitHubCreatedAt:          "github_created_at",
	GitHubPushedAt:           "github_pushed_at",
	DiscoveredAt:             "discovered_at",
	LastDiscoveredAt:         "last_discovered_at",
	LastCheckedAt:            "last_checked_at",
	LastSuccessAt:            "last_success_at",
	FirstNotFoundAt:          "first_not_found_at",
	NextFetchAt:              "next_fetch_at",
	MetadataETag:             "metadata_etag",
	UpdatedAt:                "updated_at",
}

// SearchObservationColumns 对应 SearchObservation 的 GORM 字段。
var SearchObservationColumns = struct {
	RepoID       string
	SourceKey    string
	ObservedSlot string
	ObservedAt   string
}{
	RepoID:       "repo_id",
	SourceKey:    "source_key",
	ObservedSlot: "observed_slot",
	ObservedAt:   "observed_at",
}

// SummaryCheckColumns 对应 SummaryCheck 的 GORM 字段。
var SummaryCheckColumns = struct {
	RepoID            string
	ReadmeETag        string
	LastSeenReadmeSHA string
	SourceFingerprint string
	SourceLanguage    string
	LastCheckedAt     string
	NextCheckAt       string
	LastErrorCode     string
}{
	RepoID:            "repo_id",
	ReadmeETag:        "readme_etag",
	LastSeenReadmeSHA: "last_seen_readme_sha",
	SourceFingerprint: "source_fingerprint",
	SourceLanguage:    "source_language",
	LastCheckedAt:     "last_checked_at",
	NextCheckAt:       "next_check_at",
	LastErrorCode:     "last_error_code",
}

// SystemSettingColumns 对应 SystemSetting 的 GORM 字段。
var SystemSettingColumns = struct {
	Key       string
	ValueJSON string
	UpdatedAt string
}{
	Key:       "key",
	ValueJSON: "value_json",
	UpdatedAt: "updated_at",
}

// TopicCandidateColumns 对应 TopicCandidate 的 GORM 字段。
var TopicCandidateColumns = struct {
	ID                  string
	RepoID              string
	AngleKey            string
	TitleZh             string
	ReaderValue         string
	SourceURL           string
	SourceFingerprint   string
	Reason              string
	TopicScore          string
	HotnessScore        string
	Growth24h           string
	Status              string
	Feedback            string
	ReviewedAt          string
	CreatedAt           string
	UpdatedAt           string
	ScoreVersion        string
	ScoreDetailsJSON    string
	EditorialReviewJSON string
	ReleaseKey          string
}{
	ID:                  "id",
	RepoID:              "repo_id",
	AngleKey:            "angle_key",
	TitleZh:             "title_zh",
	ReaderValue:         "reader_value",
	SourceURL:           "source_url",
	SourceFingerprint:   "source_fingerprint",
	Reason:              "reason",
	TopicScore:          "topic_score",
	HotnessScore:        "hotness_score",
	Growth24h:           "growth_24h",
	Status:              "status",
	Feedback:            "feedback",
	ReviewedAt:          "reviewed_at",
	CreatedAt:           "created_at",
	UpdatedAt:           "updated_at",
	ScoreVersion:        "score_version",
	ScoreDetailsJSON:    "score_details_json",
	EditorialReviewJSON: "editorial_review_json",
	ReleaseKey:          "release_key",
}

// TrendingBatchRecordColumns 对应 TrendingBatchRecord 的 GORM 字段。
var TrendingBatchRecordColumns = struct {
	SourceKey    string
	Window       string
	ObservedSlot string
	ObservedAt   string
}{
	SourceKey:    "source_key",
	Window:       "window",
	ObservedSlot: "observed_slot",
	ObservedAt:   "observed_at",
}

// TrendingObservationColumns 对应 TrendingObservation 的 GORM 字段。
var TrendingObservationColumns = struct {
	RepoID        string
	SourceKey     string
	Window        string
	ObservedSlot  string
	Rank          string
	ReportedStars string
}{
	RepoID:        "repo_id",
	SourceKey:     "source_key",
	Window:        "window",
	ObservedSlot:  "observed_slot",
	Rank:          "rank",
	ReportedStars: "reported_stars",
}
