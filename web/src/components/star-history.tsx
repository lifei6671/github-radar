import { useQuery } from '@tanstack/react-query'
import { api, errorText } from '../api/client'
import type { StarHistory } from '../api/types'
import { decimal, number, timestamp } from '../lib/format'
import { StarHistoryAction } from './tasks'
import { Notice, PageError, Skeleton } from './ui'

// 后台保留历史回补操作和入档证据。
export function StarHistoryPanel({ id }: { id: number }) {
  const history = useQuery({
    queryKey: ['star-history', id, 30, 'admin'],
    queryFn: ({ signal }) => api<StarHistory>(`/repos/${id}/star-history?days=30`, { signal }),
    staleTime: 0,
  })
  return <div className="star-history-panel">
    {history.isPending ? <Skeleton rows={1} /> : history.error && !history.data
      ? <PageError error={history.error} retry={() => { void history.refetch() }} /> : null}
    {history.error && history.data && <Notice error>{errorText(history.error)}</Notice>}
    {history.data && <>
      <StarHistoryAction repoId={id} existing={history.data.jobId} />
      {history.data.gradeEvidence && <p className="minor muted">最近爆发入档：{timestamp(new Date(history.data.gradeEvidence.at).toISOString())}，
        24h 精确净增 {number(history.data.gradeEvidence.value)}，{history.data.gradeEvidence.absolute
          ? `达到 ${history.data.gradeEvidence.direct} 绝对阈值`
          : `基线覆盖 ${history.data.gradeEvidence.coveredDays}/7 天，${decimal(history.data.gradeEvidence.ratio)} 倍`}</p>}
      {history.data.errorCode && <Notice error>{history.data.errorCode === 'star_history_page_changed'
        ? 'GitHub 历史已跨周，已保留此前结果。请重新回补。'
        : <>上次采集未完成 <code>{history.data.errorCode}</code></>}</Notice>}
    </>}
  </div>
}
