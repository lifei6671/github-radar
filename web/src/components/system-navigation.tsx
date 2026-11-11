import type { ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router'

const pages = [
  ['overview', '概览'], ['jobs', '任务'], ['runtime', '运行配置'], ['categories', '分类映射'],
  ['rules', '发现规则'], ['discovery-tabs', '发现页标签'], ['backup', '备份与恢复'],
] as const

export function systemPage(tab: string | null) {
  return pages.find(([key]) => key === tab)?.[0] ?? 'overview'
}

export function SystemHeader({ children }: { children: ReactNode }) {
  const [params] = useSearchParams()
  const tab = systemPage(params.get('tab'))
  // 运行入口复用仓库分类栏的标签样式，链接保留地址和历史记录。
  return <header className="system-head">
    <div className="system-title">{children}</div>
    <nav className="pills system-tabs" aria-label="运行管理">
      {pages.map(([key, label]) => <Link className="pill-link" key={key} aria-current={tab === key ? 'page' : undefined}
        to={key === 'overview' ? '/admin/runs' : '/admin/runs?tab=' + key}>{label}</Link>)}
    </nav>
  </header>
}
