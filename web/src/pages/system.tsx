import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUpRight, Wrench } from 'lucide-react'
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api, errorText } from '../api/client'
import type { Items, Job, Limits, StorageStatus } from '../api/types'
import { AdminMetrics, AdminPanel } from '../components/admin'
import { SystemHeader, systemPage } from '../components/system-navigation'
import { ActionButton, Badge, Dialog, Notice, PageError, Skeleton } from '../components/ui'
import { formatBytes, jobLabel, number, repoPath, timestamp } from '../lib/format'
import { CategoryEditor, DiscoveryTabEditor, ProjectThemeEditor, RulesEditor, RuntimeEditor, TechnicalTagEditor } from './settings'
import { BackupPage } from './backup'

export function System() {
  const [params] = useSearchParams()
  const tab = systemPage(params.get('tab'))
  return <div className="system-page">
    <SystemHeader tab={tab} />
    {tab === 'overview' && <Overview />}
    {tab === 'jobs' && <Jobs />}
    {tab === 'runtime' && <RuntimeEditor />}
    {tab === 'categories' && <CategoryEditor />}
    {tab === 'technical-tags' && <TechnicalTagEditor />}
    {tab === 'discovery-tabs' && <DiscoveryTabEditor />}
    {tab === 'project-themes' && <ProjectThemeEditor />}
    {tab === 'rules' && <RulesEditor />}
    {tab === 'backup' && <BackupPage />}
  </div>
}

const resourceNames: Record<string, string> = {
  core: 'GitHub Core', search: 'GitHub Search', repo_refresh: '仓库采样', readme_check: 'README 检查', star_history: 'Star 历史', github_secondary: '次级限流',
}

type RateResource = Limits['items'][number]
type RateRestriction = { until: string; label: string }

function resourceRestriction(item: RateResource, now: number): RateRestriction | null {
  // 和采集端一致，显式暂停与剩余额度耗尽都能阻止请求。
  const pausedUntil = item.pausedUntil && Date.parse(item.pausedUntil) > now ? item.pausedUntil : null
  if (item.remaining === 0 && Date.parse(item.resetAt) > now && (!pausedUntil || Date.parse(item.resetAt) > Date.parse(pausedUntil))) {
    return { until: item.resetAt, label: '额度用尽，重置于' }
  }
  return pausedUntil ? { until: pausedUntil, label: '暂停至' } : null
}

function Overview() {
  const queryClient = useQueryClient()
  const limits = useQuery({
    queryKey: ['limits'],
    queryFn: ({ signal }) => api<Limits>('/system/limits', { signal }),
    refetchInterval: 20_000,
  })
  const storage = useQuery({
    queryKey: ['storage'],
    queryFn: ({ signal }) => api<StorageStatus>('/system/storage', { signal }),
    refetchInterval: 20_000,
  })
  const jobs = useSystemJobs()
  const maintain = useMutation({
    mutationFn: () => api<{ status: string }>('/system/maintenance/run', { method: 'POST' }),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ['storage'] }) },
  })
  const rate = limits.data
  const disk = storage.data
  const now = Date.now()
  const resources = rate?.items.map((item) => ({ item, restriction: resourceRestriction(item, now) })) ?? []
  const blocked = resources.flatMap(({ item, restriction }) => restriction ? [{ item, ...restriction }] : [])
    .sort((a, b) => Date.parse(b.until) - Date.parse(a.until))[0]
  const core = rate?.items.find((item) => item.resource === 'core' && item.limit > 0)
  const configured = rate?.githubConfigured
  const collection = !rate ? '—' : !configured ? '采集待配置' : blocked ? '采集请求受限' : '采集已配置'
  const collectionNote = !rate ? null : !configured ? '缺少 GitHub 访问令牌' : blocked ?
    <>{resourceNames[blocked.item.resource] ?? blocked.item.resource} {blocked.label} <ObservedTime value={blocked.until} /></> :
    core ? <>GitHub Core {number(core.remaining)} / {number(core.limit)}</> : '等待额度观测'
  // 等待任务总数来自服务端，队列、异常和时间线只统计最近返回的任务。
  const recentJobs = jobs.data?.items.slice().sort((a, b) => Date.parse(b.updatedAt) - Date.parse(a.updatedAt) || b.id - a.id) ?? []
  const queued = recentJobs.filter((job) => job.status === 'pending' || job.status === 'running' || job.status === 'failed')
  const failed = recentJobs.filter((job) => job.status === 'failed' || job.status === 'dead' || job.status === 'unknown').length
  const latest = new Map<string, Job>()
  for (const job of recentJobs) {
    if (!latest.has(job.type)) latest.set(job.type, job)
  }
  return <div className="admin-stack">
    <AdminMetrics label="运行指标" items={[
      { label: 'GitHub 采集', value: collection, note: collectionNote, tone: rate ? configured && !blocked ? 'ok' : 'warning' : undefined },
      { label: '待处理任务', value: number(rate?.waitingTaskCount), note: '排队、运行与等待重试' },
      { label: '异常任务', value: jobs.data ? number(failed) : '—', note: '最近 50 条任务', tone: jobs.data ? failed ? 'warning' : 'ok' : undefined },
      { label: 'AI 自动调用', value: budgetValue(rate?.ai.auto) },
      { label: 'AI 手动调用', value: budgetValue(rate?.ai.manual) },
    ]} />
    {jobs.error && jobs.data && <Notice error>{errorText(jobs.error)}</Notice>}
    <div className="admin-panels">
      <AdminPanel title="队列">
        <JobFeed query={jobs} items={queued.slice(0, 5)} empty={rate?.waitingTaskCount ? '近期记录中没有待处理任务' : '暂无待处理任务'} />
      </AdminPanel>
      <AdminPanel title="各类任务最近状态">
        <JobFeed query={jobs} items={[...latest.values()]} empty="暂无任务记录" />
      </AdminPanel>
      <AdminPanel title="采集与模型额度" action={limits.dataUpdatedAt > 0 &&
        <span className="minor muted">最近检查 <ObservedTime value={new Date(limits.dataUpdatedAt).toISOString()} /></span>}>
        <div className="admin-panel-body">
          {limits.isPending ? <Skeleton rows={1} /> : rate ? <>
            {limits.error && <Notice error>{errorText(limits.error)}</Notice>}
            <dl className="definition-list">
              {!resources.length && <div><dt>GitHub 额度</dt><dd className="muted">暂无观测</dd></div>}
              {resources.map(({ item, restriction }) => <div key={item.resource}>
                <dt>{resourceNames[item.resource] ?? item.resource}</dt>
                <dd className="system-quota">
                  <strong className="numeric">{item.limit > 0 ? number(item.remaining) + ' / ' + number(item.limit) : number(item.localRequests) + ' 次'}</strong>
                  {restriction ? <span className="minor tone-hot">{restriction.label} <ObservedTime value={restriction.until} /></span> :
                    item.limit > 0 && Date.parse(item.resetAt) > 0 && <span className="minor muted">额度重置 <ObservedTime value={item.resetAt} /></span>}
                </dd>
              </div>)}
              <div><dt>AI 额度重置</dt><dd><ObservedTime value={rate.ai.resetAt} /></dd></div>
            </dl>
          </> : <PageError error={limits.error} retry={() => { void limits.refetch() }} />}
        </div>
      </AdminPanel>
      <AdminPanel title="存储与维护" action={<ActionButton disabled={!disk} pending={maintain.isPending}
        icon={<Wrench size={16} />} onClick={() => maintain.mutate()}>运行维护</ActionButton>}>
        <div className="admin-panel-body">
          {storage.isPending ? <Skeleton rows={1} /> : disk ? <>
            {storage.error && <Notice error>{errorText(storage.error)}</Notice>}
            <dl className="definition-list">
              <div><dt>SQLite / WAL</dt><dd className="numeric">{formatBytes(disk.databaseBytes)} / {formatBytes(disk.walBytes)}</dd></div>
              <div><dt>原始快照</dt><dd className="numeric">{number(disk.counts.raw)}</dd></div>
              <div><dt>小时 / 日聚合</dt><dd className="numeric">{number(disk.counts.hourly)} / {number(disk.counts.daily)}</dd></div>
              <div><dt>小时聚合水位</dt><dd><ObservedTime value={disk.watermarks.hourly} /></dd></div>
              <div><dt>日聚合水位</dt><dd><ObservedTime value={disk.watermarks.daily} /></dd></div>
              <div><dt>最近清理</dt><dd><ObservedTime value={disk.watermarks.cleanedAt} /></dd></div>
              <div><dt>最近指标计算</dt><dd><ObservedTime value={disk.watermarks.metricsUpdatedAt} /></dd></div>
            </dl>
            {maintain.data && <Notice>维护已排队。</Notice>}
            {maintain.error && <Notice error>{errorText(maintain.error)}</Notice>}
          </> : <PageError error={storage.error} retry={() => { void storage.refetch() }} />}
        </div>
      </AdminPanel>
      <AdminPanel title="任务时间线" wide action={<Link className="admin-link" to="/admin/runs?tab=jobs">查看任务</Link>}>
        <JobFeed query={jobs} items={recentJobs.slice(0, 8)} empty="暂无任务记录" />
      </AdminPanel>
    </div>
  </div>
}

function budgetValue(budget?: Limits['ai']['auto']) {
  return budget ? number(budget.used) + ' / ' + number(budget.limit) : '—'
}

function ObservedTime({ value }: { value?: string | null }) {
  return value ? <time dateTime={value}>{timestamp(value)}</time> : <>—</>
}

const jobTypes: Record<string, string> = {
  refresh_repo: '仓库采样', discover_trending: 'Trending 发现', discover_search: 'Search 发现',
  discover_manual: '人工发现',
  star_history: 'GitHub Star 历史',
  refresh_readme_summary: '中文摘要',
  translate_readme: 'README 翻译',
  compose_report: '报刊组稿',
}

function useSystemJobs() {
  return useQuery({
    queryKey: ['jobs'],
    queryFn: ({ signal }) => api<Items<Job>>('/system/jobs?limit=50', { signal }),
    refetchInterval: 10_000,
    staleTime: 0,
  })
}

function JobBadge({ status }: { status: Job['status'] }) {
  return <Badge tone={status === 'unknown' || status === 'failed' ? 'warning' : status === 'dead' ? 'hot' : status === 'succeeded' ? 'ok' : 'neutral'}>{jobLabel[status]}</Badge>
}

function Jobs() {
  const query = useSystemJobs()
  return <div className="admin-stack">
    <AdminPanel title="最近任务" action={<button className="button" onClick={() => { void query.refetch() }} disabled={query.isFetching}>刷新</button>}>
      {query.error && query.data && <Notice error>{errorText(query.error)}</Notice>}
      <JobFeed query={query} items={query.data?.items ?? []} empty="暂无任务记录" />
    </AdminPanel>
  </div>
}

function JobFeed({ query, items, empty }: { query: ReturnType<typeof useSystemJobs>; items: Job[]; empty: string }) {
  if (query.isPending) return <Skeleton rows={1} />
  if (!query.data) return <PageError error={query.error} retry={() => { void query.refetch() }} />
  if (!items.length) return <p className="admin-empty">{empty}</p>
  return <div className="admin-table-scroll"><table className="admin-table">
    <thead><tr><th scope="col">任务</th><th scope="col">状态</th><th scope="col">执行次数</th><th scope="col">更新时间</th></tr></thead>
    <tbody>{items.map((job) => <tr key={job.id}>
      <td><div className="admin-task-name">
        <strong>{jobTypes[job.type] ?? job.type}</strong><span className="minor muted numeric">#{job.id}</span>
        {job.repoId !== null && job.repoFullName && <Link className="admin-link" to={repoPath(job.repoFullName, true, job.repoNameIsCurrent === false ? job.repoId : undefined)}
          state={{ repoId: job.repoId }} title="查看关联仓库" aria-label={'查看任务 ' + job.id + ' 的仓库'}><ArrowUpRight size={14} /></Link>}
      </div>{job.submittedRepository && <div className="minor muted">{job.submittedRepository}</div>}
        {job.errorCode && <code className="minor tone-hot">{job.errorCode}</code>}</td>
      <td><JobBadge status={job.status} />{job.type === 'compose_report' && job.status === 'unknown' &&
        <ReportRetry id={job.id} onQueued={() => { void query.refetch() }} />}</td>
      <td className="numeric">{job.attempts} / {job.maxAttempts}</td>
      <td><ObservedTime value={job.updatedAt} /></td>
    </tr>)}</tbody>
  </table></div>
}

function ReportRetry({ id, onQueued }: { id: number; onQueued: () => void }) {
  const [confirm, setConfirm] = useState(false)
  const [requestId] = useState(() => crypto.getRandomValues(new Uint32Array(4)).join('-'))
  const retry = useMutation({
    mutationFn: () => api('/jobs/' + id + '/retry', {
      method: 'POST', body: { requestId, acceptPossibleCharge: true },
    }),
    onSuccess: () => { setConfirm(false); onQueued() },
  })
  return <>
    <button className="button" onClick={() => setConfirm(true)} disabled={retry.isPending || retry.isSuccess}>确认并重试</button>
    {confirm && <Dialog title="确认重新组稿" onClose={() => setConfirm(false)}>
      <p>上一次调用可能已计费。继续会使用一次手动 AI 调用额度。</p>
      {retry.error && <Notice error>{errorText(retry.error)}</Notice>}
      <div className="action-row">
        <button className="button" data-autofocus onClick={() => setConfirm(false)}>取消</button>
        <ActionButton className="button primary" pending={retry.isPending} onClick={() => retry.mutate()}>接受可能再次计费</ActionButton>
      </div>
    </Dialog>}
  </>
}
