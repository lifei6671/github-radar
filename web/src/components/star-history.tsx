import { useQuery, useQueryClient } from '@tanstack/react-query'
import { lazy, Suspense, useState } from 'react'
import { api, ApiError, errorText, publicApi } from '../api/client'
import { withdrawPublicRepo } from '../api/queries'
import type { StarHistory } from '../api/types'
import { decimal, number, timestamp } from '../lib/format'
import { StarHistoryAction } from './tasks'
import { Badge, Empty, Notice, PageError, Skeleton } from './ui'

const Chart = lazy(() => import('./star-history-chart'))

// 两种证据并列阅读，公开详情只展示数据；回补入口留在后台。
export function StarHistoryPanel({ id, admin }: { id: number; admin: boolean }) {
  const queryClient = useQueryClient()
  const [days, setDays] = useState(30)
  const history = useQuery({
    queryKey: ['star-history', id, days, admin ? 'admin' : 'public'],
    queryFn: async ({ signal }) => {
      try {
        return await (admin ? api : publicApi)<StarHistory>(`/repos/${id}/star-history?days=${days}`, { signal })
      } catch (error) {
        if (!admin && error instanceof ApiError && error.status === 404) {
          // 服务端撤回时按现有详情行为清理已缓存的公开数据。
          await withdrawPublicRepo(queryClient, id)
        }
        throw error
      }
    },
    staleTime: 0,
  })
  const signal = history.data?.breakout
  const observed = history.data?.days.some((day) => day.count !== null)
  return <section className="star-history-panel" aria-labelledby="star-history-title">
    <div className="section-head">
      <h3 id="star-history-title">GitHub 历史日 Star</h3>
      <label className="select-label"><span className="sr-only">GitHub 历史范围</span>
        <select value={days} onChange={(event) => setDays(Number(event.target.value))}>
          <option value={30}>30 天</option><option value={90}>90 天</option><option value={180}>180 天</option><option value={630}>630 天</option>
        </select>
      </label>
    </div>
    {signal && <div className="star-breakout" aria-label="爆发依据">
      <Badge tone={signal.source === 'local_net_24h' ? 'ok' : 'warning'}>
        {signal.source === 'local_net_24h' ? '本地净增确认爆发' : 'GitHub 日 Star 爆发待确认'}
      </Badge>
      <span>{signal.source === 'local_net_24h' ? '24h 净增' : '单日新增'} {number(signal.value)}</span>
      {signal.absolute ? <span>达到 {number(signal.direct)} 绝对阈值</span> : <span>基线覆盖 {signal.coveredDays}/7 天，{decimal(signal.ratio)} 倍</span>}
    </div>}
    {history.isPending ? <Skeleton rows={1} /> : history.error && !history.data
      ? <PageError error={history.error} retry={() => { void history.refetch() }} /> : observed && history.data
        ? <Suspense fallback={<Skeleton rows={1} />}><Chart days={history.data.days} /></Suspense>
        : <Empty title="暂无 GitHub 历史日计数" />}
    {history.error && history.data && <Notice error>{errorText(history.error)}</Notice>}
    {observed && <div className="chart-note minor muted">
      <span>浅色为当天未结束，缺测日期留空</span>
      {history.data?.fetchedAt && <time dateTime={history.data.fetchedAt}>采集于 {timestamp(history.data.fetchedAt)}</time>}
    </div>}
    {admin && history.data && <>
      <StarHistoryAction repoId={id} existing={history.data.jobId} />
      {history.data.gradeEvidence && <p className="minor muted">最近爆发入档：{timestamp(new Date(history.data.gradeEvidence.at).toISOString())}，
        24h 精确净增 {number(history.data.gradeEvidence.value)}，{history.data.gradeEvidence.absolute
          ? `达到 ${history.data.gradeEvidence.direct} 绝对阈值`
          : `基线覆盖 ${history.data.gradeEvidence.coveredDays}/7 天，${decimal(history.data.gradeEvidence.ratio)} 倍`}</p>}
      {history.data.errorCode && <Notice error>{history.data.errorCode === 'star_history_page_changed'
        ? 'GitHub 历史已跨周，已保留此前结果。请重新回补。'
        : <>上次采集未完成 <code>{history.data.errorCode}</code></>}</Notice>}
    </>}
  </section>
}
