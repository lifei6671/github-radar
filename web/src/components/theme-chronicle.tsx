import { ChevronDown } from 'lucide-react'
import { Link, useLocation } from 'react-router'
import type { ThemeMilestone } from '../api/types'
import { number, repoListReturn, repoPath } from '../lib/format'

const recentCount = 5
const monthKey = new Intl.DateTimeFormat('sv-SE', { year: 'numeric', month: '2-digit', timeZone: 'Asia/Shanghai' })
const monthLabel = new Intl.DateTimeFormat('zh-CN', { month: 'numeric', timeZone: 'Asia/Shanghai' })
const dayLabel = new Intl.DateTimeFormat('zh-CN', { day: 'numeric', timeZone: 'Asia/Shanghai' })

// 沿用 AIHOT 技术方向专题的月度时间线，默认露出最新五件。
export function ThemeChronicle({ events }: { events: ThemeMilestone[] }) {
  if (!events.length) return null
  const older = events.slice(recentCount)
  const newestYear = monthKey.format(new Date(events[0].at)).slice(0, 4)
  return <section className="panel theme-chronicle" aria-labelledby="theme-chronicle-title">
    <header><h2 id="theme-chronicle-title">大事记</h2><span className="minor muted">重要进展</span></header>
    <ChronicleMonths events={events.slice(0, recentCount)} newestYear={newestYear} />
    {older.length > 0 && <details>
      <summary>
        <span className="theme-chronicle-more">查看更早 {number(older.length)} 件大事</span>
        <span className="theme-chronicle-less">收起</span>
        <ChevronDown size={14} aria-hidden />
      </summary>
      <ChronicleMonths events={older} newestYear={newestYear} />
    </details>}
  </section>
}

function ChronicleMonths({ events, newestYear }: { events: ThemeMilestone[]; newestYear: string }) {
  const location = useLocation()
  const months = new Map<string, ThemeMilestone[]>()
  for (const event of events) {
    const key = monthKey.format(new Date(event.at))
    const rows = months.get(key)
    if (rows) rows.push(event)
    else months.set(key, [event])
  }
  return [...months].map(([month, rows]) => <div className="theme-chronicle-month" key={month}>
    <div className="theme-chronicle-row theme-chronicle-month-head">
      <h3>{month.startsWith(newestYear) ? null : <span className="minor muted numeric">{month.slice(0, 4)}年</span>}
        {monthLabel.format(new Date(rows[0].at))}
      </h3>
      <span className="theme-chronicle-marker" aria-hidden />
    </div>
    <ol>{rows.map((row) => <li className="theme-chronicle-row" key={row.fullName + '/' + row.kind + '/' + row.at}>
      <time className="minor muted numeric" dateTime={row.at}>{dayLabel.format(new Date(row.at))}</time>
      <span className="theme-chronicle-marker" data-kind={row.kind} aria-hidden />
      <span className="minor theme-chronicle-kind" data-kind={row.kind}>{row.kind === 'trending' ? '上榜' : 'Star'}</span>
      <Link to={repoPath(row.fullName)} state={repoListReturn(location)}>
        <strong>{row.fullName}</strong>{row.kind === 'trending' ? ' 登上 GitHub Trending 日榜第 ' + row.value + ' 名' : ' 突破 ' + number(row.value) + ' Star'}
      </Link>
    </li>)}</ol>
  </div>)
}
