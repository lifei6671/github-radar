import { expect } from '@playwright/test'
import type { Page, Route } from '@playwright/test'
import type { CategorySetting, DiscoveryRanking, DiscoverySelection, DiscoveryTab, Job, Limits, ListSettings, ProjectThemeGroup, ProjectThemeSetting, Quality, RepoDetail, RepoReadme, RuleSetting, Topic } from '../src/api/types'

const at = '2026-11-11T12:00:00Z'
const adminPassword = 'fixture-admin-password'
const adminCookie = 'radar_admin_session'
const signedSession = 'fixture-session'
const sessionCookie = adminCookie + '=' + signedSession
const categories = [
  { slug: 'agent', name: 'AI 智能体' },
  { slug: 'coding', name: 'AI 编程' },
  { slug: 'mcp', name: 'MCP' },
  { slug: 'media', name: 'AI 多媒体' },
  { slug: 'tools', name: '开发者工具' },
  { slug: 'other', name: '其他' },
]
const names = ['vercel/ai', 'openai/codex', 'pydantic/pydantic-ai', 'modelcontextprotocol/servers', 'astral-sh/ruff', 'charmbracelet/gum']
const fixtureTopics = ['llm', 'coding-agent', 'ai-agent', 'mcp-server', 'mcp-server', 'cli']
const titles = ['用于构建 AI 应用的 TypeScript 工具包', '在终端中运行的开源代码智能体', '面向生产应用的 Python 智能体框架', 'Model Context Protocol 服务端实现集合', '以 Rust 编写的 Python 代码检查工具', '用于构建交互式 Shell 脚本的组件']
const reasons = ['应用开发者可借鉴它的统一接口来复用工具调用流程。', '维护大型仓库的开发者可以参考它如何在终端里审阅和应用补丁。']
const stars = [22_846, 59_480, 14_270, 32_881, 40_244, 15_830]
const growth = [318, 212, 116, -12, null, 8]
const week = [1863, 1560, 832, 288, null, 63]
const quality = (value: number | null): Quality => value === null ? 'insufficient' : 'exact'

function repository(index: number): RepoDetail {
  const id = index + 1
  const window = (delta: number | null) => ({
    delta, quality: quality(delta), startAt: delta === null ? null : '2026-11-10T12:00:00Z',
    endAt: delta === null ? null : at, resolution: 'raw' as const,
  })
  const summary = '项目提供了统一的模型调用与流式输出接口，适用于需要把生成式 AI 集成到应用中的开发团队。文档包含基础示例，开发者可以用它构建聊天、工具调用和结构化输出流程。'
  return {
    id, fullName: names[index], htmlUrl: 'https://github.com/' + names[index], description: titles[index],
    titleZh: titles[index], language: ['TypeScript', 'Rust', 'Python', 'TypeScript', 'Rust', 'Go'][index],
    categories: index === 0 ? ['coding', 'agent'] : [index === 3 ? 'mcp' : index === 5 ? 'tools' : 'agent'],
    state: 'active', grade: index < 2 ? 'S' : 'A', stars: stars[index], forks: 472,
    growth1h: window(5), growth24h: window(growth[index]), growth7d: window(week[index]), growth30d: window(4100),
    relativeGrowth: index === 4 ? null : 0.085,
    hotnessScore: index === 4 ? null : 93.8 - index * 3.9, hotnessRank: index === 4 ? null : index === 5 ? 5 : id,
    trendingEntries: index < 3 ? [{ sourceKey: 'all', period: 'daily', rank: id + 2, observedAt: at }] : [],
    sparkline: Array.from({ length: 24 }, (_, h) => ({
      at: new Date(Date.parse(at) + (h - 24) * 3_600_000).toISOString(),
      observedAt: h === 8 ? null : new Date(Date.parse(at) + (h - 24) * 3_600_000 + 900_000).toISOString(),
      stars: h === 8 || index === 4 ? null : stars[index] - 318 + h * 14 + [0, 3, -8, 8, 1, 17][h % 6],
    })),
    dataQuality: index === 4 ? 'insufficient' : 'exact', metricsUpdatedAt: at, candidateBurst: false,
    recommendationReason: reasons[index] ?? null,
    selected: index !== 4,
    summaryPreview: summary, summaryZh: summary, summaryStatus: 'ready',
    summarySourceQuality: 'sufficient', summaryGeneratedAt: at,
    summaryModel: 'fixture-model', sourceReadmeUrl: 'https://github.com/' + names[index] + '/blob/main/README.md',
    readme: { state: 'pending', complete: false, truncated: false },
    summaryAvailable: true,
    summaryJobId: null, followed: index === 1, followedAt: index === 1 ? at : null,
    discoveredAt: index < 4 ? at : '2026-11-10T12:00:00Z', lastSuccessAt: at,
    license: index === 1 ? { spdxId: 'NOASSERTION', name: null } : { spdxId: 'MIT', name: 'MIT License' },
    features: ['流式响应', '工具调用'], audience: ['开发者'],
    valueTags: index === 0 ? ['开发工具', '流式响应', '工具调用', '模型接口'] : ['开发工具'], aiTopics: [],
  }
}

function reply(route: Route, json: unknown, status = 200, headers?: Record<string, string>) {
  return route.fulfill({ status, json, headers })
}

function published(repo: RepoDetail) {
  const item = { ...repo, summaryStatus: repo.summaryStatus === 'stale' ? 'stale' : repo.summaryZh ? 'ready' : 'missing' }
  delete item.followed
  delete item.followedAt
  delete item.summaryJobId
  delete item.summaryModel
  delete item.summaryAvailable
  delete item.discoveryEvents
  delete item.nameIsCurrent
  delete item.blocked
  return item
}

function canPublish(repo: RepoDetail) {
  return !repo.blocked && repo.nameIsCurrent !== false && ['active', 'candidate', 'paused'].includes(repo.state) &&
    repo.lastSuccessAt !== null && Boolean(repo.summaryZh?.trim()) && repo.summaryStatus !== 'stale'
}

export function barrier() {
  let resolve!: () => void
  const promise = new Promise<void>((release) => { resolve = release })
  return { promise, resolve }
}

export async function mockRadar(page: Page, startInitialized = true, withTopicArchive = false) {
  let initialized = startInitialized
  let authorized = true
  let revision = 1
  let conflict = false
  let systemLimits: Limits = {
    githubConfigured: true, waitingTaskCount: 2, items: [{ resource: 'core', limit: 5000, remaining: 4837, resetAt: at, pausedUntil: null, localRequests: 163 }],
    ai: { auto: { used: 9, limit: 50 }, manual: { used: 0, limit: 20 }, resetAt: at },
  }
  let delayedSave: { status: 200 | 401; started: () => void; gate: Promise<void> } | null = null
  let delayedFeedback: { started: () => void; gate: Promise<void> } | null = null
  let runtime = { github: { tokenEnv: 'GITHUB_TOKEN', searchSoftBudgetPerMinute: 20 }, ai: { enabled: true, baseUrl: '', model: 'fixture-model', apiKeyEnv: 'AI_API_KEY', summary: { workers: 1 } }, recommendations: { enabled: true, hour: 8, minute: 30 },
    reportPublication: { weeklyMinDays: 5, monthlyMinCoverage: 75 } }
  let ruleSettings: ListSettings<RuleSetting> = { revision: 1, items: [
    { sourceType: 'trending', sourceKey: 'all:daily', query: '', enabled: true, intervalMinutes: 60, priority: 20 },
    { sourceType: 'search', sourceKey: 'coding', query: 'topic:code-assistant stars:>50', minStars: 200, enabled: false, intervalMinutes: 30, priority: 20 },
  ] }
  let categorySettings: ListSettings<CategorySetting> = { revision: 1, items: [{ slug: 'agent', name: 'AI 智能体', anyTopics: ['agent'] }] }
  const discoveryOptions = [...categories]
  let discoveryTabs = categories.map(({ slug }) => ({ slug, label: '' }))
  let themeGroups: ProjectThemeGroup[] = [
    { slug: 'technology', name: '技术方向', description: '按技术领域追踪开源项目。' },
    { slug: 'content', name: '内容方向', description: '按用途浏览工具和工作流。' },
  ]
  let projectThemes: ProjectThemeSetting[] = [
    { slug: 'terminal-tools', groupSlug: 'content', name: '终端开发工具', description: '在终端使用的开发工具。', icon: 'terminal', iconColor: 'cyan', anyTopics: ['cli'] },
    { slug: 'browser-automation', groupSlug: 'technology', name: '浏览器自动化', description: '自动操作浏览器。', icon: 'globe', iconColor: 'green', anyTopics: ['browser-automation'] },
    { slug: 'mcp-servers', groupSlug: 'technology', name: 'MCP 服务端', description: '为模型提供工具和数据。', icon: 'plug', iconColor: 'cyan', anyTopics: ['mcp-server'] },
    { slug: 'coding-agents', groupSlug: 'technology', name: '代码智能体', description: '辅助编写代码。', icon: 'bot', iconColor: 'amber', anyTopics: ['coding-agent'] },
    { slug: 'llm-tooling', groupSlug: 'technology', name: '大模型工具链', description: '面向模型应用的工具。', icon: 'layers', iconColor: 'slate', anyTopics: ['llm'] },
    { slug: 'webhook-tools', groupSlug: 'content', name: 'Webhook 与事件集成', description: '处理 Webhook 事件。', icon: '', iconColor: '', anyTopics: ['webhook'] },
  ]
  let selection: DiscoverySelection = { recentDays: 7, recentSampleHours: 48, growthSampleHours: 24, trendingHours: 24, trendingTop: 10 }
  let ranking: DiscoveryRanking = { heatWeight: 70, freshnessWeight: 30, consecutiveDays: 3, cooldownDays: 2 }
  let moveDiscoveryAcrossCursor = false
  let topic: Topic = {
    id: 1, repoId: 1, fullName: names[0], titleZh: titles[0], readerValue: '工具调用与流式输出',
    sourceUrl: 'https://github.com/vercel/ai', summaryZh: repository(0).summaryZh, valueTags: ['开发工具'],
    reason: '24 小时精确净增长', topicScore: 81, hotnessScore: 93.8, recommendationScore: 86.1,
    status: 'candidate', feedback: null, reviewedAt: null, createdAt: at, updatedAt: at,
  }
  const archivedTopics: Topic[] = withTopicArchive ? [
    { ...topic, id: 2, repoId: 2, titleZh: titles[1], fullName: names[1], status: 'accepted', createdAt: '2026-11-11T11:00:00Z' },
    { ...topic, id: 3, repoId: 3, titleZh: titles[2], fullName: names[2], createdAt: '2026-10-31T12:00:00Z' },
  ] : []
  let job: Job | null = null
  let recentJobs: Job[] = []
  let jobPolls = 0
  const readmes = new Map<number, RepoReadme>()
  const detail = (repo: RepoDetail, publicRead: boolean) => ({
    ...(publicRead ? published(repo) : repo), readme: readmes.get(repo.id) ?? repo.readme,
  })
  const requests: string[] = []
  const attempts: unknown[] = []
  const repos = names.map((_, index) => repository(index))
  const similarRepos = new Map<number, number[]>()
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const relative = url.pathname.slice('/api/v1'.length)
    const publicRead = relative.startsWith('/public/')
    const path = publicRead ? relative.slice('/public'.length) : relative
    const method = request.method()
    const query = url.searchParams
    requests.push(method + ' ' + path + url.search)
    const headers = await request.allHeaders()
    const signedIn = authorized && Boolean(headers.cookie?.split('; ').includes(sessionCookie))
    if (publicRead) {
      expect(headers.authorization).toBeUndefined()
      expect(headers.cookie).toBeUndefined()
      if (method !== 'GET' || !/^\/(?:categories|discovery|discovery-tabs|project-themes(?:\/[^/]+)?|trending(?:\/sources)?|repos(?:\/published|\/\d+\/(?:trend|readme|similar)|\/by-name\/[^/]+\/[^/]+)?)$/.test(path)) {
        return reply(route, { code: 'not_found' }, 404)
      }
      if (query.get('followed') === 'true') return reply(route, { code: 'invalid_filter' }, 400)
    } else {
      if (path === '/auth/session') return reply(route, { authenticated: signedIn })
      if (path === '/auth/login' && method === 'POST') {
        if (request.postDataJSON().password !== adminPassword) return reply(route, { code: 'invalid_admin_password' }, 401)
        authorized = true
        return reply(route, { authenticated: true }, 200, { 'Set-Cookie': sessionCookie + '; Path=/api/v1; HttpOnly; SameSite=Strict' })
      }
      if (path === '/auth/logout' && method === 'POST') {
        return reply(route, { authenticated: false }, 200, { 'Set-Cookie': adminCookie + '=; Path=/api/v1; Max-Age=0; HttpOnly' })
      }
      if (!signedIn) return reply(route, { code: 'unauthorized' }, 401)
    }
    if (path === '/setup/status') return reply(route, { initialized })
    if (path === '/system/backup/status') return reply(route, { initialized, restoring: false, maintenance: false })
    if (path === '/setup/initialize' && method === 'POST') {
      initialized = true
      return reply(route, { initialized })
    }
    if (!initialized) return reply(route, { code: 'setup_required' }, 503)
    if (path === '/discovery-tabs') return reply(route, { items: discoveryTabs.map(({ slug, label }) => ({
      slug, name: label || discoveryOptions.find((option) => option.slug === slug)?.name,
    })) })
    if (path === '/categories') return reply(route, { items: categories.map((category) => {
      const matches = repos.filter((repo) => (!publicRead || canPublish(repo)) && repo.categories.includes(category.slug))
      const latest = matches[0]
      const latestRepo = latest ? { id: latest.id, fullName: latest.fullName, description: latest.description, discoveredAt: latest.discoveredAt } : null
      return { ...category, count: matches.length, latestRepo }
    }) })
    if (publicRead && (path === '/project-themes' || path.startsWith('/project-themes/'))) {
      const items = projectThemes.map(({ slug, name, description, anyTopics, owners, icon, iconColor, groupSlug }) => {
        const matches = repos.filter((repo) => canPublish(repo) && repo.state === 'active' &&
          (owners?.length ? owners.includes(repo.fullName.split('/')[0].toLowerCase()) : anyTopics.includes(fixtureTopics[repo.id - 1])))
        const latest = matches[0]
        return { slug, name, description, icon, iconColor, groupSlug, count: matches.length, recentCount: matches.length,
          latestRepo: latest ? { id: latest.id, fullName: latest.fullName, discoveredAt: latest.discoveredAt } : null }
      })
      if (path === '/project-themes') return reply(route, { items, groups: themeGroups })
      const theme = items.find(({ slug }) => path === '/project-themes/' + encodeURIComponent(slug))
      const milestones = theme?.slug === 'mcp-servers' ? Array.from({ length: 6 }, (_, index) => ({
        kind: index === 5 ? 'trending' : 'stars', fullName: names[3], value: index === 5 ? 2 : 1_000 * (5 - index),
        at: new Date(Date.parse(at) - index * 7 * 86_400_000).toISOString(),
      })) : []
      return theme ? reply(route, { ...theme, groupName: themeGroups.find(({ slug }) => slug === theme.groupSlug)?.name, milestones }) :
        reply(route, { code: 'not_found' }, 404)
    }
    if (path === '/repos' || path === '/discovery') {
      const categoriesWanted = query.getAll('category')
      const theme = projectThemes.find(({ slug }) => slug === query.get('theme'))
      if (query.has('theme') && !theme) return reply(route, { code: 'not_found' }, 404)
      const filtered = repos.filter((repo) =>
        (!publicRead || canPublish(repo)) &&
        (!theme || (theme.owners?.length ? theme.owners.includes(repo.fullName.split('/')[0].toLowerCase()) :
          theme.anyTopics.includes(fixtureTopics[repo.id - 1]))) &&
        (path !== '/discovery' || (repo.id !== 5 && repo.state === 'active')) &&
        (path !== '/discovery' || query.get('sort') !== 'hotness' || repo.hotnessScore !== null) &&
        (!query.has('blocked') || Boolean(repo.blocked) === (query.get('blocked') === 'true')) &&
        (!query.get('q') || repo.fullName.includes(query.get('q')!)) &&
        (!categoriesWanted.length || repo.categories.some((category) => categoriesWanted.includes(category))) &&
        (!query.get('grade') || repo.grade === query.get('grade')) &&
        (!query.has('tag') || repo.valueTags.includes(query.get('tag')!)) &&
        (!query.get('state') || repo.state === query.get('state')) &&
        (!query.get('language') || repo.language === query.get('language')) &&
        (query.get('followed') !== 'true' || repo.followed))
      if (query.get('sort') === 'hotness') filtered.sort((a, b) => (a.hotnessRank ?? Infinity) - (b.hotnessRank ?? Infinity))
      const movingDiscovery = moveDiscoveryAcrossCursor && path === '/discovery' && !query.has('sort')
      const limit = movingDiscovery ? 4 : query.get('limit') === '6' ? 6 : 5
      const offset = query.get('cursor') === 'opaque+cursor=1' ? limit : 0
      const slice = filtered.slice(offset, offset + limit)
      const page = movingDiscovery && offset > 0 && filtered.length > 0 ? [filtered[0], ...slice] : slice
      const items = path === '/discovery' ? page.map((repo) => ({ ...repo, discoveryAt: at })) : page
      return reply(route, { items: publicRead ? items.map(published) : items, nextCursor: offset + limit < filtered.length ? 'opaque+cursor=1' : null, updatedAt: at })
    }
    if (path === '/repos/published') {
      const ids = query.getAll('id').map(Number)
      return reply(route, { items: repos.filter((repo) => canPublish(repo) && ids.includes(repo.id)).map(({ id }) => id) })
    }
    if (path === '/trending/sources') return reply(route, {
      items: ['all:daily', 'all:weekly', 'all:monthly', 'language:rust:weekly'].map((sourceKey) => ({ sourceKey })),
    })
    if (path === '/trending') {
      const sourceKey = query.get('sourceKey') ?? 'all'
      const period = query.get('period') ?? 'daily'
      const items = sourceKey === 'language:rust' && period === 'weekly'
        ? repos.slice(0, 5).map((repo, index) => ({
          ...repo, language: index === 4 ? 'Jupyter Notebook' : 'Rust', stars: index === 3 ? 780 : repo.stars,
          trendingEntries: [{ sourceKey, period: 'weekly' as const, rank: index + 1, observedAt: at }],
        }))
        : repos.slice(0, 3)
      return reply(route, { sourceKey, period, updatedAt: at,
        batch: { observedSlot: 1, observedAt: at }, items: publicRead ? items.filter(canPublish).map(published) : items })
    }
    if (path.startsWith('/repos/by-name/')) {
      const name = decodeURIComponent(path.slice('/repos/by-name/'.length)).toLowerCase()
      const expected = publicRead ? query.get('repoId') : null
      const repo = repos.find((item) => item.nameIsCurrent !== false && (!publicRead || canPublish(item)) &&
        (expected ? item.id === Number(expected) : item.fullName.toLowerCase() === name))
      return repo ? reply(route, detail(repo, publicRead)) : reply(route, { code: 'not_found' }, 404)
    }
    const match = path.match(/^\/repos\/(\d+)(?:\/(.+))?$/)
    if (match) {
      const repo = repos[Number(match[1]) - 1]
      if (!repo || (publicRead && !canPublish(repo))) return reply(route, { code: 'not_found' }, 404)
      const action = match[2]
      if (action === 'block' && method === 'PATCH') {
        repo.blocked = request.postDataJSON().blocked
        repo.state = repo.blocked ? 'paused' : 'active'
        return reply(route, { blocked: repo.blocked })
      }
      if (!action) return reply(route, detail(repo, publicRead))
      if (action === 'readme') return reply(route, readmes.get(repo.id) ?? repo.readme)
      if (action === 'similar' && publicRead) {
        const ids = similarRepos.get(repo.id) ?? []
        const items = ids.map((id) => repos[id - 1]).filter(canPublish)
          .map(({ id, fullName, description, language, stars }) => ({ id, fullName, description, language, stars }))
        return reply(route, { items })
      }
      if (action === 'trend') {
        const samples = query.get('range') === '180d'
          ? Array.from({ length: 180 }, (_, index) => ({
            at: new Date(Date.parse(at) - (179 - index) * 86_400_000).toISOString(),
            stars: [25, 160].includes(index) ? repo.stars + index : null,
          })) : repo.sparkline
        return reply(route, {
          points: samples.map(({ at: time, stars: count }) => ({
          at: time, stars: count, delta: count === null ? null : 14, quality: quality(count), startAt: null, endAt: time,
          })),
          resolution: query.get('range') === '30d' ? 'hourly' : query.get('range') === '180d' ? 'daily' : 'raw', updatedAt: at,
        })
      }
      if (action === 'follow') {
        repo.followed = request.postDataJSON().follow
        repo.followedAt = repo.followed ? at : null
        return reply(route, { followed: repo.followed })
      }
      if (action === 'topic') return reply(route, topic)
      if (action === 'summary' || action === 'refresh') {
        attempts.push(request.postDataJSON())
        if (action === 'summary' && !request.postDataJSON().force && repo.summaryZh) return reply(route, { cached: true, summaryStatus: 'ready' })
        jobPolls = 0
        job = { id: 31, type: action === 'summary' ? 'refresh_readme_summary' : 'refresh_repo', status: 'pending',
          attempts: 0, maxAttempts: 4, errorCode: null, repoId: repo.id, createdAt: at, updatedAt: at }
        return reply(route, { jobId: job.id, statusUrl: '/api/v1/jobs/' + job.id }, 202)
      }
    }
    if (job && path === '/jobs/' + job.id) {
      jobPolls++
      if (job.status !== 'unknown' && jobPolls > 1) job.status = 'succeeded'
      return reply(route, job)
    }
    if (job?.status === 'unknown' && path === '/jobs/' + job.id + '/retry') {
      attempts.push(request.postDataJSON())
      job.status = 'running'
      jobPolls = 0
      return reply(route, { jobId: job.id, statusUrl: '/api/v1/jobs/' + job.id }, 202)
    }
    if (path === '/topics/days' || path === '/topics') {
      const matching = [topic, ...archivedTopics].filter((item) => !query.get('status') || query.get('status') === item.status)
      if (path === '/topics/days') {
        const days = new Map<string, { day: string; title: string; count: number }>()
        for (const item of matching) {
          const day = item.createdAt.slice(0, 10)
          const entry = days.get(day)
          if (entry) entry.count++
          else days.set(day, { day, title: item.titleZh, count: 1 })
        }
        return reply(route, { items: [...days.values()] })
      }
      return reply(route, {
        items: matching.filter((item) => !query.get('day') || item.createdAt.startsWith(query.get('day')!)),
        nextCursor: null, updatedAt: at,
      })
    }
    if (path === '/topics/1' && method === 'PATCH') {
      if (delayedFeedback) {
        const pending = delayedFeedback
        delayedFeedback = null
        pending.started()
        await pending.gate
      }
      topic = { ...topic, ...request.postDataJSON(), reviewedAt: at }
      return reply(route, topic)
    }
    if (path === '/system/settings') {
      if (method === 'PUT') {
        const body = request.postDataJSON()
        expect(body).not.toHaveProperty('restartRequired')
        if (delayedSave) {
          const pending = delayedSave
          delayedSave = null
          pending.started()
          await pending.gate
          if (pending.status === 401) return reply(route, { code: 'unauthorized' }, 401)
          return reply(route, { revision, settings: body.settings, restartRequired: false })
        }
        if (conflict) {
          conflict = false
          revision++
          runtime = { ...runtime, ai: { ...runtime.ai, model: 'server-new' } }
        }
        if (body.revision !== revision) return reply(route, { code: 'settings_conflict' }, 409)
        runtime = body.settings
        revision++
      }
      return reply(route, { revision, settings: runtime, restartRequired: false })
    }
    if (path === '/system/discovery-rules') {
      if (method === 'PUT') {
        const body: ListSettings<RuleSetting> = request.postDataJSON()
        if (body.revision !== ruleSettings.revision) return reply(route, { code: 'settings_conflict' }, 409)
        ruleSettings = { items: body.items, revision: body.revision + 1 }
      }
      return reply(route, ruleSettings)
    }
    if (path === '/system/categories') {
      if (method === 'PUT') {
        const body: ListSettings<CategorySetting> = request.postDataJSON()
        if (body.revision !== categorySettings.revision) return reply(route, { code: 'settings_conflict' }, 409)
        categorySettings = { items: body.items, revision: body.revision + 1 }
      }
      return reply(route, categorySettings)
    }
    if (path === '/system/discovery-tabs') {
      if (method === 'PUT') {
        const body: ListSettings<DiscoveryTab> & { newCategories?: CategorySetting[]; selection?: DiscoverySelection; ranking?: DiscoveryRanking } = request.postDataJSON()
        if (body.revision !== revision) return reply(route, { code: 'settings_conflict' }, 409)
        discoveryTabs = body.items
        selection = body.selection ?? selection
        ranking = body.ranking ?? ranking
        for (const category of body.newCategories ?? []) {
          discoveryOptions.push({ slug: category.slug, name: category.name })
          categorySettings.items.push(category)
        }
        revision++
      }
      return reply(route, { revision, items: discoveryTabs, options: discoveryOptions, selection, ranking })
    }
    if (path === '/system/project-theme-groups') {
      if (method === 'PUT') {
        const body: ListSettings<ProjectThemeGroup> = request.postDataJSON()
        if (body.revision !== revision) return reply(route, { code: 'settings_conflict' }, 409)
        themeGroups = body.items
        revision++
      }
      return reply(route, { revision, items: themeGroups })
    }
    if (path === '/system/project-themes') {
      if (method === 'PUT') {
        const body: ListSettings<ProjectThemeSetting> = request.postDataJSON()
        if (body.revision !== revision) return reply(route, { code: 'settings_conflict' }, 409)
        projectThemes = body.items
        revision++
      }
      return reply(route, { revision, items: projectThemes })
    }
    if (path === '/system/jobs') return reply(route, { items: (job ? [job, ...recentJobs] : recentJobs.slice())
      .sort((a, b) => b.id - a.id).map((item) => ({ ...item, repoFullName: repos.find(({ id }) => id === item.repoId)?.fullName ?? null })) })
    if (path === '/system/limits') return reply(route, systemLimits)
    if (path === '/system/storage') return reply(route, {
      databaseBytes: 4_096_000, walBytes: 0, pageCount: 1000, freelistCount: 12,
      counts: { raw: 4000, hourly: 1200, daily: 52 }, watermarks: { hourly: at, daily: at, cleanedAt: at, metricsUpdatedAt: at },
    })
    if (path === '/system/maintenance/run') return reply(route, { status: 'pending' }, 202)
    return reply(route, { code: 'not_found' }, 404)
  })

  return {
    requests, attempts,
    moveDiscoveryAcrossCursor: () => { moveDiscoveryAcrossCursor = true },
    setLimits: (values: Partial<Limits>) => { systemLimits = { ...systemLimits, ...values } },
    setRecentJobs: (items: Job[]) => { recentJobs = items },
    completeSetup: () => { initialized = true },
    renameFirstRepo: (fullName: string) => { repos[0].fullName = fullName },
    setRepoName: (id: number, fullName: string, nameIsCurrent = true) => Object.assign(repos[id - 1], { fullName, nameIsCurrent }),
    setRepoState: (id: number, state: RepoDetail['state']) => { repos[id - 1].state = state },
    setSparkline: (id: number, observed: number[]) => {
      const repo = repos[id - 1]
      repo.sparkline = repo.sparkline.map((point, index) => ({
        ...point, stars: observed.includes(index) ? repo.stars + index : null, observedAt: observed.includes(index) ? point.at : null,
      }))
    },
    setSummaryAvailable: (id: number, available: boolean) => { repos[id - 1].summaryAvailable = available },
    setSummaryTitle: (id: number, title: string | null) => { repos[id - 1].titleZh = title },
    setSummary: (id: number, summary: string) => { Object.assign(repos[id - 1], { summaryZh: summary, summaryPreview: summary }) },
    setSummaryMissing: (id: number) => { Object.assign(repos[id - 1], { summaryZh: null, summaryPreview: null, titleZh: null, summaryStatus: 'missing' }) },
    restoreSummary: (id: number) => {
      const { summaryZh, summaryPreview, titleZh, summaryStatus } = repository(id - 1)
      Object.assign(repos[id - 1], { summaryZh, summaryPreview, titleZh, summaryStatus })
    },
    setAITopics: (id: number, topics: string[]) => { repos[id - 1].aiTopics = topics },
    setDiscoveryEvents: (id: number, events: RepoDetail['discoveryEvents']) => { repos[id - 1].discoveryEvents = events },
    setSimilarRepos: (id: number, items: number[]) => { similarRepos.set(id, items) },
    setReadme: (id: number, readme: RepoReadme) => { readmes.set(id, readme) },
    expire: () => { authorized = false },
    conflict: () => { conflict = true },
    updateRule: (key: string, change: Partial<RuleSetting>) => {
      ruleSettings = { revision: ruleSettings.revision + 1, items: ruleSettings.items.map((item) => item.sourceKey === key ? { ...item, ...change } : item) }
    },
    unknown: (id = 31, updatedAt = at) => {
      job = { id, type: 'refresh_readme_summary', status: 'unknown', attempts: 1, maxAttempts: 4, errorCode: 'ai_delivery_unknown',
        repoId: 1, createdAt: at, updatedAt }
      repos[0].summaryJobId = id
      repos[0].summaryStatus = 'unknown'
    },
    delaySave: (status: 200 | 401) => {
      const gate = barrier()
      const started = barrier()
      delayedSave = { status, gate: gate.promise, started: started.resolve }
      return { started: started.promise, release: gate.resolve }
    },
    delayFeedback: () => {
      const gate = barrier()
      const started = barrier()
      delayedFeedback = { gate: gate.promise, started: started.resolve }
      return { started: started.promise, release: gate.resolve }
    },
  }
}

export async function login(page: Page, password = adminPassword) {
  await page.getByLabel('管理员密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
}

export async function session(page: Page) {
  const origin = new URL(process.env.RADAR_TEST_ORIGIN ?? 'http://127.0.0.1:4173')
  await page.context().addCookies([{
    name: adminCookie, value: signedSession, domain: origin.hostname, path: '/api/v1',
    httpOnly: true, sameSite: 'Strict', secure: origin.protocol === 'https:',
  }])
}

// 按拖动手柄实际移动鼠标，并验证松手前的拖影和吸附目标。
export async function dragSetting(page: Page, from: string, to: string) {
  const source = page.getByRole('button', { name: '拖动 ' + from, exact: true })
  const target = page.getByRole('button', { name: '拖动 ' + to, exact: true })
  await expect(source).toBeEnabled()
  await expect(target).toBeEnabled()
  const start = (await source.boundingBox())!
  const end = (await target.boundingBox())!
  const x = start.x + start.width / 2
  await page.mouse.move(x, start.y + start.height / 2)
  await page.mouse.down()
  await page.mouse.move(x, end.y + end.height / 2, { steps: 12 })
  await expect(page.locator('.settings-drag-preview')).toHaveText(from)
  await expect(page.locator('[data-drop-target="true"]')).toHaveCount(1)
  await page.mouse.up()
  await page.mouse.move(x + 80, end.y - 12, { steps: 8 })
  await expect(page.locator('.settings-drag-preview')).toHaveCount(0)
}
