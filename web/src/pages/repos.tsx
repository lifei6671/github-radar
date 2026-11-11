import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, Bot, ChevronRight, CodeXml, Folder, ListFilter, Plug, Video, Wrench } from 'lucide-react'
import { Fragment, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { errorText, publicApi } from '../api/client'
import { distinct, useAdminArea, useCategories, useRepos } from '../api/queries'
import type { Category, DiscoveryTabOption, Items, Page, Repo, Trending } from '../api/types'
import { Badge, Dialog, Empty, InlineSearch, More, Notice, PageError, Skeleton } from '../components/ui'
import { GrowthValue, RankedRepos, RepoCard, RepoLink, Sparkline } from '../components/repo'
import { dateGroup, dateHeading, dayOfWeek, decimal, number, relativeTime, repoState, timeOfDay, timestamp } from '../lib/format'

function mapNames(items: Category[] | undefined) {
  return new Map(items?.map(({ slug, name }) => [slug, name]))
}

export function Discover() {
  const categories = useCategories()
  const tabs = useQuery({
    queryKey: ['discovery-tabs'],
    queryFn: ({ signal }) => publicApi<Items<DiscoveryTabOption>>('/discovery-tabs', { signal }),
    staleTime: 0,
    refetchOnWindowFocus: true,
  })
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  // 打开旧的发现页链接时，已隐藏的标签回到「全部」。
  const categoryTags = params.getAll('category').filter((slug) => tabs.data?.items.some((tab) => tab.slug === slug))
  const criteria = new URLSearchParams({ sort: 'discovered', limit: '30' })
  for (const slug of categoryTags) criteria.append('category', slug)

  function search(q: string) {
    // 从发现页进入搜索结果时保留当前分类。
    const next = new URLSearchParams()
    for (const slug of categoryTags) next.append('category', slug)
    if (q) next.set('q', q)
    navigate({ pathname: '/repos', search: next.toString() })
  }

  return <>
    <header className="page-head"><h1>发现</h1></header>
    <div className="list-tools">
      <CategoryTabs items={tabs.data?.items} selected={categoryTags}
        onSelect={(slug) => setParams(slug ? { category: slug } : {}, { preventScrollReset: true })} />
      <InlineSearch value="" onSearch={search} />
    </div>
    {categories.error && <Notice error>{errorText(categories.error)}</Notice>}
    {tabs.error && <Notice error>{errorText(tabs.error)} <button type="button" onClick={() => { void tabs.refetch() }}>重试</button></Notice>}
    {tabs.isPending && params.has('category') ? <Skeleton /> : <div className="discover-grid">
      {categoryTags.length === 0 && <DiscoverHot />}
      <section className="timeline-section">
        <h2 className="feed-heading">项目动态</h2>
        <RepoFeed params={criteria} names={mapNames(categories.data?.items)} timeline empty="暂无项目动态" />
      </section>
    </div>}
  </>
}

function DiscoverHot() {
  const hot = useQuery({
    queryKey: ['repos', 'discover-hot'],
    queryFn: ({ signal }) => publicApi<Page<Repo>>('/repos?sort=hotness&limit=6', { signal }),
  })
  const leaders = hot.data?.items.filter((repo) => repo.hotnessRank !== null && repo.hotnessScore !== null).slice(0, 5) ?? []
  return <section className="panel rankings-panel" aria-labelledby="discover-hot">
    <header className="section-head">
      <h2 id="discover-hot"><span className="hot-dot" aria-hidden="true" />当前热点</h2>
      {leaders.length > 0 && hot.data?.updatedAt && <time className="minor muted" dateTime={hot.data.updatedAt}>计算于 {timestamp(hot.data.updatedAt)}</time>}
    </header>
    {hot.isPending ? <Skeleton rows={1} /> : hot.error && !hot.data ?
      <PageError error={hot.error} retry={() => { void hot.refetch() }} /> :
      leaders.length ? <ol className="leader-list">
        {leaders.map((repo) => <li key={repo.id}>
          <span className="rank-number numeric" data-rank={repo.hotnessRank}>{repo.hotnessRank}</span>
          <RepoLink repo={repo} />
          <GrowthValue value={repo.growth24h} label="24h" />
          <span className="leader-score"><strong className="numeric">{decimal(repo.hotnessScore)}</strong><span className="minor muted">热度</span></span>
          <Sparkline points={repo.sparkline} />
        </li>)}
      </ol> : <p className="aside-empty muted">暂无有效热度</p>}
    {hot.error && hot.data && <Notice error>{errorText(hot.error)}</Notice>}
  </section>
}

function RepoFeed({ params, names, timeline = false, ranking, recent = false, favorites = false, empty }: {
  params: URLSearchParams; names?: Map<string, string>; timeline?: boolean
  ranking?: 'growth24h' | 'growth7d' | 'hotness'; recent?: boolean; favorites?: boolean; empty: string
}) {
  const repos = useRepos(params)
  if (repos.isPending) return <Skeleton />
  if (repos.error && !repos.data) return <PageError error={repos.error} retry={() => { void repos.refetch() }} />
  const items = distinct(repos.data?.pages.flatMap((page) => page.items) ?? [])
  if (items.length === 0) return <Empty title={empty} />
  const updatedAt = repos.data?.pages[0]?.updatedAt
  // 近期发现与下方时间流复用同一批查询，排序和分类始终一致。
  let previous = ''
  return <>
    {ranking && <div className="feed-update muted minor">{updatedAt
      ? <time dateTime={updatedAt}>计算于 {timestamp(updatedAt)}</time> : '等待首次指标计算'}</div>}
    {repos.error && <Notice error>{errorText(repos.error)}</Notice>}
    {recent && <section className="panel recent-discoveries">
      <h2>近期发现</h2>
      <ol>{items.slice(0, 5).map((repo) => <li key={repo.id}>
        <time dateTime={repo.discoveredAt}>{timestamp(repo.discoveredAt)}</time>
        <div><RepoLink repo={repo} />{(repo.titleZh || repo.description) && <p>{repo.titleZh || repo.description}</p>}</div>
      </li>)}</ol>
    </section>}
    {ranking ? <RankedRepos metric={ranking} items={items.map((repo, index) => ({
      repo,
      rank: ranking === 'hotness' ? repo.hotnessRank : repo[ranking].quality === 'exact' ? index + 1 : null,
    }))} /> : <div className={'repo-list ' + (timeline ? 'timeline' : '')}>
      {items.map((repo) => {
        const at = repo.discoveredAt
        const label = timeline ? dateGroup(at) : ''
        const showDay = label && previous !== label
        previous = label
        return <Fragment key={repo.id}>
          {showDay && <h2 className="timeline-day" title={label}>{dateHeading(at)}<span className="minor muted">{dayOfWeek(at)}</span></h2>}
          <div className="timeline-item">
            {timeline && <div className="timeline-clock"><time dateTime={at}>{timeOfDay(at)}</time></div>}
            <RepoCard repo={repo} names={names} favorite={favorites} />
          </div>
        </Fragment>
      })}
    </div>}
    <More hasMore={repos.hasNextPage} pending={repos.isFetchingNextPage} onClick={() => { void repos.fetchNextPage() }} />
  </>
}

function AdminRepoTable({ params, names }: { params: URLSearchParams; names: Map<string, string> }) {
  const repos = useRepos(params)
  if (repos.isPending) return <Skeleton />
  if (!repos.data) return <PageError error={repos.error} retry={() => { void repos.refetch() }} />
  // 沿用服务端筛选和游标，仓库链接会记录当前列表地址。
  const items = distinct(repos.data.pages.flatMap((page) => page.items))
  return <>
    {repos.error && <Notice error>{errorText(repos.error)}</Notice>}
    {items.length ? <div className="admin-table-scroll" role="region" aria-label="仓库表格" tabIndex={0}>
      <table className="admin-table admin-directory-table admin-repo-table" aria-label="仓库列表">
      <thead><tr>
        {['仓库', '分类', '语言', '监控状态', 'Star', '24h 净增', '7d 净增', '热度', '最近采样'].map((label) =>
          <th key={label} scope="col">{label}</th>)}
      </tr></thead>
      <tbody>{items.map((repo) => {
        const description = repo.titleZh || repo.description
        return <tr key={repo.id}>
          <td className="admin-repo-identity">
            <RepoLink repo={repo} />
            {description && <span className="admin-repo-description minor muted" title={description}>{description}</span>}
          </td>
          <td>{repo.categories.map((slug) => names.get(slug) ?? slug).join('、') || '—'}</td>
          <td className="muted">{repo.language ?? '—'}</td>
          <td><div className="admin-repo-status">
            <Badge tone={repo.state === 'active' ? 'ok' : repo.state === 'unavailable' ? 'hot' : 'neutral'}>{repoState[repo.state]}</Badge>
            <span className="minor muted">{repo.grade} 级</span>
          </div></td>
          <td className="numeric">{number(repo.stars)}</td>
          <td><GrowthValue value={repo.growth24h} /></td>
          <td><GrowthValue value={repo.growth7d} /></td>
          <td className="numeric">{decimal(repo.hotnessScore)}</td>
          <td>{repo.lastSuccessAt ? <time dateTime={repo.lastSuccessAt}>{timestamp(repo.lastSuccessAt)}</time> : '—'}</td>
        </tr>
      })}</tbody>
    </table></div> : <p className="admin-empty">暂无匹配的仓库</p>}
    <More hasMore={repos.hasNextPage} pending={repos.isFetchingNextPage} onClick={() => { void repos.fetchNextPage() }} />
  </>
}

export function Repos() {
  const admin = useAdminArea()
  const [params, setParams] = useSearchParams()
  const categories = useCategories()
  const { slug } = useParams()
  const showCategoryTabs = !slug
  const [filtersOpen, setFiltersOpen] = useState(false)
  const selected = categories.data?.items.find((entry) => entry.slug === slug)
  const names = mapNames(categories.data?.items)
  const q = params.get('q') ?? ''
  const sort = params.get('sort') ?? 'discovered'
  const criteria = new URLSearchParams(params)
  criteria.delete('cursor')
  criteria.set('limit', '30')
  criteria.set('sort', sort)
  if (slug) {
    criteria.delete('category')
    criteria.append('category', slug)
  }
  const categoryTags = params.getAll('category')
  const advancedCount = filterKeys.slice(1).filter((name) => params.has(name)).length
  const filterCount = categoryTags.length + advancedCount
  const navigationOptions = { preventScrollReset: true, flushSync: admin }

  function change(name: string, value: string) {
    const next = new URLSearchParams(params)
    if (value) next.set(name, value)
    else next.delete(name)
    next.delete('cursor')
    setParams(next, navigationOptions)
  }

  function chooseCategory(value: string) {
    // 后台的分类标签可叠加搜索、排序及其他筛选。
    const next = new URLSearchParams(params)
    for (const key of admin ? ['category', 'cursor'] : [...filterKeys, 'sort', 'cursor']) next.delete(key)
    if (value) next.set('category', value)
    setParams(next, navigationOptions)
  }

  const categoryTabs = showCategoryTabs && <CategoryTabs items={categories.data?.items} selected={categoryTags}
    allSelected={admin ? categoryTags.length === 0 : filterCount === 0} onSelect={chooseCategory} />
  const activeFilters = admin ? advancedCount : filterCount
  const listActions = <div className="list-actions">
    <label className="select-label"><span className="sr-only">排序</span>
      <select value={sort} onChange={(e) => change('sort', e.target.value)}>
        <option value="discovered">最近发现</option><option value="hotness">综合热度</option>
        <option value="growth24h">24h 净增</option><option value="growth7d">7d 净增</option><option value="stars">累计 Star</option>
      </select>
    </label>
    <button className="button" onClick={() => setFiltersOpen(true)}><ListFilter size={17} />筛选{activeFilters > 0 && <span className="numeric">{activeFilters}</span>}</button>
  </div>
  const search = <InlineSearch key={q} value={q} onSearch={(value) => change('q', value)} />
  const categoryError = showCategoryTabs && categories.error && <Notice error>{errorText(categories.error)}</Notice>

  return <>
    {slug && <Link className="back-link" to="/categories"><ArrowLeft size={15} />返回技术主题</Link>}
    <header className="page-head">
      <div className="page-heading"><h1>{slug ? selected?.name ?? '技术主题' : admin ? '仓库管理' : '全部项目'}</h1>
        {selected && <p className="minor muted">{number(selected.count)} 个监控项目</p>}
      </div>
      {selected?.latestRepo && <time className="minor muted" dateTime={selected.latestRepo.discoveredAt}>最新发现 {timestamp(selected.latestRepo.discoveredAt)}</time>}
    </header>
    {admin ? <section className="panel admin-panel" aria-label="仓库管理列表">
      <div className="admin-table-toolbar admin-repo-toolbar">
        {search}
        <div className="admin-filters">{categoryTabs}{listActions}</div>
      </div>
      {categoryError}
      <AdminRepoTable params={criteria} names={names} />
    </section> : <>
      <div className="list-tools">{categoryTabs}{!showCategoryTabs && listActions}{search}</div>
      {categoryError}
      {slug && categories.data && !selected ? <Empty title="此主题不存在"><Link className="button" to="/categories">返回技术主题</Link></Empty> :
        <RepoFeed params={criteria} names={names} timeline={sort === 'discovered' || sort === 'followed'}
          favorites={showCategoryTabs}
          recent={Boolean(selected) && sort === 'discovered' && !q && filterCount === 0}
          empty="暂无匹配的项目" />}
    </>}
    {(admin || !showCategoryTabs) && filtersOpen && <FiltersDialog params={params} admin={admin} onClose={() => setFiltersOpen(false)}
      onApply={(next) => { setParams(next, navigationOptions); setFiltersOpen(false) }} />}
  </>
}

function CategoryTabs({ items, selected, allSelected = selected.length === 0, onSelect }: {
  items: DiscoveryTabOption[] | undefined; selected: string[]; allSelected?: boolean; onSelect: (slug: string) => void
}) {
  return <div className="pills repo-category-tabs" role="group" aria-label="项目分类">
    <button type="button" aria-pressed={allSelected} onClick={() => onSelect('')}>全部</button>
    {items?.map(({ slug, name }) => <button key={slug} type="button" aria-pressed={selected.includes(slug)}
      onClick={() => onSelect(slug)}>{name}</button>)}
  </div>
}

const filterKeys = ['category', 'grade', 'language', 'state', 'tag']

function FiltersDialog({ params, admin, onClose, onApply }: {
  params: URLSearchParams; admin: boolean; onClose: () => void; onApply: (value: URLSearchParams) => void
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
    for (const key of ['language', 'tag']) if (!draft.get(key)?.trim()) draft.delete(key)
    onApply(draft)
  }
  return <Dialog title="筛选项目" onClose={onClose}>
    <form className="dialog-form" onSubmit={submit}>
      <div className="two-fields">
        <label className="field">编程语言<input data-autofocus value={draft.get('language') ?? ''} onChange={(e) => change('language', e.target.value)} placeholder="例如 Go" /></label>
        <label className="field">AI 标签<input value={draft.get('tag') ?? ''} onChange={(e) => change('tag', e.target.value)} placeholder="输入标签名称" /></label>
        <label className="field">采样等级<select value={draft.get('grade') ?? ''} onChange={(e) => change('grade', e.target.value)}>
          <option value="">不限</option>{['S', 'A', 'B', 'C'].map((grade) => <option key={grade}>{grade}</option>)}
        </select></label>
        <label className="field">项目状态<select value={draft.get('state') ?? ''} onChange={(e) => change('state', e.target.value)}>
          <option value="">默认状态</option>{Object.entries(repoState)
            .filter(([state]) => admin || (state !== 'unavailable' && state !== 'address_pending'))
            .map(([state, label]) => <option key={state} value={state}>{label}</option>)}
        </select></label>
      </div>
      <div className="action-row">
        <button type="button" className="button" onClick={() => {
          const next = new URLSearchParams(draft)
          for (const key of admin ? filterKeys.slice(1) : filterKeys) next.delete(key)
          setDraft(next)
        }}>重置</button><button className="button primary">应用筛选</button>
      </div>
    </form>
  </Dialog>
}

const ranks = [
  ['growth24h', '24h 净增'], ['growth7d', '7d 净增'], ['hotness', '综合热度'], ['official', 'GitHub Trending'],
] as const

export function Ranking() {
  const [params, setParams] = useSearchParams()
  const view = ranks.find(([key]) => key === params.get('view'))?.[0] ?? 'growth24h'
  return <>
    <header className="page-head"><h1>热点榜</h1></header>
    <div className="ranking-content">
      <div className="pills ranking-choice" role="group" aria-label="榜单类型">{ranks.map(([key, label]) => <button key={key} aria-pressed={key === view}
        onClick={() => setParams({ view: key }, { preventScrollReset: true })}>{label}</button>)}</div>
      {view === 'official' ? <Official /> : <RepoFeed params={new URLSearchParams({ sort: view, limit: '30' })}
        ranking={view} empty="暂无可排序的项目" />}
    </div>
  </>
}

function Official() {
  const [params, setParams] = useSearchParams()
  const sources = useQuery({
    queryKey: ['trending', 'sources'],
    queryFn: ({ signal }) => publicApi<Items<{ sourceKey: string }>>('/trending/sources', { signal }),
  })
  const choices = sources.data?.items ?? []
  const key = params.get('source') ?? choices.find((rule) => rule.sourceKey === 'all:daily')?.sourceKey ?? choices[0]?.sourceKey ?? 'all:daily'
  const split = key.lastIndexOf(':')
  const sourceKey = key.slice(0, split)
  const period = key.slice(split + 1)
  const trending = useQuery({
    queryKey: ['trending', key],
    enabled: sources.isSuccess && choices.some((rule) => rule.sourceKey === key),
    queryFn: ({ signal }) => publicApi<Trending>('/trending?' + new URLSearchParams({ sourceKey, period }), { signal }),
  })
  if (sources.isPending) return <Skeleton />
  if (sources.error) return <PageError error={sources.error} retry={() => { void sources.refetch() }} />
  if (!choices.length) return <Empty title="暂无 GitHub Trending 榜单" />
  const periods: Record<string, string> = { daily: '日榜', weekly: '周榜', monthly: '月榜' }
  return <>
    <div className="list-tools official-tools">
      <label className="field compact-field">榜单来源<select value={key} onChange={(e) => setParams({ view: 'official', source: e.target.value }, { preventScrollReset: true })}>
        {!choices.some((choice) => choice.sourceKey === key) && <option value={key}>来源已移除</option>}
        {choices.map((rule) => {
          const splitAt = rule.sourceKey.lastIndexOf(':')
          const source = rule.sourceKey.slice(0, splitAt)
          return <option key={rule.sourceKey} value={rule.sourceKey}>{source === 'all' ? '全站' : source.replace('language:', '')} {periods[rule.sourceKey.slice(splitAt + 1)]}</option>
        })}
      </select></label>
      {trending.data?.updatedAt && <time className="minor muted" dateTime={trending.data.updatedAt}>抓取于 {timestamp(trending.data.updatedAt)}</time>}
    </div>
    {!choices.some((choice) => choice.sourceKey === key) ? <Empty title="请选择可用的榜单来源" /> :
      trending.isPending ? <Skeleton /> : trending.error && !trending.data
        ? <PageError error={trending.error} retry={() => { void trending.refetch() }} /> :
        trending.data?.items.length ? <>
          {trending.error && <Notice error>{errorText(trending.error)}</Notice>}
          <RankedRepos metric="official" items={trending.data.items.map((repo) => {
            const entry = repo.trendingEntries.find((item) => item.sourceKey === sourceKey && item.period === period)
            return { repo, rank: entry?.rank ?? null }
          })} />
        </> : <Empty title={trending.data?.batch ? '本批榜单没有项目' : '等待首次成功抓取'} />}
  </>
}

const categoryIcons = new Map([['agent', Bot], ['coding', CodeXml], ['mcp', Plug], ['media', Video], ['tools', Wrench]])

export function Categories() {
  const data = useCategories()
  const items = data.data?.items ?? []
  return <div className="category-directory">
    <header className="page-heading"><h1>技术主题</h1>
      {items.length > 0 && <p className="muted">按仓库标签浏览 {number(items.length)} 个主题</p>}
    </header>
    {data.isPending ? <Skeleton /> : data.error ? <PageError error={data.error} retry={() => { void data.refetch() }} /> :
      <section className="category-section" aria-labelledby="category-directions">
        <header className="category-group-head"><h2 id="category-directions">技术方向</h2><span className="minor muted">最近发现的监控项目</span></header>
        {/* 同一张卡片在桌面进入网格，在手机收为带入口图标的列表行。 */}
        <ul className="category-grid">{items.map(({ slug, name, count, latestRepo }) => {
          const Icon = categoryIcons.get(slug) ?? Folder
          return <li key={slug}><Link className="category-tile" to={'/categories/' + encodeURIComponent(slug)} aria-label={name}>
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
