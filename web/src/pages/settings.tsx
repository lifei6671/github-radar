import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { arrayMove } from '@dnd-kit/sortable'
import { ArrowLeft, Pencil, Plus, Trash2, X } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, ApiError, errorText } from '../api/client'
import { resetPublicRepoLists } from '../api/queries'
import type { CategorySetting, DiscoveryRanking, DiscoveryTabSettings, ListSettings, ProjectThemeGroup, ProjectThemeSetting, RuleSetting, RuntimeSettings, SettingFields, SettingValue } from '../api/types'
import { AdminMetrics, AdminPanel } from '../components/admin'
import { ThemeIcon } from '../components/theme-icon'
import { ThemeIconPicker } from '../components/theme-icon-picker'
import { SortableSettings, SortableSettingsRow } from '../components/sortable-settings'
import { ActionButton, Badge, Dialog, Notice, PageError, Skeleton, Spinner } from '../components/ui'
import { DiscoveryRunHistory } from '../components/discovery-run-history'
import { number, topicPath } from '../lib/format'
import { runtimeHelp } from './runtime-help'

type Snapshot = { revision: number }
const configScopes = {
  '/system/settings': 'runtime',
  '/system/categories': 'categories',
  '/system/discovery-rules': 'rules',
  '/system/discovery-tabs': 'discovery-tabs',
  '/system/project-themes': 'project-themes',
  '/system/project-theme-groups': 'project-theme-groups',
} as const
type ConfigPath = keyof typeof configScopes

function useConfig<T extends Snapshot>(path: ConfigPath) {
  return useQuery({ queryKey: ['settings', configScopes[path]], queryFn: ({ signal }) => api<T>(path, { signal }), staleTime: 0 })
}

function refreshSettings(queryClient: ReturnType<typeof useQueryClient>, path: ConfigPath) {
  if (path === '/system/settings') {
    for (const key of ['limits', 'repo', 'readme', 'similar-repos', 'topics', 'backup-status']) {
      void queryClient.invalidateQueries({ queryKey: [key] })
    }
  }
  if (path === '/system/discovery-rules') {
    // Search 门槛变化会同时影响已收录项目的公开列表与详情。
    void resetPublicRepoLists(queryClient)
    void queryClient.resetQueries({ queryKey: ['repo'], predicate: ({ queryKey }) => queryKey[2] === 'public' })
    void queryClient.invalidateQueries({ queryKey: ['discovery-runs'] })
  }
  if (path === '/system/project-themes' || path === '/system/project-theme-groups') {
    // 编辑或删除专题后，重新读取公开目录和受该专题限制的项目列表。
    void queryClient.resetQueries({ queryKey: ['project-themes'] })
    void queryClient.resetQueries({ queryKey: ['repos'], predicate: ({ queryKey: [, source] }) =>
      typeof source === 'string' && /(?:^|[?&])theme=/.test(source) })
  }
  for (const key of ['settings', 'categories', 'discovery-tabs', 'repos', 'trending']) {
    void queryClient.invalidateQueries({ queryKey: [key] })
  }
}

function ConfigEditor<T extends Snapshot, Draft>({ path, query, heading, fields, apply, payload, status, navigation, panelTitle = '设置', onSaved }: {
  path: ConfigPath
  query: ReturnType<typeof useConfig<T>>
  heading?: (server?: T) => ReactNode
  fields: (server: T, draft: Draft | undefined, edit: (value: Draft) => void) => ReactNode
  apply: (server: T, draft: Draft) => T | null
  payload: (value: T, draft?: Draft) => unknown
  status?: (server: T) => ReactNode
  navigation?: ReactNode
  panelTitle?: string
  onSaved?: () => void
}) {
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<{ revision: number; value: Draft } | null>(null)
  const [saved, setSaved] = useState(false)
  const queryKey = ['settings', configScopes[path]]
  const save = useMutation({
    mutationFn: ({ value, draft }: { value: T; draft: Draft }) => api<T>(path, { method: 'PUT', body: payload(value, draft) }),
    onSuccess: (value) => {
      queryClient.setQueryData(queryKey, value)
      setDraft(null)
      setSaved(true)
      if (path === '/system/discovery-tabs') {
        // 发现和全部项目共用精选门槛，避免返回时显示旧标记。
        void queryClient.resetQueries({ queryKey: ['repos'], predicate: ({ queryKey: [, source, scope] }) =>
          source === 'discover-hot' || scope === 'public' })
      }
      refreshSettings(queryClient, path)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'settings_conflict') void query.refetch()
    },
  })
  if (!query.data) return <div className="admin-stack">
    {heading?.()}
    {query.isPending ? <Skeleton /> : <PageError error={query.error} retry={() => { void query.refetch() }} />}
  </div>
  const server = query.data
  const outdated = draft !== null && draft.revision !== server.revision
  const proposed = draft ? apply(server, draft.value) : null
  function edit(value: Draft) {
    setDraft((previous) => ({ revision: previous?.revision ?? server.revision, value }))
    setSaved(false)
  }
  function reset() {
    setDraft(null)
    save.reset()
  }

  const side = status?.(server)
  const form = <form className={navigation ? 'config-form' : 'admin-panel-body config-form'} onSubmit={(event) => {
    event.preventDefault()
    if (proposed && draft && !outdated && !save.isPending) save.mutate({ value: proposed, draft: draft.value }, { onSuccess: onSaved })
  }}>
    <fieldset className="config-editable" disabled={save.isPending}>{fields(server, draft?.value, edit)}</fieldset>
    {query.error && <Notice error>{errorText(query.error)}</Notice>}
    {save.error && !outdated && <Notice error>{errorText(save.error)}</Notice>}
    {outdated && <div className="panel config-conflict">
      <Notice error>配置已被其他操作更新。当前表单修改已保留。</Notice>
      <details>
        <summary>核对当前配置</summary>
        <pre>{JSON.stringify(payload(server), (key, value) => key === 'revision' ? undefined : value, 2)}</pre>
      </details>
      <div className="action-row">
        <button className="button" type="button" disabled={save.isPending} onClick={reset}>载入当前配置</button>
        <button className="button" type="button" disabled={save.isPending || !proposed} onClick={() => {
          setDraft((previous) => previous && { ...previous, revision: server.revision })
          save.reset()
        }}>采用当前表单</button>
      </div>
    </div>}
    <div className="config-footer">
      {saved && <span role="status" className="tone-ok">已保存</span>}
      <button type="button" className="button" disabled={!draft || save.isPending} onClick={reset}>还原</button>
      <ActionButton type="submit" className="button primary" disabled={!proposed || outdated} pending={save.isPending}>保存配置</ActionButton>
    </div>
  </form>
  return <div className="admin-stack">
    {heading?.(server)}
    <div className={navigation ? 'runtime-layout' : side ? 'admin-form-grid' : 'config-panel'}>
      {navigation}
      {navigation ? <div className="runtime-content">{form}</div> : <AdminPanel title={panelTitle}>{form}</AdminPanel>}
      {side && <AdminPanel title="状态"><div className="admin-panel-body">{side}</div></AdminPanel>}
    </div>
  </div>
}

const settingsNames: Record<string, string> = {
  server: 'HTTP 服务', storage: 'SQLite', github: 'GitHub 采集', scheduler: '调度',
  ai: 'AI 中文摘要', embedding: '相似项目', metrics: '增长与热度', maintenance: '数据保留', recommendations: '每日选题',
  reportPublication: '报刊出刊', weeklyMinDays: '周报最少观测天数', monthlyMinCoverage: '月报最低覆盖率（%）',
  summary: '模型调用与来源检查', baseUrl: 'OpenAI 兼容 API Base URL', model: '模型', apiKeyEnv: '密钥环境变量名',
  tokenEnv: '访问令牌环境变量名', enabled: '启用', timeoutSeconds: '超时（秒）',
  batchSize: '每轮处理仓库数', intervalMinutes: '扫描间隔（分钟）', minSimilarity: '最低相似度',
  workerConcurrency: '采集并发', workers: '摘要并发', apiVersion: 'GitHub API 版本',
  tickMinutes: '扫描周期（分钟）', metricsIntervalMinutes: '指标计算周期（分钟）',
  maxMonitoredRepos: '最大监控项目数', maxHot: 'S 级上限', maxActive: 'A 级上限', maxNormal: 'B 级上限',
  observationPool: '启用观察池', maxCooling: 'C 级上限',
  intervalsMinutes: '分级采样间隔（分钟）', leaseSeconds: '任务租约（秒）', autoDailyCalls: '每日自动调用上限',
  manualDailyCalls: '每日手动调用上限', readmeMaxBytes: 'README 读取上限（字节）', inputTokens: '输入 Token 预算',
  outputTokens: '输出 Token 预算', githubBudgetPerHour: 'README 每小时 GitHub 请求上限',
  promptVersion: '提示词版本', hotRankLimit: '自动摘要的热度排名门槛', maxAttempts: '最多尝试次数',
  repairAttempts: '结构修复次数', highCheckHours: '高优先级复查（小时）', normalCheckHours: 'B 级复查（小时）',
  lowCheckHours: 'C 级复查（小时）', inputCostPerMillion: '每百万输入 Token 价格', outputCostPerMillion: '每百万输出 Token 价格',
  hour: '小时', minute: '分钟', timezone: '时区', limit: '每轮数量', reentryGrowth: '净增重入门槛',
  retentionDays: '普通选题保留天数', rawDays: '原始快照保留天数', hourlyDays: '小时聚合保留天数', dailyDays: '日聚合保留天数',
  discoveryDays: '发现记录保留天数', finishedJobDays: '完成任务保留天数', deadJobDays: '死信任务保留天数',
  candidateDays: '未监控候选保留天数', cleanupHour: '清理时刻（UTC 小时）', dailyRollupHour: '日聚合时刻（UTC 小时）',
  batchRows: '维护批次行数', readTimeoutSeconds: '读超时（秒）', writeTimeoutSeconds: '写超时（秒）',
  busyTimeoutMs: '写锁等待（毫秒）', maxOpenConns: '最大连接数', wal: '启用 WAL',
  day: '日窗口', week: '周窗口', month: '月窗口', exact: '精确容差（分钟）', max: '最大容差（分钟）',
  weights: '评分权重', hotGrowth: 'S 级增长门槛', activeGrowth: 'A 级增长门槛',
  coreSoftBudgetPerHour: 'Core 每小时请求上限', repoSoftBudgetPerHour: '采样每小时请求上限',
  coreReserveRemaining: 'Core 预留请求数', searchSoftBudgetPerMinute: 'Search 每分钟请求上限',
  hotDemotionHours: 'S 级降档等待（小时）', activeDemotionHours: 'A 级降档等待（小时）',
  normalDemotionHours: 'B 级降档等待（小时）', topGrowthRepos: 'S 级增长榜入选名次',
  relativeStarFloor: '相对增长的起始 Star 下限', velocity24Cap: '24h 增长评分封顶值',
  velocity7Cap: '7 天增长评分封顶值', accelerationCap: '加速度评分封顶值',
  minPersistenceDays: '持续增长的最少观测天数',
}
function settingName(key: string) { return settingsNames[key] ?? key }

function NumberInput({ label, value, onChange, min, step = 'any', title }: {
  label: string; value: number; onChange: (value: number) => void; min?: number; step?: number | 'any'; title?: string
}) {
  return <label className="field">{label}<input type="number" min={min} step={step} title={title} value={value} onChange={(event) => {
    const n = event.target.valueAsNumber
    if (Number.isFinite(n)) onChange(n)
  }} /></label>
}

function Setting({ name, path, value, update }: { name: string; path: string; value: SettingValue; update: (value: SettingValue) => void }) {
  const id = useId()
  const label = path === 'metrics.hour' ? '小时窗口' : settingName(name)
  const help = runtimeHelp[path]
  const reportMax = path === 'reportPublication.weeklyMinDays' ? 7 : path === 'reportPublication.monthlyMinCoverage' ? 100 : undefined
  if (typeof value !== 'object' || value === null) {
    const description = help ? id : undefined
    const field = typeof value === 'boolean' ?
      <label className="check-field"><input type="checkbox" checked={value} aria-describedby={description}
        onChange={(e) => update(e.target.checked)} />{label}</label> :
      <label className="field">{label}{typeof value === 'number' ?
        <input type="number" min={reportMax === undefined ? undefined : 1} max={reportMax} step={reportMax === undefined ? 'any' : 1}
          value={value} aria-describedby={description} onChange={(event) => {
          const next = event.target.valueAsNumber
          if (Number.isFinite(next)) update(next)
        }} /> :
        <input value={value ?? ''} aria-describedby={description} onChange={(e) => update(e.target.value)} />}
      </label>
    return <div className="runtime-setting">{field}{help && <p className="runtime-help" id={id}>{help}</p>}</div>
  }
  if (Array.isArray(value)) return <fieldset className="nested-fields"><legend>{label}</legend><div className="settings-fields">
    {value.map((item, index) => <Setting key={index} name={name === 'weights' ? ['24h 增长', '7d 增长', '加速度', '持续性', 'Trending'][index] ?? String(index + 1) : String(index + 1)}
      path={path + '.' + index} value={item} update={(next) => update(value.map((old, i) => i === index ? next : old))} />)}
  </div></fieldset>
  return <fieldset className="nested-fields"><legend>{label}</legend><Fields path={path} value={value} update={update} /></fieldset>
}

function Fields({ path, value, update }: { path: string; value: SettingFields; update: (value: SettingFields) => void }) {
  return <div className="settings-fields">{Object.entries(value).map(([key, item]) =>
    <Setting key={key} name={key} path={path + '.' + key} value={item} update={(next) => update({ ...value, [key]: next })} />)}</div>
}

export function RuntimeEditor() {
  const path = '/system/settings'
  const query = useConfig<RuntimeSettings>(path)
  const [params, setParams] = useSearchParams()
  const group = params.get('group')
  const settings = query.data?.settings
  const keys = Object.keys(settings ?? {})
  const [active, setActive] = useState<string>()
  const nav = useRef<HTMLElement>(null)
  const ready = Boolean(settings)
  function scrollToGroup(key: string) {
    const target = document.getElementById('runtime-' + key)
    if (!target) return
    setActive(key)
    target.scrollIntoView({ block: 'start', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
  }
  useEffect(() => {
    if (ready && group) scrollToGroup(group)
  }, [group, ready])
  useEffect(() => {
    if (!settings) return
    const sections = Array.from(document.querySelectorAll<HTMLElement>('.runtime-section'))
    let frame = 0
    // 以正文上沿为基准同步锚点，滚动时不改写配置地址。
    function sync() {
      if (frame) return
      frame = requestAnimationFrame(() => {
        frame = 0
        const threshold = Number.parseFloat(getComputedStyle(sections[0]).scrollMarginTop) + 8
        const last = window.scrollY > 0 && window.scrollY + window.innerHeight >= document.documentElement.scrollHeight - 2
        const visible = last ? sections.at(-1) : sections.filter((section) => section.getBoundingClientRect().top <= threshold).at(-1) ?? sections[0]
        setActive(visible?.dataset.group)
      })
    }
    if (sections.length === 0) return
    window.addEventListener('scroll', sync, { passive: true })
    window.addEventListener('resize', sync)
    return () => {
      window.removeEventListener('scroll', sync)
      window.removeEventListener('resize', sync)
      cancelAnimationFrame(frame)
    }
  }, [settings])
  useEffect(() => {
    const list = nav.current
    const current = list?.querySelector('a[aria-current="location"]')
    if (!list || !current || list.scrollWidth <= list.clientWidth) return
    const viewport = list.getBoundingClientRect()
    const item = current.getBoundingClientRect()
    list.scrollLeft += item.left < viewport.left ? item.left - viewport.left : Math.max(0, item.right - viewport.right)
  }, [active, ready])
  return <ConfigEditor<RuntimeSettings, SettingFields> path={path} query={query}
    apply={(server, settings) => ({ ...server, settings })}
    payload={({ revision, settings }) => ({ revision, settings })}
    navigation={<nav ref={nav} className="panel runtime-nav" aria-label="配置分组">{keys.map((key) => {
      const next = new URLSearchParams(params)
      next.set('group', key)
      return <a href={'?' + next.toString()} key={key} aria-current={key === (active ?? keys[0]) ? 'location' : undefined} onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
        event.preventDefault()
        if (group === key) { scrollToGroup(key); return }
        setParams(next, { replace: true, preventScrollReset: true })
      }}>{settingName(key)}</a>
    })}</nav>}
    fields={(server, draft, update) => {
      const values = draft ?? server.settings
      return <>
        {server.restartRequired && <Notice>已保存的 HTTP 或 SQLite 修改将在重启服务后生效。</Notice>}
        {keys.map((key) => {
          const value = values[key]
          const fields = value !== null && typeof value === 'object' && !Array.isArray(value) ? value : null
          const setValue = (next: SettingValue) => update({ ...values, [key]: next })
          return <section key={key} className="runtime-section" id={'runtime-' + key} data-group={key} aria-labelledby={'runtime-title-' + key}>
            <div className="panel runtime-section-card">
              <div className="section-head">
                <h2 id={'runtime-title-' + key}>{settingName(key)}</h2>
                {(key === 'server' || key === 'storage') && <span className="minor muted">重启后生效</span>}
              </div>
              {key === 'github' && <p className="minor muted">在部署 YAML 中填写 <code>credentials.github_token</code>，留空时读取 <code>{String(fields?.tokenEnv || 'GITHUB_TOKEN')}</code> 环境变量。修改 YAML 后需重启服务。</p>}
              {key === 'ai' && <p className="minor muted">仅支持 OpenAI 兼容供应商，需支持 Chat Completions 的 JSON 模式和 <code>max_completion_tokens</code>。在部署 YAML 中填写 <code>credentials.ai_api_key</code>，留空时读取 <code>{String(fields?.apiKeyEnv || 'AI_API_KEY')}</code> 环境变量。修改 YAML 后需重启服务。</p>}
              {key === 'embedding' && <p className="minor muted">使用独立的 OpenAI 兼容 <code>/embeddings</code> 接口。服务进程从 <code>{String(fields?.apiKeyEnv || 'EMBEDDING_API_KEY')}</code> 环境变量读取 API Key。</p>}
              {key === 'scheduler' && fields && <ObservationTrial apply={(scheduler) => setValue({ ...fields, ...scheduler })} />}
              {fields ? <Fields path={key} value={fields} update={setValue} /> : <Setting name={key} path={key} value={value} update={setValue} />}
            </div>
          </section>
        })}
      </>
    }} />
}

type ObservationReplay = {
  report: { legacyActiveGrades: Record<string, number>; trial: Record<string, number> }
  scheduler: SettingFields
}

function ObservationTrial({ apply }: { apply: (scheduler: SettingFields) => void }) {
  const replay = useMutation({ mutationFn: () => api<ObservationReplay>('/system/observation-replay') })
  const result = replay.data
  const active = (counts: Record<string, number>) => ['S', 'A', 'B', 'C'].reduce((total, grade) => total + (counts[grade] ?? 0), 0)
  return <div className="runtime-setting">
    <div className="detail-side-actions">
      <ActionButton type="button" className="button" pending={replay.isPending} onClick={() => replay.mutate()}>演算观察池</ActionButton>
      {result && <button type="button" className="button" onClick={() => {
        const { observationPool, maxHot, maxActive, maxNormal, maxCooling, maxMonitoredRepos, intervalsMinutes } = result.scheduler
        apply({ observationPool, maxHot, maxActive, maxNormal, maxCooling, maxMonitoredRepos, intervalsMinutes })
      }}>填入试运行参数</button>}
    </div>
    {replay.isError && <Notice error>{errorText(replay.error)}</Notice>}
    {result && <p className="minor muted" role="status">
      原规则 {number(active(result.report.legacyActiveGrades))} 个活跃，试运行 {number(active(result.report.trial))} 个活跃。
      {['S', 'A', 'B', 'C'].map((grade) => ` ${grade} ${number(result.report.trial[grade] ?? 0)}`).join('，')}。
    </p>}
  </div>
}

type Directory<T> = {
  path: ConfigPath
  title: string
  noun: string
  identity: (item: T) => string
  name: (item: T) => string
  icon?: (item: T) => ReactNode
  caption: (item: T) => string
  search: (item: T) => string
  columns: string[]
  cells: (item: T) => ReactNode[]
  metrics: (items: T[]) => { label: string; value: string }[]
  initial: T
  fields: (item: T, edit: (next: T) => void) => ReactNode
  status: (item: T) => ReactNode
  filter?: { label: string; options: { value: string; label: string }[]; match: (item: T, filter: string) => boolean }
  enabled?: (item: T) => boolean
  normalize?: (item: T) => T
  afterList?: ReactNode
}

function SettingsDirectory<T>({ directory }: { directory: Directory<T> }) {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const query = useConfig<ListSettings<T>>(directory.path)
  const queryClient = useQueryClient()
  const [deleting, setDeleting] = useState<T | null>(null)
  const saveList = useMutation({
    mutationFn: (body: ListSettings<T>) => api<ListSettings<T>>(directory.path, { method: 'PUT', body }),
    onSuccess: (value) => {
      queryClient.setQueryData(['settings', configScopes[directory.path]], value)
      setDeleting(null)
      refreshSettings(queryClient, directory.path)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'settings_conflict') void query.refetch()
    },
  })
  const id = params.get('entry')
  const creating = params.get('new') === '1'
  const search = params.get('q') ?? ''
  const filter = params.get('filter') ?? ''
  const enabled = params.get('enabled') ?? ''
  const listParams = new URLSearchParams(params)
  listParams.delete('entry')
  listParams.delete('new')
  const listUrl = '/admin/runs?' + listParams.toString()
  const entryUrl = (key: string | null) => {
    const next = new URLSearchParams(listParams)
    next.set(key === null ? 'new' : 'entry', key ?? '1')
    return '/admin/runs?' + next.toString()
  }
  function setFilter(key: 'q' | 'filter' | 'enabled', value: string) {
    const next = new URLSearchParams(listParams)
    if (value) next.set(key, value)
    else next.delete(key)
    // 同步更新受控筛选，连续操作从同一个地址读取最新状态。
    setParams(next, { replace: true, preventScrollReset: true, flushSync: true })
  }
  if (creating || id !== null) {
    const original = (server: ListSettings<T>) => server.items.find((item) => directory.identity(item) === id)
    return <ConfigEditor<ListSettings<T>, T | null> key={creating ? 'new' : 'entry:' + id} path={directory.path} query={query}
      heading={(server) => {
        const item = server && original(server)
        return <div className="page-heading">
          <Link to={listUrl} className="admin-link"><ArrowLeft size={15} />{directory.title}</Link>
          <h2>{creating ? '添加' + directory.noun : item ? directory.name(item) : server ? directory.noun + '不存在' : directory.title}</h2>
          {!creating && item && directory.caption(item) !== directory.name(item) && <p className="minor muted"><code>{directory.caption(item)}</code></p>}
        </div>
      }}
      apply={(server, draft) => {
        // API 按修订保存整份列表，只将当前条目的修改合入服务端版本。
        const item = draft !== null ? directory.normalize?.(draft) ?? draft : null
        if (creating && item !== null) return { ...server, items: [...server.items, item] }
        const index = server.items.findIndex((entry) => directory.identity(entry) === id)
        if (index < 0) return null
        const items = server.items.slice()
        if (item === null) items.splice(index, 1)
        else items[index] = item
        return { ...server, items }
      }}
      payload={({ revision, items }) => ({ revision, items })}
      fields={(server, draft, edit) => {
        const item = draft === undefined ? creating ? directory.initial : original(server) : draft
        if (item === null) return <Notice>保存后移除此{directory.noun}。</Notice>
        if (item === undefined) return <p className="muted">请返回列表重新选择。</p>
        return <>
          <div className="admin-fields">{directory.fields(item, edit)}</div>
          {!creating && original(server) && <button type="button" className="button tone-hot admin-remove"
            onClick={() => edit(null)}>移除{directory.noun}</button>}
        </>
      }}
      status={(server) => {
        const item = original(server)
        return !creating && item ? directory.status(item) : null
      }}
      onSaved={() => { void navigate(listUrl, { replace: true }) }} />
  }

  if (!query.data) return <div className="admin-stack">
    {query.isPending ? <Skeleton /> : <PageError error={query.error} retry={() => { void query.refetch() }} />}
  </div>
  const { revision, items: stored } = query.data
  const items = saveList.isPending && deleting === null ? saveList.variables.items : stored
  const keyword = search.trim().toLowerCase()
  const visible = items.filter((item) => directory.search(item).toLowerCase().includes(keyword) &&
    (!filter || !directory.filter || directory.filter.match(item, filter)) &&
    (!enabled || !directory.enabled || directory.enabled(item) === (enabled === 'yes')))
  const entries = visible.map((item) => ({ id: directory.identity(item), label: directory.name(item) }))
  function reorder(from: string, to: string) {
    const fromIndex = entries.findIndex(({ id }) => id === from)
    const toIndex = entries.findIndex(({ id }) => id === to)
    if (saveList.isPending || fromIndex < 0 || toIndex < 0) return
    const ids = new Set(entries.map(({ id }) => id))
    const ordered = arrayMove(visible, fromIndex, toIndex)
    let position = 0
    // 筛选时仅调整可见条目占用的位置，隐藏条目的位置不变。
    saveList.mutate({ revision, items: stored.map((item) => ids.has(directory.identity(item)) ? ordered[position++] : item) })
  }
  return <div className="admin-stack">
    <div className="action-row"><Link className="button primary" to={entryUrl(null)}><Plus size={16} />添加{directory.noun}</Link></div>
    <AdminMetrics label={directory.title + '统计'} items={directory.metrics(items)} />
    {query.error && <Notice error>{errorText(query.error)}</Notice>}
    {saveList.error && deleting === null && <Notice error>{errorText(saveList.error)}</Notice>}
    <section className="panel admin-panel" aria-label={directory.title + '列表'}>
      <div className="admin-table-toolbar">
        <label className="field"><span className="sr-only">搜索{directory.noun}</span>
          <input type="search" placeholder={'搜索' + directory.noun} value={search} onChange={(event) => setFilter('q', event.target.value)} />
        </label>
        <div className="admin-filters">
          {saveList.isPending && deleting === null && <span className="settings-sort-status minor muted" role="status"><Spinner />正在保存顺序</span>}
          {directory.enabled && <div className="admin-filter-buttons" role="group" aria-label="规则状态">
            {[['', '全部'], ['yes', '启用'], ['no', '停用']].map(([value, label]) => <button type="button" key={value}
              aria-pressed={enabled === value} onClick={() => setFilter('enabled', value)}>{label}</button>)}
          </div>}
          {directory.filter && <label className="field"><span className="sr-only">{directory.filter.label}</span>
            <select value={filter} onChange={(event) => setFilter('filter', event.target.value)}>
              {directory.filter.options.map(({ value, label }) => <option key={value} value={value}>{label}</option>)}
            </select>
          </label>}
        </div>
      </div>
      {visible.length ? <SortableSettings entries={entries} onMove={reorder}>
        <div className="admin-table-scroll"><table className="admin-table admin-directory-table">
          <thead><tr><th scope="col" className="settings-grip-cell"><span className="sr-only">排序</span></th>
            <th scope="col">{directory.noun}</th>{directory.columns.map((column) => <th scope="col" key={column}>{column}</th>)}<th scope="col" className="settings-action-cell">操作</th></tr></thead>
          <tbody>{visible.map((item, index) => {
            const { id, label } = entries[index]
            return <SortableSettingsRow key={id} as="tr" id={id} label={label} disabled={visible.length < 2 || saveList.isPending}>{(handle) => <>
              <td className="settings-grip-cell">{handle}</td>
              <td className="admin-entry-cell"><Link className={directory.icon ? 'admin-entry-link title-with-icon' : 'admin-entry-link'} to={entryUrl(id)}>
                {directory.icon?.(item)}<span>{label}</span>
              </Link>
                {directory.caption(item) !== label && <code className="minor muted">{directory.caption(item)}</code>}</td>
              {directory.cells(item).map((cell, index) => <td key={index}>{cell}</td>)}
              <td className="settings-action-cell"><div className="settings-row-actions">
                <Link className="icon-button" to={entryUrl(id)} aria-label={'编辑 ' + label} title="编辑"><Pencil size={15} aria-hidden /></Link>
                <button type="button" className="icon-button" aria-label={'删除 ' + label} title="删除" disabled={saveList.isPending}
                  onClick={() => { saveList.reset(); setDeleting(item) }}><Trash2 size={15} aria-hidden /></button>
              </div></td>
            </>}</SortableSettingsRow>
          })}</tbody>
        </table></div>
      </SortableSettings> : <p className="admin-empty">{items.length ? '没有符合条件的' + directory.noun : '暂无' + directory.noun}</p>}
    </section>
    {directory.afterList}
    {deleting && <Dialog title={'删除' + directory.noun} onClose={() => { if (!saveList.isPending) setDeleting(null) }}>
      <p>确定删除「{directory.name(deleting)}」？</p>
      {saveList.error && <Notice error>{errorText(saveList.error)}</Notice>}
      <div className="action-row">
        <button type="button" className="button" data-autofocus disabled={saveList.isPending} onClick={() => setDeleting(null)}>取消</button>
        <ActionButton className="button tone-hot" pending={saveList.isPending} onClick={() => {
          saveList.mutate({ revision, items: stored.filter((item) => directory.identity(item) !== directory.identity(deleting)) })
        }}>确认删除</ActionButton>
      </div>
    </Dialog>}
  </div>
}

const categories: Directory<CategorySetting> = {
  path: '/system/categories', title: '分类映射', noun: '分类',
  identity: (item) => item.slug, name: (item) => item.name, caption: (item) => item.slug,
  search: (item) => [item.name, item.slug, ...item.anyTopics].join(' '),
  columns: ['匹配 Topics', 'Topic 数量'],
  cells: (item) => [item.anyTopics.join('、') || '—', number(item.anyTopics.length)],
  metrics: (items) => [
    { label: '全部', value: number(items.length) },
    { label: '已配置匹配项', value: number(items.filter((item) => item.anyTopics.length > 0).length) },
    { label: '匹配 Topics', value: number(new Set(items.flatMap((item) => item.anyTopics)).size) },
  ],
  initial: { slug: '', name: '', anyTopics: [] },
  normalize: (item) => ({
    slug: item.slug.trim(), name: item.name.trim(), anyTopics: item.anyTopics.map((topic) => topic.trim()).filter(Boolean),
  }),
  fields: (item, edit) => <>
    <label className="field">分类名称<input required value={item.name} onChange={(e) => edit({ ...item, name: e.target.value })} /></label>
    <label className="field">分类标识<input required value={item.slug} onChange={(e) => edit({ ...item, slug: e.target.value })} /></label>
    <label className="field admin-field-wide">匹配 Topics<input required placeholder="用逗号分隔" value={item.anyTopics.join(',')}
      onChange={(e) => edit({ ...item, anyTopics: e.target.value.split(/[,，]/) })} /></label>
  </>,
  status: (item) => <dl className="definition-list">
    <div><dt>分类标识</dt><dd><code>{item.slug}</code></dd></div>
    <div><dt>匹配 Topics</dt><dd>{number(item.anyTopics.length)}</dd></div>
  </dl>,
}

const sourceNames = { search: 'GitHub Search', trending: 'GitHub Trending' }
const defaultSearchMinStars = 200
const enabledBadge = (enabled: boolean) => <Badge tone={enabled ? 'ok' : 'neutral'}>{enabled ? '已启用' : '已停用'}</Badge>
const rules: Directory<RuleSetting> = {
  path: '/system/discovery-rules', title: '发现规则', noun: '规则',
  identity: (item) => item.sourceType + ':' + item.sourceKey,
  name: (item) => item.sourceKey, caption: (item) => item.sourceKey,
  search: (item) => [item.sourceKey, item.query, sourceNames[item.sourceType]].join(' '),
  columns: ['来源类型', '状态', '扫描间隔', '优先级', '最低 Star', '仓库年龄', '搜索表达式'],
  cells: (item) => [<Badge>{sourceNames[item.sourceType]}</Badge>, enabledBadge(item.enabled),
    number(item.intervalMinutes) + ' 分钟', number(item.priority),
    item.sourceType === 'trending' ? '—' : item.minStars === 0 ? '不限' : number(item.minStars ?? defaultSearchMinStars),
    item.ageWindow ? item.ageWindow.replace('-', '–') + ' 天' : '—',
    item.query ? <code>{item.query}</code> : '—'],
  metrics: (items) => [
    { label: '全部', value: number(items.length) },
    { label: '启用', value: number(items.filter((item) => item.enabled).length) },
    { label: 'GitHub Search', value: number(items.filter((item) => item.sourceType === 'search').length) },
    { label: 'GitHub Trending', value: number(items.filter((item) => item.sourceType === 'trending').length) },
  ],
  initial: { sourceType: 'search', sourceKey: '', query: '', minStars: defaultSearchMinStars, intervalMinutes: 30, priority: 20, enabled: true },
  enabled: (item) => item.enabled,
  filter: { label: '来源类型', options: [
    { value: '', label: '全部类型' }, { value: 'search', label: 'GitHub Search' }, { value: 'trending', label: 'GitHub Trending' },
  ], match: (item, filter) => item.sourceType === filter },
  fields: (item, edit) => <>
    <label className="field">规则标识<input required value={item.sourceKey} placeholder={item.sourceType === 'trending' ? 'all:daily' : '例如 coding'}
      onChange={(e) => edit({ ...item, sourceKey: e.target.value })} /></label>
    <label className="field">来源类型<select value={item.sourceType} onChange={(e) => edit({
      ...item, sourceType: e.target.value === 'trending' ? 'trending' : 'search', sourceKey: '', query: '',
      ageWindow: undefined,
      minStars: e.target.value === 'trending' ? undefined : defaultSearchMinStars,
    })}><option value="search">GitHub Search</option><option value="trending">GitHub Trending</option></select></label>
    <NumberInput label="扫描间隔（分钟）" value={item.intervalMinutes} onChange={(intervalMinutes) => edit({ ...item, intervalMinutes })} />
    <NumberInput label="优先级" value={item.priority} onChange={(priority) => edit({ ...item, priority })} />
    {item.sourceType === 'search' && <NumberInput label="最低 Star" value={item.minStars ?? defaultSearchMinStars}
      min={0} step={1} title="0 表示不限" onChange={(minStars) => edit({ ...item, minStars })} />}
    {item.sourceType === 'search' && <label className="field">仓库年龄<select value={item.ageWindow ?? ''}
      onChange={(e) => edit({ ...item, ageWindow: e.target.value as RuleSetting['ageWindow'] })}>
      <option value="">不限</option><option value="0-3">0–3 天</option><option value="4-14">4–14 天</option><option value="15-30">15–30 天</option>
    </select></label>}
    {item.sourceType === 'search' && <label className="field admin-field-wide">搜索表达式
      <input required value={item.query} onChange={(e) => edit({ ...item, query: e.target.value })} />
    </label>}
    <label className="check-field"><input type="checkbox" checked={item.enabled} onChange={(e) => edit({ ...item, enabled: e.target.checked })} />启用规则</label>
  </>,
  status: (item) => <><dl className="definition-list">
    <div><dt>规则状态</dt><dd>{enabledBadge(item.enabled)}</dd></div>
    <div><dt>来源类型</dt><dd>{sourceNames[item.sourceType]}</dd></div>
    <div><dt>扫描间隔</dt><dd>{number(item.intervalMinutes)} 分钟</dd></div>
  </dl><DiscoveryRunHistory rule={item} /></>,
  afterList: <DiscoveryRunHistory />,
}

export function CategoryEditor() {
  return <SettingsDirectory directory={categories} />
}

function projectThemes(groups: ProjectThemeGroup[]): Directory<ProjectThemeSetting> {
  const names = new Map(groups.map(({ slug, name }) => [slug, name]))
  return {
    path: '/system/project-themes', title: '专题管理', noun: '专题',
    icon: (item) => <ThemeIcon icon={item.icon} color={item.iconColor} size={14} />,
    identity: (item) => item.slug, name: (item) => item.name, caption: (item) => item.slug,
    search: (item) => [item.name, item.slug, item.description, names.get(item.groupSlug), ...item.anyTopics, ...(item.aliases ?? []), ...(item.owners ?? [])].join(' '),
    columns: ['方向', '简介', '匹配 Topics', '别名', 'GitHub 所有者'],
    cells: (item) => [names.get(item.groupSlug), item.description, item.anyTopics.join('、'), item.aliases?.join('、'), item.owners?.join('、')],
    metrics: (items) => [
      { label: '专题', value: number(items.length) },
      { label: '匹配 Topics', value: number(new Set(items.flatMap((item) => item.anyTopics)).size) },
    ],
    initial: { slug: '', name: '', description: '', anyTopics: [], aliases: [], owners: [], icon: '', iconColor: '', groupSlug: groups[0]?.slug ?? '' },
    normalize: (item) => ({
      ...item, slug: item.slug.trim(), name: item.name.trim(), description: item.description.trim(),
      anyTopics: item.anyTopics.map((topic) => topic.trim()).filter(Boolean),
      aliases: (item.aliases ?? []).map((alias) => alias.trim()).filter(Boolean),
      owners: (item.owners ?? []).map((owner) => owner.trim()).filter(Boolean),
    }),
    fields: (item, edit) => <>
      <label className="field">专题名称<input required value={item.name} onChange={(e) => edit({ ...item, name: e.target.value })} /></label>
      <label className="field">专题标识<input required pattern="[a-z0-9][a-z0-9+_\x2d]*" value={item.slug} onChange={(e) => edit({ ...item, slug: e.target.value })} /></label>
      <label className="field admin-field-wide">所属方向
        <select required value={item.groupSlug} onChange={(e) => edit({ ...item, groupSlug: e.target.value })}>
          {groups.map(({ slug, name }) => <option key={slug} value={slug}>{name}</option>)}
        </select>
      </label>
      <ThemeIconPicker icon={item.icon} color={item.iconColor} onChange={(icon, iconColor) => edit({ ...item, icon, iconColor })} />
      <label className="field admin-field-wide">简介<textarea required rows={3} value={item.description}
        onChange={(e) => edit({ ...item, description: e.target.value })} /></label>
      <label className="field admin-field-wide">GitHub 所有者<textarea rows={2} placeholder="组织或个人账号，用逗号或换行分隔" value={(item.owners ?? []).join('\n')}
        onChange={(e) => edit({ ...item, owners: e.target.value.split(/[,，\n]/) })} />
        <span className="minor muted">填写后，只收录这些账号拥有的仓库。</span>
      </label>
      <label className="field admin-field-wide">匹配 Topics<input required={!item.owners?.some((owner) => owner.trim()) && !item.aliases?.some((alias) => alias.trim())} placeholder="GitHub 主题词，用逗号分隔" value={item.anyTopics.join(',')}
        onChange={(e) => edit({ ...item, anyTopics: e.target.value.split(/[,，]/) })} /></label>
      <label className="field admin-field-wide">主题别名<input placeholder="同义词或常用缩写，用逗号分隔" value={(item.aliases ?? []).join(',')}
        onChange={(e) => edit({ ...item, aliases: e.target.value.split(/[,，]/) })} /></label>
    </>,
    status: (item) => <Link className="admin-link" to={topicPath(item.slug)}>查看专题</Link>,
  }
}

const projectThemeGroups: Directory<ProjectThemeGroup> = {
  path: '/system/project-theme-groups', title: '主题方向', noun: '方向',
  identity: (item) => item.slug, name: (item) => item.name, caption: (item) => item.slug,
  search: (item) => [item.name, item.slug, item.description].join(' '),
  columns: ['简介'], cells: (item) => [item.description],
  metrics: (items) => [{ label: '方向', value: number(items.length) }],
  initial: { slug: '', name: '', description: '' },
  normalize: (item) => ({ slug: item.slug.trim(), name: item.name.trim(), description: item.description.trim() }),
  fields: (item, edit) => <>
    <label className="field">方向名称<input required value={item.name} onChange={(e) => edit({ ...item, name: e.target.value })} /></label>
    <label className="field">方向标识<input required pattern="[a-z0-9][a-z0-9+_\x2d]*" value={item.slug} onChange={(e) => edit({ ...item, slug: e.target.value })} /></label>
    <label className="field admin-field-wide">方向简介<textarea required rows={3} value={item.description}
      onChange={(e) => edit({ ...item, description: e.target.value })} /></label>
  </>,
  status: () => null,
}

export function ProjectThemeEditor() {
  const [params] = useSearchParams()
  const managingGroups = params.get('section') === 'groups'
  const groups = useConfig<ListSettings<ProjectThemeGroup>>('/system/project-theme-groups')
  const list = '/admin/runs?tab=project-themes'
  return <div className="admin-stack">
    <nav className="pills" aria-label="专题维护">
      <Link className="pill-link" aria-current={!managingGroups ? 'page' : undefined} to={list}>专题</Link>
      <Link className="pill-link" aria-current={managingGroups ? 'page' : undefined} to={list + '&section=groups'}>管理方向</Link>
    </nav>
    {managingGroups ? <SettingsDirectory key="groups" directory={projectThemeGroups} /> :
      groups.data ? groups.data.items.length > 0 ? <SettingsDirectory key="themes" directory={projectThemes(groups.data.items)} /> :
        <Notice>请先创建一个方向，再添加专题。</Notice> :
        groups.isPending ? <Skeleton /> : <PageError error={groups.error} retry={() => { void groups.refetch() }} />}
  </div>
}

export function DiscoveryTabEditor() {
  const path = '/system/discovery-tabs'
  const query = useConfig<DiscoveryTabSettings>(path)
  return <ConfigEditor<DiscoveryTabSettings, DiscoveryTabDraft> path={path} query={query}
    apply={(server, { items, selection, ranking }) => ({ ...server, items, selection, ranking })}
    payload={({ revision, items, selection, ranking }, draft) => ({ revision, items, selection, ranking, newCategories: draft?.newCategories })}
    fields={(server, draft, edit) => <DiscoveryTabFields options={server.options}
      draft={draft ?? { items: server.items, selection: server.selection, ranking: server.ranking, newCategories: [] }} edit={edit} />} />
}

type DiscoveryTabDraft = Pick<DiscoveryTabSettings, 'items' | 'selection' | 'ranking'> & { newCategories: CategorySetting[] }
type DiscoveryTabOptions = DiscoveryTabSettings['options']

const discoverySelectionFields = [
  ['recentDays', '首次发现窗口（天）'],
  ['recentSampleHours', '新项目采样窗口（小时）'],
  ['growthSampleHours', '增长采样窗口（小时）'],
  ['trendingHours', '全站日榜有效期（小时）'],
  ['trendingTop', '全站日榜入选名次'],
] as const

const discoveryRankingFields = [
  ['heatWeight', '热度权重（%）', 0, 100],
  ['freshnessWeight', '新鲜度权重（%）', 0, 100],
  ['consecutiveDays', '连续上榜天数', 1, undefined],
  ['cooldownDays', '冷却天数', 0, undefined],
] as const

function DiscoveryTabFields({ draft, options, edit }: { draft: DiscoveryTabDraft; options: DiscoveryTabOptions; edit: (value: DiscoveryTabDraft) => void }) {
  const [adding, setAdding] = useState(false)
  const { items, newCategories } = draft
  const allOptions = [...options, ...newCategories]
  const available = options.filter((option) => !items.some(({ slug }) => slug === option.slug))
  const names = new Map(allOptions.map(({ slug, name }) => [slug, name]))
  const entries = items.map(({ slug, label }) => ({ id: slug, label: label || names.get(slug) || slug }))

  function changeRanking(key: keyof DiscoveryRanking, value: number) {
    const ranking = { ...draft.ranking, [key]: value }
    if (Number.isFinite(value)) {
      if (key === 'heatWeight') ranking.freshnessWeight = 100 - value
      if (key === 'freshnessWeight') ranking.heatWeight = 100 - value
    }
    edit({ ...draft, ranking })
  }

  // 「全部」是默认入口，只有分类标签可以调整。
  return <>
    <div className="discovery-tab-toolbar">
      <p className="minor muted">「全部」始终显示在第一位。</p>
      <button type="button" className="button" onClick={() => setAdding(true)}><Plus size={16} aria-hidden />添加标签</button>
    </div>
    {items.length > 0 && <SortableSettings entries={entries} onMove={(from, to) => {
      const fromIndex = items.findIndex(({ slug }) => slug === from)
      const toIndex = items.findIndex(({ slug }) => slug === to)
      if (fromIndex >= 0 && toIndex >= 0) edit({ ...draft, items: arrayMove(items, fromIndex, toIndex) })
    }}><ol className="discovery-tab-settings">
      {items.map((item, index) => {
        const name = names.get(item.slug) ?? item.slug
        return <SortableSettingsRow key={item.slug} as="li" id={item.slug} label={entries[index].label} disabled={items.length < 2}>{(handle) => <>
          {handle}
          <InlineDiscoveryLabel name={name} label={item.label} onChange={(label) => edit({ ...draft,
            items: items.map((tab) => tab.slug === item.slug ? { ...tab, label } : tab) })} />
          <ActionButton className="icon-button" aria-label={'移除 ' + entries[index].label} title="移除" icon={<X size={16} />}
            onClick={() => edit({
              ...draft,
              items: items.filter(({ slug }) => slug !== item.slug), newCategories: newCategories.filter(({ slug }) => slug !== item.slug),
            })} />
        </>}</SortableSettingsRow>
      })}
    </ol></SortableSettings>}
    {adding && createPortal(<AddDiscoveryTab available={available} options={allOptions} onClose={() => setAdding(false)}
      onAdd={(slug, category) => {
        edit({ ...draft, items: [...items, { slug, label: '' }], newCategories: category ? [...newCategories, category] : newCategories })
        setAdding(false)
      }} />, document.body)}
    <section className="panel runtime-section-card">
      <h3>精选规则</h3>
      <div className="admin-fields">
        {discoverySelectionFields.map(([key, label]) => <label className="field" key={key}>{label}
          <input type="number" min="1" step="1" required
            value={Number.isFinite(draft.selection[key]) ? draft.selection[key] : ''}
            onChange={(event) => edit({ ...draft, selection: { ...draft.selection, [key]: event.target.valueAsNumber } })} />
        </label>)}
      </div>
    </section>
    <section className="panel runtime-section-card">
      <h3>展示排序与冷却</h3>
      <div className="admin-fields">
        {discoveryRankingFields.map(([key, label, min, max]) => <label className="field" key={key}>{label}
          <input type="number" min={min} max={max} step="1" required
            aria-describedby={key === 'cooldownDays' ? 'discovery-cooldown-hint' : undefined}
            value={Number.isFinite(draft.ranking[key]) ? draft.ranking[key] : ''}
            onChange={(event) => changeRanking(key, event.target.valueAsNumber)} />
        </label>)}
      </div>
      <p id="discovery-cooldown-hint" className="minor muted">冷却天数填 0 时关闭冷却。</p>
    </section>
  </>
}

function InlineDiscoveryLabel({ name, label, onChange }: { name: string; label: string; onChange: (value: string) => void }) {
  const [value, setValue] = useState<string | null>(null)
  // 编辑状态随输入焦点切换，移动鼠标时可继续输入。
  return <div className="field discovery-label-field">
    {value === null ? <button type="button" className="discovery-label" aria-label={'编辑标签 ' + (label || name)}
      onClick={() => setValue(label)}>{label || name}</button> :
      <input autoFocus aria-label={name + '的标签名称'} placeholder={name} maxLength={24} value={value}
      onChange={(event) => setValue(event.target.value)} onBlur={() => {
        if (value.trim() !== label) onChange(value.trim())
        setValue(null)
      }} onKeyDown={(event) => {
        if (event.key === 'Enter') { event.preventDefault(); event.currentTarget.blur() }
        if (event.key === 'Escape') { event.preventDefault(); setValue(null) }
      }} />}
  </div>
}

function AddDiscoveryTab({ available, options, onAdd, onClose }: {
  available: DiscoveryTabOptions; options: DiscoveryTabOptions; onAdd: (slug: string, category?: CategorySetting) => void; onClose: () => void
}) {
  const [creating, setCreating] = useState(available.length === 0)
  const [slug, setSlug] = useState(available[0]?.slug ?? '')
  const [category, setCategory] = useState(categories.initial)
  const [error, setError] = useState('')
  return <Dialog title="添加标签" onClose={onClose}>
    {available.length > 0 && <div className="pills" role="group" aria-label="添加方式">
      <button type="button" aria-pressed={!creating} onClick={() => { setCreating(false); setError('') }}>选择现有分类</button>
      <button type="button" aria-pressed={creating} onClick={() => { setCreating(true); setError('') }}>新建分类</button>
    </div>}
    <form className="config-form" onSubmit={(event) => {
      event.preventDefault()
      event.stopPropagation()
      if (!creating) return onAdd(slug)
      const next = categories.normalize?.(category) ?? category
      if (!next.name || !next.slug || !next.anyTopics.length) return setError('请填写分类名称、标识和匹配 Topics。')
      if (options.some((item) => item.slug === next.slug)) return setError('分类标识已存在。')
      onAdd(next.slug, next)
    }}>
      {creating ? <div className="admin-fields">{categories.fields(category, setCategory)}</div> :
        <label className="field">选择分类<select value={slug} onChange={(event) => setSlug(event.target.value)}>
          {available.map(({ slug, name }) => <option key={slug} value={slug}>{name}</option>)}
        </select></label>}
      {error && <Notice error>{error}</Notice>}
      <div className="action-row"><button type="button" className="button" onClick={onClose}>取消</button>
        <ActionButton type="submit" className="button primary">添加</ActionButton></div>
    </form>
  </Dialog>
}

export function RulesEditor() {
  return <SettingsDirectory directory={rules} />
}
