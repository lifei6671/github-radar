import { useQuery } from '@tanstack/react-query'
import { api, errorText } from '../api/client'
import type { DiscoveryRuns, RuleSetting } from '../api/types'
import { jobLabel, number, timestamp } from '../lib/format'
import { Badge, Notice, PageError, Skeleton } from './ui'

const alertLabels = {
  consecutive_failures: '连续三轮失败',
  quota_backlog: '积压超过 15 分钟，当前受配额限制',
  not_run: '长时间未运行',
}

// 规则列表查看最近运行，规则详情使用同一份接口按来源筛选。
export function DiscoveryRunHistory({ rule }: { rule?: RuleSetting }) {
  const sourceType = rule?.sourceType ?? ''
  const sourceKey = rule?.sourceKey ?? ''
  const history = useQuery({
    queryKey: ['discovery-runs', sourceType, sourceKey],
    queryFn: ({ signal }) => {
      const params = new URLSearchParams({ limit: '20' })
      if (sourceKey) {
        params.set('sourceType', sourceType)
        params.set('sourceKey', sourceKey)
      }
      return api<DiscoveryRuns>('/system/discovery-runs?' + params, { signal })
    },
    refetchInterval: 15_000,
  })
  const range = (from: string | null, until: string | null) =>
    from && until ? from.slice(0, 10) + ' 至 ' + until.slice(0, 10) : '—'
  return <section className={rule ? 'admin-stack' : 'panel admin-panel admin-stack'} aria-label="规则运行记录">
    <div className="section-head">
      <h3>运行记录</h3>
      <button type="button" className="button" disabled={history.isFetching} onClick={() => { void history.refetch() }}>刷新记录</button>
    </div>
    {history.isPending && <Skeleton rows={1} />}
    {history.error && (history.data ? <Notice error>{errorText(history.error)}</Notice> :
      <PageError error={history.error} retry={() => { void history.refetch() }} />)}
    {history.data?.alerts.map(({ sourceType, sourceKey, reason }) =>
      <Notice key={sourceType + ':' + sourceKey}>{sourceKey}：{alertLabels[reason]}</Notice>)}
    {history.data && (history.data.items.length ? <div className="admin-table-scroll" tabIndex={0} role="region" aria-label="运行记录表格">
      <table className="admin-table admin-directory-table">
        <thead><tr>{['计划时间', ...(rule ? [] : ['规则']), '批次', '创建日期（UTC）', '状态', '上游总量', '完成分片', '去重仓库', '新入库', '开工延迟', '失败原因']
          .map((label) => <th key={label} scope="col">{label}</th>)}</tr></thead>
        <tbody>{history.data.items.map((run) => <tr key={run.id}>
          <td><time dateTime={run.plannedAt}>{timestamp(run.plannedAt)}</time></td>
          {!rule && <td>{run.sourceKey}</td>}
          <td>{run.pass === 'backfill' ? '补扫' : '主扫描'}</td>
          <td>{range(run.createdFrom, run.createdUntil)}</td>
          <td><Badge tone={run.complete ? 'ok' : ['dead', 'failed'].includes(run.status) ? 'warning' : 'neutral'}>
            {run.complete ? '完整' : run.status === 'succeeded' ? '不完整' : jobLabel[run.status]}
          </Badge></td>
          <td>{number(run.upstreamTotal)}</td>
          <td>{run.completedShards}/{run.plannedShards}</td>
          <td>{number(run.uniqueRepos)}</td>
          <td>{number(run.newRepos)}</td>
          <td>{run.delayMs === null ? '—' : run.delayMs < 60_000 ? Math.floor(run.delayMs / 1000) + ' 秒' : Math.floor(run.delayMs / 60_000) + ' 分钟'}</td>
          <td>{run.errorCode ? <code>{run.errorCode}</code> : '—'}</td>
        </tr>)}</tbody>
      </table>
    </div> : <p className="admin-empty">暂无运行记录</p>)}
  </section>
}
