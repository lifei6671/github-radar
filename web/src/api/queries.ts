import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { Query, QueryClient, QueryFilters } from '@tanstack/react-query'
import { useMatch } from 'react-router'
import { api, publicApi } from './client'
import type { Category, Items, Page, Repo, RepoDetail, Topic } from './types'

export function useCategories() {
  return useQuery({
    queryKey: ['categories'],
    queryFn: ({ signal }) => publicApi<Items<Category>>('/categories', { signal }),
  })
}

export function useAdminArea() { return Boolean(useMatch('/admin/*')) }

function usePages<T>(path: string, params: URLSearchParams, key: string, read = api) {
  const search = params.toString()
  return useInfiniteQuery({
    // 同一个登录会话切换前后台时，也必须分开缓存公开和管理数据。
    queryKey: [key, path === '/discovery' ? path + '?' + search : search, read === api ? 'admin' : 'public'],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) => {
      const query = new URLSearchParams(search)
      if (pageParam !== null) query.set('cursor', pageParam)
      return read<Page<T>>(path + '?' + query, { signal })
    },
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  })
}

export function useRepos(params: URLSearchParams, discovery = false) {
  const admin = useAdminArea()
  // 发现和完整列表使用独立的分页缓存，同时沿用仓库撤回时的缓存清理。
  return usePages<Repo>(discovery ? '/discovery' : '/repos', params, 'repos', !discovery && admin ? api : publicApi)
}
export function useTopics(params: URLSearchParams) { return usePages<Topic>('/topics', params, 'topics') }

export function distinct<T extends { id: number }>(items: T[]): T[] {
  const ids = new Set<number>()
  return items.filter(({ id }) => {
    if (ids.has(id)) return false
    ids.add(id)
    return true
  })
}

// 名称入口和收藏入口都按仓库身份匹配详情缓存。
export function isRepoDetail(query: Query, id: number) {
  return query.queryKey[1] === id || (query.state.data as RepoDetail | null | undefined)?.id === id
}

// 公开子资源确认仓库已撤回时，同步关闭所有公开详情入口的缓存。
export async function withdrawPublicRepo(queryClient: QueryClient, id: number) {
  const aliases: QueryFilters = { queryKey: ['repo'], predicate: (query) =>
    query.queryKey[2] === 'public' && isRepoDetail(query, id) }
  await queryClient.cancelQueries(aliases)
  queryClient.setQueriesData(aliases, null)
}

export function refreshRepo(queryClient: QueryClient, id: number) {
  return Promise.all([
    // 不可见详情的空缓存也需重查，采样可能使仓库重新可见。
    queryClient.invalidateQueries({ queryKey: ['repo'], predicate: (query) => query.state.data === null || isRepoDetail(query, id) }),
    queryClient.invalidateQueries({ queryKey: ['repos'] }),
    queryClient.invalidateQueries({ queryKey: ['trending'] }),
    // 采样或摘要可能更新匹配词，目录和详情计数也要重新计算。
    queryClient.invalidateQueries({ queryKey: ['project-themes'] }),
  ])
}

export function useFollow(id: number) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationKey: ['follow', id],
    scope: { id: 'follow:' + id },
    mutationFn: (follow: boolean) => api<{ followed: boolean }>('/repos/' + id + '/follow', {
      method: 'POST', body: { follow },
    }),
    onSuccess: () => refreshRepo(queryClient, id),
  })
}

export function useRepoBlock(id: number) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (blocked: boolean) => api<{ blocked: boolean }>('/repos/' + id + '/block', { method: 'PATCH', body: { blocked } }),
    onSuccess: async ({ blocked }) => {
      if (blocked) {
        await withdrawPublicRepo(client, id)
        // 屏蔽后离开的页面也不能用旧的公开列表先绘制该仓库。
        await Promise.all([
          client.resetQueries({ queryKey: ['repos'], predicate: ({ queryKey }) => queryKey[2] === 'public' || queryKey[1] === 'discover-hot' }),
          client.resetQueries({ queryKey: ['trending'], predicate: ({ queryKey }) => queryKey[1] !== 'sources' }),
          client.resetQueries({ queryKey: ['categories'] }),
          client.resetQueries({ queryKey: ['project-themes'] }),
        ])
      }
      await Promise.all([
        refreshRepo(client, id),
        client.invalidateQueries({ queryKey: ['categories'] }),
        client.invalidateQueries({ queryKey: ['readme', id] }),
        client.invalidateQueries({ queryKey: ['trend', id] }),
        client.invalidateQueries({ queryKey: ['jobs'] }),
        client.invalidateQueries({ queryKey: ['limits'] }),
      ])
    },
  })
}
