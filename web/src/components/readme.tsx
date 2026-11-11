import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api, ApiError, errorText, publicApi } from '../api/client'
import { withdrawPublicRepo } from '../api/queries'
import type { RepoDetail, RepoReadme } from '../api/types'
import { External } from './repo'
import { TranslationAction } from './tasks'
import { Notice, Spinner } from './ui'

export function RepoReading({ repo, admin }: { repo: RepoDetail; admin: boolean }) {
  const [original, setOriginal] = useState(false)
  const queryClient = useQueryClient()
  const readme = useQuery({
    queryKey: ['readme', repo.id, admin ? 'admin' : 'public'],
    queryFn: async ({ signal }) => {
      try {
        return await (admin ? api : publicApi)<RepoReadme>('/repos/' + repo.id + '/readme', { signal })
      } catch (error) {
        if (!admin && error instanceof ApiError && error.status === 404) await withdrawPublicRepo(queryClient, repo.id)
        throw error
      }
    },
    staleTime: 0,
    refetchOnWindowFocus: false,
    retry: false,
  })
  const data = readme.error ? undefined : readme.data
  const bilingual = Boolean(data?.chineseHtml && data.originalHtml)
  const showOriginal = !data?.chineseHtml || (bilingual && original)
  const html = showOriginal ? data?.originalHtml : data?.chineseHtml

  return <section className="readme-section" id="readme" aria-labelledby="readme-title">
    <header className="section-head">
      <h2 id="readme-title">README</h2>
      {bilingual && <div className="pills" role="group" aria-label="正文语言">
        <button type="button" aria-pressed={!showOriginal} onClick={() => setOriginal(false)}>中文</button>
        <button type="button" aria-pressed={showOriginal} onClick={() => setOriginal(true)}>原文</button>
      </div>}
    </header>
    {readme.isPending && <span className="minor muted" role="status"><Spinner />正在读取 README</span>}
    {readme.error && <Notice error>{errorText(readme.error)} <button type="button" onClick={() => { void readme.refetch() }}>重试</button></Notice>}
    {data?.state === 'missing' && <p className="minor muted">此仓库暂无 README</p>}
    {data?.state === 'unavailable' && <p className="minor muted">站内 README 暂不可用</p>}
    {data?.truncated && <Notice>README 超出站内读取长度，可到 GitHub 查看全文。</Notice>}
    {bilingual && !showOriginal && !data?.complete && <Notice>中文译文尚不完整，可切换到原文。</Notice>}
    {/* HTML 由服务端 Markdown 安全渲染器生成。 */}
    {html && <div className="readme-body" dangerouslySetInnerHTML={{ __html: html }} />}
    {(data?.state === 'ready' || data?.state === 'unavailable' || readme.error) &&
      <div><External href={data?.sourceUrl || repo.sourceReadmeUrl || repo.htmlUrl}>在 GitHub 查看</External></div>}
    {admin && data?.originalHtml && (!data.complete || data.translationJobId) &&
      <TranslationAction repoId={repo.id} available={repo.summaryAvailable === true} existing={data.translationJobId} />}
  </section>
}
