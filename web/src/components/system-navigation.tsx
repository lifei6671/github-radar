import { Link } from 'react-router'
import { PageTitle } from './page-title'

const pages = [
  ['overview', '概览', '查看采集进度、调用额度和存储状态。'],
  ['jobs', '任务', '查看采集和摘要任务的执行记录。'],
  ['runtime', '运行配置', '配置 GitHub 采集、热度计算、AI 与数据保留。'],
  ['categories', '分类映射', '将项目的主要技术用途映射到分类。'],
  ['technical-tags', '技术词表', ''],
  ['rules', '发现规则', '管理 GitHub Search 与 Trending 的采集来源。'],
  ['discovery-tabs', '发现页标签', '设置发现页的分类顺序与精选门槛。'],
  ['project-themes', '专题管理', '维护专题的简介、图标和项目匹配词。'],
  ['backup', '备份与恢复', '管理 R2 备份计划、保留期限和数据库恢复。'],
] as const

export function systemPage(tab: string | null) {
  return pages.find(([key]) => key === tab)?.[0] ?? 'overview'
}

export function SystemHeader({ tab }: { tab: ReturnType<typeof systemPage> }) {
  const [key, name, subtitle] = pages.find(([key]) => key === tab) ?? pages[0]
  // 运行入口复用仓库分类栏的标签样式，链接保留地址和历史记录。
  return <header className="system-head">
    <PageTitle title={key === 'overview' ? '运行状态' : name} subtitle={subtitle} />
    <nav className="pills system-tabs" aria-label="运行管理">
      {pages.map(([key, label]) => <Link className="pill-link" key={key} aria-current={tab === key ? 'page' : undefined}
        to={key === 'overview' ? '/admin/runs' : '/admin/runs?tab=' + key}>{label}</Link>)}
    </nav>
  </header>
}
