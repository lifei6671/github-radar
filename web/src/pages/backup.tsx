import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CloudUpload, DatabaseBackup } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { api, ApiError, errorText } from '../api/client'
import { AdminPanel } from '../components/admin'
import { Badge, Dialog, Empty, More, Notice, PageError, Skeleton, Spinner } from '../components/ui'
import { formatBytes, timestamp } from '../lib/format'

type Connection = { endpoint: string; bucket: string; prefix: string; accessKeyId: string; secretAccessKey: string }
type BackupSettings = {
  enabled: boolean
  schedule: { hour: number; minute: number; timezone: string }
  retentionDays: number
  r2: Pick<Connection, 'endpoint' | 'bucket' | 'prefix'> & { configured: boolean; accessKeyIdMasked: string }
}
type Backup = { objectKey: string; date: string; size: number; schemaVersion: number; generatedAt: string | null }
type BackupList = { items: Backup[]; nextCursor: string | null }
type Operation = {
  id: string
  kind: 'backup' | 'restore' | 'cleanup'
  status: 'running' | 'completed' | 'failed'
  phase: 'prepare' | 'drain' | 'copy' | 'reload' | 'done'
  databaseApplied: boolean
  errorCode?: string
  warning?: string
}
type BackupStatus = {
  initialized: boolean
  restoring: boolean
  maintenance: boolean
  lastBackup: Backup | null
  lastBackupOperation: Operation | null
  lastCleanup: Operation | null
  lastRestore: Operation | null
  nextBackupAt: string | null
  nextCleanupAt: string | null
  autoAIResumeRequired: boolean
  restartRequired: boolean
}
type Accepted = { operationId: string; statusUrl: string }
type SetupSession = { sessionId: string; expiresAt: string }

const backupPath = '/system/backup'
const statusKey = ['backup-status']
const emptyConnection: Connection = { endpoint: '', bucket: '', prefix: 'github-radar/backups/', accessKeyId: '', secretAccessKey: '' }
const restorePhases = { prepare: '正在下载和校验备份', drain: '正在暂停业务访问', copy: '正在恢复数据', reload: '正在加载恢复后的配置', done: '恢复完成' }

export function useBackupStatus(enabled = true) {
  return useQuery({
    queryKey: statusKey,
    queryFn: ({ signal }) => api<BackupStatus>(backupPath + '/status', { signal }),
    refetchInterval: (query) => query.state.data?.restoring ? 2000 : 10_000,
    staleTime: 0,
    enabled,
  })
}

// 业务查询跟随全局恢复凭证，即使操作发起后已经离开备份页。
export function useRestoredData(state: BackupStatus | undefined) {
  const client = useQueryClient()
  const applied = useRef('')
  const restore = state?.lastRestore
  useLayoutEffect(() => {
    if (restore?.status !== 'completed' || !restore.databaseApplied || applied.current === restore.id) return
    applied.current = restore.id
    void client.resetQueries({ predicate: ({ queryKey }) =>
      !['setup', 'backup-operation', 'backup-status'].includes(String(queryKey[0])),
    })
    // 防止恢复提交前发出的安装查询迟到并覆盖已提交状态。
    void client.cancelQueries({ queryKey: ['setup'], exact: true })
    client.setQueryData(['setup'], { initialized: true })
  }, [client, restore])
}

function ConnectionFields({ value, onChange, fixedPrefix = false }: { value: Connection; onChange: (value: Connection) => void; fixedPrefix?: boolean }) {
  function field(key: keyof Connection, label: string, type = 'text', required = true) {
    return <label className="field">{label}
      <input type={type} value={value[key]} onChange={(event) => onChange({ ...value, [key]: event.target.value })}
        autoComplete={key === 'secretAccessKey' ? 'new-password' : 'off'} spellCheck={false} required={required} />
    </label>
  }
  return <>
    {field('endpoint', 'R2 端点', 'url')}
    {fixedPrefix ? field('bucket', '存储桶') :
      <div className="two-fields">{field('bucket', '存储桶')}{field('prefix', '备份前缀', 'text', false)}</div>}
    {field('accessKeyId', 'Access Key ID')}
    {field('secretAccessKey', 'Secret Access Key', 'password')}
  </>
}

function useConnectionTest(path: string) {
  return useMutation({ mutationFn: (body: Connection) => api<{ ok: boolean }>(path, { method: 'POST', body, timeoutMs: 30_000 }) })
}

type OperationRequest = { kind: 'backup' | 'restore'; previousId: string }

function useOperationSelection(scope: string, state: BackupStatus | undefined) {
  const key = 'radar.backup-operation.' + scope
  const [id, setId] = useState(() => {
    try { return sessionStorage.getItem(key) ?? '' } catch { return '' }
  })
  const [pending, setPending] = useState<OperationRequest | null>(() => {
    try { return JSON.parse(sessionStorage.getItem(key + '.pending') ?? 'null') as OperationRequest | null } catch { return null }
  })
  function setRequest(value: OperationRequest | null) {
    setPending(value)
    try {
      if (value) sessionStorage.setItem(key + '.pending', JSON.stringify(value))
      else sessionStorage.removeItem(key + '.pending')
    } catch { /* 状态仍保留在当前页面。 */ }
  }
  function select(value: string) {
    setId(value)
    setRequest(null)
    // 浏览器仅保留操作 ID，用于断线或刷新后继续查询。
    try {
      if (value) sessionStorage.setItem(key, value)
      else sessionStorage.removeItem(key)
    } catch { /* 当前页面仍可查询。 */ }
  }
  const observed = pending?.kind === 'restore' ? state?.lastRestore : state?.lastBackupOperation
  const recovered = pending && observed && observed.id !== pending.previousId ? observed.id : ''
  useEffect(() => {
    if (recovered) select(recovered)
    // select 只依赖当前操作 ID 和本页面固定的存储键。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [recovered])
  return {
    id: recovered || id, pending: Boolean(pending && !recovered),
    begin: (kind: OperationRequest['kind']) => setRequest({ kind, previousId: (kind === 'restore' ? state?.lastRestore : state?.lastBackupOperation)?.id ?? '' }),
    select, clear: () => setRequest(null),
  }
}

function uncertain(error: unknown) { return error instanceof ApiError && (error.status === 0 || error.status >= 500) }

function PendingOperation({ refresh, dismiss }: {
  refresh: () => Promise<{ isSuccess: boolean }>
  dismiss: () => void
}) {
  const [checked, setChecked] = useState(false)
  const check = useMutation({ mutationFn: refresh, onSuccess: (value) => setChecked(value.isSuccess) })
  return <div className="backup-stack">
    <Notice>正在核对任务是否已受理。</Notice>
    <div className="action-row">
      <button className="button" disabled={check.isPending} onClick={() => check.mutate()}>刷新任务状态</button>
      {checked && <button className="button" onClick={dismiss}>结束核对</button>}
    </div>
  </div>
}

function OperationProgress({ id, published, setup = false, onDone }: {
  id: string
  published?: Operation | null
  setup?: boolean
  onDone?: () => void
}) {
  const client = useQueryClient()
  const handled = useRef('')
  const query = useQuery({
    queryKey: ['backup-operation', id],
    queryFn: ({ signal }) => api<Operation>((setup ? '/setup' : backupPath) + '/operations/' + encodeURIComponent(id), { signal }),
    staleTime: 0,
    refetchInterval: (current) => {
      if (current.state.error instanceof ApiError && current.state.error.status === 404) return false
      return current.state.data?.status === 'failed' || current.state.data?.status === 'completed' ? false : 2000
    },
  })
  const op = query.data
  const missing = query.error instanceof ApiError && query.error.status === 404
  const publishedStatus = published?.id === id ? published.status : undefined
  const refetch = query.refetch
  useEffect(() => {
    // 重载 POST 的响应可能丢失，状态变化时继续查询原操作。
    if (publishedStatus && !missing) void refetch()
  }, [publishedStatus, missing, refetch])
  const reload = useMutation({
    mutationFn: () => api<Accepted>(backupPath + '/reload', { method: 'POST' }),
    onSettled: () => { void query.refetch(); void client.invalidateQueries({ queryKey: statusKey }) },
  })
  useEffect(() => {
    const result = missing ? 'missing' : op?.status
    if (!result || result === 'running' || handled.current === result) return
    handled.current = result
    if (result === 'completed' && op?.kind === 'restore') {
      void client.cancelQueries({ queryKey: ['setup'], exact: true })
      client.setQueryData(['setup'], { initialized: true })
      onDone?.()
    } else if (result === 'missing') {
      void client.invalidateQueries({ queryKey: ['setup'] })
    } else if (result === 'completed') {
      void client.invalidateQueries({ queryKey: ['backup-list'] })
    }
    void client.invalidateQueries({ queryKey: statusKey })
  }, [client, missing, onDone, op])
  if (missing) return <Notice error>无法确认该任务结果，已刷新系统状态。</Notice>
  if (query.error) return <Notice error>暂时无法查询进度，连接恢复后会继续查询。</Notice>
  if (!op) return <Notice>正在查询任务进度。</Notice>
  if (op.status === 'running') return <Notice>
    {op.kind === 'restore' ? restorePhases[op.phase] : op.kind === 'backup' ? '正在备份' : '正在清理备份'}
    {op.kind === 'restore' && ['drain', 'copy', 'reload'].includes(op.phase) && '，恢复期间暂不可访问项目数据'}。
  </Notice>
  if (op.status === 'completed') return <Notice>
    {op.kind === 'restore' ? '恢复完成。' : op.kind === 'backup' ? '备份完成。' : '清理完成。'}
    {op.warning === 'backup_cleanup_failed' && '保留清理失败，将在下次清理时重试。'}
  </Notice>
  return <div className="backup-stack">
    <Notice error>{errorText(new ApiError(500, { code: op.errorCode }))}</Notice>
    {op.databaseApplied && op.errorCode === 'restore_reload_failed' &&
      <button className="button" disabled={reload.isPending || query.isFetching} onClick={() => reload.mutate()}>重试加载</button>}
    {reload.error && <Notice error>{errorText(reload.error)}</Notice>}
  </div>
}

function BackupHistory({ sessionId, disabled, onRestore }: {
  sessionId?: string
  disabled?: boolean
  onRestore: (backup: Backup) => void
}) {
  const [selectedKey, setSelectedKey] = useState('')
  const list = useInfiniteQuery({
    queryKey: ['backup-list', sessionId ?? 'system'],
    queryFn: ({ pageParam, signal }) => {
      const path = sessionId ? '/setup/backups' : backupPath + '/list'
      const search = new URLSearchParams({ limit: '15' })
      if (pageParam) search.set('cursor', pageParam)
      return api<BackupList>(path + '?' + search, { setupSession: sessionId, signal, timeoutMs: 30_000 })
    },
    initialPageParam: '',
    getNextPageParam: (last) => last.nextCursor ?? undefined,
    enabled: !disabled,
  })
  const items = list.data?.pages.flatMap((page) => page.items) ?? []
  if (disabled && !list.data) return null
  if (list.isPending) return <Skeleton rows={2} />
  if (!list.data) return <PageError error={list.error} retry={() => { void list.refetch() }} />
  if (!items.length) return <>{list.error && <Notice error>{errorText(list.error)}</Notice>}
    {sessionId ? <Empty title="暂无云端备份" /> : <p className="admin-empty">暂无云端备份</p>}</>
  const selected = items.find((item) => item.objectKey === selectedKey) ?? items[0]
  const more = !disabled && <More hasMore={list.hasNextPage} pending={list.isFetchingNextPage} onClick={() => { void list.fetchNextPage() }} />
  // 初始化仍以单选方式选择备份，后台列表逐条提供恢复入口。
  if (!sessionId) return <>
    {list.error && <Notice error>{errorText(list.error)}</Notice>}
    <div className="admin-table-scroll"><table className="admin-table">
      <thead><tr><th scope="col">备份日期</th><th scope="col">文件大小</th><th scope="col">生成时间</th><th scope="col">结构版本</th><th scope="col">操作</th></tr></thead>
      <tbody>{items.map((item) => <tr key={item.objectKey}>
        <td className="numeric">{item.date}</td><td>{formatBytes(item.size)}</td><td>{timestamp(item.generatedAt)}</td><td>{item.schemaVersion || '—'}</td>
        <td><button className="button" disabled={disabled || !item.schemaVersion} onClick={() => onRestore(item)} aria-label={'恢复 ' + item.date}>恢复</button></td>
      </tr>)}</tbody>
    </table></div>
    {more}
  </>
  return <>
    {list.error && <Notice error>{errorText(list.error)}</Notice>}
    <ul className="backup-history">
      {items.map((item) => {
        const record = <div className="backup-record">
          <strong className="numeric">{item.date}</strong>
          <div className="backup-meta muted minor">
            <span>{formatBytes(item.size)}</span><span>生成于 {timestamp(item.generatedAt)}</span>
            <span>结构版本 {item.schemaVersion || '—'}</span>
          </div>
        </div>
        return <li key={item.objectKey}>
          <label className="backup-option">
            <input type="radio" name="setup-backup" aria-label={'选择 ' + item.date}
              disabled={disabled || !item.schemaVersion} checked={selected.objectKey === item.objectKey}
              onChange={() => setSelectedKey(item.objectKey)} />
            {record}
          </label>
        </li>
      })}
    </ul>
    {more}
    <button className="button primary" disabled={disabled || !selected.schemaVersion}
      aria-label={'恢复 ' + selected.date} onClick={() => onRestore(selected)}>恢复所选备份</button>
  </>
}

function RestoreDialog({ backup, saved, sessionId, close, accepted, begin, rejected }: {
  backup: Backup
  saved?: BackupSettings['r2']
  sessionId?: string
  close: () => void
  accepted: (id: string) => void
  begin: () => void
  rejected: () => void
}) {
  const client = useQueryClient()
  const [replace, setReplace] = useState(false)
  const [r2, setR2] = useState<Connection>(() => saved ? { ...emptyConnection, endpoint: saved.endpoint, bucket: saved.bucket, prefix: saved.prefix } : emptyConnection)
  const restore = useMutation({
    mutationFn: () => api<Accepted>(sessionId ? '/setup/restore' : backupPath + '/restore', {
      method: 'POST',
      body: sessionId ? { objectKey: backup.objectKey, sessionId } : { objectKey: backup.objectKey, ...(replace ? { r2 } : {}) },
    }),
    onMutate: begin,
    onSuccess: (result) => { accepted(result.operationId); close() },
    onError: (error) => {
      if (uncertain(error)) close()
      else rejected()
      void client.invalidateQueries({ queryKey: statusKey })
    },
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (!restore.isPending) restore.mutate()
  }
  return <Dialog title={'恢复 ' + backup.date + ' 的备份'} onClose={() => { if (!restore.isPending) close() }}>
    <form className="dialog-form" onSubmit={submit}>
      <Notice>备份后的数据会丢失，历史 AI 调用可能已计费。</Notice>
      {saved && <label className="check-label"><input type="checkbox" checked={replace} onChange={(event) => setReplace(event.target.checked)} />用新 R2 连接读取此备份</label>}
      {replace && <ConnectionFields value={r2} onChange={(value) => setR2({ ...value, prefix: r2.prefix })} fixedPrefix />}
      {restore.error && <Notice error>{errorText(restore.error)}</Notice>}
      <div className="action-row">
        <button type="button" className="button" onClick={close} disabled={restore.isPending} data-autofocus>取消</button>
        <button className="button primary" disabled={restore.isPending}>{restore.isPending && <Spinner />}确认恢复</button>
      </div>
    </form>
  </Dialog>
}

function BackupEditor({ settings, disabled }: { settings: BackupSettings; disabled: boolean }) {
  const client = useQueryClient()
  const [editing, setEditing] = useState(!settings.r2.configured)
  const [enabled, setEnabled] = useState(settings.enabled)
  const [schedule, setSchedule] = useState(settings.schedule)
  const [retentionDays, setRetention] = useState(settings.retentionDays)
  const [r2, setR2] = useState<Connection>(() => ({ ...emptyConnection, endpoint: settings.r2.endpoint, bucket: settings.r2.bucket, prefix: settings.r2.prefix }))
  const test = useConnectionTest(backupPath + '/test')
  const save = useMutation({
    mutationFn: () => api<BackupSettings>(backupPath + '/settings', {
      method: 'PUT', body: { enabled, schedule, retentionDays, ...(editing ? { r2 } : {}) }, timeoutMs: 30_000,
    }),
    onSuccess: (data) => {
      setR2({ ...emptyConnection, endpoint: data.r2.endpoint, bucket: data.r2.bucket, prefix: data.r2.prefix })
      setEditing(false)
      test.reset()
      client.removeQueries({ queryKey: ['backup-list', 'system'], type: 'inactive' })
      void client.invalidateQueries({ queryKey: ['backup-list', 'system'] })
      client.setQueryData(['backup-settings'], data)
      void client.invalidateQueries({ queryKey: statusKey })
    },
  })
  const busy = disabled || save.isPending || test.isPending
  const scheduled = String(schedule.hour).padStart(2, '0') + ':' + String(schedule.minute).padStart(2, '0')
  return <form className="backup-stack" onSubmit={(event) => { event.preventDefault(); if (!busy) save.mutate() }}>
    <fieldset className="backup-stack" disabled={busy}>
      <label className="check-label"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />启用每日备份</label>
      <div className="two-fields">
        <label className="field">每日时间<input type="time" value={scheduled} required onChange={(event) => {
          const [hour, minute] = event.target.value.split(':').map(Number)
          if (hour !== undefined && minute !== undefined) setSchedule({ ...schedule, hour, minute })
        }} /></label>
        <label className="field">保留天数<input type="number" min="1" max="30" required value={Number.isFinite(retentionDays) ? retentionDays : ''}
          onChange={(event) => setRetention(event.target.valueAsNumber)} /></label>
      </div>
      <label className="field">备份时区<input value={schedule.timezone} placeholder="Asia/Shanghai" required
        onChange={(event) => setSchedule({ ...schedule, timezone: event.target.value })} /></label>
      {settings.r2.configured && <dl className="definition-list">
        <div><dt>存储桶</dt><dd>{settings.r2.bucket}</dd></div>
        <div><dt>备份前缀</dt><dd>{settings.r2.prefix}</dd></div>
        <div><dt>Access Key ID</dt><dd>{settings.r2.accessKeyIdMasked}</dd></div>
      </dl>}
      {!editing && <button type="button" className="button" onClick={() => setEditing(true)}>更新 R2 连接</button>}
      {editing && <ConnectionFields value={r2} onChange={(value) => { setR2(value); test.reset() }} />}
      <div className="action-row">
        {editing && <button type="button" className="button" disabled={!r2.endpoint || !r2.bucket || !r2.accessKeyId || !r2.secretAccessKey}
          onClick={() => test.mutate(r2)}>{test.isPending && <Spinner />}测试连接</button>}
        {editing && settings.r2.configured && <button type="button" className="button" onClick={() => {
          setEditing(false); setR2({ ...emptyConnection, endpoint: settings.r2.endpoint, bucket: settings.r2.bucket, prefix: settings.r2.prefix }); test.reset()
        }}>取消更新连接</button>}
        <button className="button primary">{save.isPending && <Spinner />}保存配置</button>
      </div>
    </fieldset>
    {test.isSuccess && <Notice>R2 连接测试通过。</Notice>}
    {test.error && <Notice error>{errorText(test.error)}</Notice>}
    {save.error && <Notice error>{errorText(save.error)}</Notice>}
  </form>
}

function ResumeAI() {
  const client = useQueryClient()
  const [confirm, setConfirm] = useState(false)
  const resume = useMutation({
    mutationFn: () => api('/system/ai/resume', { method: 'POST', body: { acceptIncompleteUsage: true } }),
    onSuccess: () => { setConfirm(false); void client.invalidateQueries({ queryKey: statusKey }) },
  })
  return <section className="panel system-panel">
    <header className="section-head"><h2>自动 AI 摘要</h2><Badge tone="warning">已暂停</Badge></header>
    <button className="button" onClick={() => setConfirm(true)}>启用自动摘要</button>
    {confirm && <Dialog title="启用自动 AI 摘要" onClose={() => { if (!resume.isPending) setConfirm(false) }}>
      <div className="dialog-form">
        <Notice>恢复前的 AI 用量记录可能不完整。后续调用按恢复库的用量记录计入预算。</Notice>
        {resume.error && <Notice error>{errorText(resume.error)}</Notice>}
        <div className="action-row">
          <button className="button" disabled={resume.isPending} onClick={() => setConfirm(false)} data-autofocus>取消</button>
          <button className="button primary" disabled={resume.isPending} onClick={() => resume.mutate()}>确认启用</button>
        </div>
      </div>
    </Dialog>}
  </section>
}

export function BackupPage() {
  const client = useQueryClient()
  const status = useBackupStatus()
  const [picked, setPicked] = useState<Backup | null>(null)
  const state = status.data
  const selection = useOperationSelection('system', state)
  const busy = Boolean(selection.pending || state?.restoring || state?.maintenance || state?.lastBackupOperation?.status === 'running' || state?.lastCleanup?.status === 'running')
  const settings = useQuery({
    queryKey: ['backup-settings'],
    queryFn: ({ signal }) => api<BackupSettings>(backupPath + '/settings', { signal }),
    enabled: Boolean(state && !state.maintenance),
  })
  const run = useMutation({
    mutationFn: () => api<Accepted>(backupPath + '/run', { method: 'POST' }),
    onMutate: () => selection.begin('backup'),
    onSuccess: (result) => { selection.select(result.operationId); void client.invalidateQueries({ queryKey: statusKey }) },
    onError: (error) => {
      if (!uncertain(error)) selection.clear()
      void client.invalidateQueries({ queryKey: statusKey })
    },
  })
  if (!state) return status.isPending ? <Skeleton /> : <PageError error={status.error} retry={() => { void status.refetch() }} />
  const tracked = state.restoring || state.maintenance ? state.lastRestore?.id :
    selection.id || state.lastBackupOperation?.id || state.lastRestore?.id
  const published = state.lastRestore?.id === tracked ? state.lastRestore : state.lastBackupOperation
  return <div className="backup-stack">
    {status.error && <Notice error>{errorText(status.error)}</Notice>}
    {selection.pending && !run.isPending && !picked && <PendingOperation refresh={status.refetch} dismiss={selection.clear} />}
    {tracked && <OperationProgress key={tracked} id={tracked} published={published} />}
    {state.restartRequired && <Notice>恢复的运行配置将在重启服务后全部生效。</Notice>}
    {!state.maintenance && !state.restoring && state.autoAIResumeRequired && <ResumeAI />}
    <div className="admin-form-grid">
      <AdminPanel title="备份配置" action={settings.data && <Badge tone={settings.data.r2.configured ? 'ok' : 'neutral'}>
        {settings.data.r2.configured ? 'R2 已配置' : 'R2 待配置'}
      </Badge>}>
        <div className="admin-panel-body">
          {state.maintenance ? <Notice>恢复期间暂不可访问项目数据。</Notice> : settings.data ?
            <BackupEditor key={JSON.stringify([settings.data.enabled, settings.data.schedule, settings.data.retentionDays, settings.data.r2])}
              settings={settings.data} disabled={busy || run.isPending} /> :
            settings.isPending ? <Skeleton rows={2} /> : <PageError error={settings.error} retry={() => { void settings.refetch() }} />}
        </div>
      </AdminPanel>
      <AdminPanel title="备份状态">
        <div className="admin-panel-body">
          <dl className="definition-list">
            <div><dt>最近成功</dt><dd>{timestamp(state.lastBackup?.generatedAt)}</dd></div>
            <div><dt>下次备份</dt><dd>{timestamp(state.nextBackupAt)}</dd></div>
            <div><dt>下次清理</dt><dd>{timestamp(state.nextCleanupAt)}</dd></div>
          </dl>
          {state.lastCleanup?.status === 'failed' && <Notice error>最近一次保留清理失败。</Notice>}
        </div>
      </AdminPanel>
    </div>
    <AdminPanel title="云端备份" action={<button className="button" disabled={busy || run.isPending || !settings.data?.r2.configured} onClick={() => run.mutate()}>
      {run.isPending ? <Spinner /> : <CloudUpload size={16} />}立即备份
    </button>}>
      {run.error && !uncertain(run.error) && <Notice error>{errorText(run.error)}</Notice>}
      {settings.data?.r2.configured ? <BackupHistory disabled={busy || run.isPending} onRestore={setPicked} /> :
        <p className="admin-empty">{settings.isPending && !state.maintenance ? '正在读取备份配置' : state.maintenance ? '恢复期间暂停读取备份列表' : '配置 R2 后可查看备份'}</p>}
    </AdminPanel>
    {picked && settings.data && <RestoreDialog backup={picked} saved={settings.data.r2} close={() => setPicked(null)}
      begin={() => selection.begin('restore')} rejected={selection.clear}
      accepted={(value) => { selection.select(value); void client.invalidateQueries({ queryKey: statusKey }) }} />}
  </div>
}

export function SetupBackup({ initialized, onDone, onRestoring }: {
  initialized: boolean
  onDone: () => void
  onRestoring: (value: boolean) => void
}) {
  const client = useQueryClient()
  const status = useBackupStatus()
  const selection = useOperationSelection('setup', status.data)
  const [r2, setR2] = useState(emptyConnection)
  const [session, setSession] = useState<SetupSession | null>(null)
  const [picked, setPicked] = useState<Backup | null>(null)
  const [expanded, setExpanded] = useState(false)
  const test = useConnectionTest('/setup/r2/test')
  const connect = useMutation({
    mutationFn: () => api<SetupSession>('/setup/r2/session', { method: 'POST', body: r2, timeoutMs: 30_000 }),
    onSuccess: (result) => { setSession(result); setR2(emptyConnection); test.reset() },
  })
  const restoring = Boolean(status.data?.restoring || status.data?.maintenance)
  const locked = restoring || selection.pending || connect.isPending || test.isPending
  useEffect(() => { onRestoring(Boolean(locked)) }, [locked, onRestoring])
  const tracked = restoring ? status.data?.lastRestore?.id : selection.id
  if (initialized && !tracked) return null
  const submitting = connect.isPending || test.isPending
  return <section className="panel setup-card">
    <header className="section-head"><h2><DatabaseBackup size={20} />从 R2 恢复</h2></header>
    {selection.pending && !picked && <PendingOperation refresh={status.refetch} dismiss={selection.clear} />}
    {tracked && <OperationProgress key={tracked} id={tracked} published={status.data?.lastRestore} setup onDone={onDone} />}
    {!restoring && !initialized && (session ?
      <>
        <div className="action-row">
          <button className="button" onClick={() => { setSession(null); connect.reset() }}>更换 R2 连接</button>
          <span className="muted minor">会话有效至 {timestamp(session.expiresAt)}</span>
        </div>
        <BackupHistory sessionId={session.sessionId} disabled={selection.pending} onRestore={setPicked} />
      </> : expanded ?
      <form className="backup-stack" onSubmit={(event) => { event.preventDefault(); if (!submitting) connect.mutate() }}>
        <fieldset className="backup-stack" disabled={submitting}>
          <ConnectionFields value={r2} onChange={(value) => { setR2(value); test.reset() }} />
          <div className="action-row">
            <button type="button" className="button" disabled={!r2.endpoint || !r2.bucket || !r2.accessKeyId || !r2.secretAccessKey} onClick={() => test.mutate(r2)}>测试连接</button>
            <button className="button primary">{connect.isPending && <Spinner />}连接并查看备份</button>
          </div>
        </fieldset>
        {test.isSuccess && <Notice>R2 连接测试通过。</Notice>}
        {(test.error || connect.error) && <Notice error>{errorText(connect.error || test.error)}</Notice>}
      </form> : <button className="button" onClick={() => setExpanded(true)}>配置 R2 连接</button>)}
    {picked && session && <RestoreDialog backup={picked} sessionId={session.sessionId} close={() => setPicked(null)}
      begin={() => selection.begin('restore')} rejected={selection.clear}
      accepted={(value) => { selection.select(value); onRestoring(true); void client.invalidateQueries({ queryKey: statusKey }) }} />}
  </section>
}
