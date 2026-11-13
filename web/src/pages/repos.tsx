import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, ArrowRight, Bot, Check, ChevronDown, ChevronRight, CodeXml, Folder, ListFilter, Plug, Video, Wrench, X } from 'lucide-react'
import { Fragment, useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { Link, matchPath, Navigate, useNavigate, useNavigation, useParams, useSearchParams } from 'react-router'
import type { LoaderFunctionArgs } from 'react-router'
import { ApiError, errorText, publicApi, sessionQueryClient } from '../api/client'
import { distinct, publicRepoListOptions, useAdminArea, useCategories, useRepos } from '../api/queries'
import type { Category, DiscoveryTabOption, Items, Page, ProjectThemeDirectory, ProjectThemeDetail as ThemeDetail, Repo, Trending } from '../api/types'
import { Badge, Empty, InlineSearch, More, Notice, PageError, Skeleton } from '../components/ui'
import { AnimatedPills } from '../components/navigation-feedback'
import { GrowthValue, RankedRepos, RepoBlockButton, RepoCard, RepoLink, Sparkline } from '../components/repo'
import { RepoCollectionEmpty } from '../components/repo-collection'
import { RepoSubmission } from '../components/repo-submission'
import { PageTitle } from '../components/page-title'
import { ThemeChronicle } from '../components/theme-chronicle'
import { ThemeIcon } from '../components/theme-icon'
import { dateGroup, dateHeading, dayOfWeek, decimal, discoveryCategoryPath, number, relativeTime, repoCategoryPath, repoCategoryRoute, repoState, timeOfDay, timestamp, topicPath, topicsPath } from '../lib/format'

function mapNames(items: Category[] | undefined) {
  return new Map(items?.map(({ slug, name }) => [slug, name]))
}

export function Discover() {
  const categories = useCategories()
  const tabs = useQuery({
    queryKey: ['discovery-tabs'],
    meta: { pageLoad: true },
    queryFn: ({ signal }) => publicApi<Items<DiscoveryTabOption>>('/discovery-tabs', { signal }),
    staleTime: 0,
    refetchOnWindowFocus: true,
  })
  const navigate = useNavigate()
  const { slug } = useParams()
  const category = tabs.data?.items.find((tab) => tab.slug === slug)?.slug
  if (slug && tabs.data && !category) return <Navigate replace to="/" />
  const criteria = new URLSearchParams({ limit: '30' })
  if (category) criteria.set('category', category)

  function search(q: string) {
    navigate({ pathname: repoCategoryPath(category), search: q ? new URLSearchParams({ q }).toString() : '' })
  }

  return <>
    <header className="page-head"><PageTitle title="发现" subtitle="精选近期值得关注的 GitHub 开源项目。" /></header>
    <div className="list-tools">
      <CategoryTabs items={tabs.data?.items} selected={category ? [category] : []}
        onSelect={(value) => navigate(discoveryCategoryPath(value), { preventScrollReset: true })} />
      <InlineSearch value="" onSearch={search} />
    </div>
    {categories.error && <Notice error>{errorText(categories.error)}</Notice>}
    {tabs.error && <Notice error>{errorText(tabs.error)} <button type="button" onClick={() => { void tabs.refetch() }}>重试</button></Notice>}
    {tabs.isPending && slug ? <Skeleton /> : <div className="discover-grid">
      {!category && <DiscoverHot />}
      <section className="timeline-section" aria-label="项目动态">
        <RepoFeed params={criteria} names={mapNames(categories.data?.items)} timeline favorites discovery
          empty={<RepoCollectionEmpty icon={<Folder size={20} aria-hidden />} message="暂无项目动态"
            action={category && <Link to="/repos">浏览全部项目<ArrowRight size={14} aria-hidden /></Link>} />} />
      </section>
    </div>}
  </>
}

function DiscoverHot() {
  const hot = useQuery({
    queryKey: ['repos', 'discover-hot'],
    meta: { pageLoad: true },
    queryFn: ({ signal }) => publicApi<Page<Repo>>('/discovery?sort=hotness&limit=5', { signal }),
  })
  const leaders = hot.data?.items ?? []
  if (leaders.length === 0) return hot.error ? <Notice error>{errorText(hot.error)} <button type="button" onClick={() => { void hot.refetch() }}>重试</button></Notice> : null
  return <section className="panel rankings-panel" aria-labelledby="discover-hot">
    <header className="section-head">
      <h2 id="discover-hot"><span className="hot-dot" aria-hidden="true" />当前热点</h2>
      {hot.data?.updatedAt && <time className="minor muted" dateTime={hot.data.updatedAt}>计算于 {timestamp(hot.data.updatedAt)}</time>}
    </header>
    <ol className="leader-list">
        {leaders.map((repo) => <li key={repo.id}>
          <span className="rank-number numeric" data-rank={repo.hotnessRank}>{repo.hotnessRank}</span>
          <RepoLink repo={repo} />
          <GrowthValue value={repo.growth24h} label="24h" />
          <span className="leader-score"><strong className="numeric">{decimal(repo.hotnessScore)}</strong><span className="minor muted">热度</span></span>
          <Sparkline points={repo.sparkline} />
        </li>)}
    </ol>
    {hot.error && <Notice error>{errorText(hot.error)}</Notice>}
  </section>
}

function RepoFeed({ params, names, timeline = false, ranking, favorites = false, discovery = false, routed = false, empty }: {
  params: URLSearchParams; names?: Map<string, string>; timeline?: boolean
  ranking?: 'growth24h' | 'growth7d' | 'hotness'; favorites?: boolean; discovery?: boolean; routed?: boolean; empty: ReactNode
}) {
  const repos = useRepos(params, discovery, routed)
  if (repos.isPending) return <Skeleton />
  if (repos.error && !repos.data) return <PageError error={repos.error} retry={() => { void repos.refetch() }} />
  // 跨页期间指标可能更新，合并已显示的页面时仍按仓库身份处理。
  const loaded = distinct(repos.data?.pages.flatMap((page) => page.items) ?? [])
  const items = ranking ? loaded.filter((repo) => ranking === 'hotness' ? repo.hotnessRank !== null : repo[ranking].quality === 'exact') : loaded
  if (items.length === 0) return typeof empty === 'string' ? <Empty title={empty} /> : empty
  const updatedAt = repos.data?.pages[0]?.updatedAt
  let previous = ''
  return <>
    {ranking && <div className="feed-update muted minor">{updatedAt
      ? <time dateTime={updatedAt}>计算于 {timestamp(updatedAt)}</time> : '等待首次指标计算'}</div>}
    {repos.error && <RepoListError error={repos.error} reload={() => { void repos.refetch() }} />}
    {ranking ? <RankedRepos key={params.toString()} metric={ranking} items={items.map((repo, index) => ({
      repo,
      rank: ranking === 'hotness' ? repo.hotnessRank : index + 1,
    }))} /> : <div key={params.toString()} className={'repo-list page-reveal ' + (timeline ? 'timeline' : '')}>
      {items.map((repo) => {
        const at = repo.discoveredAt
        const label = timeline ? dateGroup(at, discovery) : ''
        const showDay = label && previous !== label
        previous = label
        return <Fragment key={repo.id}>
          {showDay && <h2 className="timeline-day" title={label}>{dateHeading(at, discovery)}<span className="minor muted">{dayOfWeek(at, discovery)}</span></h2>}
          <div className="timeline-item">
            {timeline && <div className="timeline-clock"><time dateTime={at}>{timeOfDay(at, discovery)}</time></div>}
            <RepoCard repo={repo} names={names} favorite={favorites} showTags={!discovery} />
          </div>
        </Fragment>
      })}
    </div>}
    <More hasMore={repos.hasNextPage && (!ranking || loaded.length === items.length)} pending={repos.isFetchingNextPage} onClick={() => { void repos.fetchNextPage() }} />
  </>
}

function RepoListError({ error, reload }: { error: Error; reload: () => void }) {
  return <Notice error>{errorText(error)}{error instanceof ApiError && error.code === 'invalid_cursor' &&
    <button type="button" className="text-button" onClick={reload}>重新加载</button>}</Notice>
}

function AdminRepoTable({ params, names }: { params: URLSearchParams; names: Map<string, string> }) {
  const repos = useRepos(params)
  if (repos.isPending) return <Skeleton />
  if (!repos.data) return <PageError error={repos.error} retry={() => { void repos.refetch() }} />
  // 沿用服务端筛选和游标，仓库链接会记录当前列表地址。
  const items = distinct(repos.data.pages.flatMap((page) => page.items))
  return <>
    {repos.error && <RepoListError error={repos.error} reload={() => { void repos.refetch() }} />}
    {items.length ? <div className="admin-table-scroll" role="region" aria-label="仓库表格" tabIndex={0}>
      <table className="admin-table admin-directory-table admin-repo-table" aria-label="仓库列表">
      <thead><tr>
        {['仓库', '分类', '语言', '监控状态', 'Star', '24h 净增', '7d 净增', '热度', '最近采样', '操作'].map((label) =>
          <th key={label} scope="col">{label}</th>)}
      </tr></thead>
      <tbody>{items.map((repo) => {
        const description = repo.titleZh || repo.summaryPreview || repo.description
        return <tr key={repo.id}>
          <td className="admin-repo-identity">
            <RepoLink repo={repo} />
            {description && <span className="admin-repo-description minor muted" title={description}>{description}</span>}
          </td>
          <td>{repo.categories.map((slug) => names.get(slug) ?? slug).join('、') || '—'}</td>
          <td className="muted">{repo.language ?? '—'}</td>
          <td><div className="admin-repo-status">
            <Badge tone={repo.blocked ? 'warning' : repo.state === 'active' ? 'ok' : repo.state === 'unavailable' ? 'hot' : 'neutral'}>
              {repo.blocked ? '已屏蔽' : repoState[repo.state]}
            </Badge>
            <span className="minor muted">{repo.grade} 级</span>
          </div></td>
          <td className="numeric">{number(repo.stars)}</td>
          <td><GrowthValue value={repo.growth24h} showMissing /></td>
          <td><GrowthValue value={repo.growth7d} showMissing /></td>
          <td className="numeric">{decimal(repo.hotnessScore)}</td>
          <td>{repo.lastSuccessAt ? <time dateTime={repo.lastSuccessAt}>{timestamp(repo.lastSuccessAt)}</time> : '—'}</td>
          <td><RepoBlockButton repo={repo} /></td>
        </tr>
      })}</tbody>
    </table></div> : <p className="admin-empty">暂无匹配的仓库</p>}
    <More hasMore={repos.hasNextPage} pending={repos.isFetchingNextPage} onClick={() => { void repos.fetchNextPage() }} />
  </>
}

function repoListCriteria(search: URLSearchParams, admin: boolean, slug?: string) {
  const params = new URLSearchParams(search)
  params.delete('cursor')
  if (!admin) params.delete('category')
  const q = params.get('q') ?? ''
  const hasQuery = q.trim() !== ''
  const defaultSort = hasQuery ? 'relevance' : 'discovered'
  const sort = params.get('sort') ?? defaultSort
  const criteria = new URLSearchParams(params)
  criteria.set('limit', '30')
  criteria.set('sort', sort)
  if (slug) criteria.set('category', slug)
  return { params, criteria, q, hasQuery, sort }
}

export async function loadPublicRepos({ request, params }: LoaderFunctionArgs) {
  const { criteria } = repoListCriteria(new URL(request.url).searchParams, false, params.slug)
  // 首屏和后续分页共用缓存。切换期间继续展示当前结果，请求失败由列表提供重试。
  await sessionQueryClient().infiniteQuery({ ...publicRepoListOptions(criteria), staleTime: 'static' }).catch(() => undefined)
  return null
}

export function Repos() {
  const admin = useAdminArea()
  const [routeParams, setParams] = useSearchParams()
  const categories = useCategories()
  const { slug } = useParams()
  const navigation = useNavigation()
  const selected = categories.data?.items.find((entry) => entry.slug === slug)
  const names = mapNames(categories.data?.items)
  const { params, criteria, q, hasQuery, sort } = repoListCriteria(routeParams, admin, slug)
  const tag = !admin && params.get('tag')?.trim()
  const pendingPath = navigation.state === 'loading' ? navigation.location.pathname : ''
  const pendingCategory = matchPath(repoCategoryPath(), pendingPath) ? '' : matchPath(repoCategoryRoute, pendingPath)?.params.slug
  const activeFilters = filterKeys.filter((name) => params.has(name)).length
  const navigationOptions = { preventScrollReset: true, flushSync: admin }

  function change(name: string, value: string) {
    const next = new URLSearchParams(params)
    if (value) next.set(name, value)
    else next.delete(name)
    if (name === 'q' && !value && next.get('sort') === 'relevance') next.delete('sort')
    setParams(next, navigationOptions)
  }

  function chooseAdminCategory(value: string) {
    const next = new URLSearchParams(params)
    next.delete('category')
    if (value) next.set('category', value)
    setParams(next, navigationOptions)
  }

  const listActions = <div className="list-actions">
    <label className="select-label"><span className="sr-only">排序</span>
      <select value={sort} onChange={(e) => change('sort', e.target.value)}>
        {hasQuery && <option value="relevance">相关度</option>}
        <option value="discovered">最近发现</option><option value="hotness">综合热度</option>
        <option value="growth24h">24h 净增</option><option value="growth7d">7d 净增</option><option value="stars">累计 Star</option>
      </select>
    </label>
    <RepoFilterPopover params={params} activeCount={activeFilters} onApply={(next) => setParams(next, navigationOptions)} />
  </div>
  const search = <InlineSearch key={q} value={q} onSearch={(value) => change('q', value)} />
  const categoryError = categories.error && <Notice error>{errorText(categories.error)}</Notice>
  const searchParams = params.toString()

  return <>
    <header className={'page-head' + (admin ? ' admin-repo-heading' : tag ? ' repo-tag-heading' : '')}>
      <PageTitle title={admin ? '仓库管理' : tag ? '#' + tag : '全部项目'}
        subtitle={admin ? '搜索和筛选已收录仓库，管理重点监控与屏蔽。' : tag ? undefined : '浏览已收录的 GitHub 仓库，按分类查看或收藏。'} />
      {admin && <RepoSubmission />}
    </header>
    {admin ? <section className="panel admin-panel" aria-label="仓库管理列表">
      <div className="admin-table-toolbar admin-repo-toolbar">
        {search}
        <div className="admin-filters">
          <CategoryTabs items={categories.data?.items} selected={params.getAll('category')} onSelect={chooseAdminCategory} />
          {listActions}
        </div>
      </div>
      {categoryError}
      <AdminRepoTable params={criteria} names={names} />
    </section> : <>
      <div className={'list-tools' + (tag ? ' repo-tag-tools' : '')}>
        <AnimatedPills className="repo-category-tabs" role="group" aria-label="项目分类">
          <Link className="pill-link" aria-current={slug ? undefined : 'page'} preventScrollReset
            data-pending={pendingCategory === '' && slug ? '' : undefined}
            to={{ pathname: repoCategoryPath(), search: searchParams }}>全部</Link>
          {categories.data?.items.map((entry) => <Link className="pill-link" key={entry.slug}
            aria-current={entry.slug === slug ? 'page' : undefined} preventScrollReset
            data-pending={entry.slug === pendingCategory && entry.slug !== slug ? '' : undefined}
            to={{ pathname: repoCategoryPath(entry.slug), search: searchParams }}>{entry.name}</Link>)}
        </AnimatedPills>
        {search}
      </div>
      <div className="list-actions">
        <label className="select-label"><span className="sr-only">爆发信号</span>
          <select value={params.get('breakout') ?? ''} onChange={(event) => change('breakout', event.target.value)}>
            <BreakoutOptions />
          </select>
        </label>
      </div>
      {tag && <button type="button" className="badge badge-accent repo-tag-filter" aria-label={'清除标签筛选 ' + tag}
        onClick={() => change('tag', '')}><span>#{tag}</span><X size={14} aria-hidden /></button>}
      {categoryError}
      <div className="repo-results" aria-busy={pendingCategory !== undefined || undefined}>
        {slug && categories.data && !selected ? <Empty title="此分类不存在"><Link className="button" to="/categories">返回分类</Link></Empty> :
          <RepoFeed key={criteria.toString()} params={criteria} names={names} timeline={sort === 'discovered' || sort === 'followed'} favorites routed
            empty="暂无匹配的项目" />}
      </div>
    </>}
  </>
}

function CategoryTabs({ items, selected, onSelect }: {
  items: DiscoveryTabOption[] | undefined; selected: string[]; onSelect: (slug: string) => void
}) {
  return <AnimatedPills className="repo-category-tabs" role="group" aria-label="项目分类">
    <button type="button" aria-pressed={selected.length === 0} onClick={() => onSelect('')}>全部</button>
    {items?.map(({ slug, name }) => <button key={slug} type="button" aria-pressed={selected.includes(slug)}
      onClick={() => onSelect(slug)}>{name}</button>)}
  </AnimatedPills>
}

const filterKeys = ['grade', 'language', 'state', 'tag', 'blocked', 'breakout']

function BreakoutOptions() {
  return <><option value="">全部爆发状态</option><option value="local">本地 24h 净增确认</option>
    <option value="github">GitHub 历史日 Star 待确认</option></>
}

function RepoFilterPopover({ params, activeCount, onApply }: {
  params: URLSearchParams; activeCount: number; onApply: (value: URLSearchParams) => void
}) {
  const [open, setOpen] = useState(false)
  const [left, setLeft] = useState(0)
  const id = useId()
  const root = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  useLayoutEffect(() => {
    if (!open) return
    // 以按钮右边缘为基准，窄屏时把浮窗限制在视口内。
    function place() {
      const anchor = root.current?.getBoundingClientRect()
      const width = root.current?.querySelector('form')?.offsetWidth
      if (!anchor || !width) return
      const x = Math.max(12, Math.min(anchor.right - width, document.documentElement.clientWidth - width - 12))
      setLeft(x - anchor.left)
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [open])
  useEffect(() => {
    if (!open) return
    // 浮窗无需锁定页面，点击外部或按 Escape 即关闭。
    function outside(event: PointerEvent) {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpen(false)
    }
    function escape(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false)
        button.current?.focus()
      }
    }
    document.addEventListener('pointerdown', outside)
    document.addEventListener('keydown', escape)
    return () => {
      document.removeEventListener('pointerdown', outside)
      document.removeEventListener('keydown', escape)
    }
  }, [open])
  return <div className="repo-filter-anchor" ref={root}>
    <button className="button" type="button" ref={button} aria-expanded={open} aria-controls={open ? id : undefined}
      onClick={() => setOpen((value) => !value)}><ListFilter size={17} />筛选{activeCount > 0 && <span className="numeric">{activeCount}</span>}</button>
    {open && <RepoFiltersForm id={id} left={left} params={params} onApply={(value) => {
      onApply(value)
      setOpen(false)
      button.current?.focus()
    }} />}
  </div>
}

function RepoFiltersForm({ id, left, params, onApply }: {
  id: string; left: number; params: URLSearchParams; onApply: (value: URLSearchParams) => void
}) {
  const [draft, setDraft] = useState(() => new URLSearchParams(params))
  function change(name: string, value: string) {
    setDraft((before) => {
      const next = new URLSearchParams(before)
      if (value) next.set(name, value)
      else next.delete(name)
      return next
    })
  }
  function submit(event: FormEvent) {
    event.preventDefault()
    // 浮窗打开期间仍可操作排序和搜索，提交只覆盖浮窗中的筛选项。
    const next = new URLSearchParams(params)
    for (const key of filterKeys) {
      const value = draft.get(key)?.trim()
      if (value) next.set(key, value)
      else next.delete(key)
    }
    onApply(next)
  }
  return <form id={id} className="panel repo-filter-popover" style={{ insetInlineStart: left }} aria-label="筛选项目" onSubmit={submit}>
      <div className="two-fields">
        <label className="field">编程语言<input value={draft.get('language') ?? ''} onChange={(e) => change('language', e.target.value)} placeholder="例如 Go" /></label>
        <label className="field">AI 标签<input value={draft.get('tag') ?? ''} onChange={(e) => change('tag', e.target.value)} placeholder="输入标签名称" /></label>
        <label className="field">采样等级<select value={draft.get('grade') ?? ''} onChange={(e) => change('grade', e.target.value)}>
          <option value="">不限</option>{['S', 'A', 'B', 'C'].map((grade) => <option key={grade}>{grade}</option>)}
        </select></label>
        <label className="field">项目状态<select value={draft.get('state') ?? ''} onChange={(e) => change('state', e.target.value)}>
          <option value="">默认状态</option>{Object.entries(repoState)
            .map(([state, label]) => <option key={state} value={state}>{label}</option>)}
        </select></label>
        <label className="field">屏蔽状态<select value={draft.get('blocked') ?? ''} onChange={(e) => change('blocked', e.target.value)}>
          <option value="">全部</option><option value="true">已屏蔽</option><option value="false">未屏蔽</option>
        </select></label>
        <label className="field">爆发信号<select value={draft.get('breakout') ?? ''} onChange={(e) => change('breakout', e.target.value)}>
          <BreakoutOptions />
        </select></label>
      </div>
      <div className="action-row">
        <button type="button" className="button" onClick={() => {
          const next = new URLSearchParams(draft)
          for (const key of filterKeys) next.delete(key)
          setDraft(next)
        }}>重置</button><button className="button primary">应用筛选</button>
      </div>
    </form>
}

const ranks = [
  ['growth24h', '24h 净增'], ['growth7d', '7d 净增'], ['hotness', '综合热度'], ['official', 'GitHub Trending'],
] as const

export function Ranking() {
  const [params, setParams] = useSearchParams()
  const view = ranks.find(([key]) => key === params.get('view'))?.[0] ?? 'growth24h'
  return <>
    <header className="page-head"><PageTitle title="热点榜" subtitle="追踪 Star 增长、综合热度与 GitHub Trending。" /></header>
    <div className="ranking-content">
      <AnimatedPills className="ranking-choice" role="group" aria-label="榜单类型">{ranks.map(([key, label]) => <button key={key} aria-pressed={key === view}
        onClick={() => setParams({ view: key }, { preventScrollReset: true })}>{label}</button>)}</AnimatedPills>
      {view === 'official' ? <Official /> : <RepoFeed params={new URLSearchParams({ sort: view, limit: '30' })}
        ranking={view} empty="暂无可排序的项目" />}
    </div>
  </>
}

const officialPeriods = [
  { value: 'daily', label: '日榜' }, { value: 'weekly', label: '周榜' }, { value: 'monthly', label: '月榜' },
]
const officialLanguageNames = new Map([
  ['all', '不限'], ['language:go', 'Go'], ['language:python', 'Python'], ['language:rust', 'Rust'], ['language:typescript', 'TypeScript'],
])

function Official() {
  const [params, setParams] = useSearchParams()
  const sources = useQuery({
    queryKey: ['trending', 'sources'],
    meta: { pageLoad: true },
    queryFn: ({ signal }) => publicApi<Items<{ sourceKey: string }>>('/trending/sources', { signal }),
  })
  const choices = new Set(sources.data?.items.map(({ sourceKey }) => sourceKey))
  const requested = params.get('source')
  const key = requested && choices.has(requested) ? requested : choices.has('all:daily') ? 'all:daily' : [...choices][0] ?? 'all:daily'
  const split = key.lastIndexOf(':')
  const sourceKey = key.slice(0, split)
  const period = key.slice(split + 1)
  const trending = useQuery({
    queryKey: ['trending', key],
    meta: { pageLoad: true },
    enabled: sources.isSuccess && choices.size > 0,
    queryFn: ({ signal }) => publicApi<Trending>('/trending?' + new URLSearchParams({ sourceKey, period }), { signal }),
  })
  if (sources.isPending) return <Skeleton />
  if (sources.error) return <PageError error={sources.error} retry={() => { void sources.refetch() }} />
  if (!choices.size) return <Empty title="暂无 GitHub Trending 榜单" />

  const languages = Array.from(new Set(Array.from(choices, (choice) => choice.slice(0, choice.lastIndexOf(':')))),
    (value) => ({ value, label: officialLanguageNames.get(value) ?? value.slice('language:'.length) }))
  const periodsFor = (source: string) => officialPeriods.filter(({ value }) => choices.has(source + ':' + value))
  const select = (source: string, range: string) => {
    const next = source + ':' + range
    if (next !== key) setParams({ view: 'official', source: next }, { preventScrollReset: true })
  }
  return <>
    <div className="official-tools">
      {trending.data?.updatedAt && <time className="minor muted" dateTime={trending.data.updatedAt}>抓取于 {timestamp(trending.data.updatedAt)}</time>}
      <div className="official-filters" role="group" aria-label="GitHub Trending 筛选">
        <OfficialFilter label="编程语言" options={languages} value={sourceKey} searchable onSelect={(language) => {
          // 只读取已配置的组合，换语言时优先沿用当前周期。
          const range = choices.has(language + ':' + period) ? period : periodsFor(language)[0]?.value
          if (range) select(language, range)
        }} />
        <OfficialFilter label="时间范围" options={periodsFor(sourceKey)} value={period} onSelect={(range) => select(sourceKey, range)} />
      </div>
    </div>
    {trending.isPending ? <Skeleton /> : trending.error && !trending.data
        ? <PageError error={trending.error} retry={() => { void trending.refetch() }} /> :
        trending.data?.items.length ? <>
          {trending.error && <Notice error>{errorText(trending.error)}</Notice>}
          <RankedRepos key={key} metric="official" items={trending.data.items.map((repo) => {
            const entry = repo.trendingEntries.find((item) => item.sourceKey === sourceKey && item.period === period)
            return { repo, rank: entry?.rank ?? null }
          })} />
        </> : <Empty title={trending.data?.batch ? '本批榜单没有项目' : '等待首次成功抓取'} />}
  </>
}

function OfficialFilter({ label, options, value, searchable, onSelect }: {
  label: string; options: { value: string; label: string }[]; value: string; searchable?: boolean; onSelect: (value: string) => void
}) {
  const id = useId()
  const menu = useRef<HTMLDivElement>(null)
  const [search, setSearch] = useState('')
  const keyword = search.trim().toLowerCase()
  const visible = options.filter((option) => option.label.toLowerCase().includes(keyword))
  return <div className="official-filter">
    <button className="button official-filter-trigger" type="button" popoverTarget={id}>
      <span className="muted">{label}</span><span className="official-filter-value">{options.find((option) => option.value === value)?.label}</span>
      <ChevronDown size={14} aria-hidden />
    </button>
    {/* 原生浮层负责点外关闭和焦点还原。搜索框内按 Esc 直接关闭浮层。 */}
    <div ref={menu} id={id} className="panel official-filter-menu" role="group" aria-label={label} popover="auto"
      onKeyDown={(event) => {
        if (event.key !== 'Escape') return
        event.preventDefault()
        event.currentTarget.hidePopover()
      }}
      onToggle={(event) => {
        if (event.newState === 'open') event.currentTarget.querySelector<HTMLElement>('input, button[aria-pressed="true"]')?.focus()
        else setSearch('')
      }}>
      {searchable && <label className="field official-filter-search">
        <input type="search" aria-label="搜索编程语言" placeholder="搜索编程语言" value={search} onChange={(event) => setSearch(event.target.value)} />
      </label>}
      <div className="official-filter-options">
        {visible.map((option) => <button key={option.value} type="button" aria-pressed={option.value === value} onClick={() => {
          menu.current?.hidePopover()
          onSelect(option.value)
        }}><span>{option.label}</span>{option.value === value && <Check size={14} aria-hidden />}</button>)}
        {!visible.length && <p className="minor muted">无匹配语言</p>}
      </div>
    </div>
  </div>
}

const categoryIcons = new Map([['agent', Bot], ['coding', CodeXml], ['mcp', Plug], ['media', Video], ['tools', Wrench]])

export function Categories() {
  const data = useCategories()
  const items = data.data?.items ?? []
  return <div className="category-directory">
    <header className="page-head"><PageTitle title="项目分类" subtitle="按仓库标签浏览不同技术方向的项目。" /></header>
    {data.isPending ? <Skeleton /> : data.error ? <PageError error={data.error} retry={() => { void data.refetch() }} /> :
      <section className="category-section" aria-labelledby="category-directions">
        <header className="category-group-head"><h2 id="category-directions">技术方向</h2><span className="minor muted">最近发现的监控项目</span></header>
        {/* 同一张卡片在桌面进入网格，在手机收为带入口图标的列表行。 */}
        <ul className="category-grid">{items.map(({ slug, name, count, latestRepo }) => {
          const Icon = categoryIcons.get(slug) ?? Folder
          return <li key={slug}><Link className="category-tile" to={repoCategoryPath(slug)} aria-label={name}>
            <span className="category-icon" data-category={slug}><Icon size={15} aria-hidden /></span>
            <div className="category-tile-body">
              <h3>{name}</h3>
              <p className="category-preview">{latestRepo
                ? [latestRepo.fullName, latestRepo.description].filter(Boolean).join(' — ')
                : '暂无项目'}</p>
              <div className="category-tile-foot minor muted">
                {latestRepo && <time dateTime={latestRepo.discoveredAt} title={timestamp(latestRepo.discoveredAt)}>{relativeTime(latestRepo.discoveredAt)}</time>}
                <span><strong>{number(count)}</strong> 个项目</span>
              </div>
            </div>
            <ChevronRight className="category-chevron muted" size={17} aria-hidden />
          </Link></li>
        })}</ul>
      </section>}
  </div>
}

export function ProjectThemes() {
  const query = useQuery({
    queryKey: ['project-themes'],
    meta: { pageLoad: true },
    queryFn: ({ signal }) => publicApi<ProjectThemeDirectory>('/project-themes', { signal }),
  })
  const themes = query.data?.items ?? []
  return <div className="category-directory">
    <header className="page-head"><PageTitle title="主题" subtitle={themes.length > 0
      ? '按方向浏览 ' + number(themes.length) + ' 个专题，追踪近期精选与重要进展。'
      : '按方向追踪近期精选与重要进展。'} /></header>
    {query.isPending ? <Skeleton /> :
      query.error ? <PageError error={query.error} retry={() => { void query.refetch() }} /> :
        themes.length === 0 ? <Empty title="暂无专题" /> :
          <div className="project-theme-groups" role="region" aria-label="专题目录">
            {query.data?.groups.map((group) => {
              const cards = themes.filter((theme) => theme.groupSlug === group.slug)
              if (!cards.length) return null
              return <section key={group.slug} aria-labelledby={'theme-group-' + group.slug}>
                <header className="category-group-head">
                  <h2 id={'theme-group-' + group.slug}>{group.name}</h2>
                  <p className="minor muted">{group.description}</p>
                </header>
                <ul className="project-theme-list">
                  {cards.map((theme) => <li key={theme.slug}>
                    <Link className="panel project-theme-card" to={topicPath(theme.slug)} aria-label={theme.name}>
                      <h3 className="title-with-icon"><ThemeIcon icon={theme.icon} color={theme.iconColor} />{theme.name}</h3>
                      <p>{theme.latestRepo?.fullName || theme.description}</p>
                      <div className="project-theme-meta minor muted">
                        {theme.latestRepo && <time dateTime={theme.latestRepo.discoveredAt}>{relativeTime(theme.latestRepo.discoveredAt)}</time>}
                        <span>近 30 天 <strong className="numeric">{number(theme.recentCount)}</strong> 个</span>
                        <span>共 <strong className="numeric">{number(theme.count)}</strong> 个项目</span>
                      </div>
                    </Link>
                  </li>)}
                </ul>
              </section>
            })}
          </div>}
  </div>
}

export function ProjectThemeDetail() {
  const { slug = '' } = useParams()
  const [params] = useSearchParams()
  const all = params.get('view') === 'all'
  const categories = useCategories()
  const query = useQuery({
    queryKey: ['project-themes', slug],
    meta: { pageLoad: true },
    queryFn: ({ signal }) => publicApi<ThemeDetail>('/project-themes/' + encodeURIComponent(slug), { signal }),
  })
  const theme = query.data
  // 两个视图共用仓库卡片及分页，精选复用发现页的后端规则。
  const criteria = new URLSearchParams({ theme: slug, limit: '30' })
  if (all) criteria.set('sort', 'discovered')
  const latestAt = theme?.latestRepo?.discoveredAt
  return <div className="project-topic-detail">
    <Link className="project-topic-back project-topic-back-desktop" to={topicsPath}><ArrowLeft size={15} aria-hidden />返回全部主题</Link>
    {query.isPending ? <Skeleton /> : query.error instanceof ApiError && query.error.status === 404 ?
      <Empty title="此专题不存在" /> : query.error ? <PageError error={query.error} retry={() => { void query.refetch() }} /> :
        theme && <>
          <header className="project-topic-head">
            <div className="project-topic-intro">
              <p className="project-topic-group minor">{theme.groupName}</p>
              <h1 className="title-with-icon">
                <ThemeIcon icon={theme.icon} color={theme.iconColor} size={24} />
                <span>{theme.name}{' '}<span className="project-topic-context" aria-hidden>{all ? '全部项目' : '最新动态'}</span></span>
              </h1>
              <p className="page-subtitle">{theme.description}</p>
              <p className="project-topic-counts minor muted">
                <span>近 30 天新增 <strong className="numeric">{number(theme.recentCount)}</strong> 个</span>
                <span>共收录 <strong className="numeric">{number(theme.count)}</strong> 个项目</span>
              </p>
            </div>
            <div className="project-topic-tools minor muted">
              {latestAt && <span>最近发现 <time dateTime={latestAt} className="numeric">{dateHeading(latestAt, true)} {timeOfDay(latestAt, true)}</time></span>}
              <Link to={topicPath(slug) + (all ? '' : '?view=all')}>
                {all ? <><ArrowLeft size={14} aria-hidden />返回近期精选</> : <>查看该专题全部项目<ArrowRight size={14} aria-hidden /></>}
              </Link>
            </div>
          </header>
          {!all && <ThemeChronicle key={slug} events={theme.milestones ?? []} />}
          <section className="timeline-section" aria-labelledby="project-theme-list">
            <h2 id="project-theme-list" className="sr-only">{all ? '全部项目' : '近期精选'}</h2>
            <RepoFeed params={criteria} names={mapNames(categories.data?.items)} timeline favorites discovery={!all}
              empty={all ? '暂无项目' : '暂无近期精选'} />
          </section>
        </>}
  </div>
}
