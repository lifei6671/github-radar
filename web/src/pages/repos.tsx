import { useQuery } from '@tanstack/react-query'
import { ArrowDownRight, ArrowRight, ArrowUpRight, Folder, ListFilter, Search, Star } from 'lucide-react'
import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { api, errorText } from '../api/client'
import { distinct, useCategories, useRepos } from '../api/queries'
import type { Category, ListSettings, Page, Repo, RuleSetting, Trending } from '../api/types'
import { Badge, Dialog, Empty, More, Notice, PageError, Skeleton } from '../components/ui'
import { External, GrowthValue, RepoCard, RepoLink, Sparkline } from '../components/repo'
import { dateGroup, decimal, number, repoState, timestamp } from '../lib/format'

function mapNames(items: Category[] | undefined) {
  return new Map(items?.map(({ slug, name }) => [slug, name]))
}

export function Discover() {
  const hot = useQuery({
    queryKey: ['repos', 'discover-hot'],
    queryFn: ({ signal }) => api<Page<Repo>>('/repos?sort=hotness&limit=6', { signal }),
  })
  const categories = useCategories()
  const [params, setParams] = useSearchParams()
  const feed = params.get('feed') === 'growth' ? 'growth' : 'latest'
  const featured = hot.data?.items.find((repo) => repo.hotnessScore !== null)
  const leaders = hot.data?.items.filter((repo) => repo.hotnessRank !== null).slice(0, 5) ?? []
  const names = mapNames(categories.data?.items)

  return <>
    <header className="page-head"><h1>发现</h1><Link to="/repos" className="button">全部项目<ArrowRight size={16} /></Link></header>
    <div className="discover-grid">
      <div className="discover-main">
        {hot.isPending ? <Skeleton rows={1} /> : hot.error && !hot.data
          ? <PageError error={hot.error} retry={() => { void hot.refetch() }} /> :
          featured ? <Spotlight repo={featured} names={names} /> : <section className="feature-panel feature-empty">
            <div className="feature-empty-mark"><ArrowUpRight size={30} strokeWidth={1.4} /></div>
            <div><h2>增长热点待观察</h2><p className="muted">积累有效 Star 观测后，这里会显示热度最高的项目。</p></div>
          </section>}
        <section className="timeline-section">
          <header className="section-head section-divider">
            <h2>项目动态</h2>
            <div className="pills" aria-label="项目动态排序">
              <button aria-pressed={feed === 'latest'} onClick={() => setParams({}, { preventScrollReset: true })}>最新发现</button>
              <button aria-pressed={feed === 'growth'} onClick={() => setParams({ feed: 'growth' }, { preventScrollReset: true })}>近期增长</button>
            </div>
          </header>
          <RepoFeed params={new URLSearchParams({ sort: feed === 'growth' ? 'growth24h' : 'discovered', limit: '30' })}
            names={names} timeline={feed === 'latest'} empty="暂无项目动态" />
        </section>
      </div>
      <aside className="discover-aside" aria-label="发现侧栏">
        <section className="panel rankings-panel">
          <header className="section-head"><h2>热度榜</h2><Link className="icon-button" to="/trending?view=hotness" aria-label="查看热度榜"><ArrowUpRight size={18} /></Link></header>
          {leaders.length ? <ol className="leader-list">
            {leaders.map((repo) => <li key={repo.id}>
              <span className="leader-rank numeric">{String(repo.hotnessRank).padStart(2, '0')}</span>
              <div><RepoLink repo={repo} /><span className="minor muted">{repo.language ?? '未标注语言'}</span></div>
              <strong className="numeric tone-accent">{decimal(repo.hotnessScore)}</strong>
            </li>)}
          </ol> : <div className="aside-empty muted">{hot.isPending ? '加载中' : hot.error ? '榜单暂不可用' : '暂无有效热度'}</div>}
          {hot.data?.updatedAt && <footer className="panel-note">计算于 {timestamp(hot.data.updatedAt)}</footer>}
        </section>
        <section className="panel side-topics">
          <header className="section-head"><h2>技术主题</h2><Link className="icon-button" to="/categories" aria-label="查看技术主题"><ArrowUpRight size={18} /></Link></header>
          <div className="topic-links">{categories.data?.items.map((item) => <Link key={item.slug} to={'/categories/' + encodeURIComponent(item.slug)}>
            <span>{item.name}</span><span className="numeric muted">{number(item.count)}</span>
          </Link>)}</div>
          {categories.error && <Notice error>{errorText(categories.error)}</Notice>}
        </section>
      </aside>
    </div>
  </>
}

function Spotlight({ repo, names }: { repo: Repo; names: Map<string, string> }) {
  const [owner, project] = repo.fullName.split('/', 2)
  return <section className="feature-panel">
    <div className="feature-info">
      <div className="feature-meta"><Badge tone="accent">热度 #{repo.hotnessRank}</Badge><span className="muted minor">{repo.language}</span>
        {repo.categories[0] && <Link className="context-chip" to={'/categories/' + encodeURIComponent(repo.categories[0])}>{names.get(repo.categories[0]) ?? repo.categories[0]}</Link>}
      </div>
      <div className="feature-name"><RepoLink repo={repo}><span>{owner}/</span>{project ?? ''}</RepoLink><External href={repo.htmlUrl} iconOnly /></div>
      {(repo.titleZh || repo.description) && <p>{repo.titleZh || repo.description}</p>}
      <div className="metrics-inline feature-metrics">
        <span className="metric"><Star size={17} /><strong className="numeric">{number(repo.stars)}</strong></span>
        <GrowthValue value={repo.growth24h} label="24h" />
        <GrowthValue value={repo.growth7d} label="7d" />
      </div>
    </div>
    <div className="feature-chart">
      <div className="spark-heading"><span>累计 Star</span><span className="minor muted">最近 24 小时</span></div>
      <Sparkline points={repo.sparkline} large />
    </div>
  </section>
}

function RepoFeed({ params, names, timeline = false, rank = false, score = false, watching = false, empty }: {
  params: URLSearchParams; names: Map<string, string>; timeline?: boolean; rank?: boolean; score?: boolean; watching?: boolean; empty: string
}) {
  const repos = useRepos(params)
  if (repos.isPending) return <Skeleton />
  if (repos.error && !repos.data) return <PageError error={repos.error} retry={() => { void repos.refetch() }} />
  const items = distinct(repos.data?.pages.flatMap((page) => page.items) ?? [])
  if (items.length === 0) return <Empty title={empty} />
  let previous = ''
  return <>
    {rank && <div className="feed-update muted minor">{repos.data?.pages[0]?.updatedAt
      ? '计算于 ' + timestamp(repos.data.pages[0].updatedAt) : '等待首次指标计算'}</div>}
    {repos.error && <Notice error>{errorText(repos.error)}</Notice>}
    <div className={'repo-list ' + (timeline ? 'timeline' : '')}>
      {items.map((repo, i) => {
        const label = timeline ? dateGroup(repo.discoveredAt) : ''
        const showDay = label && previous !== label
        previous = label
        const position = score ? repo.hotnessRank ?? undefined :
          params.get('sort') === 'growth24h' ? repo.growth24h.quality === 'exact' ? i + 1 : undefined :
            params.get('sort') === 'growth7d' && repo.growth7d.quality === 'exact' ? i + 1 : undefined
        return <div className="timeline-item" key={repo.id}>
          {showDay && <div className="timeline-day"><span />{label}</div>}
          <RepoCard repo={repo} names={names} rank={rank ? position : undefined} score={score} watching={watching} />
        </div>
      })}
    </div>
    <More hasMore={repos.hasNextPage} pending={repos.isFetchingNextPage} onClick={() => { void repos.fetchNextPage() }} />
  </>
}

export function Repos({ watching = false }: { watching?: boolean }) {
  const [params, setParams] = useSearchParams()
  const categories = useCategories()
  const { slug } = useParams()
  const [filtersOpen, setFiltersOpen] = useState(false)
  const selected = categories.data?.items.find((entry) => entry.slug === slug)
  const names = mapNames(categories.data?.items)
  const q = params.get('q') ?? ''
  const sort = params.get('sort') ?? (watching ? 'followed' : 'discovered')
  const criteria = new URLSearchParams(params)
  criteria.delete('cursor')
  criteria.set('limit', '30')
  criteria.set('sort', sort)
  if (watching) criteria.set('followed', 'true')
  if (slug) {
    criteria.delete('category')
    criteria.append('category', slug)
  }
  const filterCount = params.getAll('category').length + ['grade', 'language', 'state', 'tag'].filter((name) => params.has(name)).length

  function change(name: string, value: string) {
    const next = new URLSearchParams(params)
    if (value) next.set(name, value)
    else next.delete(name)
    next.delete('cursor')
    setParams(next, { preventScrollReset: true })
  }

  return <>
    {slug && <Link className="back-link" to="/categories"><ArrowDownRight size={16} />技术主题</Link>}
    <header className="page-head"><h1>{watching ? '我的关注' : slug ? selected?.name ?? '技术主题' : '全部项目'}</h1>
      {!slug && <Link className="button" to={watching ? '/repos' : '/categories'}>{watching ? '浏览项目' : '技术主题'}<ArrowRight size={15} /></Link>}
    </header>
    <div className="list-tools">
      <InlineSearch key={q} value={q} onSearch={(value) => change('q', value)} />
      <div className="list-actions">
        <label className="select-label"><span className="sr-only">排序</span>
          <select value={sort} onChange={(e) => change('sort', e.target.value)}>
            {watching && <option value="followed">最近关注</option>}
            <option value="discovered">最近发现</option><option value="hotness">综合热度</option>
            <option value="growth24h">24h 净增</option><option value="growth7d">7d 净增</option><option value="stars">累计 Star</option>
          </select>
        </label>
        <button className="button" onClick={() => setFiltersOpen(true)}><ListFilter size={17} />筛选{filterCount > 0 && <span className="numeric">{filterCount}</span>}</button>
      </div>
    </div>
    {filtersOpen && <FiltersDialog params={params} categories={slug ? [] : categories.data?.items ?? []} onClose={() => setFiltersOpen(false)}
      onApply={(next) => { setParams(next, { preventScrollReset: true }); setFiltersOpen(false) }} />}
    {slug && categories.data && !selected ? <Empty title="此主题不存在"><Link className="button" to="/categories">返回技术主题</Link></Empty> :
      <RepoFeed params={criteria} names={names} watching={watching} empty={watching ? '暂无关注的项目' : '暂无匹配的项目'} />}
  </>
}

function InlineSearch({ value, onSearch }: { value: string; onSearch: (value: string) => void }) {
  const [input, setInput] = useState(value)
  return <form className="inline-search" onSubmit={(e) => { e.preventDefault(); onSearch(input.trim()) }}>
    <Search size={17} /><input aria-label="搜索仓库名或简介" placeholder="仓库名或简介" value={input} onChange={(e) => setInput(e.target.value)} />
    {value && <button type="button" className="text-button" onClick={() => onSearch('')}>清除</button>}
    <button type="submit" className="text-button">搜索</button>
  </form>
}

const filterKeys = ['category', 'grade', 'language', 'state', 'tag']

function FiltersDialog({ params, categories, onClose, onApply }: {
  params: URLSearchParams; categories: Category[]; onClose: () => void; onApply: (value: URLSearchParams) => void
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
          <option value="">默认状态</option>{Object.entries(repoState).map(([state, label]) => <option key={state} value={state}>{label}</option>)}
        </select></label>
      </div>
      {categories.length > 0 && <fieldset><legend>技术主题</legend><div className="pills wrap">
        {categories.map((category) => <button type="button" key={category.slug} aria-pressed={draft.getAll('category').includes(category.slug)}
          onClick={() => setDraft((old) => {
            const next = new URLSearchParams(old)
            if (next.getAll('category').includes(category.slug)) next.delete('category', category.slug)
            else next.append('category', category.slug)
            return next
          })}>{category.name}</button>)}
      </div></fieldset>}
      <div className="action-row">
        <button type="button" className="button" onClick={() => {
          const next = new URLSearchParams(draft)
          for (const key of filterKeys) next.delete(key)
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
  const categories = useCategories()
  const names = mapNames(categories.data?.items)
  return <>
    <header className="page-head"><h1>热点榜</h1></header>
    <div className="pills wrap ranking-tabs" aria-label="榜单类型">{ranks.map(([key, label]) => <button key={key} aria-pressed={key === view}
      onClick={() => setParams({ view: key }, { preventScrollReset: true })}>{label}</button>)}</div>
    {view === 'official' ? <Official names={names} /> : <RepoFeed params={new URLSearchParams({ sort: view, limit: '30' })}
      names={names} rank score={view === 'hotness'} empty="暂无可排序的项目" />}
  </>
}

function Official({ names }: { names: Map<string, string> }) {
  const [params, setParams] = useSearchParams()
  const rules = useQuery({
    queryKey: ['settings', 'rules'],
    queryFn: ({ signal }) => api<ListSettings<RuleSetting>>('/system/discovery-rules', { signal }),
  })
  const choices = rules.data?.items.filter((rule) => rule.sourceType === 'trending') ?? []
  const key = params.get('source') ?? choices.find((rule) => rule.sourceKey === 'all:daily')?.sourceKey ?? choices[0]?.sourceKey ?? 'all:daily'
  const split = key.lastIndexOf(':')
  const sourceKey = key.slice(0, split)
  const period = key.slice(split + 1)
  const trending = useQuery({
    queryKey: ['trending', key],
    enabled: rules.isSuccess && choices.some((rule) => rule.sourceKey === key),
    queryFn: ({ signal }) => api<Trending>('/trending?' + new URLSearchParams({ sourceKey, period }), { signal }),
  })
  if (rules.isPending) return <Skeleton />
  if (rules.error) return <PageError error={rules.error} retry={() => { void rules.refetch() }} />
  if (!choices.length) return <Empty title="暂无已配置的 GitHub Trending 来源"><Link className="button" to="/system?tab=rules">管理发现规则</Link></Empty>
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
          <div className="repo-list">{trending.data.items.map((repo) => {
            const entry = repo.trendingEntries.find((item) => item.sourceKey === sourceKey && item.period === period)
            return <RepoCard key={repo.id} repo={repo} names={names} rank={entry?.rank} officialEntry={entry ?? null} />
          })}</div>
        </> : <Empty title={trending.data?.batch ? '本批榜单没有项目' : '等待首次成功抓取'} />}
  </>
}

export function Categories() {
  const data = useCategories()
  return <>
    <header className="page-head"><h1>技术主题</h1><Link className="button" to="/repos">全部项目<ArrowRight size={15} /></Link></header>
    {data.isPending ? <Skeleton /> : data.error ? <PageError error={data.error} retry={() => { void data.refetch() }} /> :
      <div className="category-grid">{data.data.items.map((item) => <Link key={item.slug} className="panel category-tile" to={'/categories/' + encodeURIComponent(item.slug)}>
        <Folder size={20} className="tone-accent" /><div><h2>{item.name}</h2><span className="minor muted">{number(item.count)} 个监控项目</span></div><ArrowUpRight size={17} />
      </Link>)}</div>}
  </>
}
