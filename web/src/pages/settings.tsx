import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Plus, Save, Trash2 } from 'lucide-react'
import { useState } from 'react'
import type { ReactNode } from 'react'
import { api, ApiError, errorText } from '../api/client'
import type { CategorySetting, ListSettings, RuleSetting, RuntimeSettings, SettingFields, SettingValue } from '../api/types'
import { Badge, Notice, PageError, Skeleton, Spinner } from '../components/ui'

type Snapshot = { revision: number }
type Editor<T> = (value: T, update: (next: T) => void) => ReactNode

function ConfigEditor<T extends Snapshot>({ scope, path, editor, payload, footer }: {
  scope: string
  path: string
  editor: Editor<T>
  payload: (value: T) => unknown
  footer?: (value: T) => ReactNode
}) {
  const queryKey = ['settings', scope]
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<T | null>(null)
  const [saved, setSaved] = useState(false)
  const query = useQuery({ queryKey, queryFn: ({ signal }) => api<T>(path, { signal }), staleTime: 0 })
  const save = useMutation({
    mutationFn: (value: T) => api<T>(path, { method: 'PUT', body: payload(value) }),
    onSuccess: (value) => {
      queryClient.setQueryData(queryKey, value)
      setDraft(null)
      setSaved(true)
      void queryClient.invalidateQueries({ queryKey: ['settings'] })
      void queryClient.invalidateQueries({ queryKey: ['categories'] })
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
      void queryClient.invalidateQueries({ queryKey: ['trending'] })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'settings_conflict') void query.refetch()
    },
  })
  if (query.isPending) return <Skeleton />
  if (!query.data) return <PageError error={query.error} retry={() => { void query.refetch() }} />
  const server = query.data
  const value = draft ?? server
  const outdated = draft !== null && draft.revision !== server.revision
  function edit(next: T) {
    setDraft(next)
    setSaved(false)
  }

  return <form className="config-form" onSubmit={(event) => {
    event.preventDefault()
    if (draft && !outdated && !save.isPending) save.mutate(draft)
  }}>
    {footer?.(server)}
    <fieldset className="config-editable" disabled={save.isPending}>{editor(value, edit)}</fieldset>
    {query.error && <Notice error>{errorText(query.error)}</Notice>}
    {save.error && !outdated && <Notice error>{errorText(save.error)}</Notice>}
    {outdated && <div className="panel config-conflict">
      <Notice error>发现较新的配置版本。当前表单修改已保留。</Notice>
      <details>
        <summary>核对服务端配置（修订 {server.revision}）</summary>
        <pre>{JSON.stringify(payload(server), null, 2)}</pre>
      </details>
      <div className="action-row">
        <button className="button" type="button" disabled={save.isPending} onClick={() => { setDraft(null); save.reset() }}>载入最新版本</button>
        <button className="button" type="button" disabled={save.isPending} onClick={() => { edit({ ...value, revision: server.revision }); save.reset() }}>采用当前表单</button>
      </div>
      <p className="minor muted">采用当前表单后，保存将以表单内容覆盖此配置项。</p>
    </div>}
    <div className="config-footer">
      <span className="minor muted">修订 {value.revision}</span>
      {saved && <span role="status" className="tone-ok">已保存</span>}
      <button className="button primary" disabled={!draft || outdated || save.isPending}>
        {save.isPending ? <Spinner /> : <Save size={16} />}保存配置
      </button>
    </div>
  </form>
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
  return <ConfigEditor<RuntimeSettings> scope="runtime" path="/system/settings"
    payload={({ revision, settings }) => ({ revision, settings })}
    footer={(snapshot) => snapshot.restartRequired && <Notice>已保存的运行配置将在重启服务后生效。</Notice>}
    editor={(snapshot, update) => <div className="config-groups">
      {Object.entries(snapshot.settings).map(([key, value]) => <details className="panel config-group" key={key}>
        <summary><h2>{settingName(key)}</h2><ChevronDown size={17} /></summary>
        {value !== null && typeof value === 'object' && !Array.isArray(value)
          ? <Fields value={value} update={(next) => update({ ...snapshot, settings: { ...snapshot.settings, [key]: next } })} />
          : <Setting name={key} value={value} update={(next) => update({ ...snapshot, settings: { ...snapshot.settings, [key]: next } })} />}
      </details>)}
    </div>} />
}

function updateItem<T>(items: T[], index: number, value: T) {
  return items.map((item, i) => i === index ? value : item)
}

export function CategoryEditor() {
  return <ConfigEditor<ListSettings<CategorySetting>> scope="categories" path="/system/categories"
    payload={({ revision, items }) => ({ revision, items: items.map((item) => ({
      slug: item.slug.trim(), name: item.name.trim(), anyTopics: item.anyTopics.map((topic) => topic.trim()).filter(Boolean),
    })) })}
    editor={(snapshot, update) => <>
      <div className="config-rows">{snapshot.items.map((item, index) => {
        const change = (next: CategorySetting) => update({ ...snapshot, items: updateItem(snapshot.items, index, next) })
        return <div className="panel config-item" key={index}>
          <div className="settings-fields">
            <label className="field">分类标识<input required value={item.slug} onChange={(e) => change({ ...item, slug: e.target.value })} /></label>
            <label className="field">分类名称<input required value={item.name} onChange={(e) => change({ ...item, name: e.target.value })} /></label>
            <label className="field">匹配 Topics<input placeholder="用逗号分隔" value={item.anyTopics.join(',')} onChange={(e) => change({ ...item, anyTopics: e.target.value.split(/[,，]/) })} /></label>
          </div>
          <button className="icon-button tone-hot" type="button" aria-label={'移除分类 ' + (item.name || index + 1)}
            onClick={() => update({ ...snapshot, items: snapshot.items.filter((_, i) => i !== index) })}><Trash2 size={17} /></button>
        </div>
      })}</div>
      <button className="button" type="button" onClick={() => update({ ...snapshot, items: [...snapshot.items, { slug: '', name: '', anyTopics: [] }] })}><Plus size={16} />添加分类</button>
    </>} />
}

export function RulesEditor() {
  return <ConfigEditor<ListSettings<RuleSetting>> scope="rules" path="/system/discovery-rules"
    payload={({ revision, items }) => ({ revision, items })}
    editor={(snapshot, update) => <>
      <div className="config-rows">{snapshot.items.map((item, index) => {
        const change = (next: RuleSetting) => update({ ...snapshot, items: updateItem(snapshot.items, index, next) })
        return <div className="panel config-item rule-item" key={index}>
          <div className="settings-fields">
            <label className="field">来源类型<select value={item.sourceType} onChange={(e) => change({
              ...item, sourceType: e.target.value as RuleSetting['sourceType'], sourceKey: '', query: '',
            })}><option value="search">GitHub Search</option><option value="trending">GitHub Trending</option></select></label>
            <label className="field">规则标识<input required value={item.sourceKey} placeholder={item.sourceType === 'trending' ? 'all:daily' : '例如 coding'}
              onChange={(e) => change({ ...item, sourceKey: e.target.value })} /></label>
            {item.sourceType === 'search' && <label className="field">搜索表达式<input required value={item.query} onChange={(e) => change({ ...item, query: e.target.value })} /></label>}
            <NumberInput label="扫描间隔（分钟）" value={item.intervalMinutes} onChange={(intervalMinutes) => change({ ...item, intervalMinutes })} />
            <NumberInput label="优先级" value={item.priority} onChange={(priority) => change({ ...item, priority })} />
            <label className="check-field"><input type="checkbox" checked={item.enabled} onChange={(e) => change({ ...item, enabled: e.target.checked })} />启用规则</label>
          </div>
          <button className="icon-button tone-hot" type="button" aria-label={'移除规则 ' + (item.sourceKey || index + 1)}
            onClick={() => update({ ...snapshot, items: snapshot.items.filter((_, i) => i !== index) })}><Trash2 size={17} /></button>
        </div>
      })}</div>
      {!snapshot.items.length && <Badge>暂无发现规则</Badge>}
      <button className="button" type="button" onClick={() => update({
        ...snapshot, items: [...snapshot.items, { sourceType: 'search', sourceKey: '', query: '', intervalMinutes: 30, priority: 20, enabled: true }],
      })}><Plus size={16} />添加规则</button>
    </>} />
}
