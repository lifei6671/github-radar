import { expect } from '@playwright/test'
import type { Page, Route } from '@playwright/test'
import type { Job, Quality, RepoDetail, Topic } from '../src/api/types'

const at = '2026-11-11T12:00:00Z'
const admin = 'fixture-token'
const categories = [
  { slug: 'agent', name: 'AI 智能体', count: 102 },
  { slug: 'coding', name: 'AI 编程', count: 85 },
  { slug: 'mcp', name: 'MCP', count: 43 },
  { slug: 'media', name: 'AI 多媒体', count: 28 },
  { slug: 'tools', name: '开发者工具', count: 71 },
  { slug: 'other', name: '其他', count: 22 },
]
const names = ['vercel/ai', 'openai/codex', 'pydantic/pydantic-ai', 'modelcontextprotocol/servers', 'astral-sh/ruff', 'charmbracelet/gum']
const titles = ['用于构建 AI 应用的 TypeScript 工具包', '在终端中运行的开源代码智能体', '面向生产应用的 Python 智能体框架', 'Model Context Protocol 服务端实现集合', '以 Rust 编写的 Python 代码检查工具', '用于构建交互式 Shell 脚本的组件']
const stars = [22_846, 59_480, 14_270, 32_881, 40_244, 15_830]
const growth = [318, 212, 116, -12, null, 8]
const week = [1863, 1560, 832, 288, null, 63]
const quality = (value: number | null): Quality => value === null ? 'insufficient' : 'exact'

function repository(index: number): RepoDetail {
  const id = index + 1
  const hasSummary = index !== 4
  const window = (delta: number | null) => ({
    delta, quality: quality(delta), startAt: delta === null ? null : '2026-11-10T12:00:00Z',
    endAt: delta === null ? null : at, resolution: 'raw' as const,
  })
  const summary = '项目提供了统一的模型调用与流式输出接口，适用于需要把生成式 AI 集成到应用中的开发团队。文档包含基础示例，开发者可以用它构建聊天、工具调用和结构化输出流程。'
  return {
    id, fullName: names[index], htmlUrl: 'https://github.com/' + names[index], description: titles[index],
    titleZh: hasSummary ? titles[index] : null, language: ['TypeScript', 'Rust', 'Python', 'TypeScript', 'Rust', 'Go'][index],
    categories: [index === 3 ? 'mcp' : index === 5 ? 'tools' : index === 0 ? 'coding' : 'agent'],
    state: 'active', grade: index < 2 ? 'S' : 'A', stars: stars[index], forks: 472,
    growth1h: window(5), growth24h: window(growth[index]), growth7d: window(week[index]), growth30d: window(4100),
    relativeGrowth: index === 4 ? null : 0.085,
    hotnessScore: index === 4 ? null : 93.8 - index * 3.9, hotnessRank: index === 4 ? null : id,
    trendingEntries: index < 3 ? [{ sourceKey: 'all', period: 'daily', rank: id + 2, observedAt: at }] : [],
    sparkline: Array.from({ length: 24 }, (_, h) => ({
      at: new Date(Date.parse(at) + (h - 24) * 3_600_000).toISOString(),
      observedAt: h === 8 ? null : new Date(Date.parse(at) + (h - 24) * 3_600_000 + 900_000).toISOString(),
      stars: h === 8 || index === 4 ? null : stars[index] - 318 + h * 14 + [0, 3, -8, 8, 1, 17][h % 6],
    })),
    dataQuality: index === 4 ? 'insufficient' : 'exact', metricsUpdatedAt: at, candidateBurst: false,
    summaryPreview: hasSummary ? summary : null, summaryZh: hasSummary ? summary : null, summaryStatus: hasSummary ? 'ready' : 'missing',
    summarySourceQuality: hasSummary ? 'sufficient' : null, summaryGeneratedAt: hasSummary ? at : null,
    summaryModel: hasSummary ? 'fixture-model' : null, sourceReadmeUrl: hasSummary ? 'https://github.com/' + names[index] + '/blob/main/README.md' : null,
    summaryJobId: null, followed: index === 1, followedAt: index === 1 ? at : null,
    discoveredAt: index < 4 ? at : '2026-11-10T12:00:00Z', lastSuccessAt: index === 4 ? null : at,
    license: index === 1 ? { spdxId: 'NOASSERTION', name: null } : { spdxId: 'MIT', name: 'MIT License' },
    features: ['流式响应', '工具调用'], audience: ['开发者'], valueTags: ['开发工具'],
  }
}

function reply(route: Route, json: unknown, status = 200) {
  return route.fulfill({ status, json })
}

function barrier() {
  let resolve!: () => void
  const promise = new Promise<void>((release) => { resolve = release })
  return { promise, resolve }
}

export async function mockRadar(page: Page, startInitialized = true) {
  let initialized = startInitialized
  let authorized = true
  let revision = 1
  let conflict = false
  let delayedSave: { status: 200 | 401; started: () => void; gate: Promise<void> } | null = null
  let delayedFeedback: { started: () => void; gate: Promise<void> } | null = null
  let runtime = { ai: { enabled: true, baseUrl: '', model: 'fixture-model', summary: { workers: 1 } }, recommendations: { enabled: true, hour: 8, minute: 30 } }
  let topic: Topic = {
    id: 1, repoId: 1, fullName: names[0], titleZh: titles[0], readerValue: '工具调用与流式输出',
    sourceUrl: 'https://github.com/vercel/ai', summaryZh: repository(0).summaryZh, valueTags: ['开发工具'],
    reason: '24 小时精确净增长', topicScore: 81, hotnessScore: 93.8, recommendationScore: 86.1,
    status: 'candidate', feedback: null, reviewedAt: null, updatedAt: at,
  }
  let job: Job | null = null
  let jobPolls = 0
  const requests: string[] = []
  const attempts: unknown[] = []
  const repos = names.map((_, index) => repository(index))
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.slice('/api/v1'.length)
    const method = request.method()
    const query = url.searchParams
    requests.push(method + ' ' + path + url.search)
    if (!authorized || request.headers().authorization !== 'Bearer ' + admin) return reply(route, { code: 'unauthorized' }, 401)
    if (path === '/setup/status') return reply(route, { initialized })
    if (path === '/setup/initialize' && method === 'POST') {
      initialized = true
      return reply(route, { initialized })
    }
    if (!initialized) return reply(route, { code: 'setup_required' }, 503)
    if (path === '/categories') return reply(route, { items: categories })
    if (path === '/repos') {
      const categoriesWanted = query.getAll('category')
      const filtered = repos.filter((repo) =>
        (!query.get('q') || repo.fullName.includes(query.get('q')!)) &&
        (!categoriesWanted.length || repo.categories.some((category) => categoriesWanted.includes(category))) &&
        (!query.get('grade') || repo.grade === query.get('grade')) &&
        (!query.get('language') || repo.language === query.get('language')) &&
        (query.get('followed') !== 'true' || repo.followed))
      const limit = query.get('limit') === '6' ? 6 : 5
      const offset = query.get('cursor') === 'opaque+cursor=1' ? limit : 0
      return reply(route, { items: filtered.slice(offset, offset + limit), nextCursor: offset + limit < filtered.length ? 'opaque+cursor=1' : null, updatedAt: at })
    }
    if (path === '/trending') return reply(route, { sourceKey: 'all', period: 'daily', updatedAt: at, batch: { observedSlot: 1, observedAt: at }, items: repos.slice(0, 3) })
    const match = path.match(/^\/repos\/(\d+)(?:\/(.+))?$/)
    if (match) {
      const repo = repos[Number(match[1]) - 1]
      if (!repo) return reply(route, { code: 'not_found' }, 404)
      const action = match[2]
      if (!action) return reply(route, repo)
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
    if (path === '/topics') return reply(route, { items: !query.get('status') || query.get('status') === topic.status ? [topic] : [], nextCursor: null, updatedAt: at })
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
          return reply(route, { revision, settings: body.settings, restartRequired: true })
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
      return reply(route, { revision, settings: runtime, restartRequired: revision > 2 })
    }
    if (path === '/system/discovery-rules') return reply(route, { revision, items: [{ sourceType: 'trending', sourceKey: 'all:daily', query: '', enabled: true, intervalMinutes: 60, priority: 20 }] })
    if (path === '/system/categories') return reply(route, { revision, items: [{ slug: 'agent', name: 'AI 智能体', anyTopics: ['agent'] }] })
    if (path === '/system/jobs') return reply(route, { items: job ? [job] : [] })
    if (path === '/system/limits') return reply(route, {
      githubConfigured: true, waitingTaskCount: 2, items: [{ resource: 'core', limit: 5000, remaining: 4837, resetAt: at, pausedUntil: null, localRequests: 163 }],
      ai: { auto: { used: 9, limit: 50 }, manual: { used: 0, limit: 20 }, resetAt: at },
    })
    if (path === '/system/storage') return reply(route, {
      databaseBytes: 4_096_000, walBytes: 0, pageCount: 1000, freelistCount: 12,
      counts: { raw: 4000, hourly: 1200, daily: 52 }, watermarks: { hourly: at, daily: at, cleanedAt: at, metricsUpdatedAt: at },
    })
    if (path === '/system/maintenance/run') return reply(route, { status: 'pending' }, 202)
    return reply(route, { code: 'not_found' }, 404)
  })

  return {
    requests, attempts,
    expire: () => { authorized = false },
    conflict: () => { conflict = true },
    unknown: (id = 31) => {
      job = { id, type: 'refresh_readme_summary', status: 'unknown', attempts: 1, maxAttempts: 4, errorCode: 'ai_delivery_unknown',
        repoId: 1, createdAt: at, updatedAt: at }
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

export async function login(page: Page, token = admin) {
  await page.getByLabel('管理员令牌').fill(token)
  await page.getByRole('button', { name: '连接', exact: true }).click()
}

export async function session(page: Page) {
  await page.addInitScript((value) => sessionStorage.setItem('radar.adminToken', value), admin)
}
