import type { JobStatus, Quality, Repo, SourceQuality, SummaryStatus, TopicStatus } from '../api/types'

const digits = new Intl.NumberFormat('zh-CN')
const shortDate = new Intl.DateTimeFormat('zh-CN', {
  month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
})
const day = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'long', day: 'numeric' })
const monthDay = new Intl.DateTimeFormat('zh-CN', { month: 'long', day: 'numeric' })
const weekday = new Intl.DateTimeFormat('zh-CN', { weekday: 'short' })
const clock = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
const relative = new Intl.RelativeTimeFormat('zh-CN')

export function number(value: number | null | undefined) { return value == null ? '—' : digits.format(value) }
export function decimal(value: number | null | undefined) { return value == null ? '—' : value.toFixed(1) }
export function signed(value: number) { return (value > 0 ? '+' : value < 0 ? '−' : '') + digits.format(Math.abs(value)) }
export function timestamp(value: string | null | undefined) { return value ? shortDate.format(new Date(value)) : '—' }
export function encodedRepoName(fullName: string) { return fullName.split('/').map(encodeURIComponent).join('/') }

export function repoPath(fullName: string, admin = false, historicalId?: number) {
  // 后台同名历史记录携带独立身份，复制链接和新标签页打开也指向原记录。
  return (admin ? '/admin/repos/' : '/repos/') + encodedRepoName(fullName) +
    (admin && historicalId !== undefined ? '?repoId=' + historicalId : '')
}
export function dateGroup(value: string) { return day.format(new Date(value)) }
export function dateHeading(value: string) { return monthDay.format(new Date(value)) }
export function dayOfWeek(value: string) { return weekday.format(new Date(value)) }
export function timeOfDay(value: string) { return clock.format(new Date(value)) }

// 主题卡片用相对时间快速提示新旧，完整发现时间由 time 元素保留。
export function relativeTime(value: string) {
  const minutes = Math.floor((Date.now() - Date.parse(value)) / 60_000)
  if (minutes >= 1_440) return relative.format(-Math.floor(minutes / 1_440), 'day')
  if (minutes >= 60) return relative.format(-Math.floor(minutes / 60), 'hour')
  return minutes > 0 ? relative.format(-minutes, 'minute') : '刚刚'
}

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
