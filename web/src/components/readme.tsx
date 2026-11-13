import type { RepoDetail } from '../api/types'
import { TranslationAction } from './tasks'
import { Notice } from './ui'

export function RepoReading({ repo, admin }: { repo: RepoDetail; admin: boolean }) {
  const data = repo.readme

  return <section className="readme-section" id="readme" aria-labelledby="readme-title">
    <h2 id="readme-title">README</h2>
    {(!data || data.state === 'pending') && <p className="minor muted">中文 README 尚未就绪</p>}
    {data?.state === 'missing' && <p className="minor muted">此仓库暂无 README</p>}
    {data?.state === 'unavailable' && <p className="minor muted">站内 README 暂不可用</p>}
    {data?.chineseHtml && data.truncated && <Notice>站内仅展示部分 README，可到 GitHub 查看全文。</Notice>}
    {/* HTML 由服务端 Markdown 安全渲染器生成。 */}
    {data?.chineseHtml && <div className="readme-body" dangerouslySetInnerHTML={{ __html: data.chineseHtml }} />}
    {admin && data?.state === 'pending' && data.language && data.language !== 'zh' &&
      <TranslationAction repoId={repo.id} available={repo.summaryAvailable === true} existing={data.translationJobId} />}
  </section>
}
