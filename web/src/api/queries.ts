import { infiniteQueryOptions, queryOptions, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import type { Query, QueryClient, QueryFilters } from '@tanstack/react-query'
import { useMatch } from 'react-router'
import type { LoaderFunctionArgs, ShouldRevalidateFunctionArgs } from 'react-router'
import { api, ApiError, publicApi, sessionQueryClient } from './client'
import type { Category, Items, Page, Repo, RepoDetail, RepoReadme, Topic } from './types'
import { encodedRepoName } from '../lib/format'

export function useCategories() {
  const admin = useAdminArea()
  return useQuery({
    queryKey: ['categories', admin ? 'admin' : 'public'],
    meta: { pageLoad: true },
    queryFn: ({ signal }) => (admin ? api : publicApi)<Items<Category>>('/categories', { signal }),
  })
}

export function useAdminArea() { return Boolean(useMatch('/admin/*')) }

export function detailRepoId(value: unknown) {
  const id = typeof value === 'string' ? Number(value) : value
  return typeof id === 'number' && Number.isSafeInteger(id) && id > 0 ? id : undefined
}

export function repoDetailOptions(client: QueryClient, fullName: string, repoId: number | undefined, admin: boolean) {
  const pinnedPublic = !admin && repoId !== undefined
  return queryOptions({
    queryKey: ['repo', repoId ?? fullName.toLowerCase(), admin ? 'admin' : 'public'] as const,
    meta: { pageLoad: pinnedPublic ? 'revalidate' : true },
    retryOnMount: admin,
    networkMode: admin ? 'online' : 'always',
    queryFn: async ({ signal }): Promise<RepoDetail | null> => {
      const path = admin && repoId !== undefined ? '/repos/' + repoId : '/repos/by-name/' + encodedRepoName(fullName) +
        (repoId === undefined ? '' : '?repoId=' + repoId)
      try {
        return await (admin ? api : publicApi)<RepoDetail>(path, { signal })
      } catch (error) {
        if (!admin && error instanceof ApiError && error.status === 404) {
          void resetPublicRepoLists(client)
          return null
        }
        throw error
      }
    },
  })
}

export async function loadPublicRepo({ request, params }: LoaderFunctionArgs) {
  const identity = new URL(request.url).searchParams.get('repoId')
  const repoId = detailRepoId(identity)
  if (!params.owner || !params.name || (identity !== null && repoId === undefined)) return null
  const client = sessionQueryClient()
  const options = repoDetailOptions(client, params.owner + '/' + params.name, repoId, false)
  // 进入详情时重新确认仓库可见性和当前 README 版本。
  await client.query({ ...options, staleTime: 0 }).catch(() => undefined)
  return null
}

export function revalidatePublicRepo({ currentUrl, nextUrl, defaultShouldRevalidate }: ShouldRevalidateFunctionArgs) {
  return defaultShouldRevalidate && (currentUrl.pathname !== nextUrl.pathname || currentUrl.search === nextUrl.search ||
    currentUrl.searchParams.get('repoId') !== nextUrl.searchParams.get('repoId'))
}

export function repoReadmeOptions(client: QueryClient, repoId: number, admin: boolean) {
  return queryOptions({
    queryKey: ['readme', repoId, admin ? 'admin' : 'public'],
    meta: { pageLoad: true },
    queryFn: async ({ signal }) => {
      try {
        return await (admin ? api : publicApi)<RepoReadme>('/repos/' + repoId + '/readme', { signal })
      } catch (error) {
        if (!admin && error instanceof ApiError && error.status === 404) await withdrawPublicRepo(client, repoId)
        throw error
      }
    },
    staleTime: 0,
    refetchOnWindowFocus: false,
    retry: false,
  })
}

function pageOptions<T>(path: string, params: URLSearchParams, key: string, read = api, retryOnMount = true) {
  const search = params.toString()
  return infiniteQueryOptions({
    // 同一个登录会话切换前后台时，也必须分开缓存公开和管理数据。
    queryKey: [key, path === '/discovery' ? path + '?' + search : search, read === api ? 'admin' : 'public'],
    meta: { pageLoad: true },
    retryOnMount,
    initialPageParam: null as string | null,
    queryFn: ({ pageParam, signal }) => {
      const query = new URLSearchParams(search)
      if (pageParam !== null) query.set('cursor', pageParam)
      return read<Page<T>>(path + '?' + query, { signal })
    },
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  })
}

export function publicRepoListOptions(params: URLSearchParams) {
  // 路由已完成首屏请求。失败时交由列表的重试按钮处理。
  return { ...pageOptions<Repo>('/repos', params, 'repos', publicApi, false), networkMode: 'always' as const }
}

export function useRepos(params: URLSearchParams, discovery = false, routed = false) {
  const admin = useAdminArea()
  // 发现和完整列表使用独立的分页缓存，同时沿用仓库撤回时的缓存清理。
  return useInfiniteQuery(routed ? publicRepoListOptions(params) :
    pageOptions<Repo>(discovery ? '/discovery' : '/repos', params, 'repos', !discovery && admin ? api : publicApi))
}
export function useTopics(params: URLSearchParams) { return useInfiniteQuery(pageOptions<Topic>('/topics', params, 'topics')) }

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

export const similarReposKey = 'similar-repos'
export const publishedFavoritesKey = 'published-favorites'
export const dailyReportsKey = 'daily-reports'

export function resetPublicRepoLists(queryClient: QueryClient) {
  return Promise.all([
    queryClient.resetQueries({ queryKey: ['repos'], predicate: ({ queryKey }) => queryKey[2] === 'public' || queryKey[1] === 'discover-hot' }),
    queryClient.resetQueries({ queryKey: ['trending'], predicate: ({ queryKey }) => queryKey[1] !== 'sources' }),
    queryClient.resetQueries({ queryKey: ['categories', 'public'] }),
    queryClient.resetQueries({ queryKey: ['project-themes'] }),
    queryClient.resetQueries({ queryKey: [similarReposKey] }),
    queryClient.resetQueries({ queryKey: [publishedFavoritesKey] }),
    queryClient.resetQueries({ queryKey: [dailyReportsKey] }),
  ])
}

// 公开子资源确认仓库已撤回时，同时关闭详情和列表中的旧数据。
export async function withdrawPublicRepo(queryClient: QueryClient, id: number) {
  const aliases: QueryFilters = { queryKey: ['repo'], predicate: (query) =>
    query.queryKey[2] === 'public' && isRepoDetail(query, id) }
  await queryClient.cancelQueries(aliases)
  queryClient.setQueriesData(aliases, null)
  await resetPublicRepoLists(queryClient)
}

export function refreshRepo(queryClient: QueryClient, id: number) {
  return Promise.all([
    // 不可见详情的空缓存也需重查，采样可能使仓库重新可见。
    queryClient.invalidateQueries({ queryKey: ['repo'], predicate: (query) => query.state.data === null || isRepoDetail(query, id) }),
    queryClient.invalidateQueries({ queryKey: ['repos'] }),
    queryClient.invalidateQueries({ queryKey: ['trending'] }),
    queryClient.invalidateQueries({ queryKey: [publishedFavoritesKey] }),
    queryClient.invalidateQueries({ queryKey: [dailyReportsKey] }),
    queryClient.invalidateQueries({ queryKey: ['categories'] }),
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
      if (blocked) await withdrawPublicRepo(client, id)
      await Promise.all([
        refreshRepo(client, id),
        client.invalidateQueries({ queryKey: ['readme', id] }),
        ...(!blocked ? [client.invalidateQueries({ queryKey: [similarReposKey] })] : []),
        client.invalidateQueries({ queryKey: ['trend', id] }),
        client.invalidateQueries({ queryKey: ['jobs'] }),
        client.invalidateQueries({ queryKey: ['limits'] }),
      ])
    },
  })
}
