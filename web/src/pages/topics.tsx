import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUpRight, ChevronDown, Download } from 'lucide-react'
import { useState } from 'react'
import { Link, useLocation, useSearchParams } from 'react-router'
import { api, errorText } from '../api/client'
import { distinct, useTopics } from '../api/queries'
import { PageTitle } from '../components/page-title'
import type { Items, Topic, TopicDay, TopicStatus } from '../api/types'
import { External } from '../components/repo'
import { ActionButton, Badge, Empty, More, Notice, PageError, Skeleton } from '../components/ui'
import { decimal, githubLink, number, repoPath, timestamp, topicLabel } from '../lib/format'

const statusList = Object.keys(topicLabel) as TopicStatus[]
const editionDate = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'full', timeZone: 'UTC' })
const editionMonth = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'long', timeZone: 'UTC' })
const editionWeekday = new Intl.DateTimeFormat('zh-CN', { weekday: 'short', timeZone: 'UTC' })
const dateOf = (day: string) => new Date(day + 'T00:00:00Z')

export function Topics() {
  const [params, setParams] = useSearchParams()
  const status = statusList.find((entry) => entry === params.get('status'))
  const days = useQuery({
    queryKey: ['topics', 'days', status],
    queryFn: ({ signal }) => api<Items<TopicDay>>('/topics/days' + (status ? '?' + new URLSearchParams({ status }) : ''), { signal }),
  })
  const archive = days.data?.items ?? []
  const day = params.get('day') || archive[0]?.day
  const active = archive.find((item) => item.day === day)

  function choose(key: 'day' | 'status', value: string) {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(next)
  }

  return <div className="topic-edition-layout">
    <TopicArchive days={archive} day={day} status={status} onDay={(value) => choose('day', value)} onStatus={(value) => choose('status', value)} />
    <div className="topic-edition">
      <TopicMasthead day={day} />
      {days.error && !days.data ? <PageError error={days.error} retry={() => { void days.refetch() }} /> :
        days.isPending ? <Skeleton /> : <>
          {days.error && <Notice error>{errorText(days.error)}</Notice>}
          {day ? <TopicEdition day={day} total={active?.count} status={status} /> :
            <EmptyTopics />}
        </>}
    </div>
  </div>
}

function TopicArchive({ days, day, status, onDay, onStatus }: {
  days: TopicDay[]; day?: string; status?: TopicStatus; onDay: (day: string) => void; onStatus: (status: string) => void
}) {
  const months = new Map<string, TopicDay[]>()
  for (const item of days) {
    const month = item.day.slice(0, 7)
    const group = months.get(month)
    if (group) group.push(item)
    else months.set(month, [item])
  }
  return <aside className="topic-archive" aria-label="选题版面导航">
    <div className="pills topic-archive-filters" role="group" aria-label="选题状态">
      <button aria-pressed={!status} onClick={() => onStatus('')}>全部</button>
      {statusList.map((key) => <button key={key} aria-pressed={status === key} onClick={() => onStatus(key)}>{topicLabel[key]}</button>)}
    </div>
    {days.length > 0 && <>
      <nav className="topic-archive-days" aria-label="往期选题">
        <h2>往期选题</h2>
        {Array.from(months, ([month, entries], index) => <details key={month} open={index === 0 || day?.startsWith(month)}>
          <summary><ChevronDown size={14} aria-hidden />{editionMonth.format(dateOf(entries[0].day))}<span className="minor muted">{number(entries.length)}</span></summary>
          <ul>{entries.map((entry) => <li key={entry.day}>
            <button type="button" onClick={() => onDay(entry.day)} aria-current={entry.day === day ? 'date' : undefined}>
              <time dateTime={entry.day}><strong className="numeric">{entry.day.slice(-2)}</strong><span>{editionWeekday.format(dateOf(entry.day))}</span></time>
              <span className="topic-archive-title">{entry.title}</span>
            </button>
          </li>)}</ul>
        </details>)}
      </nav>
      <label className="field topic-mobile-days">选题日期
        <select value={day ?? ''} onChange={(event) => onDay(event.target.value)}>
          {day && !days.some((entry) => entry.day === day) && <option value={day}>{day}</option>}
          {days.map((entry) => <option key={entry.day} value={entry.day}>{editionDate.format(dateOf(entry.day))}</option>)}
        </select>
      </label>
    </>}
  </aside>
}

function TopicMasthead({ day }: { day?: string }) {
  const date = day ? dateOf(day) : null
  const shown = date && !Number.isNaN(date.getTime()) ? date : null
  return <header className="topic-masthead">
    <PageTitle title="选题池" subtitle="按日期浏览 GitHub 选题，记录审核状态与反馈。" />
    {shown && <time className="topic-date-tile" dateTime={day}>
      <strong className="numeric">{shown.getUTCDate()}</strong>
      <span>{editionMonth.format(shown)}</span><span>{editionWeekday.format(shown)}</span>
    </time>}
  </header>
}

function EmptyTopics() {
  return <Empty title="暂无匹配的选题"><Link className="button" to="/admin/repos">浏览项目<ArrowUpRight size={16} /></Link></Empty>
}

function TopicEdition({ day, total, status }: { day: string; total?: number; status?: TopicStatus }) {
  const criteria = new URLSearchParams({ limit: '30', day })
  if (status) criteria.set('status', status)
  const query = useTopics(criteria)
  const items = distinct(query.data?.pages.flatMap((page) => page.items) ?? [])
  const [lead, ...rest] = items
  const groups = statusList.map((key) => ({ key, items: rest.filter((item) => item.status === key) })).filter((group) => group.items.length > 0)
  if (query.isPending) return <Skeleton />
  if (query.error && !query.data) return <PageError error={query.error} retry={() => { void query.refetch() }} />
  return <>
    {query.error && <Notice error>{errorText(query.error)}</Notice>}
    <div className="topic-edition-stats">
      <span><strong className="numeric">{number(total ?? items.length)}</strong> 个选题</span>
      {status && <span>{topicLabel[status]}</span>}
      {query.hasNextPage && <span>已加载 {number(items.length)} 个</span>}
      {items.length > 0 && <div className="action-row">
        <button className="text-button" title="导出当前已加载选题" onClick={() => download(items, 'md')}><Download size={14} />Markdown</button>
        <button className="text-button" title="导出当前已加载选题" onClick={() => download(items, 'json')}><Download size={14} />JSON</button>
      </div>}
    </div>
    {lead ? <div className="topic-list">
      <section className="topic-lead" aria-labelledby="topic-lead-title">
        <h2 id="topic-lead-title" className="topic-section-label">头条</h2>
        <TopicCard key={lead.id} topic={lead} lead />
      </section>
      {groups.length > 0 && <div className="topic-overview">
        <nav className="panel topic-contents" aria-label="本期看点">
          <h2>本期看点</h2>
          {rest.slice(0, 3).map((item) => <a key={item.id} href={'#topic-item-' + item.id}>{item.titleZh}</a>)}
        </nav>
        <nav className="panel topic-contents" aria-label="本期目录">
          <h2>本期目录</h2>
          {groups.map(({ key, items: group }) => <a key={key} href={'#topic-' + key}>{topicLabel[key]}<span className="minor muted">{number(group.length)}</span></a>)}
        </nav>
      </div>}
      {groups.map(({ key, items: group }) => <section className="topic-section" key={key} id={'topic-' + key} aria-labelledby={'topic-heading-' + key}>
        <header className="topic-section-head"><h2 id={'topic-heading-' + key}>{topicLabel[key]}</h2><span className="minor muted">{number(group.length)} 个</span></header>
        {group.map((item) => <TopicCard key={item.id} topic={item} />)}
      </section>)}
      <More hasMore={query.hasNextPage} pending={query.isFetchingNextPage} onClick={() => { void query.fetchNextPage() }} />
    </div> : <EmptyTopics />}
  </>
}

function TopicCard({ topic, lead = false }: { topic: Topic; lead?: boolean }) {
  const location = useLocation()
  const from = location.pathname + location.search
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState({ status: topic.status, feedback: topic.feedback ?? '' })
  const save = useMutation({
    mutationFn: () => api<Pick<Topic, 'id' | 'status'>>('/topics/' + topic.id, { method: 'PATCH', body: draft }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['topics'] })
      setEditing(false)
    },
  })
  return <article id={'topic-item-' + topic.id} className={'topic-card' + (lead ? ' panel topic-lead-card' : '')}>
    <header className="section-head">
      <div className="topic-repo"><Link to={repoPath(topic.fullName, true, topic.nameIsCurrent === false ? topic.repoId : undefined)} state={{ from, repoId: topic.repoId }}>{topic.fullName}</Link><External href={topic.sourceUrl} iconOnly /></div>
      <Badge tone={topic.status === 'accepted' || topic.status === 'published' ? 'ok' : 'neutral'}>{topicLabel[topic.status]}</Badge>
    </header>
    <h3 className="topic-title"><Link to={repoPath(topic.fullName, true, topic.nameIsCurrent === false ? topic.repoId : undefined)} state={{ from, repoId: topic.repoId }}>{topic.titleZh}</Link></h3>
    {(topic.summaryZh || topic.readerValue) && <p className="topic-summary">{topic.summaryZh || topic.readerValue}</p>}
    {topic.summaryZh && topic.readerValue && topic.summaryZh !== topic.readerValue && <p className="minor muted">选题价值 {topic.readerValue}</p>}
    <div className="context-tags repo-tags">{topic.valueTags.map((tag) => <Link className="context-chip" key={tag} to={'/admin/repos?' + new URLSearchParams({ tag })}>{tag}</Link>)}</div>
    <div className="topic-evidence">
      <span>{topic.reason}</span>
      <span>选题分 <strong className="numeric">{decimal(topic.topicScore)}</strong></span>
      {topic.hotnessScore !== null && <span>热度 <strong className="numeric">{decimal(topic.hotnessScore)}</strong></span>}
      {topic.recommendationScore !== null && <span>推荐分 <strong className="numeric">{decimal(topic.recommendationScore)}</strong></span>}
    </div>
    {topic.feedback && !editing && <p className="feedback-text">反馈：{topic.feedback}</p>}
    {editing ? <form className="topic-form" onSubmit={(event) => { event.preventDefault(); if (!save.isPending) save.mutate() }}>
      <label className="field">状态<select value={draft.status} disabled={save.isPending} onChange={(event) => setDraft({ ...draft, status: event.target.value as TopicStatus })}>
        {statusList.map((key) => <option key={key} value={key}>{topicLabel[key]}</option>)}
      </select></label>
      <label className="field feedback-field">反馈<textarea value={draft.feedback} disabled={save.isPending} rows={2} maxLength={500}
        onChange={(event) => setDraft({ ...draft, feedback: event.target.value })} /></label>
      <div className="action-row">
        <button type="button" className="button" disabled={save.isPending} onClick={() => setEditing(false)}>取消</button>
        <ActionButton type="submit" className="button primary" pending={save.isPending}>保存反馈</ActionButton>
      </div>
      {save.error && <Notice error>{errorText(save.error)}</Notice>}
    </form> : <footer className="topic-footer">
      <span className="muted minor">更新于 {timestamp(topic.updatedAt)}</span>
      <button className="button" onClick={() => {
        setDraft({ status: topic.status, feedback: topic.feedback ?? '' })
        save.reset()
        setEditing(true)
      }}>编辑状态与反馈</button>
    </footer>}
  </article>
}

function download(items: Topic[], format: 'md' | 'json') {
  // 导出当前已加载的条目与已保存摘要，证据只包含来源链接。
  const content = format === 'json' ? JSON.stringify(items, null, 2) : items.map((item) => [
    '## ' + item.titleZh,
    item.fullName + ' | ' + topicLabel[item.status],
    item.reason + ' | 选题分 ' + decimal(item.topicScore),
    githubLink(item.sourceUrl) ?? '',
    item.summaryZh ?? item.readerValue,
    item.feedback ? '反馈：' + item.feedback : '',
  ].filter(Boolean).join('\n\n')).join('\n\n---\n\n')
  const url = URL.createObjectURL(new Blob([content], { type: format === 'json' ? 'application/json' : 'text/markdown;charset=utf-8' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = 'github-radar-topics.' + format
  anchor.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}
