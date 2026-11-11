import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ArrowUpRight, Download, Workflow } from 'lucide-react'
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api, errorText } from '../api/client'
import { distinct, useTopics } from '../api/queries'
import type { Topic, TopicStatus } from '../api/types'
import { External } from '../components/repo'
import { Badge, Empty, More, Notice, PageError, Skeleton, Spinner } from '../components/ui'
import { decimal, githubLink, timestamp, topicLabel } from '../lib/format'

const statusList = Object.keys(topicLabel) as TopicStatus[]

export function Topics() {
  const [params, setParams] = useSearchParams()
  const status = statusList.find((entry) => entry === params.get('status'))
  const criteria = new URLSearchParams({ limit: '30' })
  if (status) criteria.set('status', status)
  const query = useTopics(criteria)
  const items = distinct(query.data?.pages.flatMap((page) => page.items) ?? [])
  return <>
    <header className="page-head"><h1>选题池</h1>
      {items.length > 0 && <div className="action-row">
        <button className="button" onClick={() => download(items, 'md')}><Download size={16} />Markdown</button>
        <button className="button" onClick={() => download(items, 'json')}><Download size={16} />JSON</button>
      </div>}
    </header>
    <div className="pills wrap ranking-tabs" aria-label="选题状态">
      <button aria-pressed={!status} onClick={() => setParams({}, { preventScrollReset: true })}>全部</button>
      {statusList.map((key) => <button key={key} aria-pressed={status === key}
        onClick={() => setParams({ status: key }, { preventScrollReset: true })}>{topicLabel[key]}</button>)}
    </div>
    {query.isPending ? <Skeleton /> : query.error && !query.data ? <PageError error={query.error} retry={() => { void query.refetch() }} /> :
      items.length === 0 ? <Empty title="暂无匹配的选题"><Link className="button" to="/repos">浏览项目<ArrowUpRight size={16} /></Link></Empty> :
        <div className="topic-list">
          {query.error && <Notice error>{errorText(query.error)}</Notice>}
          {items.map((item) => <TopicCard key={item.id} topic={item} />)}
          <More hasMore={query.hasNextPage} pending={query.isFetchingNextPage} onClick={() => { void query.fetchNextPage() }} />
        </div>}
  </>
}

function TopicCard({ topic }: { topic: Topic }) {
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
  return <article className="panel topic-card">
    <header className="section-head">
      <div className="topic-title"><Workflow size={18} className="tone-accent" /><h2><Link to={'/repos/' + topic.repoId}>{topic.titleZh}</Link></h2></div>
      <Badge tone={topic.status === 'accepted' || topic.status === 'published' ? 'ok' : 'neutral'}>{topicLabel[topic.status]}</Badge>
    </header>
    <div className="topic-repo"><Link to={'/repos/' + topic.repoId}>{topic.fullName}</Link><External href={topic.sourceUrl} iconOnly /></div>
    {topic.readerValue && <p>{topic.readerValue}</p>}
    {topic.summaryZh && topic.summaryZh !== topic.readerValue && <details><summary>中文摘要</summary><p className="summary-body">{topic.summaryZh}</p></details>}
    <div className="context-tags repo-tags">{topic.valueTags.map((tag) => <Link className="context-chip" key={tag} to={'/repos?' + new URLSearchParams({ tag })}>{tag}</Link>)}</div>
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
        <button className="button primary" disabled={save.isPending}>{save.isPending && <Spinner />}保存反馈</button>
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
