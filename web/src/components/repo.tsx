import { ArrowDownRight, ArrowUpRight, ExternalLink, Star } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import { errorText } from '../api/client'
import { useFollow } from '../api/queries'
import type { Growth, Repo, SparkPoint, TrendingEntry } from '../api/types'
import { decimal, githubLink, number, qualityLabel, repoState, signed, summaryLabel, timestamp } from '../lib/format'
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
export function Sparkline({ points, large = false }: { points: SparkPoint[]; large?: boolean }) {
  const width = large ? 320 : 104
  const height = large ? 108 : 32
  const samples = points.flatMap(({ stars }) => stars === null ? [] : [stars])
  if (samples.length === 0) return <div className={'spark-empty ' + (large ? 'spark-large' : '')} aria-label="暂无小时观测">{large ? '暂无小时观测' : <span />}</div>
  const min = Math.min(...samples)
  const max = Math.max(...samples)
  const coords = points.map(({ stars }, i) => stars === null ? null : {
    x: 5 + i / Math.max(1, points.length - 1) * (width - 10),
    y: max === min ? height / 2 : 5 + (max - stars) / (max - min) * (height - 10),
  })
  const path = coords.map((p, i) => p === null ? '' :
    (coords[i - 1] ? 'L' : 'M') + p.x.toFixed(1) + ',' + p.y.toFixed(1)).join(' ')
  const latest = coords.filter((p) => p !== null).at(-1)
  return <svg className={'sparkline ' + (large ? 'spark-large' : '')} viewBox={'0 0 ' + width + ' ' + height}
    role="img" aria-label="最近 24 个已完成小时的累计 Star 曲线">
    {large && [8, 44, 80, 105].map((y) => <line className="spark-grid" key={y} x1="0" x2={width} y1={y} y2={y} />)}
    <path d={path} fill="none" stroke="currentColor" strokeWidth={large ? 2 : 1.5} strokeLinejoin="round" strokeLinecap="round" />
    {coords.map((p, i) => p && !coords[i - 1] && !coords[i + 1]
      ? <circle key={i} cx={p.x} cy={p.y} r={large ? 2.5 : 1.75} fill="currentColor" /> : null)}
    {large && latest && <circle cx={latest.x} cy={latest.y} r="3.5" fill="currentColor" />}
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

export function FollowButton({ repo, full = false }: { repo: Repo; full?: boolean }) {
  const follow = useFollow(repo.id)
  const label = repo.followed ? '取消关注' : '关注'
  return <span className="follow-action">
    <button className={(full ? 'button' : 'icon-button') + (repo.followed ? ' is-followed' : '')}
      aria-label={label + ' ' + repo.fullName} aria-pressed={repo.followed} title={label} disabled={follow.isPending}
      onClick={() => follow.mutate(!repo.followed)}>
      {follow.isPending ? <Spinner /> : <Star size={17} fill={repo.followed ? 'currentColor' : 'none'} />}
      {full && label}
    </button>
    {follow.error && (full ? <Notice error>{errorText(follow.error)}</Notice> :
      <span role="alert" className="follow-error">{errorText(follow.error)}</span>)}
  </span>
}

export function RepoLink({ repo, children }: { repo: Repo; children?: React.ReactNode }) {
  const location = useLocation()
  return <Link className="repo-name" to={'/repos/' + repo.id} state={{ from: location.pathname + location.search }}>
    {children ?? repo.fullName}
  </Link>
}

export function RepoCard({ repo, names, rank, score = false, watching = false, officialEntry }: {
  repo: Repo; names: Map<string, string>; rank?: number; score?: boolean; watching?: boolean; officialEntry?: TrendingEntry | null
}) {
  const trending = officialEntry === undefined
    ? repo.trendingEntries.find((entry) => entry.sourceKey === 'all' && entry.period === 'daily') ?? repo.trendingEntries[0]
    : officialEntry
  return <article className={'repo-card' + (rank === 1 ? ' rank-feature' : '')}>
    <div className="repo-row-head">
      <div className="repo-identity">
        {rank !== undefined && <span className={'repo-rank numeric ' + (rank <= 3 ? 'rank-leading' : '')} aria-label={'第 ' + rank + ' 名'}>{String(rank).padStart(2, '0')}</span>}
        <RepoLink repo={repo} />
        <External href={repo.htmlUrl} iconOnly />
      </div>
      <div className="repo-tags">
        {repo.language && <span className="language"><span />{repo.language}</span>}
        <Badge tone={repo.grade === 'S' ? 'accent' : 'neutral'}>{repo.grade} 级</Badge>
        {repo.state !== 'active' && <Badge tone="warning">{repoState[repo.state]}</Badge>}
      </div>
    </div>
    {(repo.titleZh || repo.description) && <p className="repo-title">{repo.titleZh || repo.description}</p>}
    {repo.summaryPreview && <p className="repo-summary">{repo.summaryPreview}</p>}
    <div className="repo-tags context-tags">
      {repo.categories.map((slug) => <Link key={slug} to={'/categories/' + encodeURIComponent(slug)} className="context-chip">{names.get(slug) ?? slug}</Link>)}
      {trending && <Badge tone="hot"><span title={trending.sourceKey + ' / ' + trending.period}>GitHub Trending #{trending.rank}</span></Badge>}
      {repo.candidateBurst && <Badge tone="warning">候选爆发</Badge>}
      {repo.summaryStatus !== 'ready' && <span className="muted minor">{summaryLabel[repo.summaryStatus]}</span>}
    </div>
    <div className="repo-card-foot">
      <div className="metrics-inline">
        <span className="metric"><Star size={15} aria-hidden /><strong className="numeric">{number(repo.stars)}</strong></span>
        <GrowthValue value={repo.growth24h} label="24h" />
        <GrowthValue value={repo.growth7d} label="7d" />
        {score && repo.hotnessScore !== null && <span className="metric"><span className="metric-label">热度</span><strong className="numeric">{decimal(repo.hotnessScore)}</strong></span>}
        {!score && repo.relativeGrowth != null && repo.growth7d.quality === 'exact' &&
          <span className="metric relative"><span className="metric-label">7d 相对</span><strong className="numeric">{(repo.relativeGrowth * 100).toFixed(1)}%</strong></span>}
      </div>
      <div className="repo-mini">
        <Sparkline points={repo.sparkline} />
        <FollowButton repo={repo} />
      </div>
    </div>
    <div className="repo-times minor muted">
      {watching && repo.followedAt && <span>关注于 {timestamp(repo.followedAt)}</span>}
      {repo.lastSuccessAt ? <time dateTime={repo.lastSuccessAt}>采样于 {timestamp(repo.lastSuccessAt)}</time> : <span>等待采样</span>}
    </div>
  </article>
}
