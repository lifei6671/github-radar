import type { ReactNode } from 'react'

// 菜单页共用标题与下方说明的层级，操作区由各页面自己布局。
export function PageTitle({ title, subtitle, icon }: { title: string; subtitle: string; icon?: ReactNode }) {
  return <div className="page-heading">
    <h1 className={icon ? 'title-with-icon' : undefined}>{icon}<span>{title}</span></h1>
    <p className="page-subtitle">{subtitle}</p>
  </div>
}
