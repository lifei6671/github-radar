import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { UseQueryResult } from '@tanstack/react-query'
import { ArrowLeft, CalendarDays, Clock3, GitFork, Plus, Star } from 'lucide-react'
import { lazy, Suspense, useEffect } from 'react'
import { Link, Navigate, useLocation, useNavigate, useParams } from 'react-router'
import { api, ApiError, errorText, publicApi, useSession } from '../api/client'
import { useAdminArea, useCategories, withdrawPublicRepo } from '../api/queries'
import type { RepoDetail, Topic, Trend, TrendRange } from '../api/types'
import { External, GrowthValue, MonitorButton, RepoBlockButton } from '../components/repo'
import { RefreshAction, SummaryActions } from '../components/tasks'
import { RepoReading } from '../components/readme'
import { ActionButton, Badge, Empty, Notice, PageError, Skeleton } from '../components/ui'
import { decimal, encodedRepoName, number, qualityLabel, relativeTime, repoPath, repoState, resolutionLabel, sourceLabel, summaryLabel, timestamp } from '../lib/format'

const TrendChart = lazy(() => import('../components/trend-chart'))
const ranges: [TrendRange, string][] = [['24h', '24 小时'], ['7d', '7 天'], ['30d', '30 天'], ['180d', '180 天']]
const sections = [['#summary', '仓库概览'], ['#readme', 'README'], ['#star-trend', 'Star 趋势']] as const
const periodLabel = { daily: '日榜', weekly: '周榜', monthly: '月榜' }
const repoNotFound = new ApiError(404, { code: 'not_found' })

export function Detail() {
  const { owner, name } = useParams()
  const admin = useAdminArea()
  const { authenticated } = useSession()
  const location = useLocation()
  const from: unknown = location.state?.from
  const back = typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') ? from : admin ? '/admin/repos' : '/repos'
  const read = admin ? api : publicApi
  const scope = admin ? 'admin' : 'public'
  const queryClient = useQueryClient()
  const params = new URLSearchParams(location.search)
  // 收藏与管理入口携带仓库身份，名称复用后仍指向所选记录。
  const identityParam = params.get('repoId')
  const pinned: unknown = identityParam !== null ? Number(identityParam) : location.state?.repoId
  const lookupId = typeof pinned === 'number' && Number.isSafeInteger(pinned) && pinned > 0 ? pinned : undefined
  const invalidIdentity = identityParam !== null && lookupId === undefined
  const fullName = owner && name ? owner + '/' + name : ''
  const detailKey = ['repo', lookupId ?? fullName.toLowerCase(), scope] as const
  const detail = useQuery<RepoDetail | null>({
    queryKey: detailKey,
    enabled: !invalidIdentity,
    ...(!admin && lookupId !== undefined && { staleTime: 0 }),
    queryFn: async ({ signal }) => {
      try {
        const path = admin && lookupId !== undefined ? '/repos/' + lookupId : '/repos/by-name/' + encodedRepoName(fullName) +
          (lookupId === undefined ? '' : '?repoId=' + lookupId)
        return await read<RepoDetail>(path, { signal })
      } catch (error) {
        if (!admin && error instanceof ApiError && error.status === 404) return null
        throw error
      }
    },
  })
  const requestedRange = params.get('range')
  const range = ranges.find(([key]) => key === requestedRange)?.[0] ?? '7d'
  const withdrawn = detail.data === null
  const loadedRepoId = detail.data?.id
  const resolvedPath = !admin && lookupId !== undefined && detail.data ? repoPath(detail.data.fullName) : null
  const canonicalize = resolvedPath !== null && (resolvedPath !== location.pathname || identityParam !== null)
  const resolvingBookmark = !admin && lookupId !== undefined && detail.isFetching
  const wrongHistoricalName = admin && identityParam !== null && Boolean(detail.data && detail.data.fullName.toLowerCase() !== fullName.toLowerCase())
  const trend = useQuery({
    queryKey: ['trend', loadedRepoId, range, scope],
    enabled: loadedRepoId !== undefined && !canonicalize && !resolvingBookmark && !wrongHistoricalName,
    queryFn: async ({ signal }) => {
      try {
        return await read<Trend>('/repos/' + loadedRepoId + '/trend?range=' + range, { signal })
      } catch (error) {
        if (!admin && loadedRepoId !== undefined && error instanceof ApiError && error.status === 404) {
          // 公开趋势确认撤回后，整个仓库的旧缓存都不能再用于返回或切换时段。
          await withdrawPublicRepo(queryClient, loadedRepoId)
        }
        throw error
      }
    },
  })
  const categories = useCategories()
  const hash = location.hash
  const activeAnchor = sections.some(([anchor]) => anchor === hash) ? hash : '#summary'
  useEffect(() => {
    // 深链接打开时先加载仓库数据，章节挂载后再定位。
    if (loadedRepoId !== undefined && !canonicalize && !resolvingBookmark && sections.some(([anchor]) => anchor === hash)) {
      document.getElementById(hash.slice(1))?.scrollIntoView()
    }
  }, [loadedRepoId, hash, canonicalize, resolvingBookmark])
  if (invalidIdentity || wrongHistoricalName) return <Empty title="仓库地址无效" />
  if (detail.isPending || resolvingBookmark) return <Skeleton />
  if (withdrawn) return <PageError error={repoNotFound} retry={() => { void detail.refetch() }} />
  if (!detail.data) return <PageError error={detail.error} retry={() => { void detail.refetch() }} />
  if (canonicalize && resolvedPath && !detail.error) {
    params.delete('repoId')
    return <Navigate replace to={{ pathname: resolvedPath, search: params.toString(), hash }} state={{ from, repoId: lookupId }} />
  }
  const repo = detail.data
  const names = new Map(categories.data?.items.map((item) => [item.slug, item.name]))
  const showSummaryStatus = Boolean(repo.summaryZh) || (admin && repo.summaryStatus !== 'missing')
  return <div className="repo-detail">
    <nav className="detail-breadcrumbs" aria-label="当前位置">
      <Link to={back}><ArrowLeft size={14} />返回列表</Link><span aria-current="page">仓库详情</span>
      {!admin && authenticated && <Link className="detail-text-link" to={repoPath(repo.fullName, true) + location.search + location.hash}
        state={{ repoId: repo.id }}>管理仓库</Link>}
    </nav>
    <header className="article-head">
      <div className="repo-tags">
        {repo.language && <span className="language"><span />{repo.language}</span>}<Badge>{repo.grade} 级</Badge>
        {repo.state !== 'active' && <Badge tone="warning">{repoState[repo.state]}</Badge>}
        {repo.blocked && <Badge tone="warning">已屏蔽</Badge>}
      </div>
      <h1>{repo.fullName}</h1>
      <div className="detail-stats minor muted">
        <span><Star size={14} aria-hidden /><strong className="numeric">{number(repo.stars)}</strong> Star</span>
        <span><GitFork size={14} aria-hidden /><strong className="numeric">{number(repo.forks)}</strong> Fork</span>
        <span><CalendarDays size={14} aria-hidden /><time dateTime={repo.discoveredAt}>{timestamp(repo.discoveredAt)}</time> 发现</span>
        {repo.lastSuccessAt && <span><Clock3 size={14} aria-hidden /><time dateTime={repo.lastSuccessAt} title={timestamp(repo.lastSuccessAt)}>{relativeTime(repo.lastSuccessAt)}</time> 采样</span>}
      </div>
    </header>
    {detail.error && <Notice error>{errorText(detail.error)} 上次采样 {timestamp(repo.lastSuccessAt)}</Notice>}
    <nav className="reading-links" aria-label="仓库详情章节">
      <div className="pills">{sections.filter(([anchor]) => !repo.blocked || anchor !== '#readme').map(([anchor, label]) => <Link key={anchor} to={{ search: location.search, hash: anchor }}
        state={location.state} preventScrollReset aria-current={activeAnchor === anchor ? 'location' : undefined}>{label}</Link>)}</div>
    </nav>
    <div className="detail-grid">
      <section id="summary" className="panel detail-panel summary-panel" aria-labelledby="repo-summary-title">
        <header className="section-head">
          <h2 id="repo-summary-title">{repo.summaryZh ? 'AI 导读' : '项目简介'}</h2>
          {showSummaryStatus ? <span className="minor muted">{summaryLabel[repo.summaryStatus]}</span> :
            repo.description && <span className="minor muted">GitHub About</span>}
        </header>
        <p className="summary-body">{repo.summaryZh || repo.description || '暂无项目简介'}</p>
        {repo.recommendationReason && <p className="recommendation-reason"><strong>推荐理由</strong>{repo.recommendationReason}</p>}
        {repo.features.length > 0 && <div className="topic-links">
          {repo.features.map((tag) => <Badge key={tag}>{tag}</Badge>)}
        </div>}
        {repo.summaryZh && <div className="summary-meta">
          {repo.summarySourceQuality && <span>{sourceLabel[repo.summarySourceQuality]}</span>}
          {repo.summaryGeneratedAt && <time dateTime={repo.summaryGeneratedAt}>{timestamp(repo.summaryGeneratedAt)} 更新</time>}
          {repo.summaryModel && <span>{repo.summaryModel}</span>}
          <External href={repo.sourceReadmeUrl}>README 来源</External>
        </div>}
        {admin && !repo.blocked && <footer className="summary-footer"><SummaryActions key={repo.id} repo={repo} repoUpdatedAt={detail.dataUpdatedAt} /></footer>}
        {!repo.blocked && <RepoReading key={repo.id + ':' + scope} repo={repo} admin={admin} />}
      </section>
      <DetailAside key={repo.id} repo={repo} names={names} admin={admin} />
      <TrendSection repo={repo} range={range} trend={trend} />
    </div>
  </div>
}

function DetailAside({ repo, names, admin }: { repo: RepoDetail; names: Map<string, string>; admin: boolean }) {
  const license = repo.license?.spdxId === 'NOASSERTION' ? '未识别许可' : repo.license?.name ?? repo.license?.spdxId ?? '未声明'
  return <aside className="detail-aside" aria-label="仓库信息栏">
    <section className="panel detail-side-card" aria-labelledby="repo-heat-title">
      <h2 id="repo-heat-title">热点信号</h2>
      <dl className="detail-side-facts">
        <div><dt>全局热度</dt><dd className="numeric">{repo.hotnessScore === null ? '待观察' : decimal(repo.hotnessScore)}</dd></div>
        <div><dt>评分质量</dt><dd>{qualityLabel[repo.dataQuality]}</dd></div>
        {repo.metricsUpdatedAt && <div><dt>最近计算</dt><dd><time dateTime={repo.metricsUpdatedAt}>{timestamp(repo.metricsUpdatedAt)}</time></dd></div>}
      </dl>
      {repo.hotnessRank !== null && <Link className="detail-text-link minor" to="/trending?view=hotness">热点榜第 {repo.hotnessRank} 名</Link>}
    </section>
    <section className="panel detail-side-card" aria-labelledby="repo-source-title">
      <h2 id="repo-source-title">GitHub 仓库</h2>
      <div className="detail-origin"><External href={repo.htmlUrl}>{repo.fullName}</External></div>
      {repo.summaryZh && repo.description && <p className="minor muted">{repo.description}</p>}
      <dl className="detail-side-facts"><div><dt>许可证</dt><dd>{license}</dd></div></dl>
      {repo.trendingEntries.length > 0 && <ul className="detail-source-list" aria-label="GitHub Trending 排名">
        {repo.trendingEntries.map((entry) => <li key={entry.sourceKey + ':' + entry.period}>
          <Link to={'/trending?' + new URLSearchParams({ view: 'official', source: entry.sourceKey + ':' + entry.period })}>
            <span title={timestamp(entry.observedAt)}>{entry.sourceKey === 'all' ? '全站' : entry.sourceKey.replace('language:', '')} {periodLabel[entry.period]}</span>
            <strong className="numeric">#{entry.rank}</strong>
          </Link>
        </li>)}
      </ul>}
      {admin && <div className="detail-side-actions">
        {!repo.blocked && <><MonitorButton repo={repo} full /><AddTopic repoId={repo.id} /></>}
        <RepoBlockButton repo={repo} />
      </div>}
    </section>
    {(repo.categories.length > 0 || repo.valueTags.length > 0) && <section className="panel detail-side-card" aria-labelledby="repo-topics-title">
      <h2 id="repo-topics-title">相关主题</h2>
      <div className="topic-links">
        {repo.categories.map((slug) => <Link key={slug} className="context-chip" to={'/categories/' + encodeURIComponent(slug)}>{names.get(slug) ?? slug}</Link>)}
        {repo.valueTags.map((tag) => <Link key={'tag:' + tag} className="context-chip" to={'/repos?' + new URLSearchParams({ tag })}>{tag}</Link>)}
      </div>
    </section>}
    {repo.aiTopics.length > 0 && <section className="panel detail-side-card" aria-labelledby="repo-ai-topics-title">
      <h2 id="repo-ai-topics-title">AI Topics</h2>
      <div className="topic-links">
        {repo.aiTopics.map((topic) => <span key={topic} className="context-chip">{topic}</span>)}
      </div>
    </section>}
    <section className="panel detail-side-card" aria-labelledby="repo-record-title">
      <h2 id="repo-record-title">监控记录</h2>
      <dl className="detail-side-facts">
        <div><dt>发现时间</dt><dd><time dateTime={repo.discoveredAt}>{timestamp(repo.discoveredAt)}</time></dd></div>
        <div><dt>最近采样</dt><dd>{repo.lastSuccessAt ? <time dateTime={repo.lastSuccessAt}>{timestamp(repo.lastSuccessAt)}</time> : '等待采样'}</dd></div>
        {repo.followedAt && <div><dt>加入重点监控时间</dt><dd><time dateTime={repo.followedAt}>{timestamp(repo.followedAt)}</time></dd></div>}
      </dl>
      {admin && !repo.blocked && <RefreshAction repoId={repo.id} />}
    </section>
  </aside>
}

function AddTopic({ repoId }: { repoId: number }) {
  const queryClient = useQueryClient()
  const add = useMutation({
    mutationFn: () => api<Pick<Topic, 'id' | 'status'>>('/repos/' + repoId + '/topic', { method: 'POST' }),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ['topics'] }) },
  })
  return <div className="task-area">
    {add.data ? <Link to="/admin/topics" className="button">查看选题池</Link> :
      <ActionButton onClick={() => add.mutate()} pending={add.isPending} icon={<Plus size={17} />}>加入选题池</ActionButton>}
    {add.error && <Notice error>{errorText(add.error)}</Notice>}
  </div>
}

function TrendSection({ repo, range, trend }: { repo: RepoDetail; range: TrendRange; trend: UseQueryResult<Trend> }) {
  const location = useLocation()
  const navigate = useNavigate()
  const params = new URLSearchParams(location.search)
  const hasPoints = trend.data?.points.some(({ stars }) => stars !== null)
  return <section id="star-trend" className="panel detail-panel trend-section" aria-labelledby="repo-trend-title">
    <h2 id="repo-trend-title">Star 趋势</h2>
    <div className="trend-toolbar">
      <div className="trend-stats">
        <GrowthValue value={repo.growth24h} label="24h 净增" />
        <GrowthValue value={repo.growth7d} label="7d 净增" />
        <GrowthValue value={repo.growth30d} label="30d 净增" />
      </div>
      <div className="pills" role="group" aria-label="趋势范围">{ranges.map(([key, label]) => <button key={key} aria-pressed={range === key}
        onClick={() => {
          const next = new URLSearchParams(params)
          next.set('range', key)
          navigate({ search: next.toString(), hash: location.hash }, { preventScrollReset: true, state: location.state })
        }}>{label}</button>)}</div>
    </div>
    {trend.isPending ? <Skeleton rows={1} /> : trend.error && !trend.data
      ? <PageError error={trend.error} retry={() => { void trend.refetch() }} /> : hasPoints && trend.data
        ? <Suspense fallback={<Skeleton rows={1} />}><TrendChart data={trend.data} /></Suspense>
        : <Empty title="此时段暂无 Star 观测" />}
    {trend.error && trend.data && <Notice error>{errorText(trend.error)}</Notice>}
    {trend.data && <div className="chart-note muted minor">
      <span>{resolutionLabel[trend.data.resolution]}</span>
      {hasPoints && <time dateTime={trend.data.updatedAt}>观测于 {timestamp(trend.data.updatedAt)}</time>}
    </div>}
  </section>
}
