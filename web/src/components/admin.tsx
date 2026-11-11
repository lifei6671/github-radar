import type { ReactNode } from 'react'

export function AdminPanel({ title, action, children, wide }: {
  title: string
  action?: ReactNode
  children: ReactNode
  wide?: boolean
}) {
  return <section className={wide ? 'panel admin-panel admin-wide' : 'panel admin-panel'} aria-label={title}>
    <header className="admin-panel-head"><h2>{title}</h2>{action}</header>
    {children}
  </section>
}

export function AdminMetrics({ items, label }: {
  items: { label: string; value: ReactNode; note?: ReactNode; tone?: 'ok' | 'warning' }[]
  label: string
}) {
  return <dl className="admin-metrics" aria-label={label}>
    {items.map((item) => <div className="panel admin-metric" key={item.label} data-tone={item.tone}>
      <dt>{item.label}</dt>
      <dd><span>{item.value}</span>{item.note && <small>{item.note}</small>}</dd>
    </div>)}
  </dl>
}
