import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { UseQueryResult } from '@tanstack/react-query'
import { ArrowLeft, CalendarDays, Clock3, GitFork, Plus, Star } from 'lucide-react'
import { lazy, Suspense, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import type { CSSProperties, MouseEvent } from 'react'
import { Link, useLocation, useNavigate, useNavigationType, useParams } from 'react-router'
import { api, ApiError, errorText, publicApi } from '../api/client'
import { detailRepoId, repoDetailOptions, similarReposKey, useAdminArea, useCategories, withdrawPublicRepo } from '../api/queries'
import type { Items, RepoDetail, Topic, Trend, TrendRange } from '../api/types'
import { External, GrowthValue, MonitorButton, RepoBlockButton } from '../components/repo'
import { AnimatedPills } from '../components/navigation-feedback'
import { RepoReaderTools } from '../components/repo-reader-tools'
import { RefreshAction, SummaryActions } from '../components/tasks'
import { RepoReading } from '../components/readme'
import { StarHistoryPanel } from '../components/star-history'
import { ActionButton, Badge, Empty, Notice, PageError, Skeleton } from '../components/ui'
import { decimal, number, qualityLabel, relativeTime, repoCategoryPath, repoPath, repoState, resolutionLabel, sourceLabel, summaryLabel, timestamp } from '../lib/format'

const TrendChart = lazy(() => import('../components/trend-chart'))
const ranges: [TrendRange, string][] = [['24h', '24 小时'], ['7d', '7 天'], ['30d', '30 天'], ['180d', '180 天']]
const sections = [['#summary', '仓库概览'], ['#readme', 'README'], ['#star-trend', 'Star 趋势']] as const
const periodLabel = { daily: '日榜', weekly: '周榜', monthly: '月榜' }
const repoNotFound = new ApiError(404, { code: 'not_found' })
const readmeHeadingSelector = 'h1, h2, h3, h4, h5, h6'
const readmeHeadingPrefix = '#readme-heading-'
const outlineViewport = '(min-width: 1280px)'
const longOutlineSize = 12

function subscribeOutlineViewport(update: () => void) {
  const media = window.matchMedia(outlineViewport)
  media.addEventListener('change', update)
  return () => media.removeEventListener('change', update)
}

function showDesktopOutline() {
  return window.matchMedia(outlineViewport).matches
}

function readmeOutline(html: string | undefined) {
  if (!html) return []
  const template = document.createElement('template')
  template.innerHTML = html
  const headings = Array.from(template.content.querySelectorAll(readmeHeadingSelector))
  const parents: { level: number; anchor: string }[] = []
  const anchors = new Set<string>()
  return headings.flatMap((heading, index) => {
    const label = heading.textContent.trim()
    if (!label) return []
    const level = Number(heading.tagName.slice(1))
    while (parents.length && parents[parents.length - 1].level >= level) parents.pop()
    const depth = parents.length
    const parent = parents.at(-1)?.anchor
    const base = readmeHeadingPrefix + encodeURIComponent(heading.id || label)
    let anchor = base
    for (let suffix = 2; anchors.has(anchor); suffix++) anchor = base + '-' + suffix
    anchors.add(anchor)
    parents.push({ level, anchor })
    return [{ anchor, label, depth, index, parent }]
  })
}

// 长目录常驻主节，沿当前锚点展开祖先路径，让同级章节仍可直接跳转。
function visibleReadmeOutline(outline: ReturnType<typeof readmeOutline>, activeAnchor: string) {
  if (outline.length <= longOutlineSize) return outline
  const parents = new Map(outline.map(({ anchor, parent }) => [anchor, parent]))
  const roots = outline.filter(({ parent }) => parent === undefined)
  const expanded = new Set<string>()
  // README 只有一个总标题时，也显示它下面的各章入口。
  if (roots.length === 1) expanded.add(roots[0].anchor)
  for (let anchor: string | undefined = activeAnchor; anchor; anchor = parents.get(anchor)) expanded.add(anchor)
  return outline.filter(({ parent }) => parent === undefined || expanded.has(parent))
}

export function Detail() {
  const { owner, name } = useParams()
  const admin = useAdminArea()
  const location = useLocation()
  const navigate = useNavigate()
  const navigationType = useNavigationType()
  const from: unknown = location.state?.from
  const origin = typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') ? from : undefined
  const back = origin ?? (admin ? '/admin/repos' : '/repos')
  const read = admin ? api : publicApi
  const scope = admin ? 'admin' : 'public'
  const queryClient = useQueryClient()
  const params = new URLSearchParams(location.search)
  // 收藏与管理入口携带仓库身份，名称复用后仍指向所选记录。
  const identityParam = params.get('repoId')
  const lookupId = detailRepoId(identityParam ?? location.state?.repoId)
  const invalidIdentity = identityParam !== null && lookupId === undefined
  const fullName = owner && name ? owner + '/' + name : ''
  const detail = useQuery({
    ...repoDetailOptions(queryClient, fullName, lookupId, admin),
    enabled: !invalidIdentity,
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
    meta: { pageLoad: true },
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
  const article = useRef<HTMLElement>(null)
  const outlineNav = useRef<HTMLElement>(null)
  const previousArticle = useRef<HTMLElement | null>(null)
  const anchoredPosition = useRef<{ hash: string; y: number } | null>(null)
  const [activeAnchor, setActiveAnchor] = useState('#summary')
  const blocked = detail.data?.blocked === true
  const readmeHTML = !admin && !blocked ? detail.data?.readme?.chineseHtml : undefined
  const outline = useMemo(() => readmeOutline(readmeHTML), [readmeHTML])
  const desktopOutline = useSyncExternalStore(subscribeOutlineViewport, showDesktopOutline)
  const withinReadme = activeAnchor.startsWith(readmeHeadingPrefix)
  useEffect(() => {
    if (!canonicalize || !resolvedPath || detail.error || resolvingBookmark) return
    const next = new URLSearchParams(location.search)
    next.delete('repoId')
    void navigate({ pathname: resolvedPath, search: next.toString(), hash }, {
      replace: true, state: { ...location.state, repoId: lookupId },
    })
  }, [canonicalize, resolvedPath, detail.error, resolvingBookmark, location.search, location.state, hash, lookupId, navigate])
  useEffect(() => {
    // 深链接打开时先加载仓库数据，章节挂载后再定位。
    anchoredPosition.current = null
    const content = article.current
    const restored = navigationType === 'POP' && previousArticle.current === content
    previousArticle.current = content
    if (restored || !content || canonicalize || resolvingBookmark) return
    const heading = outline.find(({ anchor }) => anchor === hash)
    const target = heading
      ? content.querySelector('.readme-body')?.querySelectorAll(readmeHeadingSelector)[heading.index]
      : sections.some(([anchor]) => anchor === hash) ? content.querySelector('section' + hash) : undefined
    if (!target) return
    target.scrollIntoView()
    anchoredPosition.current = { hash, y: window.scrollY }
  }, [loadedRepoId, hash, location.key, navigationType, canonicalize, resolvingBookmark, outline])
  useEffect(() => {
    if (loadedRepoId === undefined || canonicalize || resolvingBookmark || wrongHistoricalName || !article.current) return
    const content = article.current
    const readmeHeadings = content.querySelector('.readme-body')?.querySelectorAll<HTMLElement>(readmeHeadingSelector)
    const headings = sections.flatMap(([anchor]) => {
      const element = content.querySelector<HTMLElement>('section' + anchor)
      if (!element) return []
      const group = [{ anchor: String(anchor), element }]
      if (anchor === '#readme' && readmeHeadings) {
        for (const heading of outline) {
          const target = readmeHeadings[heading.index]
          if (target) group.push({ anchor: heading.anchor, element: target })
        }
      }
      return group
    })
    const firstSection = headings[0]?.element
    if (!firstSection) return
    let frame = 0
    function update() {
      frame = 0
      // 短章节可能只能滚到页底。显式跳转后保留所选章节，继续滚动时重新判断。
      const selected = anchoredPosition.current
      if (selected && Math.abs(window.scrollY - selected.y) <= 1) {
        setActiveAnchor(selected.hash)
        return
      }
      anchoredPosition.current = null
      const root = document.documentElement
      const atBottom = window.scrollY > 0 && window.scrollY + root.clientHeight >= root.scrollHeight - 1
      const offset = parseFloat(getComputedStyle(firstSection).scrollMarginTop) || 0
      let current: string = '#summary'
      for (const { anchor, element } of headings) {
        const top = element.getBoundingClientRect().top
        if (top <= offset + 1 || (atBottom && top < root.clientHeight)) current = anchor
      }
      setActiveAnchor(current)
    }
    function scheduleUpdate() {
      if (!frame) frame = requestAnimationFrame(update)
    }
    update()
    window.addEventListener('scroll', scheduleUpdate, { passive: true })
    window.addEventListener('resize', scheduleUpdate)
    const observer = new ResizeObserver(scheduleUpdate)
    observer.observe(content)
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      window.removeEventListener('scroll', scheduleUpdate)
      window.removeEventListener('resize', scheduleUpdate)
    }
  }, [loadedRepoId, location.key, canonicalize, resolvingBookmark, wrongHistoricalName, blocked, outline])
  useEffect(() => {
    const current = outlineNav.current?.querySelector<HTMLElement>('[aria-current="location"]')
    const rail = outlineNav.current?.parentElement
    if (!current?.offsetHeight || !rail || rail.scrollHeight <= rail.clientHeight) return
    const item = current.getBoundingClientRect()
    const bounds = rail.getBoundingClientRect()
    if (item.top < bounds.top) rail.scrollTop += item.top - bounds.top
    else if (item.bottom > bounds.bottom) rail.scrollTop += item.bottom - bounds.bottom
  }, [activeAnchor, desktopOutline, outline])
  if (invalidIdentity || wrongHistoricalName) return <Empty title="仓库地址无效" />
  if (detail.isPending || resolvingBookmark) return <Skeleton />
  if (withdrawn) return <PageError error={repoNotFound} retry={() => { void detail.refetch() }} />
  if (!detail.data) return <PageError error={detail.error} retry={() => { void detail.refetch() }} />
  function returnToList(event: MouseEvent<HTMLAnchorElement>) {
    const fromIndex: unknown = location.state?.fromIndex
    const currentIndex: unknown = window.history.state?.idx
    if (admin || !origin || typeof fromIndex !== 'number' || !Number.isSafeInteger(fromIndex) || fromIndex < 0 ||
      typeof currentIndex !== 'number' || currentIndex <= fromIndex || event.button !== 0 ||
      event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    void navigate(fromIndex - currentIndex)
  }
  const repo = detail.data
  const names = new Map(categories.data?.items.map((item) => [item.slug, item.name]))
  const showSummaryStatus = admin && repo.summaryStatus !== 'missing'
  const visibleSections = sections.filter(([anchor]) => !repo.blocked || anchor !== '#readme')
  const visibleOutline = desktopOutline ? visibleReadmeOutline(outline, activeAnchor) : []
  function chapterLink(anchor: string, label: string) {
    const parent = anchor === '#readme' && withinReadme
    return <Link key={anchor} to={{ search: location.search, hash: anchor }} state={location.state}
      preventScrollReset aria-current={activeAnchor === anchor || (parent && !desktopOutline) ? 'location' : undefined}
      data-active-section={parent && desktopOutline || undefined}>{label}</Link>
  }
  return <div className={admin ? 'repo-detail page-reveal' : 'repo-detail public-reader page-reveal'} key={repo.id}>
    <div className="detail-navigation">
      <nav className="detail-return" aria-label="仓库详情导航">
        <Link to={back} onClick={returnToList}><ArrowLeft size={16} aria-hidden />返回列表</Link>
      </nav>
      {!admin && <section className="reader-source" aria-labelledby="reader-source-title">
        <h2 id="reader-source-title">来源</h2>
        <strong>{repo.fullName}</strong>
        <dl>
          <div><dt>首次发现</dt><dd><time dateTime={repo.discoveredAt}>{timestamp(repo.discoveredAt)}</time></dd></div>
          {repo.lastSuccessAt && <div><dt>最近采样</dt><dd><time dateTime={repo.lastSuccessAt}>{timestamp(repo.lastSuccessAt)}</time></dd></div>}
        </dl>
      </section>}
      <nav ref={outlineNav} className="reading-links" aria-label="仓库详情章节">
        {!admin && <h2 className="reader-outline-title">项目目录</h2>}
        {admin ? <AnimatedPills>{visibleSections.map(([anchor, label]) => chapterLink(anchor, label))}</AnimatedPills> :
          <div className="pills">{visibleSections.map(([anchor, label]) => <div key={anchor} className="reader-chapter">
            {chapterLink(anchor, label)}
            {anchor === '#readme' && visibleOutline.length > 0 && <ul className="readme-outline" aria-label="README 目录">
              {visibleOutline.map(({ anchor, label, depth }) => <li key={anchor} style={{ '--heading-depth': depth } as CSSProperties}>
                {chapterLink(anchor, label)}
              </li>)}
            </ul>}
          </div>)}</div>}
      </nav>
    </div>
    <article ref={article} className="detail-article" aria-labelledby="repo-title">
      <header className="article-head">
        {admin && <div className="repo-tags">
          {repo.language && <span className="language"><span />{repo.language}</span>}<Badge>{repo.grade} 级</Badge>
          {repo.state !== 'active' && <Badge tone="warning">{repoState[repo.state]}</Badge>}
          {repo.blocked && <Badge tone="warning">已屏蔽</Badge>}
        </div>}
        <h1 id="repo-title">{repo.fullName}</h1>
        {!admin && repo.titleZh && <p className="reader-subtitle">{repo.titleZh}</p>}
        <div className="detail-stats minor muted">
          {admin && <>
            <span><Star size={14} aria-hidden /><strong className="numeric">{number(repo.stars)}</strong> Star</span>
            <span><GitFork size={14} aria-hidden /><strong className="numeric">{number(repo.forks)}</strong> Fork</span>
          </>}
          <span><CalendarDays size={14} aria-hidden /><time dateTime={repo.discoveredAt}>{timestamp(repo.discoveredAt)}</time> 发现</span>
          {repo.lastSuccessAt && <span><Clock3 size={14} aria-hidden /><time dateTime={repo.lastSuccessAt} title={timestamp(repo.lastSuccessAt)}>{relativeTime(repo.lastSuccessAt)}</time> 采样</span>}
        </div>
      </header>
      {detail.error && <Notice error>{errorText(detail.error)} 上次采样 {timestamp(repo.lastSuccessAt)}</Notice>}
      <section id="summary" className="detail-panel" aria-labelledby="repo-summary-title">
        <header className="section-head">
          <h2 id="repo-summary-title">{repo.summaryZh ? 'AI 导读' : '项目简介'}</h2>
          {showSummaryStatus ? <span className="minor muted">{summaryLabel[repo.summaryStatus]}</span> :
            !repo.summaryZh && repo.description && <span className="minor muted">GitHub About</span>}
        </header>
        <p className="summary-body">{repo.summaryZh || repo.description || '暂无项目简介'}</p>
        {admin && repo.recommendationReason && <p className="recommendation-reason"><strong>推荐理由</strong>{repo.recommendationReason}</p>}
        {repo.features.length > 0 && <div className="topic-links">
          {repo.features.map((tag) => <Badge key={tag}>{tag}</Badge>)}
        </div>}
        {repo.summaryZh && <div className="summary-meta">
          {repo.summarySourceQuality && <span>{sourceLabel[repo.summarySourceQuality]}</span>}
          {repo.summaryGeneratedAt && <time dateTime={repo.summaryGeneratedAt}>{timestamp(repo.summaryGeneratedAt)} 更新</time>}
          {repo.summaryModel && <span>{repo.summaryModel}</span>}
        </div>}
        {admin && !repo.blocked && <footer className="summary-footer"><SummaryActions key={repo.id} repo={repo} repoUpdatedAt={detail.dataUpdatedAt} /></footer>}
        {!repo.blocked && <RepoReading key={repo.id + ':' + scope} repo={repo} admin={admin} />}
      </section>
      <TrendSection repo={repo} range={range} trend={trend} admin={admin} />
    </article>
    <div className="detail-rail">
      {!admin && <div className="detail-actions"><RepoReaderTools key={repo.id} repo={repo} /></div>}
      <DetailAside key={repo.id} repo={repo} names={names} admin={admin} />
    </div>
  </div>
}

function DetailAside({ repo, names, admin }: { repo: RepoDetail; names: Map<string, string>; admin: boolean }) {
  const license = repo.license?.spdxId === 'NOASSERTION' ? '未识别许可' : repo.license?.name ?? repo.license?.spdxId ?? '未声明'
  return <aside className="detail-aside" aria-label="仓库信息栏">
    {!admin && repo.recommendationReason && <section className="detail-side-card" aria-labelledby="repo-reason-title">
      <h2 id="repo-reason-title">推荐理由</h2>
      <p className="recommendation-reason">{repo.recommendationReason}</p>
    </section>}
    {(admin || repo.hotnessScore !== null) && <section className="detail-side-card" aria-labelledby="repo-heat-title">
      <h2 id="repo-heat-title">热点信号</h2>
      <dl className="detail-side-facts">
        <div><dt>全局热度</dt><dd className="numeric">{repo.hotnessScore === null ? '待观察' : decimal(repo.hotnessScore)}</dd></div>
        {(admin || repo.dataQuality !== 'insufficient') && <div><dt>评分质量</dt><dd>{qualityLabel[repo.dataQuality]}</dd></div>}
        {repo.metricsUpdatedAt && <div><dt>最近计算</dt><dd><time dateTime={repo.metricsUpdatedAt}>{timestamp(repo.metricsUpdatedAt)}</time></dd></div>}
      </dl>
      {repo.hotnessRank !== null && <Link className="detail-text-link minor" to="/trending?view=hotness">热点榜第 {repo.hotnessRank} 名</Link>}
    </section>}
    <section className="detail-side-card" aria-labelledby="repo-source-title">
      <h2 id="repo-source-title">GitHub 仓库</h2>
      {admin && <div className="detail-origin"><External href={repo.htmlUrl}>{repo.fullName}</External></div>}
      {repo.summaryZh && repo.description && <p className="minor muted">{repo.description}</p>}
      <dl className="detail-side-facts">
        {!admin && <>
          {repo.language && <div><dt>语言</dt><dd>{repo.language}</dd></div>}
          <div><dt>级别</dt><dd>{repo.grade} 级</dd></div>
          <div><dt>Star</dt><dd className="numeric">{number(repo.stars)}</dd></div>
          <div><dt>Fork</dt><dd className="numeric">{number(repo.forks)}</dd></div>
          {repo.state !== 'active' && <div><dt>状态</dt><dd>{repoState[repo.state]}</dd></div>}
        </>}
        <div><dt>许可证</dt><dd>{license}</dd></div>
      </dl>
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
    {(repo.categories.length > 0 || repo.valueTags.length > 0) && <section className="detail-side-card" aria-labelledby="repo-topics-title">
      <h2 id="repo-topics-title">相关主题</h2>
      <div className="topic-links">
        {repo.categories.map((slug) => <Link key={slug} className="context-chip" to={repoCategoryPath(slug)}>{names.get(slug) ?? slug}</Link>)}
        {repo.valueTags.map((tag) => <Link key={'tag:' + tag} className="context-chip" to={'/repos?' + new URLSearchParams({ tag })}>{tag}</Link>)}
      </div>
    </section>}
    {repo.aiTopics.length > 0 && <section className="detail-side-card" aria-labelledby="repo-ai-topics-title">
      <h2 id="repo-ai-topics-title">AI Topics</h2>
      <div className="topic-links">
        {repo.aiTopics.map((topic) => <span key={topic} className="context-chip">{topic}</span>)}
      </div>
    </section>}
    {admin && Boolean(repo.discoveryEvents?.length) && <section className="detail-side-card" aria-labelledby="repo-discovery-title">
      <h2 id="repo-discovery-title">发现来源</h2>
      <ul className="repo-discovery-events">
        {repo.discoveryEvents?.map((event) => <li key={event.sourceType + ':' + event.sourceKey}>
          <strong>{event.sourceType === 'manual' ? '人工提交' : event.sourceType}</strong>
          <time className="minor muted" dateTime={event.firstObservedAt}>{timestamp(event.firstObservedAt)}</time>
          {event.sourceUrl && <a href={event.sourceUrl} target="_blank" rel="noopener noreferrer">{event.sourceUrl}</a>}
          {event.note && <p>{event.note}</p>}
        </li>)}
      </ul>
    </section>}
    {!repo.blocked && <SimilarProjects repoId={repo.id} />}
    {admin && <section className="detail-side-card" aria-labelledby="repo-record-title">
      <h2 id="repo-record-title">监控记录</h2>
      <dl className="detail-side-facts">
        <div><dt>发现时间</dt><dd><time dateTime={repo.discoveredAt}>{timestamp(repo.discoveredAt)}</time></dd></div>
        <div><dt>最近采样</dt><dd>{repo.lastSuccessAt ? <time dateTime={repo.lastSuccessAt}>{timestamp(repo.lastSuccessAt)}</time> : '等待采样'}</dd></div>
        {repo.followedAt && <div><dt>加入重点监控时间</dt><dd><time dateTime={repo.followedAt}>{timestamp(repo.followedAt)}</time></dd></div>}
      </dl>
      {Boolean(repo.observationHistory?.length) && <ul className="repo-discovery-events" aria-label="观察池变更">
        {repo.observationHistory?.map((entry) => <li key={entry.id}>
          <strong>{observationReason[entry.reason] ?? entry.reason}</strong>
          <time className="minor muted" dateTime={entry.at}>{timestamp(entry.at)}</time>
          <span className="minor">{observationLabel(entry.fromState, entry.fromGrade)} → {observationLabel(entry.toState, entry.toGrade)}</span>
          {entry.evidence.exact24h !== undefined && <span className="minor muted">24h 实测净增 {number(entry.evidence.exact24h)}</span>}
        </li>)}
      </ul>}
      {!repo.blocked && <RefreshAction repoId={repo.id} />}
    </section>}
  </aside>
}

const observationReason: Record<string, string> = {
  breakout: '本地快照确认爆发',
  manual_follow: '人工重点监控',
  exact_growth_50: '达到重点观察增长门槛',
  exact_growth_15: '达到普通观察增长门槛',
  slowed_48h: '连续 48 小时放缓',
  below_peers_7d: '连续 7 天低于同类基线',
  quiet_14d: '连续 14 天无增长及新传播信号',
  capacity: '观察池容量调整',
  github_unavailable: 'GitHub 仓库不可用',
  blocked: '管理员屏蔽',
  identity_changed: '仓库地址待确认',
  identity_restored: '公开仓库身份已恢复',
}

function observationLabel(state: RepoDetail['state'], grade: RepoDetail['grade']) {
  if (state !== 'active') return repoState[state]
  return { S: 'S 爆发', A: 'A 重点', B: 'B 普通', C: 'C 冷却' }[grade]
}

type SimilarProject = Pick<RepoDetail, 'id' | 'fullName' | 'description' | 'language' | 'stars'>

function SimilarProjects({ repoId }: { repoId: number }) {
  const location = useLocation()
  const query = useQuery({
    queryKey: [similarReposKey, repoId],
    queryFn: ({ signal }) => publicApi<Items<SimilarProject>>('/repos/' + repoId + '/similar', { signal }),
    staleTime: 0,
  })
  // 重新校验公开可见性时隐藏旧结果，空列表不占据详情栏。
  if (query.isFetching || query.isError || !query.data?.items.length) return null
  return <section className="detail-side-card" aria-labelledby="repo-similar-title">
    <h2 id="repo-similar-title">相似项目</h2>
    <ul className="detail-similar-list">
      {query.data.items.map((repo) => <li key={repo.id}>
        <Link className="detail-text-link" to={repoPath(repo.fullName) + '?repoId=' + repo.id}
          state={{ ...location.state, repoId: repo.id }}>{repo.fullName}</Link>
        {repo.description && <p className="minor muted">{repo.description}</p>}
        <span className="minor muted">{repo.language && <span>{repo.language} </span>}<Star size={12} aria-hidden /> {number(repo.stars)}</span>
      </li>)}
    </ul>
  </section>
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

function TrendSection({ repo, range, trend, admin }: { repo: RepoDetail; range: TrendRange; trend: UseQueryResult<Trend>; admin: boolean }) {
  const location = useLocation()
  const navigate = useNavigate()
  const params = new URLSearchParams(location.search)
  const hasPoints = trend.data?.points.some(({ stars }) => stars !== null)
  const growth = [[repo.growth24h, '24h 净增'], [repo.growth7d, '7d 净增'], [repo.growth30d, '30d 净增']] as const
  return <section id="star-trend" className="detail-panel trend-section" aria-labelledby="repo-trend-title">
    <h2 id="repo-trend-title">Star 趋势</h2>
    <h3 className="star-source-title">本地累计 Star 与窗口净增</h3>
    <div className="trend-toolbar">
      {(admin || growth.some(([value]) => value.delta !== null)) && <div className="trend-stats">
        {growth.map(([value, label]) => <GrowthValue key={label} value={value} label={label} showMissing={admin} />)}
      </div>}
      <AnimatedPills role="group" aria-label="趋势范围">{ranges.map(([key, label]) => <button key={key} aria-pressed={range === key}
        onClick={() => {
          const next = new URLSearchParams(params)
          next.set('range', key)
          navigate({ search: next.toString(), hash: '#star-trend' }, { preventScrollReset: true, state: location.state })
        }}>{label}</button>)}</AnimatedPills>
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
    {!repo.blocked && <StarHistoryPanel key={repo.id} id={repo.id} admin={admin} />}
  </section>
}
