import { ChevronDown } from 'lucide-react'
import { Link } from 'react-router'
import type { ThemeMilestone } from '../api/types'
import { number, repoPath } from '../lib/format'

const recentCount = 5
const monthKey = new Intl.DateTimeFormat('sv-SE', { year: 'numeric', month: '2-digit', timeZone: 'Asia/Shanghai' })
const monthLabel = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'long', timeZone: 'Asia/Shanghai' })
const dayLabel = new Intl.DateTimeFormat('zh-CN', { day: 'numeric', timeZone: 'Asia/Shanghai' })

// 沿用 AIHOT 技术方向专题的月度时间线，默认露出最新五件。
export function ThemeChronicle({ events }: { events: ThemeMilestone[] }) {
  if (!events.length) return null
  const older = events.slice(recentCount)
  return <section className="panel theme-chronicle" aria-labelledby="theme-chronicle-title">
    <header><h2 id="theme-chronicle-title">大事记</h2><span className="minor muted">重要进展</span></header>
    <ChronicleMonths events={events.slice(0, recentCount)} />
    {older.length > 0 && <details>
      <summary><ChevronDown size={14} aria-hidden />查看更早 {number(older.length)} 件大事</summary>
      <ChronicleMonths events={older} />
    </details>}
  </section>
}

function ChronicleMonths({ events }: { events: ThemeMilestone[] }) {
  const months = new Map<string, ThemeMilestone[]>()
  for (const event of events) {
    const key = monthKey.format(new Date(event.at))
    const rows = months.get(key)
    if (rows) rows.push(event)
    else months.set(key, [event])
  }
  return [...months].map(([month, rows]) => <div className="theme-chronicle-month" key={month}>
    <h3>{monthLabel.format(new Date(rows[0].at))}</h3>
    <ol>{rows.map((row) => <li key={row.fullName + '/' + row.kind + '/' + row.at}>
      <time className="minor muted numeric" dateTime={row.at}>{dayLabel.format(new Date(row.at))}</time>
      <span className="minor theme-chronicle-kind" data-kind={row.kind}>{row.kind === 'trending' ? '上榜' : 'Star'}</span>
      <Link to={repoPath(row.fullName)}>
        <strong>{row.fullName}</strong>{row.kind === 'trending' ? ' 登上 GitHub Trending 日榜第 ' + row.value + ' 名' : ' 突破 ' + number(row.value) + ' Star'}
      </Link>
    </li>)}</ol>
  </div>)
}
