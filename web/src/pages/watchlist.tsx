import { ArrowRight, Crosshair, Star } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { errorText } from '../api/client'
import { distinct, useRepos } from '../api/queries'
import type { Repo } from '../api/types'
import { MonitorButton, RepoLink } from '../components/repo'
import { RepoCollection, RepoCollectionEmpty, RepoCollectionRow } from '../components/repo-collection'
import { InlineSearch, More, Notice, PageError, Skeleton } from '../components/ui'
import { number, repoState } from '../lib/format'

export function Watchlist() {
  const [params, setParams] = useSearchParams()
  const q = params.get('q') ?? ''
  const sort = params.get('sort') ?? 'followed'
  const criteria = new URLSearchParams(params)
  criteria.delete('cursor')
  // 重点监控沿用服务端的 followed 筛选、排序与游标。
  criteria.set('followed', 'true')
  criteria.set('sort', sort)
  criteria.set('limit', '30')
  const repos = useRepos(criteria)
  const items = distinct(repos.data?.pages.flatMap((page) => page.items) ?? [])
  const filtered = Boolean(q) || ['category', 'grade', 'language', 'state', 'tag'].some((key) => params.has(key))

  function search(value: string) {
    const next = new URLSearchParams(params)
    if (value) next.set('q', value)
    else next.delete('q')
    next.delete('cursor')
    setParams(next, { preventScrollReset: true })
  }

  return <RepoCollection title="重点监控" actions={<InlineSearch key={q} value={q} onSearch={search} />}>
    {repos.isPending ? <Skeleton /> : repos.error && !repos.data
      ? <PageError error={repos.error} retry={() => { void repos.refetch() }} />
      : <>
        {repos.error && <Notice error>{errorText(repos.error)}</Notice>}
        <p className="repo-collection-note" role="status">
          {repos.hasNextPage ? '已显示 ' : '共 '}{number(items.length)} 个{filtered ? '匹配的' : '重点监控'}仓库
        </p>
        {items.length > 0 ? <ul className="repo-collection-rows" aria-label="重点监控仓库">
          {items.map((repo) => <WatchlistRow key={repo.id} repo={repo} />)}
        </ul> : <RepoCollectionEmpty icon={<Crosshair size={20} aria-hidden />}
          message={filtered ? '暂无匹配的重点监控仓库' : '还没有加入重点监控的仓库'}
          action={filtered
            ? <button type="button" onClick={() => setParams({}, { preventScrollReset: true })}>查看全部重点监控</button>
            : <Link to="/admin/repos">浏览项目<ArrowRight size={14} aria-hidden /></Link>} />}
        <More hasMore={repos.hasNextPage} pending={repos.isFetchingNextPage} onClick={() => { void repos.fetchNextPage() }} />
      </>}
  </RepoCollection>
}

function WatchlistRow({ repo }: { repo: Repo }) {
  const excerpt = repo.summaryPreview || repo.titleZh || repo.description
  return <RepoCollectionRow title={<RepoLink repo={repo} />} date={repo.followedAt} dateLabel="加入于"
    excerpt={excerpt} action={<MonitorButton repo={repo} removeIcon />} metadata={<>
      <span>{repo.language ?? 'GitHub'}</span>
      <span className="metric" title="累计 Star"><Star size={13} aria-hidden /><span className="numeric">{number(repo.stars)}</span></span>
      {repo.state !== 'active' && <span>{repoState[repo.state]}</span>}
    </>} />
}
