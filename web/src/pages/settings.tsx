import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDown, ArrowLeft, ArrowUp, ChevronDown, Plus, X } from 'lucide-react'
import { useState } from 'react'
import type { ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, ApiError, errorText } from '../api/client'
import type { CategorySetting, DiscoveryTab, DiscoveryTabSettings, ListSettings, RuleSetting, RuntimeSettings, SettingFields, SettingValue } from '../api/types'
import { AdminMetrics, AdminPanel } from '../components/admin'
import { SystemHeader } from '../components/system-navigation'
import { Badge, Notice, PageError, Skeleton, Spinner } from '../components/ui'
import { number } from '../lib/format'

type Snapshot = { revision: number }
const configScopes = { '/system/settings': 'runtime', '/system/categories': 'categories', '/system/discovery-rules': 'rules', '/system/discovery-tabs': 'discovery-tabs' } as const
type ConfigPath = keyof typeof configScopes

function useConfig<T extends Snapshot>(path: ConfigPath) {
  return useQuery({ queryKey: ['settings', configScopes[path]], queryFn: ({ signal }) => api<T>(path, { signal }), staleTime: 0 })
}

function ConfigEditor<T extends Snapshot, Draft>({ path, query, heading, fields, apply, payload, status, onSaved }: {
  path: ConfigPath
  query: ReturnType<typeof useConfig<T>>
  heading: (server?: T) => ReactNode
  fields: (server: T, draft: Draft | undefined, edit: (value: Draft) => void) => ReactNode
  apply: (server: T, draft: Draft) => T | null
  payload: (value: T) => unknown
  status?: (server: T) => ReactNode
  onSaved?: () => void
}) {
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<{ revision: number; value: Draft } | null>(null)
  const [saved, setSaved] = useState(false)
  const queryKey = ['settings', configScopes[path]]
  const save = useMutation({
    mutationFn: (value: T) => api<T>(path, { method: 'PUT', body: payload(value) }),
    onSuccess: (value) => {
      queryClient.setQueryData(queryKey, value)
      setDraft(null)
      setSaved(true)
      void queryClient.invalidateQueries({ queryKey: ['settings'] })
      void queryClient.invalidateQueries({ queryKey: ['categories'] })
      void queryClient.invalidateQueries({ queryKey: ['discovery-tabs'] })
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
      void queryClient.invalidateQueries({ queryKey: ['trending'] })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'settings_conflict') void query.refetch()
    },
  })
  if (!query.data) return <div className="admin-stack">
    <SystemHeader>{heading()}</SystemHeader>
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
  return <div className="admin-stack">
    <SystemHeader>{heading(server)}</SystemHeader>
    <div className={side ? 'admin-form-grid' : undefined}>
      <AdminPanel title="设置" action={<span className="minor muted">修订 {server.revision}</span>}>
        <form className="admin-panel-body config-form" onSubmit={(event) => {
          event.preventDefault()
          if (proposed && !outdated && !save.isPending) save.mutate(proposed, { onSuccess: onSaved })
        }}>
          <fieldset className="config-editable" disabled={save.isPending}>{fields(server, draft?.value, edit)}</fieldset>
          {query.error && <Notice error>{errorText(query.error)}</Notice>}
          {save.error && !outdated && <Notice error>{errorText(save.error)}</Notice>}
          {outdated && <div className="panel config-conflict">
            <Notice error>发现较新的配置版本。当前表单修改已保留。</Notice>
            <details>
              <summary>核对服务端配置（修订 {server.revision}）</summary>
              <pre>{JSON.stringify(payload(server), null, 2)}</pre>
            </details>
            <div className="action-row">
              <button className="button" type="button" disabled={save.isPending} onClick={reset}>载入最新版本</button>
              <button className="button" type="button" disabled={save.isPending || !proposed} onClick={() => {
                setDraft((previous) => previous && { ...previous, revision: server.revision })
                save.reset()
              }}>采用当前表单</button>
            </div>
          </div>}
          <div className="config-footer">
            {saved && <span role="status" className="tone-ok">已保存</span>}
            <button type="button" className="button" disabled={!draft || save.isPending} onClick={reset}>还原</button>
            <button className="button primary" disabled={!proposed || outdated || save.isPending}>
              {save.isPending && <Spinner />}保存配置
            </button>
          </div>
        </form>
      </AdminPanel>
      {side && <AdminPanel title="状态"><div className="admin-panel-body">{side}</div></AdminPanel>}
    </div>
  </div>
}

const settingsNames: Record<string, string> = {
  server: 'HTTP 服务', storage: 'SQLite', github: 'GitHub 采集', scheduler: '调度',
  ai: 'AI 中文摘要', metrics: '增长与热度', maintenance: '数据保留', recommendations: '每日选题',
  summary: '模型调用与来源检查', baseUrl: '供应商 Base URL', model: '模型', apiKeyEnv: '密钥环境变量名',
  tokenEnv: '访问令牌环境变量名', enabled: '启用', timeoutSeconds: '超时（秒）',
  workerConcurrency: '采集并发', workers: '摘要并发', apiVersion: 'GitHub API 版本',
  tickMinutes: '扫描周期（分钟）', metricsIntervalMinutes: '指标计算周期（分钟）',
  maxMonitoredRepos: '最大监控项目数', maxHot: 'S 级上限', maxActive: 'A 级上限', maxNormal: 'B 级上限',
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
}
function settingName(key: string) { return settingsNames[key] ?? key }

function NumberInput({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) {
  return <label className="field">{label}<input type="number" step="any" value={value} onChange={(event) => {
    const n = event.target.valueAsNumber
    if (Number.isFinite(n)) onChange(n)
  }} /></label>
}

function Setting({ name, value, update }: { name: string; value: SettingValue; update: (value: SettingValue) => void }) {
  const label = settingName(name)
  if (typeof value === 'boolean') return <label className="check-field"><input type="checkbox" checked={value} onChange={(e) => update(e.target.checked)} />{label}</label>
  if (typeof value === 'number') return <NumberInput label={label} value={value} onChange={update} />
  if (typeof value === 'string' || value === null) return <label className="field">{label}<input value={value ?? ''} onChange={(e) => update(e.target.value)} /></label>
  if (Array.isArray(value)) return <fieldset className="nested-fields"><legend>{label}</legend><div className="settings-fields">
    {value.map((item, index) => <Setting key={index} name={name === 'weights' ? ['24h 增长', '7d 增长', '加速度', '持续性', 'Trending'][index] ?? String(index + 1) : String(index + 1)}
      value={item} update={(next) => update(value.map((old, i) => i === index ? next : old))} />)}
  </div></fieldset>
  return <fieldset className="nested-fields"><legend>{label}</legend><Fields value={value} update={update} /></fieldset>
}

function Fields({ value, update }: { value: SettingFields; update: (value: SettingFields) => void }) {
  return <div className="settings-fields">{Object.entries(value).map(([key, item]) =>
    <Setting key={key} name={key} value={item} update={(next) => update({ ...value, [key]: next })} />)}</div>
}

export function RuntimeEditor() {
  const path = '/system/settings'
  const query = useConfig<RuntimeSettings>(path)
  return <ConfigEditor<RuntimeSettings, SettingFields> path={path} query={query}
    heading={() => <h1>运行配置</h1>}
    apply={(server, settings) => ({ ...server, settings })}
    payload={({ revision, settings }) => ({ revision, settings })}
    status={(snapshot) => snapshot.restartRequired ? <Notice>已保存的运行配置将在重启服务后生效。</Notice> : <Badge tone="ok">配置已生效</Badge>}
    fields={(server, draft, update) => {
      const settings = draft ?? server.settings
      return <div className="config-groups">
        {Object.entries(settings).map(([key, value]) => <details className="panel config-group" key={key}>
          <summary><h3>{settingName(key)}</h3><ChevronDown size={17} /></summary>
          {value !== null && typeof value === 'object' && !Array.isArray(value)
            ? <Fields value={value} update={(next) => update({ ...settings, [key]: next })} />
            : <Setting name={key} value={value} update={(next) => update({ ...settings, [key]: next })} />}
        </details>)}
      </div>
    }} />
}

type Directory<T> = {
  path: ConfigPath
  title: string
  noun: string
  identity: (item: T) => string
  name: (item: T) => string
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
}

function SettingsDirectory<T>({ directory }: { directory: Directory<T> }) {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const query = useConfig<ListSettings<T>>(directory.path)
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
          <h1>{creating ? '添加' + directory.noun : item ? directory.name(item) : server ? directory.noun + '不存在' : directory.title}</h1>
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
    <SystemHeader><h1>{directory.title}</h1></SystemHeader>
    {query.isPending ? <Skeleton /> : <PageError error={query.error} retry={() => { void query.refetch() }} />}
  </div>
  const items = query.data.items
  const keyword = search.trim().toLowerCase()
  const visible = items.filter((item) => directory.search(item).toLowerCase().includes(keyword) &&
    (!filter || !directory.filter || directory.filter.match(item, filter)) &&
    (!enabled || !directory.enabled || directory.enabled(item) === (enabled === 'yes')))
  return <div className="admin-stack">
    <SystemHeader>
      <h1>{directory.title}</h1>
      <Link className="button primary" to={entryUrl(null)}><Plus size={16} />添加{directory.noun}</Link>
    </SystemHeader>
    <AdminMetrics label={directory.title + '统计'} items={directory.metrics(items)} />
    {query.error && <Notice error>{errorText(query.error)}</Notice>}
    <section className="panel admin-panel" aria-label={directory.title + '列表'}>
      <div className="admin-table-toolbar">
        <label className="field"><span className="sr-only">搜索{directory.noun}</span>
          <input type="search" placeholder={'搜索' + directory.noun} value={search} onChange={(event) => setFilter('q', event.target.value)} />
        </label>
        <div className="admin-filters">
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
      {visible.length ? <div className="admin-table-scroll"><table className="admin-table admin-directory-table">
        <thead><tr><th scope="col">{directory.noun}</th>{directory.columns.map((column) => <th scope="col" key={column}>{column}</th>)}</tr></thead>
        <tbody>{visible.map((item) => <tr key={directory.identity(item)}>
          <td><Link className="admin-entry-link" to={entryUrl(directory.identity(item))}>{directory.name(item)}</Link>
            {directory.caption(item) !== directory.name(item) && <code className="minor muted">{directory.caption(item)}</code>}</td>
          {directory.cells(item).map((cell, index) => <td key={index}>{cell}</td>)}
        </tr>)}</tbody>
      </table></div> : <p className="admin-empty">{items.length ? '没有符合条件的' + directory.noun : '暂无' + directory.noun}</p>}
    </section>
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
    <label className="field admin-field-wide">匹配 Topics<input placeholder="用逗号分隔" value={item.anyTopics.join(',')}
      onChange={(e) => edit({ ...item, anyTopics: e.target.value.split(/[,，]/) })} /></label>
  </>,
  status: (item) => <dl className="definition-list">
    <div><dt>分类标识</dt><dd><code>{item.slug}</code></dd></div>
    <div><dt>匹配 Topics</dt><dd>{number(item.anyTopics.length)}</dd></div>
  </dl>,
}

const sourceNames = { search: 'GitHub Search', trending: 'GitHub Trending' }
const enabledBadge = (enabled: boolean) => <Badge tone={enabled ? 'ok' : 'neutral'}>{enabled ? '已启用' : '已停用'}</Badge>
const rules: Directory<RuleSetting> = {
  path: '/system/discovery-rules', title: '发现规则', noun: '规则',
  identity: (item) => item.sourceType + ':' + item.sourceKey,
  name: (item) => item.sourceKey, caption: (item) => item.sourceKey,
  search: (item) => [item.sourceKey, item.query, sourceNames[item.sourceType]].join(' '),
  columns: ['来源类型', '状态', '扫描间隔', '优先级', '搜索表达式'],
  cells: (item) => [<Badge>{sourceNames[item.sourceType]}</Badge>, enabledBadge(item.enabled),
    number(item.intervalMinutes) + ' 分钟', number(item.priority), item.query ? <code>{item.query}</code> : '—'],
  metrics: (items) => [
    { label: '全部', value: number(items.length) },
    { label: '启用', value: number(items.filter((item) => item.enabled).length) },
    { label: 'GitHub Search', value: number(items.filter((item) => item.sourceType === 'search').length) },
    { label: 'GitHub Trending', value: number(items.filter((item) => item.sourceType === 'trending').length) },
  ],
  initial: { sourceType: 'search', sourceKey: '', query: '', intervalMinutes: 30, priority: 20, enabled: true },
  enabled: (item) => item.enabled,
  filter: { label: '来源类型', options: [
    { value: '', label: '全部类型' }, { value: 'search', label: 'GitHub Search' }, { value: 'trending', label: 'GitHub Trending' },
  ], match: (item, filter) => item.sourceType === filter },
  fields: (item, edit) => <>
    <label className="field">规则标识<input required value={item.sourceKey} placeholder={item.sourceType === 'trending' ? 'all:daily' : '例如 coding'}
      onChange={(e) => edit({ ...item, sourceKey: e.target.value })} /></label>
    <label className="field">来源类型<select value={item.sourceType} onChange={(e) => edit({
      ...item, sourceType: e.target.value === 'trending' ? 'trending' : 'search', sourceKey: '', query: '',
    })}><option value="search">GitHub Search</option><option value="trending">GitHub Trending</option></select></label>
    <NumberInput label="扫描间隔（分钟）" value={item.intervalMinutes} onChange={(intervalMinutes) => edit({ ...item, intervalMinutes })} />
    <NumberInput label="优先级" value={item.priority} onChange={(priority) => edit({ ...item, priority })} />
    {item.sourceType === 'search' && <label className="field admin-field-wide">搜索表达式
      <input required value={item.query} onChange={(e) => edit({ ...item, query: e.target.value })} />
    </label>}
    <label className="check-field"><input type="checkbox" checked={item.enabled} onChange={(e) => edit({ ...item, enabled: e.target.checked })} />启用规则</label>
  </>,
  status: (item) => <dl className="definition-list">
    <div><dt>规则状态</dt><dd>{enabledBadge(item.enabled)}</dd></div>
    <div><dt>来源类型</dt><dd>{sourceNames[item.sourceType]}</dd></div>
    <div><dt>扫描间隔</dt><dd>{number(item.intervalMinutes)} 分钟</dd></div>
  </dl>,
}

export function CategoryEditor() {
  return <SettingsDirectory directory={categories} />
}

export function DiscoveryTabEditor() {
  const path = '/system/discovery-tabs'
  const query = useConfig<DiscoveryTabSettings>(path)
  return <ConfigEditor<DiscoveryTabSettings, DiscoveryTab[]> path={path} query={query} heading={() => <h1>发现页标签</h1>}
    apply={(server, items) => ({ ...server, items })}
    payload={({ revision, items }) => ({ revision, items })}
    fields={(server, draft, edit) => <DiscoveryTabFields options={server.options} items={draft ?? server.items} edit={edit} />} />
}

function DiscoveryTabFields({ items, options, edit }: Pick<DiscoveryTabSettings, 'items' | 'options'> & { edit: (items: DiscoveryTab[]) => void }) {
  const available = options.filter((option) => !items.some(({ slug }) => slug === option.slug))
  const names = new Map(options.map(({ slug, name }) => [slug, name]))

  function move(index: number, offset: number) {
    const next = items.slice()
    const [item] = next.splice(index, 1)
    next.splice(index + offset, 0, item)
    edit(next)
  }

  // 「全部」是默认入口，只有分类标签可以调整。
  return <>
    <p className="minor muted">「全部」始终显示在第一位。</p>
    {items.length > 0 && <ol className="discovery-tab-settings">
      {items.map((item, index) => {
        const name = names.get(item.slug) ?? item.slug
        return <li key={item.slug}>
          <label className="field">{name}
            <input aria-label={name + '的标签名称'} placeholder={name} value={item.label} maxLength={24}
              onChange={(event) => edit(items.map((tab) => tab.slug === item.slug ? { ...tab, label: event.target.value } : tab))} />
          </label>
          <div className="action-row">
            <button type="button" className="icon-button" aria-label={'上移 ' + name} title="上移" disabled={index === 0} onClick={() => move(index, -1)}><ArrowUp size={16} aria-hidden /></button>
            <button type="button" className="icon-button" aria-label={'下移 ' + name} title="下移" disabled={index === items.length - 1} onClick={() => move(index, 1)}><ArrowDown size={16} aria-hidden /></button>
            <button type="button" className="icon-button" aria-label={'移除 ' + name} title="移除" onClick={() => edit(items.filter(({ slug }) => slug !== item.slug))}><X size={16} aria-hidden /></button>
          </div>
        </li>
      })}
    </ol>}
    <label className="field discovery-tab-add">添加标签
      <select value="" disabled={available.length === 0} onChange={(event) => {
        if (event.target.value) edit([...items, { slug: event.target.value, label: '' }])
      }}>
        <option value="">{available.length ? '选择分类' : '已添加全部分类'}</option>
        {available.map(({ slug, name }) => <option key={slug} value={slug}>{name}</option>)}
      </select>
    </label>
  </>
}

export function RulesEditor() {
  return <SettingsDirectory directory={rules} />
}
