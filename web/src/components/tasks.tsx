import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw, Sparkles } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { api, ApiError, errorText } from '../api/client'
import { refreshRepo } from '../api/queries'
import type { Job, JobAccepted, RepoDetail, SummaryResponse } from '../api/types'
import { jobLabel } from '../lib/format'
import { ActionButton, Badge, Dialog, Notice } from './ui'

function requestID() {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, '0')).join('')
}

function ongoing(status: Job['status'] | undefined) {
  return status === 'pending' || status === 'running' || status === 'failed'
}

function useRepoTask(repoId: number, kind: 'refresh' | 'summary' | 'readme/translate' | 'star-history', existing: number | null = null, repoUpdatedAt?: number) {
  const queryClient = useQueryClient()
  const [tracked, setTracked] = useState<number | null>(null)
  const [cached, setCached] = useState(false)
  const keys = useRef(new Map<string, string>())
  const notified = useRef('')
  // 同一仓库的任务 ID 递增，其他标签页或定时任务产生的新任务优先。
  const jobId = Math.max(tracked ?? 0, existing ?? 0) || null

  function withID<T>(action: string, run: (requestId: string) => Promise<T>) {
    const id = keys.current.get(action) ?? requestID()
    keys.current.set(action, id)
    return run(id).then((data) => {
      keys.current.delete(action)
      return data
    }, (error: unknown) => {
      // 网络错误和服务端错误可能发生在请求已入队之后，重试沿用同一次请求的 ID。
      if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 429) keys.current.delete(action)
      throw error
    })
  }

  const job = useQuery({
    queryKey: ['job', jobId],
    enabled: jobId !== null,
    queryFn: ({ signal }) => api<Job>('/jobs/' + jobId, { signal }),
    staleTime: 0,
    retry: false,
    refetchInterval: ({ state }) => {
      if (state.error instanceof ApiError && [401, 404].includes(state.error.status)) return false
      return !state.data || ongoing(state.data.status) ? 2500 : false
    },
  })
  const gone = job.error instanceof ApiError && job.error.status === 404
  const status = job.data?.status
  const refetchJob = job.refetch
  useEffect(() => {
    // 服务端已无未决任务时，重新确认本地旧 unknown 是否已被人工处理。
    if (kind === 'summary' && status === 'unknown' && existing !== jobId) void refetchJob()
  }, [existing, jobId, kind, refetchJob, repoUpdatedAt, status])
  useEffect(() => {
    if (!jobId || (!gone && (!status || ongoing(status)))) return
    const event = jobId + ':' + (gone ? 'gone' : status)
    if (notified.current === event) return
    notified.current = event
    void refreshRepo(queryClient, repoId)
    void queryClient.invalidateQueries({ queryKey: ['jobs'] })
    if (kind === 'star-history') void queryClient.invalidateQueries({ queryKey: ['star-history', repoId] })
  }, [gone, jobId, kind, queryClient, repoId, status])

  const submit = useMutation({
    mutationFn: (force: boolean) => {
      setCached(false)
      return withID(kind + ':' + force, async (id) => {
        try {
          return await api<SummaryResponse>('/repos/' + repoId + '/' + kind, {
            method: 'POST', body: kind === 'summary' ? { requestId: id, force } : { requestId: id },
          })
        } catch (error) {
          if (error instanceof ApiError && error.code === 'summary_in_progress' && error.existingJobId) {
            return { jobId: error.existingJobId, statusUrl: '/api/v1/jobs/' + error.existingJobId }
          }
          throw error
        }
      })
    },
    onSuccess: (response) => {
      if ('cached' in response) {
        setCached(true)
        setTracked(null)
        void refreshRepo(queryClient, repoId)
      } else {
        setTracked(response.jobId)
      }
    },
  })

  const retry = useMutation({
    mutationFn: () => withID('retry:' + jobId, (id) => api<JobAccepted>('/jobs/' + jobId + '/retry', {
      method: 'POST', body: { requestId: id, acceptPossibleCharge: true },
    })),
    onSuccess: (response) => { setTracked(response.jobId) },
  })

  return {
    submit, retry, job, jobId, gone, status, cached,
    busy: submit.isPending || retry.isPending || Boolean(jobId && !gone && (job.isPending || ongoing(status))),
  }
}

// 回补的下一页在上一页结算时入队，刷新历史查询可接续展示最新任务。
export function StarHistoryAction({ repoId, existing }: { repoId: number; existing?: number }) {
  const task = useRepoTask(repoId, 'star-history', existing)
  return <div className="task-area">
    <ActionButton disabled={task.busy} pending={task.submit.isPending}
      onClick={() => task.submit.mutate(false)}>回补 GitHub 历史</ActionButton>
    {task.busy && <TaskStatus task={task} />}
    {task.submit.error && <Notice error>{errorText(task.submit.error)}</Notice>}
  </div>
}

type Task = ReturnType<typeof useRepoTask>

function TaskStatus({ task }: { task: Task }) {
  if (task.gone) return <Notice>任务已过保留期，已重新读取仓库结果。</Notice>
  if (task.job.error) return <Notice error>{errorText(task.job.error)}</Notice>
  if (!task.job.data) return task.busy ? <span className="muted minor" role="status">正在读取任务状态</span> : null
  const job = task.job.data
  return <div className="job-caption" role="status">
    <Badge tone={job.status === 'succeeded' ? 'ok' : job.status === 'unknown' || job.status === 'failed' ? 'warning' : job.status === 'dead' ? 'hot' : 'neutral'}>{jobLabel[job.status]}</Badge>
    <span className="muted minor">任务 #{job.id}</span>
    {job.errorCode && <code className="muted">{job.errorCode}</code>}
  </div>
}

export function RefreshAction({ repoId }: { repoId: number }) {
  const task = useRepoTask(repoId, 'refresh')
  return <div className="task-area">
    <ActionButton disabled={task.busy} pending={task.submit.isPending}
      icon={<RefreshCw size={16} />} onClick={() => task.submit.mutate(false)}>刷新采样</ActionButton>
    {task.submit.error && <Notice error>{errorText(task.submit.error)}</Notice>}
    <TaskStatus task={task} />
  </div>
}

export function TranslationAction({ repoId, available, existing }: { repoId: number; available: boolean; existing?: number }) {
  const task = useRepoTask(repoId, 'readme/translate', existing)
  const [confirm, setConfirm] = useState<number | null>(null)
  const unknown = task.status === 'unknown'
  const canRetry = available && unknown && !task.job.isFetching && !task.retry.isPending
  const retryChanged = confirm !== null && (confirm !== task.jobId || !canRetry)
  const error = task.retry.error ?? task.submit.error
  if (!available && !task.jobId) return null
  return <div className="task-area">
    <div className="action-row">
      <ActionButton pending={task.submit.isPending || task.retry.isPending}
        disabled={unknown ? !canRetry : !available || task.busy} onClick={() => {
          if (unknown) setConfirm(task.jobId)
          else task.submit.mutate(false)
        }}>{unknown ? '确认并继续翻译' : '生成中文译文'}</ActionButton>
    </div>
    {error && <Notice error>{errorText(error)}</Notice>}
    <TaskStatus task={task} />
    {confirm !== null && <Dialog title="确认再次调用" onClose={() => setConfirm(null)}>
      <p>上一批译文的调用可能已计费。</p>
      {retryChanged && <Notice>任务状态已变化，请关闭对话框后重新确认。</Notice>}
      <div className="action-row">
        <button className="button" data-autofocus onClick={() => setConfirm(null)}>取消</button>
        <ActionButton className="button primary" disabled={retryChanged} onClick={() => {
          task.retry.mutate()
          setConfirm(null)
        }}>接受可能再次计费</ActionButton>
      </div>
    </Dialog>}
  </div>
}

export function SummaryActions({ repo, repoUpdatedAt }: { repo: RepoDetail; repoUpdatedAt: number }) {
  const task = useRepoTask(repo.id, 'summary', repo.summaryJobId, repoUpdatedAt)
  const [confirm, setConfirm] = useState<'force' | number | null>(null)
  const unknown = task.status === 'unknown' || (!task.status && !task.gone &&
    repo.summaryStatus === 'unknown' && repo.summaryJobId === task.jobId)
  const available = repo.summaryAvailable === true
  const canRetry = available && task.status === 'unknown' && !task.job.isFetching && !task.retry.isPending
  const retryChanged = typeof confirm === 'number' && (confirm !== task.jobId || !canRetry)
  const unavailable = repo.state === 'unavailable' || repo.state === 'address_pending'
  const error = task.retry.error ?? task.submit.error
  return <div className="task-area summary-actions">
    <div className="action-row">
      {unknown ? <ActionButton disabled={!canRetry} pending={task.retry.isPending} onClick={() => setConfirm(task.jobId)}>确认并重新调用</ActionButton> :
        <>
          <ActionButton className="button primary" disabled={!available || task.busy || unavailable}
            pending={task.submit.isPending && !task.submit.variables} icon={<Sparkles size={16} />}
            onClick={() => task.submit.mutate(false)}>生成总结</ActionButton>
          {repo.summaryZh && <ActionButton disabled={!available || task.busy || unavailable}
            pending={task.submit.isPending && task.submit.variables} onClick={() => setConfirm('force')}>重新总结</ActionButton>}
        </>}
    </div>
    {!available && <p className="minor muted">手动摘要未启用。<Link className="detail-text-link" to="/admin/runs?tab=runtime&group=ai">查看 AI 摘要配置</Link></p>}
    {unknown && <Notice>上一次调用结果待确认，可能已经计费。</Notice>}
    {task.cached && <Badge tone="ok">已复用缓存</Badge>}
    {error && <Notice error>{errorText(error)}</Notice>}
    <TaskStatus task={task} />
    {confirm && <Dialog title={confirm === 'force' ? '重新生成中文总结' : '确认再次调用'} onClose={() => setConfirm(null)}>
      <p>{confirm === 'force' ? '重新生成会使用一次手动调用额度。' : '上一次调用可能已计费。继续会关闭未决任务并发起一次新的手动调用。'}</p>
      {retryChanged && <Notice>任务状态已变化，请关闭对话框后重新确认。</Notice>}
      <div className="action-row">
        <button data-autofocus className="button" onClick={() => setConfirm(null)}>取消</button>
        <ActionButton className="button primary" disabled={!available || retryChanged} onClick={() => {
          if (typeof confirm === 'number') task.retry.mutate()
          else task.submit.mutate(true)
          setConfirm(null)
        }}>{typeof confirm === 'number' ? '接受可能再次计费' : '确认重新生成'}</ActionButton>
      </div>
    </Dialog>}
  </div>
}
