import { queryOptions, useQuery } from '@tanstack/react-query'
import { ArrowRight, Bookmark } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import { publicApi, sessionQueryClient } from '../api/client'
import { publishedFavoritesKey } from '../api/queries'
import type { Items } from '../api/types'
import { FavoriteButton } from '../components/favorite'
import { RepoCollection, RepoCollectionEmpty, RepoCollectionRow } from '../components/repo-collection'
import { PageError, Skeleton } from '../components/ui'
import { getFavorites, useFavorites } from '../lib/favorites'
import { repoListReturn, repoPath } from '../lib/format'

function publishedOptions(favorites: ReturnType<typeof getFavorites>) {
  const ids = [...new Set(favorites.map(({ id }) => id))].sort((a, b) => a - b)
  return queryOptions({
    queryKey: [publishedFavoritesKey, ids],
    enabled: ids.length > 0,
    staleTime: 0,
    networkMode: 'always',
    refetchOnMount: false,
    retryOnMount: false,
    refetchOnWindowFocus: true,
    refetchOnReconnect: true,
    queryFn: async ({ signal }) => {
      const batches: Promise<Items<number>>[] = []
      for (let start = 0; start < ids.length; start += 100) {
        const query = new URLSearchParams()
        for (const id of ids.slice(start, start + 100)) query.append('id', String(id))
        batches.push(publicApi<Items<number>>('/repos/published?' + query, { signal }))
      }
      return new Set((await Promise.all(batches)).flatMap(({ items }) => items))
    },
  })
}

export async function loadStarred() {
  const favorites = getFavorites()
  // 历史回退时先校验可见性，让滚动恢复在收藏列表挂载后执行。
  if (favorites.length) await sessionQueryClient().query(publishedOptions(favorites)).catch(() => undefined)
  return null
}

export function Starred() {
  const location = useLocation()
  const favorites = useFavorites()
  const published = useQuery(publishedOptions(favorites))
  const visible = favorites.filter(({ id }) => published.data?.has(id))
  return <RepoCollection title="我的收藏" subtitle="收藏保存在此浏览器，清除浏览器数据后会丢失。">
    {favorites.length > 0 && published.isFetching ? <Skeleton /> :
      favorites.length > 0 && published.error ? <PageError error={published.error} retry={() => { void published.refetch() }} /> :
      visible.length ? <ul className="repo-collection-rows" aria-label="收藏仓库">
      {visible.map((item) => <RepoCollectionRow key={item.id} metadata={<span>{item.language ?? 'GitHub'}</span>}
        title={<Link className="repo-name" to={repoPath(item.fullName) + '?repoId=' + item.id} state={repoListReturn(location)}>{item.fullName}</Link>}
        date={item.savedAt} dateLabel="收藏于" excerpt={item.excerpt} action={<FavoriteButton item={item} removeIcon />} />)}
    </ul> : <RepoCollectionEmpty icon={<Bookmark size={20} aria-hidden />} message={favorites.length ? '暂无可展示的收藏' : '还没有收藏的仓库'}
      action={<Link to="/repos">浏览全部项目<ArrowRight size={14} aria-hidden /></Link>} />}
  </RepoCollection>
}
