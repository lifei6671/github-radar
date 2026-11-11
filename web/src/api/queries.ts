import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { QueryClient } from '@tanstack/react-query'
import { useMatch } from 'react-router'
import { api, publicApi } from './client'
import type { Category, Items, Page, Repo, Topic } from './types'

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
    queryKey: [key, search, read === api ? 'admin' : 'public'],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) => {
      const query = new URLSearchParams(search)
      if (pageParam !== null) query.set('cursor', pageParam)
      return read<Page<T>>(path + '?' + query, { signal })
    },
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  })
}

export function useRepos(params: URLSearchParams) {
  const admin = useAdminArea()
  return usePages<Repo>('/repos', params, 'repos', admin ? api : publicApi)
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

export function refreshRepo(queryClient: QueryClient, id: number) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: ['repo', String(id)] }),
    queryClient.invalidateQueries({ queryKey: ['repos'] }),
    queryClient.invalidateQueries({ queryKey: ['trending'] }),
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
