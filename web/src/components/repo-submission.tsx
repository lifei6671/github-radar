import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type { FormEvent } from 'react'
import { Link } from 'react-router'
import { api, ApiError, errorText } from '../api/client'
import { isRepoDetail } from '../api/queries'
import type { Items, Job, RepoSubmissionItem } from '../api/types'
import { jobLabel, repoPath } from '../lib/format'
import { ActionButton, Badge, Dialog, Notice } from './ui'

const emptyForm = { repositories: '', sourceUrl: '', note: '' }

function errorLabel(code: string) { return errorText(new ApiError(400, { code })) }

export function RepoSubmission() {
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState(emptyForm)
  const [batch, setBatch] = useState<{ id: string; items: RepoSubmissionItem[] }>()
  const requestID = useRef<string | undefined>(undefined)
  const reported = useRef(new Set<number>())
  const hasJobs = batch?.items.some((item) => item.jobId !== undefined) ?? false
  const jobs = useQuery({
    queryKey: ['repo-submissions', batch?.id],
    enabled: hasJobs,
    queryFn: ({ signal }) => api<Items<Job>>('/repo-submissions/' + batch?.id, { signal }),
    refetchInterval: ({ state }) => !state.data || state.data.items.some((job) =>
      job.status === 'pending' || job.status === 'running' || job.status === 'failed') ? 2500 : false,
  })
  useEffect(() => {
    const newResults = jobs.data?.items.filter((job) => job.status === 'succeeded' && job.repoId !== null && !reported.current.has(job.id))
    if (!newResults?.length) return
    for (const job of newResults) reported.current.add(job.id)
    const ids = new Set(newResults.flatMap((job) => job.repoId === null ? [] : [job.repoId]))
    void queryClient.invalidateQueries({ queryKey: ['repos'], predicate: ({ queryKey }) => queryKey[2] === 'admin' })
    void queryClient.invalidateQueries({ queryKey: ['repo'], predicate: (query) =>
      query.queryKey[2] === 'admin' && Array.from(ids).some((id) => isRepoDetail(query, id)) })
  }, [jobs.data, queryClient])

  const submit = useMutation({
    mutationFn: async () => {
      const id = requestID.current ??= crypto.getRandomValues(new Uint32Array(4)).join('-')
      const response = await api<Items<RepoSubmissionItem>>('/repo-submissions', { method: 'POST', body: { ...form, requestId: id } })
      return { id, items: response.items }
    },
    onSuccess: (result) => {
      reported.current.clear()
      setBatch(result)
      void queryClient.invalidateQueries({ queryKey: ['jobs'] })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'submission_conflict') requestID.current = undefined
    },
  })

  function update(field: keyof typeof emptyForm, value: string) {
    requestID.current = undefined
    submit.reset()
    setForm((previous) => ({ ...previous, [field]: value }))
  }

  function send(event: FormEvent) {
    event.preventDefault()
    if (!submit.isPending) submit.mutate()
  }

  const byID = new Map(jobs.data?.items.map((job) => [job.id, job]))
  return <>
    <button type="button" className="button primary" onClick={() => setOpen(true)}><Plus size={16} aria-hidden />提交仓库</button>
    {open && <Dialog title="提交仓库" onClose={() => { if (!submit.isPending) setOpen(false) }}>
      {batch ? <>
        <h3 className="minor">提交结果</h3>
        {jobs.error && <Notice error>{errorText(jobs.error)} <button className="button" onClick={() => { void jobs.refetch() }}>刷新状态</button></Notice>}
        <ul className="repo-submission-results" aria-label="逐行提交结果">
          {batch.items.map((item) => {
            const job = item.jobId === undefined ? undefined : byID.get(item.jobId)
            const error = item.errorCode ?? job?.errorCode
            const label = item.errorCode ? '失败' : job ? job.status === 'succeeded' ? '已收录' : jobLabel[job.status] : jobs.data ? '任务记录已过期' : '已排队'
            return <li key={item.line}>
              <div className="repo-submission-result">
                {job?.repoFullName && job.repoId !== null ? <Link className="admin-link" to={repoPath(job.repoFullName, true, job.repoId)}>{item.input}</Link> : <span>{item.input}</span>}
                <Badge tone={item.errorCode || job?.status === 'dead' ? 'hot' : job?.status === 'succeeded' ? 'ok' : 'neutral'}>{label}</Badge>
              </div>
              {error && <p className="minor muted">{errorLabel(error)}</p>}
            </li>
          })}
        </ul>
        <div className="action-row">
          <Link className="button" to="/admin/runs?tab=jobs">任务记录</Link>
          <button type="button" className="button primary" onClick={() => {
            if (hasJobs) setForm(emptyForm)
            requestID.current = undefined
            setBatch(undefined)
            submit.reset()
          }}>{hasJobs ? '继续提交' : '修改输入'}</button>
        </div>
      </> : <form className="dialog-form" onSubmit={send}>
        <fieldset className="dialog-form" disabled={submit.isPending}>
          <label className="field">仓库地址，每行一个，最多 30 个
            <textarea data-autofocus required rows={5} maxLength={15000} value={form.repositories}
              placeholder={'https://github.com/owner/repo\nowner/another-repo'} onChange={(event) => update('repositories', event.target.value)} />
          </label>
          <label className="field">来源链接（可选，适用于本批）
            <input type="url" maxLength={2048} value={form.sourceUrl} onChange={(event) => update('sourceUrl', event.target.value)} />
          </label>
          <label className="field">备注（可选，适用于本批）
            <textarea rows={2} maxLength={500} value={form.note} onChange={(event) => update('note', event.target.value)} />
          </label>
        </fieldset>
        {submit.error && <Notice error>{errorText(submit.error)}</Notice>}
        <div className="action-row"><ActionButton type="submit" className="button primary" pending={submit.isPending}>提交</ActionButton></div>
      </form>}
    </Dialog>}
  </>
}
