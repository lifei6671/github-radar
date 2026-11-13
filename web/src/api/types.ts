export type Quality = 'exact' | 'estimated' | 'insufficient'
export type Resolution = 'raw' | 'hourly' | 'daily' | 'mixed'
export type SummaryStatus = 'missing' | 'queued' | 'running' | 'ready' | 'stale' | 'failed' | 'unknown'
export type SourceQuality = 'sufficient' | 'incomplete_source' | 'insufficient_source'
export type JobStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'dead' | 'unknown' | 'canceled'
export type TopicStatus = 'candidate' | 'shortlisted' | 'accepted' | 'ignored' | 'published'
export type TrendRange = '24h' | '7d' | '30d' | '180d'

export interface Growth {
  delta: number | null
  quality: Quality
  startAt: string | null
  endAt: string | null
  resolution: Resolution
}

export interface Category {
  slug: string
  name: string
  count: number
  latestRepo: Pick<Repo, 'id' | 'fullName' | 'description' | 'discoveredAt'> | null
}

export interface ProjectTheme {
  groupSlug: string
  slug: string
  name: string
  description: string
  icon: string
  iconColor: string
  count: number
  recentCount: number
  latestRepo: Pick<Repo, 'id' | 'fullName' | 'discoveredAt'> | null
}

export interface ProjectThemeGroup { slug: string; name: string; description: string }
export interface ProjectThemeDirectory { items: ProjectTheme[]; groups: ProjectThemeGroup[] }

export interface ThemeMilestone { kind: 'trending' | 'stars'; fullName: string; at: string; value: number }
export interface ProjectThemeDetail extends ProjectTheme { groupName: string; milestones: ThemeMilestone[] }

export interface TrendingEntry {
  sourceKey: string
  period: 'daily' | 'weekly' | 'monthly'
  rank: number
  observedAt: string
}

export interface SparkPoint { at: string; stars: number | null; observedAt: string | null }

export interface Repo {
  id: number
  fullName: string
  nameIsCurrent?: boolean
  htmlUrl: string
  description: string | null
  titleZh: string | null
  language: string | null
  categories: string[]
  valueTags: string[]
  state: 'candidate' | 'active' | 'paused' | 'unavailable' | 'address_pending'
  grade: 'S' | 'A' | 'B' | 'C'
  stars: number
  forks: number
  growth24h: Growth
  growth7d: Growth
  relativeGrowth: number | null
  hotnessScore: number | null
  hotnessRank: number | null
  trendingEntries: TrendingEntry[]
  sparkline: SparkPoint[]
  dataQuality: Quality
  metricsUpdatedAt: string | null
  candidateBurst: boolean
  summaryPreview: string | null
  recommendationReason?: string | null
  selected?: boolean
  summaryStatus: SummaryStatus
  summarySourceQuality: SourceQuality | null
  summaryJobId?: number | null
  followed?: boolean
  blocked?: boolean
  followedAt?: string | null
  discoveredAt: string
  discoveryAt?: string
  lastSuccessAt: string | null
}

export interface RepoDetail extends Repo {
  growth1h: Growth
  growth30d: Growth
  license: { spdxId: string | null; name: string | null } | null
  summaryZh: string | null
  summaryGeneratedAt: string | null
  summaryModel?: string | null
  summaryAvailable?: boolean
  discoveryEvents?: DiscoveryEvent[]
  observationHistory?: ObservationTransition[]
  sourceReadmeUrl: string | null
  readme: RepoReadme
  features: string[]
  audience: string[]
  aiTopics: string[]
}

export interface ObservationTransition {
  id: number
  at: string
  fromState: Repo['state']
  toState: Repo['state']
  fromGrade: Repo['grade']
  toGrade: Repo['grade']
  reason: string
  evidence: { stars: number; exact24h?: number; lastSignalAt?: number; requestedGrade?: Repo['grade'] }
}

export interface DiscoveryEvent {
  sourceType: string
  sourceKey: string
  sourceUrl: string
  firstObservedAt: string
  note: string
}

export interface RepoReadme {
  state: 'ready' | 'pending' | 'missing' | 'unavailable'
  language?: string
  sourceUrl?: string
  chineseHtml?: string
  complete: boolean
  truncated: boolean
  translationJobId?: number
}

export interface TrendPoint {
  at: string
  stars: number | null
  delta: number | null
  quality: Quality
  startAt: string | null
  endAt: string | null
}

export interface Trend { points: TrendPoint[]; resolution: Resolution; updatedAt: string }

export interface StarHistoryDay {
  date: string
  count: number | null
  state: 'complete' | 'partial' | 'missing'
}

export interface StarBreakout {
  source: 'local_net_24h' | 'github_daily_stars'
  value: number
  at: number
  startAt: number
  coveredDays: number
  baseline: number | null
  ratio: number | null
  absolute: boolean
  minimum: number
  direct: number
  minimumRatio: number
  minimumDays: number
  smoothing: number
}

export interface StarHistory {
  days: StarHistoryDay[]
  breakout: StarBreakout | null
  fetchedAt?: string
  errorCode?: string | null
  jobId?: number
  gradeEvidence?: StarBreakout
}

export interface Page<T> { items: T[]; nextCursor: string | null; updatedAt: string | null }
export interface Items<T> { items: T[] }
export interface Trending extends Items<Repo> {
  sourceKey: string
  period: TrendingEntry['period']
  batch: { observedSlot: number; observedAt: string } | null
  updatedAt: string | null
}

export interface Job {
  id: number
  type: string
  status: JobStatus
  attempts: number
  maxAttempts: number
  errorCode: string | null
  repoId: number | null
  repoFullName?: string | null
  repoNameIsCurrent?: boolean | null
  submittedRepository?: string
  createdAt: string
  updatedAt: string
}
export interface JobAccepted { jobId: number; statusUrl: string }
export interface RepoSubmissionItem { line: number; input: string; jobId?: number; errorCode?: string }
export type SummaryResponse = JobAccepted | { cached: true; summaryStatus: 'ready' }

export interface Topic {
  id: number
  repoId: number
  fullName: string
  nameIsCurrent?: boolean
  titleZh: string
  readerValue: string
  sourceUrl: string
  summaryZh: string | null
  valueTags: string[]
  reason: string
  topicScore: number
  hotnessScore: number | null
  recommendationScore: number | null
  growth24h?: number | null
  scoreVersion?: number
  suggestion?: 'prepare' | 'candidate' | 'observe'
  assessment?: {
    revision?: string
    dimensions: { key: string; points: number; maximum: number; source: string; confidence: string; evidence: string }[]
    brief: { readerBenefit?: string; demo?: string; angle?: string; steps?: string[]; risks?: string[] }
  }
  review?: { verification?: 'pending' | 'verified' | 'failed'; evidence?: string; override?: string }
  status: TopicStatus
  feedback: string | null
  reviewedAt: string | null
  createdAt: string
  updatedAt: string
}

export interface TopicDay { day: string; title: string; count: number }

export interface TopicEvidence {
  events: DiscoveryEvent[]
  trending: { source: string; period: string; rank: number; at: string }[]
  history: (Pick<Topic, 'id' | 'status' | 'feedback' | 'topicScore' | 'scoreVersion' | 'review' | 'sourceUrl'> & { archivedAt: string })[]
}

export interface Limits {
  githubConfigured: boolean
  waitingTaskCount: number
  items: { resource: string; limit: number; remaining: number; resetAt: string; pausedUntil: string | null; localRequests: number }[]
  ai: { auto: Budget; manual: Budget; resetAt: string }
}
interface Budget { used: number; limit: number }

export interface StorageStatus {
  databaseBytes: number
  walBytes: number
  pageCount: number
  freelistCount: number
  counts: { raw: number; hourly: number; daily: number }
  watermarks: { hourly: string | null; daily: string | null; cleanedAt: string | null; metricsUpdatedAt: string | null }
}

export type SettingValue = boolean | number | string | null | SettingValue[] | SettingFields
export interface SettingFields { [key: string]: SettingValue }
export interface RuntimeSettings { revision: number; settings: SettingFields; restartRequired: boolean }
export interface CategorySetting { slug: string; name: string; anyTopics: string[] }
export interface ProjectThemeSetting { slug: string; name: string; description: string; anyTopics: string[]; aliases?: string[]; owners?: string[]; icon: string; iconColor: string; groupSlug: string }
export interface DiscoveryTab { slug: string; label: string }
export interface DiscoveryTabOption { slug: string; name: string }
export interface DiscoverySelection {
  recentDays: number
  recentSampleHours: number
  growthSampleHours: number
  trendingHours: number
  trendingTop: number
}
export interface DiscoveryRanking {
  heatWeight: number
  freshnessWeight: number
  consecutiveDays: number
  cooldownDays: number
}
export interface DiscoveryTabSettings extends ListSettings<DiscoveryTab> {
  options: DiscoveryTabOption[]
  selection: DiscoverySelection
  ranking: DiscoveryRanking
}
export interface RuleSetting {
  sourceType: 'trending' | 'search'
  sourceKey: string
  query: string
  minStars?: number
  enabled: boolean
  intervalMinutes: number
  priority: number
  ageWindow?: '' | '0-3' | '4-14' | '15-30'
}

export interface DiscoveryRun {
  id: number
  sourceType: RuleSetting['sourceType']
  sourceKey: string
  pass: 'primary' | 'backfill'
  plannedAt: string
  startedAt: string | null
  finishedAt: string | null
  createdFrom: string | null
  createdUntil: string | null
  delayMs: number | null
  upstreamTotal: number | null
  plannedShards: number
  completedShards: number
  uniqueRepos: number
  newRepos: number
  status: JobStatus
  errorCode: string | null
  complete: boolean
}

export interface DiscoveryRuns {
  items: DiscoveryRun[]
  alerts: { sourceType: RuleSetting['sourceType']; sourceKey: string; reason: 'consecutive_failures' | 'quota_backlog' | 'not_run' }[]
}
export interface ListSettings<T> { revision: number; items: T[] }
