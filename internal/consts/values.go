package consts

const DefaultSearchMinStars = 200

// 仓库、任务、摘要和备份的状态值与接口或数据库约定一致。
const (
	StatusActive         = "active"
	StatusCandidate      = "candidate"
	StatusPaused         = "paused"
	StatusUnavailable    = "unavailable"
	StatusAddressPending = "address_pending"
	StatusPending        = "pending"
	StatusQueued         = "queued"
	StatusRunning        = "running"
	StatusFailed         = "failed"
	StatusUnknown        = "unknown"
	StatusDead           = "dead"
	StatusCanceled       = "canceled"
	StatusSucceeded      = "succeeded"
	StatusCompleted      = "completed"
	StatusReserved       = "reserved"
	StatusSending        = "sending"
	StatusReady          = "ready"
	StatusStale          = "stale"
	StatusMissing        = "missing"
	StatusShortlisted    = "shortlisted"
	StatusAccepted       = "accepted"
	StatusIgnored        = "ignored"
	StatusPublished      = "published"
)

// 采集和摘要队列使用固定任务类型，状态持久化后需可被后续版本识别。
const (
	JobRefreshRepo      = "refresh_repo"
	JobDiscoverSearch   = "discover_search"
	JobDiscoverTrending = "discover_trending"
	JobDiscoverManual   = "discover_manual"
	JobStarHistory      = "star_history"
	JobRefreshSummary   = "refresh_readme_summary"
	JobTranslateReadme  = "translate_readme"
	JobComposeReport    = "compose_report"
	OriginAuto          = "auto"
	OriginManual        = "manual"
)

// 发现来源、采样等级和时间窗口在设置、采集与查询之间共享。
const (
	SourceSearch        = "search"
	SourceTrending      = "trending"
	SourceManual        = "manual"
	TrendingAll         = "all"
	TrendingAllDaily    = "all:daily"
	PeriodDaily         = "daily"
	PeriodWeekly        = "weekly"
	PeriodMonthly       = "monthly"
	ResolutionHourly    = "hourly"
	ResolutionDaily     = "daily"
	ResolutionRaw       = "raw"
	ResolutionMixed     = "mixed"
	GradeS              = "S"
	GradeA              = "A"
	GradeB              = "B"
	GradeC              = "C"
	QualityExact        = "exact"
	QualityEstimated    = "estimated"
	QualityInsufficient = "insufficient"
	Window1h            = "1h"
	Window24h           = "24h"
	Window7d            = "7d"
	Window30d           = "30d"
	Window180d          = "180d"
	WindowPrevious24h   = "previous24h"
)

// 命令行子命令是启动入口的固定标识。
const (
	CommandServer                = "server"
	CommandMigrate               = "migrate"
	CommandCollectOnce           = "collect-once"
	CommandMaintenanceOnce       = "maintenance-once"
	CommandBackup                = "backup"
	CommandHashPassword          = "hash-password"
	CommandGenerateSessionSecret = "generate-session-secret"
)

// 列表排序标识和分类回退标识。
const (
	SortDiscovered = "discovered"
	SortRelevance  = "relevance"
	SortStars      = "stars"
	SortFollowed   = "followed"
	SortHotness    = "hotness"
	SortGrowth24h  = "growth24h"
	SortGrowth7d   = "growth7d"
	CategoryOther  = "other"
)

// 备份工作类型和操作阶段。
const (
	OperationBackup   = "backup"
	OperationRestore  = "restore"
	OperationCleanup  = "cleanup"
	OperationSettings = "settings"
	PhaseDrain        = "drain"
	PhaseCopy         = "copy"
	PhasePrepare      = "prepare"
	PhaseDone         = "done"
	PhaseReload       = "reload"
	R2Region          = "auto"
)

// AI 摘要来源质量和语言标识。
const (
	SourceSufficient     = "sufficient"
	SourceInsufficient   = "insufficient_source"
	SourceIncomplete     = "incomplete_source"
	LanguageUndetermined = "und"
	LanguageChinese      = "zh"
)

// 队列、选题和采集状态使用固定标识。
const (
	TopicAngleProject    = "project"
	AutoJobPattern       = OriginAuto + ":%"
	TrendingStatePrefix  = SourceTrending + ":"
	TrendingStatePattern = TrendingStatePrefix + "%"
	AdmissionDayKey      = "admission.day"
)

// 去重键和来源指纹的格式属于持久化约定。
const (
	RefreshAutoDedupFormat       = OriginAuto + ":" + JobRefreshRepo + ":%d:%d"
	RefreshManualDedupFormat     = OriginManual + ":" + JobRefreshRepo + ":%d:%s"
	DiscoveryAutoDedupFormat     = OriginAuto + ":discover_%s:%d:%d"
	SearchShardDedupFormat       = OriginAuto + ":" + JobDiscoverSearch + ":%d:%d:%d:%d:%d:%d:%d"
	ReadmeAutoDedupFormat        = OriginAuto + ":readme:%d:%s:%s"
	ReadmeManualDedupFormat      = OriginManual + ":readme:%d:%s"
	SummaryAutoDedupFormat       = OriginAuto + ":summary:%d:%d"
	SummaryManualDedupFormat     = OriginManual + ":summary:%d:%s"
	ReadmeFingerprintPrefix      = "readme:"
	DescriptionFingerprintFormat = "description:%x"
)
