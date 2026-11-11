import type { ReactNode } from 'react'
import { timestamp } from '../lib/format'
import { PageTitle } from './page-title'

export function RepoCollection({ title, subtitle, actions, children }: { title: string; subtitle: string; actions?: ReactNode; children: ReactNode }) {
  return <section className="repo-collection">
    <header className="repo-collection-head"><PageTitle title={title} subtitle={subtitle} />{actions}</header>
    {children}
  </section>
}

// 两个列表共用卡片层级和整卡链接，移除按钮保留独立点击区域。
export function RepoCollectionRow({ title, metadata, date, dateLabel, action, excerpt }: {
  title: ReactNode; metadata: ReactNode; date?: string | null; dateLabel: string; action: ReactNode; excerpt: string | null
}) {
  return <li className="repo-collection-row">
    <div className="repo-collection-meta minor muted">
      <div className="repo-collection-source">{metadata}</div>
      {date && <time className="repo-collection-time" dateTime={date}>{dateLabel} <span className="numeric">{timestamp(date)}</span></time>}
      <div className="repo-collection-action">{action}</div>
    </div>
    <h2>{title}</h2>
    {excerpt && <p className="repo-collection-excerpt">{excerpt}</p>}
  </li>
}

export function RepoCollectionEmpty({ icon, message, action }: { icon: ReactNode; message: string; action?: ReactNode }) {
  return <div className="repo-collection-empty">
    {icon}<p>{message}</p>{action}
  </div>
}
