import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { api, errorText } from '../api/client'
import type { Topic, TopicEvidence } from '../api/types'
import { decimal, githubLink, number, repoPath, timestamp, topicLabel } from '../lib/format'
import { Notice } from './ui'

const dimensions: Record<string, string> = {
  reader: '普通读者需求', demo: '演示效果', heat: '项目热度', reproduction: '实测与复现', freshness: '话题新鲜度',
}
const confidence: Record<string, string> = {
  inferred: '推测', observed: '已有观测', pending: '待验证', verified: '人工通过', failed: '人工未通过',
}
const riskLabel: Record<string, string> = { code_execution: '执行代码', system_access: '系统权限', personal_data: '个人数据' }

export function TopicAssessment({ topic }: { topic: Topic }) {
  const assessment = topic.assessment
  const review = topic.review
  if (!assessment && !review?.verification && !review?.override) return null
  const brief = assessment?.brief
  return <details className="topic-review-section">
    <summary>分项评分与复核</summary>
    {assessment && <dl className="topic-dimensions" aria-label="五维评分">
      {assessment.dimensions.map((part) => <div key={part.key}>
        <dt>{dimensions[part.key] ?? part.key}</dt>
        <dd className="numeric">{decimal(part.points)} / {part.maximum}</dd>
        <dd className="minor muted">{[part.source, confidence[part.confidence], part.evidence].filter(Boolean).join(' / ')}</dd>
      </div>)}
    </dl>}
    {brief?.readerBenefit && <p className="minor">读者收益　{brief.readerBenefit}</p>}
    {brief?.demo && <p className="minor">演示场景　{brief.demo}</p>}
    {brief?.angle && <p className="minor">写作角度　{brief.angle}</p>}
    {Boolean(brief?.steps?.length) && <ol className="minor topic-review-steps" aria-label="建议实测步骤">
      {brief?.steps?.map((step, i) => <li key={i}>{step}</li>)}
    </ol>}
    {Boolean(brief?.risks?.length) && <p className="minor">需人工核验　{brief?.risks?.map((risk) => riskLabel[risk] ?? risk).join('、')}</p>}
    {review?.verification && <p className="minor">人工核验　{confidence[review.verification]}{review.evidence && ' / ' + review.evidence}</p>}
    {review?.override && <p className="minor">覆盖理由　{review.override}</p>}
  </details>
}

function sourceLink(value: string) {
  try {
    const url = new URL(value)
    if (url.protocol === 'https:' || url.protocol === 'http:') return url.href
  } catch { /* 来源无效时只显示其他可用证据。 */ }
  return null
}

export function TopicSources({ topic, from }: { topic: Topic; from: string }) {
  const [opened, setOpened] = useState(false)
  const evidence = useQuery({
    queryKey: ['topics', 'evidence', topic.id],
    queryFn: ({ signal }) => api<TopicEvidence>('/topics/' + topic.id + '/evidence', { signal }),
    enabled: opened,
  })
  const repo = repoPath(topic.fullName, true, topic.nameIsCurrent === false ? topic.repoId : undefined)
  const link = githubLink(topic.sourceUrl)
  return <details className="topic-review-section" onToggle={(event) => setOpened(event.currentTarget.open)}>
    <summary>来源与历史</summary>
    <div className="topic-evidence">
      <Link to={repo + '#readme'} state={{ from, repoId: topic.repoId }}>README</Link>
      <Link to={repo + '#star-trend'} state={{ from, repoId: topic.repoId }}>Star 趋势</Link>
      {link && <a href={link} target="_blank" rel="noopener noreferrer">GitHub 来源</a>}
      {topic.growth24h != null && <span>24h 实测净增 {number(topic.growth24h)}</span>}
    </div>
    {opened && evidence.isPending && <p className="minor muted">正在读取来源</p>}
    {evidence.error && <Notice error>{errorText(evidence.error)}</Notice>}
    {evidence.data?.events.map((event) => {
      const href = sourceLink(event.sourceUrl)
      return <p className="minor" key={event.sourceType + ':' + event.sourceKey}>
        {href ? <a href={href} target="_blank" rel="noopener noreferrer">{href}</a> : '人工提交'}
        {' / '}{timestamp(event.firstObservedAt)}{event.note && ' / ' + event.note}
      </p>
    })}
    {evidence.data?.trending.map((entry) => <p className="minor" key={[entry.source, entry.period, entry.at].join('/')}>
      GitHub Trending {entry.source} / {entry.period} / #{entry.rank} / {timestamp(entry.at)}
    </p>)}
    {Boolean(evidence.data?.history.length) && <ul className="repo-discovery-events" aria-label="往期复核">
      {evidence.data?.history.map((entry) => <li key={entry.id}>
        <strong>{topicLabel[entry.status]} / 选题分 {decimal(entry.topicScore)}{entry.scoreVersion === 1 && '（旧版）'}</strong>
        <time className="minor muted" dateTime={entry.archivedAt}>{timestamp(entry.archivedAt)}</time>
        {entry.feedback && <p className="minor">{entry.feedback}</p>}
        {entry.review?.evidence && <p className="minor">{entry.review.evidence}</p>}
        {entry.review?.override && <p className="minor">{entry.review.override}</p>}
      </li>)}
    </ul>}
  </details>
}
