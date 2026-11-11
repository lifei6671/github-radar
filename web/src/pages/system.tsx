import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUpRight, Database, Gauge, ListChecks, Wrench } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { api, errorText } from '../api/client'
import type { Items, Job, Limits, StorageStatus } from '../api/types'
import { Badge, Empty, Notice, PageError, Skeleton, Spinner } from '../components/ui'
import { formatBytes, jobLabel, number, timestamp } from '../lib/format'
import { CategoryEditor, RulesEditor, RuntimeEditor } from './settings'
import { BackupPage } from './backup'

const tabs = [
  ['overview', '概览'], ['jobs', '任务'], ['runtime', '运行配置'], ['categories', '分类映射'], ['rules', '发现规则'],
  ['backup', '备份与恢复'],
] as const

export function System() {
  const [params, setParams] = useSearchParams()
  const tab = tabs.find(([key]) => key === params.get('tab'))?.[0] ?? 'overview'
  return <>
    <header className="page-head"><h1>运行状态</h1>
      <div className="action-row system-shortcuts"><Link className="button" to="/repos">全部项目</Link><Link className="button" to="/categories">技术主题</Link></div>
    </header>
    <div className="pills wrap ranking-tabs" aria-label="运行管理">
      {tabs.map(([key, label]) => <button key={key} aria-pressed={tab === key} onClick={() =>
        setParams(key === 'overview' ? {} : { tab: key }, { preventScrollReset: true })}>{label}</button>)}
    </div>
    {tab === 'overview' && <Overview />}
    {tab === 'jobs' && <Jobs />}
    {tab === 'runtime' && <RuntimeEditor />}
    {tab === 'categories' && <CategoryEditor />}
    {tab === 'rules' && <RulesEditor />}
    {tab === 'backup' && <BackupPage />}
  </>
}

const resourceNames: Record<string, string> = {
  core: 'GitHub Core', search: 'GitHub Search', repo_refresh: '仓库采样', readme_check: 'README 检查', github_secondary: '次级限流',
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
  const maintain = useMutation({
    mutationFn: () => api<{ status: string }>('/system/maintenance/run', { method: 'POST' }),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ['storage'] }) },
  })
  return <div className="system-grid">
    <section className="panel system-panel">
      <header className="section-head"><h2><Gauge size={18} />采集与模型额度</h2>
        {limits.data && <Badge tone={limits.data.githubConfigured ? 'ok' : 'warning'}>{limits.data.githubConfigured ? 'GitHub 已配置' : 'GitHub 待配置'}</Badge>}</header>
      {limits.isPending ? <Skeleton rows={1} /> : !limits.data ?
        <PageError error={limits.error} retry={() => { void limits.refetch() }} /> : <>
          {limits.error && <Notice error>{errorText(limits.error)}</Notice>}
          <dl className="definition-list">
            <div><dt>待处理任务</dt><dd className="numeric">{number(limits.data.waitingTaskCount)}</dd></div>
            {limits.data.items.map((item) => <div key={item.resource}>
              <dt>{resourceNames[item.resource] ?? item.resource}</dt>
              <dd className="numeric" title={item.pausedUntil ? '暂停至 ' + timestamp(item.pausedUntil) : item.limit > 0 ? '重置于 ' + timestamp(item.resetAt) : undefined}>
                {item.limit > 0 ? number(item.remaining) + ' / ' + number(item.limit) : number(item.localRequests) + ' 次'}
              </dd>
            </div>)}
            <div><dt>AI 自动调用</dt><dd className="numeric">{number(limits.data.ai.auto.used)} / {number(limits.data.ai.auto.limit)}</dd></div>
            <div><dt>AI 手动调用</dt><dd className="numeric">{number(limits.data.ai.manual.used)} / {number(limits.data.ai.manual.limit)}</dd></div>
            <div><dt>AI 额度重置</dt><dd>{timestamp(limits.data.ai.resetAt)}</dd></div>
          </dl>
          <Link className="button" to="/system?tab=jobs"><ListChecks size={16} />查看任务</Link>
        </>}
    </section>
    <section className="panel system-panel">
      <header className="section-head"><h2><Database size={18} />存储与维护</h2></header>
      {storage.isPending ? <Skeleton rows={1} /> : !storage.data ?
        <PageError error={storage.error} retry={() => { void storage.refetch() }} /> : <>
          {storage.error && <Notice error>{errorText(storage.error)}</Notice>}
          <dl className="definition-list">
            <div><dt>SQLite / WAL</dt><dd className="numeric">{formatBytes(storage.data.databaseBytes)} / {formatBytes(storage.data.walBytes)}</dd></div>
            <div><dt>原始快照</dt><dd className="numeric">{number(storage.data.counts.raw)}</dd></div>
            <div><dt>小时 / 日聚合</dt><dd className="numeric">{number(storage.data.counts.hourly)} / {number(storage.data.counts.daily)}</dd></div>
            <div><dt>最近计算</dt><dd>{timestamp(storage.data.watermarks.metricsUpdatedAt)}</dd></div>
            <div><dt>小时聚合水位</dt><dd>{timestamp(storage.data.watermarks.hourly)}</dd></div>
            <div><dt>日聚合水位</dt><dd>{timestamp(storage.data.watermarks.daily)}</dd></div>
            <div><dt>最近清理</dt><dd>{timestamp(storage.data.watermarks.cleanedAt)}</dd></div>
          </dl>
          <button className="button" disabled={maintain.isPending} onClick={() => maintain.mutate()}>
            {maintain.isPending ? <Spinner /> : <Wrench size={16} />}运行维护
          </button>
          {maintain.data && <Notice>维护已排队。</Notice>}
          {maintain.error && <Notice error>{errorText(maintain.error)}</Notice>}
        </>}
    </section>
  </div>
}

const jobTypes: Record<string, string> = {
  refresh_repo: '仓库采样', discover_trending: 'Trending 发现', discover_search: 'Search 发现',
  refresh_readme_summary: '中文摘要',
}

function Jobs() {
  const query = useQuery({
    queryKey: ['jobs'],
    queryFn: ({ signal }) => api<Items<Job>>('/system/jobs?limit=50', { signal }),
    refetchInterval: 10_000,
    staleTime: 0,
  })
  if (query.isPending) return <Skeleton />
  if (!query.data) return <PageError error={query.error} retry={() => { void query.refetch() }} />
  if (!query.data.items.length) return <Empty title="暂无任务记录" />
  return <section className="panel jobs-panel">
    {query.error && <Notice error>{errorText(query.error)}</Notice>}
    <div className="jobs-heading"><h2>最近任务</h2><button className="button" onClick={() => { void query.refetch() }} disabled={query.isFetching}>刷新</button></div>
    <div className="job-table">
      {query.data.items.map((job) => <div className="job-row" key={job.id}>
        <div><strong>{jobTypes[job.type] ?? job.type}</strong><span className="minor muted numeric">#{job.id}</span></div>
        <Badge tone={job.status === 'unknown' || job.status === 'failed' ? 'warning' : job.status === 'dead' ? 'hot' : job.status === 'succeeded' ? 'ok' : 'neutral'}>{jobLabel[job.status]}</Badge>
        <span className="numeric minor muted">{job.attempts} / {job.maxAttempts} 次</span>
        <time className="minor muted" dateTime={job.updatedAt}>{timestamp(job.updatedAt)}</time>
        {job.errorCode && <code className="minor tone-hot">{job.errorCode}</code>}
        {job.repoId !== null && <Link className="icon-button" to={'/repos/' + job.repoId} title="查看关联仓库" aria-label={'查看任务 ' + job.id + ' 的仓库'}><ArrowUpRight size={16} /></Link>}
      </div>)}
    </div>
  </section>
}
