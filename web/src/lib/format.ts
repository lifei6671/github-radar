import type { JobStatus, Quality, Repo, SourceQuality, SummaryStatus, TopicStatus } from '../api/types'

const digits = new Intl.NumberFormat('zh-CN')
const shortDate = new Intl.DateTimeFormat('zh-CN', {
  month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
})
const day = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'long', day: 'numeric' })

export function number(value: number | null | undefined) { return value == null ? '—' : digits.format(value) }
export function decimal(value: number | null | undefined) { return value == null ? '—' : value.toFixed(1) }
export function signed(value: number) { return (value > 0 ? '+' : value < 0 ? '−' : '') + digits.format(Math.abs(value)) }
export function timestamp(value: string | null | undefined) { return value ? shortDate.format(new Date(value)) : '—' }
export function dateGroup(value: string) { return day.format(new Date(value)) }

export function formatBytes(value: number) {
  if (value < 1024) return number(value) + ' B'
  if (value < 1024 ** 2) return decimal(value / 1024) + ' KB'
  if (value < 1024 ** 3) return decimal(value / 1024 ** 2) + ' MB'
  return decimal(value / 1024 ** 3) + ' GB'
}

export const qualityLabel: Record<Quality, string> = { exact: '精确', estimated: '估算', insufficient: '采样不足' }
export const summaryLabel: Record<SummaryStatus, string> = {
  missing: '待总结', queued: '已排队', running: '生成中', ready: '已总结',
  stale: '待更新', failed: '生成失败', unknown: '调用结果待确认',
}
export const sourceLabel: Record<SourceQuality, string> = {
  sufficient: '来源完整', incomplete_source: '来源已截取', insufficient_source: '来源信息不足',
}
export const jobLabel: Record<JobStatus, string> = {
  pending: '已排队', running: '执行中', succeeded: '已完成', failed: '等待重试',
  dead: '失败', unknown: '结果待确认', canceled: '已取消',
}
export const topicLabel: Record<TopicStatus, string> = {
  candidate: '候选', shortlisted: '已入围', accepted: '已采用', ignored: '已忽略', published: '已发布',
}
export const repoState: Record<Repo['state'], string> = {
  active: '监控中', candidate: '候选', paused: '已暂停', unavailable: '不可用', address_pending: '地址待确认',
}
export const resolutionLabel = { raw: '原始观测', hourly: '小时聚合', daily: '日聚合', mixed: '混合观测' }

export function githubLink(value: string | null | undefined) {
  if (!value) return undefined
  try {
    const url = new URL(value)
    if (url.protocol === 'https:' && url.hostname === 'github.com' && !url.username && !url.password) return url.href
  } catch { /* 过期或缺失的外链保持不可用。 */ }
  return undefined
}
