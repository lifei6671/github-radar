import { ArrowDownRight, ArrowUpRight, Crosshair, ExternalLink, Star, X } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import { errorText } from '../api/client'
import { useAdminArea, useFollow } from '../api/queries'
import type { Growth, Repo, SparkPoint } from '../api/types'
import { decimal, githubLink, number, qualityLabel, repoState, signed, summaryLabel, timestamp } from '../lib/format'
import { FavoriteButton } from './favorite'
import { Badge, Notice, Spinner } from './ui'

export function GrowthValue({ value, label }: { value: Growth; label?: string }) {
  const delta = value.delta
  const tone = delta == null ? 'muted' : delta < 0 ? 'tone-hot' : delta > 0 ? 'tone-ok' : ''
  const observed = value.startAt && value.endAt ? '，' + timestamp(value.startAt) + ' 至 ' + timestamp(value.endAt) : ''
  return <span className="growth" title={qualityLabel[value.quality] + observed}>
    {label && <span className="metric-label">{label}</span>}
    <span className={'growth-value ' + tone}>
      {delta == null ? '采样不足' : <>
        {delta > 0 && <ArrowUpRight size={14} aria-hidden />}
        {delta < 0 && <ArrowDownRight size={14} aria-hidden />}
        {value.quality === 'estimated' && <span className="estimate">约</span>}
        <span className="numeric">{signed(delta)}</span>
      </>}
    </span>
  </span>
}

// 小图按小时网格定位，遇到空桶重新开始路径。
export function Sparkline({ points, expanded = false }: { points: SparkPoint[]; expanded?: boolean }) {
  const width = expanded ? 320 : 104
  const height = expanded ? 100 : 32
  const samples = points.flatMap(({ stars }) => stars === null ? [] : [stars])
  if (samples.length === 0) return <div className="spark-empty" aria-label="暂无小时观测"><span /></div>
  const min = Math.min(...samples)
  const max = Math.max(...samples)
  const coords = points.map(({ stars }, i) => stars === null ? null : {
    x: 5 + i / Math.max(1, points.length - 1) * (width - 10),
    y: max === min ? height / 2 : 5 + (max - stars) / (max - min) * (height - 10),
  })
  const path = coords.map((p, i) => p === null ? '' :
    (coords[i - 1] ? 'L' : 'M') + p.x.toFixed(1) + ',' + p.y.toFixed(1)).join(' ')
  return <svg className="sparkline" viewBox={'0 0 ' + width + ' ' + height}
    role="img" aria-label="最近 24 个已完成小时的累计 Star 曲线">
    <path d={path} fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
    {coords.map((p, i) => p && !coords[i - 1] && !coords[i + 1]
      ? <circle key={i} cx={p.x} cy={p.y} r={1.75} fill="currentColor" /> : null)}
  </svg>
}

export function External({ href, children = 'GitHub', iconOnly = false }: { href: string | null | undefined; children?: string; iconOnly?: boolean }) {
  const link = githubLink(href)
  if (!link) return null
  return <a className={iconOnly ? 'icon-button external' : 'button external'} href={link} target="_blank" rel="noopener noreferrer"
    aria-label={iconOnly ? '在 GitHub 打开' : undefined} title={iconOnly ? '在 GitHub 打开' : undefined}>
    {!iconOnly && children}<ExternalLink size={iconOnly ? 15 : 16} aria-hidden />
  </a>
}

export function MonitorButton({ repo, full = false, removeIcon = false }: { repo: Repo; full?: boolean; removeIcon?: boolean }) {
  const follow = useFollow(repo.id)
  const label = repo.followed ? '移出重点监控' : '加入重点监控'
  const Icon = removeIcon && repo.followed ? X : Crosshair
  return <span className="monitor-action">
    <button type="button" className={(full ? 'button' : 'icon-button') + (repo.followed ? ' is-monitored' : '')}
      aria-label={label + ' ' + repo.fullName} aria-pressed={repo.followed} title={label} disabled={follow.isPending}
      onClick={() => follow.mutate(!repo.followed)}>
      {follow.isPending ? <Spinner /> : <Icon size={removeIcon ? 14 : 17} aria-hidden />}
      {full && label}
    </button>
    {follow.error && (full ? <Notice error>{errorText(follow.error)}</Notice> :
      <span role="alert" className="repo-action-error">{errorText(follow.error)}</span>)}
  </span>
}

export function RepoLink({ repo }: { repo: Repo }) {
  const location = useLocation()
  const admin = useAdminArea()
  return <Link className="repo-name" to={(admin ? '/admin/repos/' : '/repos/') + repo.id} state={{ from: location.pathname + location.search }}>
    {repo.fullName}
  </Link>
}

export function RepoCard({ repo, names, favorite = false }: { repo: Repo; names?: Map<string, string>; favorite?: boolean }) {
  const admin = useAdminArea()
  const trending = repo.trendingEntries.find((entry) => entry.sourceKey === 'all' && entry.period === 'daily') ?? repo.trendingEntries[0]
  const title = repo.titleZh || repo.description
  return <article className="repo-card">
    <div className="repo-row-head">
      <div className="repo-tags">
        {repo.language && <span className="language"><span />{repo.language}</span>}
        <Badge>{repo.grade} 级</Badge>
        {repo.state !== 'active' && <Badge tone="warning">{repoState[repo.state]}</Badge>}
        {trending && <Badge tone="hot"><span title={trending.sourceKey + ' / ' + trending.period}>GitHub Trending #{trending.rank}</span></Badge>}
        {repo.candidateBurst && <Badge tone="warning">候选爆发</Badge>}
      </div>
      {admin && <MonitorButton repo={repo} />}
      {favorite && <FavoriteButton item={{ id: repo.id, fullName: repo.fullName, language: repo.language,
        excerpt: repo.summaryPreview || title }} />}
    </div>
    <h3 className="repo-identity"><RepoLink repo={repo} /><External href={repo.htmlUrl} iconOnly /></h3>
    {title && !repo.summaryPreview?.startsWith(title) && <p className="repo-title">{title}</p>}
    {repo.summaryPreview && <p className="repo-summary">{repo.summaryPreview}</p>}
    <div className="repo-tags context-tags">
      {repo.categories.map((slug) => <Link key={slug} to={'/categories/' + encodeURIComponent(slug)} className="context-chip">{names?.get(slug) ?? slug}</Link>)}
      {repo.summaryStatus !== 'ready' && <span className="muted minor">{summaryLabel[repo.summaryStatus]}</span>}
    </div>
    <div className="repo-card-foot">
      <div className="metrics-inline">
        <span className="metric"><Star size={15} aria-hidden /><strong className="numeric">{number(repo.stars)}</strong></span>
        <GrowthValue value={repo.growth24h} label="24h" />
        <GrowthValue value={repo.growth7d} label="7d" />
        {repo.relativeGrowth != null && repo.growth7d.quality === 'exact' &&
          <span className="metric relative"><span className="metric-label">7d 相对</span><strong className="numeric">{(repo.relativeGrowth * 100).toFixed(1)}%</strong></span>}
      </div>
      <Sparkline points={repo.sparkline} />
    </div>
    <div className="repo-times minor muted">
      {repo.lastSuccessAt ? <time dateTime={repo.lastSuccessAt}>采样于 {timestamp(repo.lastSuccessAt)}</time> : <span>等待采样</span>}
    </div>
  </article>
}

type RankMetric = 'growth24h' | 'growth7d' | 'hotness' | 'official'
type RankItem = { repo: Repo; rank: number | null }
type PodiumItem = RankItem & { rank: number }
type RankProps = { item: RankItem; metric: RankMetric }
const rankLabel = { growth24h: '24h 净增', growth7d: '7d 净增', hotness: '热度指数', official: '累计 Star' }

function rankNumber(rank: number) {
  return String(rank).padStart(2, '0')
}

export function RankedRepos({ metric, items }: { metric: RankMetric; items: RankItem[] }) {
  const podium = items.filter((item): item is PodiumItem => item.rank !== null && item.rank <= 3)
  const rest = items.filter(({ rank }) => rank === null || rank > 3)
  const positions = rest.flatMap(({ rank }) => rank === null ? [] : [rank])
  const first = positions[0]
  const last = positions.at(-1)
  return <div className="ranked-repos">
    {podium.length > 0 && <section className="rank-podium" aria-label="榜单前三" data-full={podium.length === 3}>
      {podium.map((item) => <RankPodiumCard key={item.repo.id} item={item} metric={metric} />)}
    </section>}
    {rest.length > 0 && <section className="rank-rest" aria-labelledby="rank-rest-title">
      <header className="rank-rest-head">
        <h2 id="rank-rest-title">{first === undefined ? '待排名项目' : '后续排名'}
          {first !== undefined && last !== undefined && <span className="numeric muted">No. {rankNumber(first)}{last !== first && '–' + rankNumber(last)}</span>}
        </h2>
        <p className="minor muted">近 24 小时 Star 走势 / {rankLabel[metric]}</p>
      </header>
      <ol className="panel rank-list">
        {rest.map((item) => <RankListRow key={item.repo.id} item={item} metric={metric} />)}
      </ol>
    </section>}
  </div>
}

function RankPodiumCard({ item: { repo, rank }, metric }: { item: PodiumItem; metric: RankMetric }) {
  const lead = rank === 1
  const excerpt = lead ? repo.summaryPreview || repo.titleZh || repo.description : repo.titleZh || repo.summaryPreview || repo.description
  const contextWindow = metric === 'growth24h' ? 'growth7d' : 'growth24h'
  return <article className="panel podium-card" data-rank={rank}>
    <header className="rank-entry-head">
      <span className="rank-number numeric" data-rank={rank}>No. {rankNumber(rank)}</span>
      <div className="rank-change"><GrowthValue value={repo[contextWindow]} label={rankLabel[contextWindow]} /></div>
    </header>
    <div className="rank-podium-body">
      <div className="rank-entry-copy">
        <h2><RepoLink repo={repo} /></h2>
        {excerpt && <p>{excerpt}</p>}
      </div>
      {lead && <div className="rank-lead-chart">
        <span className="minor muted">近 24 小时累计 Star</span>
        <Sparkline points={repo.sparkline} expanded />
      </div>}
    </div>
    <footer className="rank-entry-footer">
      <RankRepoMeta repo={repo} rank={rank} metric={metric} />
      <div className="rank-entry-metric">
        {!lead && <Sparkline points={repo.sparkline} />}
        <RankValue repo={repo} metric={metric} />
      </div>
    </footer>
  </article>
}

function RankListRow({ item: { repo, rank }, metric }: RankProps) {
  const excerpt = repo.titleZh || repo.summaryPreview || repo.description
  return <li className="rank-list-row">
    <span className="rank-position rank-number numeric" aria-label={rank === null ? '暂无排名' : '第 ' + rank + ' 名'}>
      {rank === null ? '—' : rankNumber(rank)}
    </span>
    <div className="rank-entry-copy">
      <h3><RepoLink repo={repo} /></h3>
      {excerpt && <p>{excerpt}</p>}
    </div>
    <RankRepoMeta repo={repo} rank={rank} metric={metric} />
    <div className="rank-row-trend"><Sparkline points={repo.sparkline} /></div>
    <RankValue repo={repo} metric={metric} />
  </li>
}

function RankRepoMeta({ repo, metric, rank }: { repo: Repo; metric: RankMetric; rank: number | null }) {
  return <div className="rank-repo-meta">
    <div className="rank-repo-facts">
      {repo.language && <span className="language"><span />{repo.language}</span>}
      {metric === 'official' && rank !== null
        ? <Badge tone="hot">GitHub Trending #{rank}</Badge>
        : <span className="metric minor muted"><Star size={13} aria-hidden /><span className="numeric">{number(repo.stars)}</span></span>}
    </div>
    <div className="rank-actions"><External href={repo.htmlUrl} iconOnly /></div>
  </div>
}

function RankValue({ repo, metric }: { repo: Repo; metric: RankMetric }) {
  // 榜单分数和 Star 走势使用各自的数据，缺测沿用原有质量展示。
  return <div className="rank-value">
    {metric === 'hotness' || metric === 'official'
      ? <strong className="numeric">{metric === 'hotness' ? decimal(repo.hotnessScore) : number(repo.stars)}</strong>
      : <GrowthValue value={repo[metric]} />}
    <span className="metric-label">{rankLabel[metric]}</span>
  </div>
}
