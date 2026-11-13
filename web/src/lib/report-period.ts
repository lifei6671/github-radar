export const reportKinds = ['daily', 'weekly', 'monthly'] as const
export type ReportKind = typeof reportKinds[number]

export const reportNames = { daily: '日报', weekly: '周报', monthly: '月报' }

export function reportPath(kind: ReportKind, date = '') {
  return '/' + kind + (date ? '/' + date : '')
}

export function issueGrid(kind: ReportKind, date: string) {
  const year = date.slice(0, 4)
  const pad = (n: number) => String(n).padStart(2, '0')
  if (kind === 'daily') {
    const month = date.slice(0, 7)
    const first = new Date(month + '-01T00:00:00Z')
    const last = new Date(first)
    last.setUTCMonth(first.getUTCMonth() + 1, 0)
    const leading = (first.getUTCDay() + 6) % 7
    return {
      title: ['一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '十一', '十二'][first.getUTCMonth()] + '月',
      columns: 7, heads: ['一', '二', '三', '四', '五', '六', '日'],
      keys: [...Array.from({ length: leading }, () => ''), ...Array.from({ length: last.getUTCDate() }, (_, i) => month + '-' + pad(i + 1))],
    }
  }
  if (kind === 'weekly') {
    const start = new Date(year + '-01-01T00:00:00Z').getUTCDay()
    const leap = new Date(year + '-03-01T00:00:00Z').getTime() - new Date(year + '-02-01T00:00:00Z').getTime() === 29 * 86_400_000
    const weeks = start === 4 || (start === 3 && leap) ? 53 : 52
    return { title: year + ' 年', columns: 13, heads: [], keys: Array.from({ length: weeks }, (_, i) => year + '-W' + pad(i + 1)) }
  }
  return { title: year + ' 年', columns: 6, heads: [], keys: Array.from({ length: 12 }, (_, i) => year + '-' + pad(i + 1)) }
}
