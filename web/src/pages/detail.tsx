import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, FileText, GitFork, Plus, Star } from 'lucide-react'
import { lazy, Suspense } from 'react'
import { Link, useLocation, useParams, useSearchParams } from 'react-router'
import { api, errorText } from '../api/client'
import { useCategories } from '../api/queries'
import type { RepoDetail, Topic, Trend, TrendRange } from '../api/types'
import { External, FollowButton, GrowthValue } from '../components/repo'
import { RefreshAction, SummaryActions } from '../components/tasks'
import { Badge, Empty, Notice, PageError, Skeleton, Spinner } from '../components/ui'
import { decimal, number, qualityLabel, repoState, resolutionLabel, sourceLabel, summaryLabel, timestamp } from '../lib/format'

const TrendChart = lazy(() => import('../components/trend-chart'))
const ranges: [TrendRange, string][] = [['24h', '24 小时'], ['7d', '7 天'], ['30d', '30 天'], ['180d', '180 天']]

export function Detail() {
  const { id } = useParams()
  const location = useLocation()
  const from: unknown = location.state?.from
  const back = typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') ? from : '/repos'
  const detail = useQuery({
    queryKey: ['repo', id],
    queryFn: ({ signal }) => api<RepoDetail>('/repos/' + id, { signal }),
  })
  const categories = useCategories()
  if (detail.isPending) return <Skeleton />
  if (!detail.data) return <PageError error={detail.error} retry={() => { void detail.refetch() }} />
  const repo = detail.data
  const names = new Map(categories.data?.items.map((item) => [item.slug, item.name]))
  return <>
    <Link className="back-link" to={back}><ArrowLeft size={16} />返回列表</Link>
    <header className="detail-head">
      <div className="detail-identity">
        <div className="repo-tags">{repo.language && <span className="language"><span />{repo.language}</span>}<Badge>{repo.grade} 级</Badge>
          {repo.state !== 'active' && <Badge tone="warning">{repoState[repo.state]}</Badge>}</div>
        <h1>{repo.fullName}</h1>
        {repo.description && <p className="muted">{repo.description}</p>}
      </div>
      <div className="action-row"><FollowButton repo={repo} full /><External href={repo.htmlUrl} /></div>
    </header>
    {detail.error && <Notice error>{errorText(detail.error)} 上次采样 {timestamp(repo.lastSuccessAt)}</Notice>}
    <div className="detail-grid">
      <div className="detail-main">
        <section className="panel detail-panel">
          <div className="detail-stats">
            <div><span className="metric-label"><Star size={15} />累计 Star</span><strong className="numeric">{number(repo.stars)}</strong></div>
            <GrowthValue value={repo.growth24h} label="24h 净增" />
            <GrowthValue value={repo.growth7d} label="7d 净增" />
            <GrowthValue value={repo.growth30d} label="30d 净增" />
          </div>
          <TrendSection id={repo.id} />
        </section>
        <section className="panel detail-panel summary-panel">
          <header className="section-head"><h2>AI 中文摘要</h2><Badge tone={repo.summaryStatus === 'ready' ? 'ok' : 'warning'}>{summaryLabel[repo.summaryStatus]}</Badge></header>
          {repo.summaryZh ? <p className="summary-body">{repo.summaryZh}</p> :
            <p className="muted">{repo.description ?? '暂无项目简介'}</p>}
          {repo.summaryZh && <div className="summary-meta">
            {repo.summarySourceQuality && <Badge tone={repo.summarySourceQuality === 'sufficient' ? 'neutral' : 'warning'}>{sourceLabel[repo.summarySourceQuality]}</Badge>}
            {repo.summaryGeneratedAt && <time dateTime={repo.summaryGeneratedAt}>{timestamp(repo.summaryGeneratedAt)}</time>}
            {repo.summaryModel && <span>{repo.summaryModel}</span>}
            <External href={repo.sourceReadmeUrl}>README 来源</External>
          </div>}
          {repo.features.length > 0 && <div className="topic-links summary-tags">
            {repo.features.map((tag) => <Badge key={tag}>{tag}</Badge>)}
          </div>}
          <SummaryActions key={repo.id} repo={repo} repoUpdatedAt={detail.dataUpdatedAt} />
        </section>
      </div>
      <aside className="detail-aside">
        <section className="panel detail-panel">
          <header className="section-head"><h2>仓库信息</h2></header>
          <dl className="definition-list">
            <div><dt><GitFork size={15} />Fork</dt><dd className="numeric">{number(repo.forks)}</dd></div>
            <div><dt><FileText size={15} />许可证</dt><dd>{repo.license === null ? '未声明' : repo.license.spdxId === 'NOASSERTION' ? '未识别许可' : repo.license.name ?? repo.license.spdxId ?? '未声明'}</dd></div>
            <div><dt>最近采样</dt><dd>{timestamp(repo.lastSuccessAt)}</dd></div>
            <div><dt>发现时间</dt><dd>{timestamp(repo.discoveredAt)}</dd></div>
            {repo.followedAt && <div><dt>关注时间</dt><dd>{timestamp(repo.followedAt)}</dd></div>}
          </dl>
          <div className="topic-links">
            {repo.categories.map((slug) => <Link key={slug} className="context-chip" to={'/categories/' + encodeURIComponent(slug)}>{names.get(slug) ?? slug}</Link>)}
            {repo.valueTags.map((tag) => <Link key={tag} className="context-chip" to={'/repos?' + new URLSearchParams({ tag })}>{tag}</Link>)}
          </div>
          <RefreshAction key={repo.id} repoId={repo.id} />
        </section>
        <section className="panel detail-panel">
          <header className="section-head"><h2>热点信号</h2></header>
          <dl className="definition-list">
            <div><dt>全局热度</dt><dd className="numeric">{repo.hotnessScore === null ? '待观察' : decimal(repo.hotnessScore)}</dd></div>
            <div><dt>热度排名</dt><dd className="numeric">{repo.hotnessRank === null ? '—' : '#' + repo.hotnessRank}</dd></div>
            <div><dt>评分质量</dt><dd>{qualityLabel[repo.dataQuality]}</dd></div>
            <div><dt>最近计算</dt><dd>{timestamp(repo.metricsUpdatedAt)}</dd></div>
            {repo.trendingEntries.map((entry) => <div key={entry.sourceKey + entry.period}>
              <dt title={timestamp(entry.observedAt)}>GitHub {entry.sourceKey === 'all' ? '全站' : entry.sourceKey.replace('language:', '')} {entry.period === 'daily' ? '日榜' : entry.period === 'weekly' ? '周榜' : '月榜'}</dt>
              <dd className="numeric">#{entry.rank}</dd>
            </div>)}
          </dl>
          <AddTopic key={repo.id} repoId={repo.id} />
        </section>
      </aside>
    </div>
  </>
}

function AddTopic({ repoId }: { repoId: number }) {
  const queryClient = useQueryClient()
  const add = useMutation({
    mutationFn: () => api<Pick<Topic, 'id' | 'status'>>('/repos/' + repoId + '/topic', { method: 'POST' }),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ['topics'] }) },
  })
  return <div className="task-area">
    {add.data ? <Link to="/topics" className="button">查看选题池</Link> :
      <button className="button" onClick={() => add.mutate()} disabled={add.isPending}>
        {add.isPending ? <Spinner /> : <Plus size={17} />}加入选题池
      </button>}
    {add.error && <Notice error>{errorText(add.error)}</Notice>}
  </div>
}

function TrendSection({ id }: { id: number }) {
  const [params, setParams] = useSearchParams()
  const range = ranges.find(([key]) => key === params.get('range'))?.[0] ?? '7d'
  const trend = useQuery({
    queryKey: ['trend', id, range],
    queryFn: ({ signal }) => api<Trend>('/repos/' + id + '/trend?range=' + range, { signal }),
  })
  const hasPoints = trend.data?.points.some(({ stars }) => stars !== null)
  return <section className="trend-section">
    <header className="section-head trend-head">
      <h2>Star 趋势</h2>
      <div className="pills wrap" aria-label="趋势范围">{ranges.map(([key, label]) => <button key={key} aria-pressed={range === key}
        onClick={() => setParams({ range: key }, { preventScrollReset: true })}>{label}</button>)}</div>
    </header>
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
