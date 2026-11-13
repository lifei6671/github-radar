import { queryOptions, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ArrowLeft, ChevronRight, List } from 'lucide-react'
import { Link, useLocation, useParams, useSearchParams } from 'react-router'
import type { LoaderFunctionArgs, Location } from 'react-router'
import { publicApi, sessionQueryClient } from '../api/client'
import { dailyReportsKey } from '../api/queries'
import { External } from '../components/repo'
import { Dialog, Empty, PageError, Skeleton } from '../components/ui'
import { repoListReturn, repoPath } from '../lib/format'
import { issueGrid, reportKinds, reportNames, reportPath } from '../lib/report-period'
import type { ReportKind } from '../lib/report-period'

interface Citation {
  repoId: number
  fullName: string
  htmlUrl: string
}

interface Story { title: string; body: string; repoIds: number[] }

interface DailyIssue {
  date: string
  number: number | null
  observedFrom: string
  observedTo: string
  generatedAt: string
  periodFrom?: string
  periodTo?: string
  publicationStatus?: 'preview' | 'published'
  coverageDays?: number
  requiredDays?: number
  headline: string
  lead: string
  stories: Story[]
  items: Citation[]
}

interface DailyResponse {
  report: DailyIssue | null
  archive: ArchiveItem[]
  nextBefore: string | null
  total: number
  calendar: string[] | null
}

type ArchiveItem = { date: string; count: number; headline: string; periodFrom?: string }

const archiveDate = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'long', day: 'numeric', timeZone: 'Asia/Shanghai' })
const shortDate = new Intl.DateTimeFormat('zh-CN', { month: 'long', day: 'numeric', timeZone: 'Asia/Shanghai' })
const weekday = new Intl.DateTimeFormat('zh-CN', { weekday: 'long', timeZone: 'Asia/Shanghai' })
const coverageDate = new Intl.DateTimeFormat('zh-CN', {
  month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone: 'Asia/Shanghai',
})

function reportOptions(kind: ReportKind, date: string, before: string) {
  const params = new URLSearchParams()
  if (kind !== 'daily') params.set('kind', kind)
  if (date) params.set('date', date)
  if (before) params.set('before', before)
  return queryOptions({
    queryKey: [dailyReportsKey, kind, date, before] as const,
    meta: { pageLoad: true },
    queryFn: ({ signal }) => publicApi<DailyResponse>('/daily-reports' + (params.size ? '?' + params : ''), { signal }),
  })
}

export async function loadReportPage(kind: ReportKind, { params, request }: LoaderFunctionArgs) {
  const before = new URL(request.url).searchParams.get('before') ?? ''
  const date = kind === 'daily' && params.date === 'archive' ? '' : params.date ?? ''
  await sessionQueryClient().query({ ...reportOptions(kind, date, before), staleTime: 0 }).catch(() => undefined)
  return null
}

export function ReportPage({ kind }: { kind: ReportKind }) {
  const { date: routeDate = '' } = useParams()
  const archivePage = kind === 'daily' && routeDate === 'archive'
  const date = archivePage ? '' : routeDate
  const [params] = useSearchParams()
  const before = params.get('before') ?? ''
  const location = useLocation()
  const query = useQuery(reportOptions(kind, date, before))
  const [outlineIssue, setOutlineIssue] = useState('')
  if (query.isPending) return <Skeleton />
  const { archive = [], report, nextBefore = null, total = archive.length, calendar = null } = query.data ?? {}
  const name = reportNames[kind]
  const issueIndex = archive.findIndex((issue) => issue.date === report?.date)
  const older = issueIndex >= 0 ? archive[issueIndex + 1] : undefined
  const newer = issueIndex > 0 ? archive[issueIndex - 1] : undefined
  const recent = kind === 'daily' ? archive.slice(0, 3) : [...archive]
  if (report && report.publicationStatus !== 'preview' && !recent.some((issue) => issue.date === report.date)) {
    recent.push({ date: report.date, count: report.stories.length, headline: report.headline, periodFrom: report.periodFrom })
  }
  const current = archivePage ? '' : report?.date ?? ''
  return <div className="daily-layout">
    <aside className="daily-archive">
      <ReportSwitch kind={kind} />
      <h2>往期</h2>
      <Archive kind={kind} archive={archive} before={before} nextBefore={nextBefore} path={location.pathname} current={current} />
      {kind === 'daily' && <Link className="daily-book-link" to="/daily/archive" aria-current={archivePage ? 'page' : undefined}>
        日报合订本<ChevronRight size={14} aria-hidden />
      </Link>}
    </aside>
    <div className="daily-reader">
      <div className="daily-phone-nav">
        <div className="daily-phone-tools">
          <ReportSwitch kind={kind} />
          {report && !archivePage && <button className="button" type="button" onClick={() => setOutlineIssue(report.date)}><List size={15} aria-hidden />本期目录</button>}
        </div>
        {!archivePage && recent.length > 0 && <nav aria-label={'往期' + name} className="daily-recent">
          {recent.map((issue) => <Link key={issue.date} to={reportPath(kind, issue.date) + (before ? '?' + new URLSearchParams({ before }) : '')}
            aria-current={issue.date === current ? 'page' : undefined}>{issueLabel(kind, issue.date)}</Link>)}
          {kind === 'daily' && <Link to="/daily/archive">更早</Link>}
          {kind !== 'daily' && nextBefore && <Link to={location.pathname + '?' + new URLSearchParams({ before: nextBefore })}>更早</Link>}
          {kind !== 'daily' && before && <Link to={reportPath(kind)}>最新{name}</Link>}
        </nav>}
      </div>
      {query.error ? <div className="daily-empty">
        <h1>GitHub 雷达{name}</h1>
        {date && <Link className="daily-latest" to={reportPath(kind)}><ArrowLeft size={14} aria-hidden />最新{name}</Link>}
        <PageError error={query.error} retry={() => { void query.refetch() }} />
      </div> : archivePage ? <ArchiveBook archive={archive} before={before} nextBefore={nextBefore} total={total} /> :
        !report ? <div className="daily-empty">
          <h1>GitHub 雷达{name}</h1>
          <Empty title={kind === 'daily' ? '首期日报尚未发布' : '等待采集与组稿'} />
        </div> : <ReportPaper kind={kind} report={report} calendar={calendar ?? []} location={location} older={older} newer={newer}
          showOutline={outlineIssue === report.date} closeOutline={() => setOutlineIssue('')} />}
    </div>
  </div>
}

function ReportPaper({ kind, report, calendar, location, older, newer, showOutline, closeOutline }: {
  kind: ReportKind; report: DailyIssue; location: Location; older?: ArchiveItem; newer?: ArchiveItem;
  calendar: string[]; showOutline: boolean; closeOutline: () => void
}) {
  const citations = new Map(report.items.map((item) => [item.repoId, item]))
  const highlights = report.publicationStatus === 'preview' ? '采集看点' : '本期看点'
  return <>
      {showOutline && <Dialog title="本期目录" onClose={closeOutline}>
        <a className="daily-outline-link" href="#daily-title" onClick={closeOutline}>头版</a>
        {report.stories.map((story, index) => <a key={index} className="daily-outline-link" href={'#story-' + (index + 1)}
          onClick={closeOutline}>{story.title}</a>)}
      </Dialog>}
      <article className="daily-paper" aria-labelledby="daily-title">
        <Masthead kind={kind} report={report} calendar={calendar} />
        <div className="daily-front">
          <section className="daily-lead" aria-label="头条">
            <div className="daily-kicker">头条</div>
            <h2>{report.headline}</h2>
            <p className="repo-summary">{report.lead}</p>
          </section>
          <aside className="daily-front-index">
            <nav className="daily-highlights" aria-label={highlights}>
              <h2 className="daily-kicker">{highlights}</h2>
              <ol>{report.stories.map((story, index) => <li key={index}>
                <a href={'#story-' + (index + 1)}><strong>{index + 1}</strong><span>{story.title}</span></a>
              </li>)}</ol>
            </nav>
          </aside>
        </div>
        {report.stories.map((story, index) => <section key={index} className="daily-section" aria-labelledby={'story-' + (index + 1)}>
          <header><span>{pageNumber(index)}</span><h2 id={'story-' + (index + 1)}>{story.title}</h2></header>
          <p className="report-story-body">{story.body}</p>
          <ul className="report-citations" aria-label="相关项目">{story.repoIds.map((id) => {
            const item = citations.get(id)
            return item && <li key={id}>
              <Link to={repoPath(item.fullName)} state={repoListReturn(location)}>{item.fullName}</Link>
              <External href={item.htmlUrl} iconOnly />
            </li>
          })}</ul>
        </section>)}
        {(older || newer) && <nav aria-label="相邻期号" className="daily-neighbours">
          {older && <Link to={reportPath(kind, older.date)}>上一期 {issueLabel(kind, older.date)}</Link>}
          {newer && <Link to={reportPath(kind, newer.date)}>下一期 {issueLabel(kind, newer.date)}</Link>}
        </nav>}
        {kind === 'daily' && <footer className="daily-colophon"><Link to="/daily/archive">日报合订本</Link></footer>}
      </article>
  </>
}

function pageNumber(index: number) { return String(index + 1).padStart(2, '0') }

function ReportSwitch({ kind }: { kind: ReportKind }) {
  return <nav aria-label="切换日报、周报、月报" className="report-kind-switch">
    {reportKinds.map((value) => <Link key={value} to={reportPath(value)}
      aria-current={kind === value ? 'true' : undefined}>{reportNames[value]}</Link>)}
  </nav>
}

function issueLabel(kind: ReportKind, key: string) {
  const year = Number(key.slice(0, 4))
  if (kind === 'weekly') return year + ' 年第 ' + Number(key.slice(-2)) + ' 周'
  if (kind === 'monthly') return year + ' 年 ' + Number(key.slice(-2)) + ' 月'
  return archiveDate.format(new Date(key))
}

function issueMark(kind: ReportKind, issue: Pick<ArchiveItem, 'date' | 'periodFrom'>) {
  if (kind === 'weekly') return issue.periodFrom ? Number(issue.periodFrom.slice(5, 7)) + '.' + Number(issue.periodFrom.slice(-2)) + ' 起' : ''
  return kind === 'daily' ? weekday.format(new Date(issue.date)).replace('星期', '周') : ''
}

function Masthead({ kind, report, calendar }: { kind: ReportKind; report: DailyIssue; calendar: string[] }) {
  const label = issueLabel(kind, report.date)
  const name = reportNames[kind]
  const preview = report.publicationStatus === 'preview'
  const coverage = coverageDate.formatRange(new Date(report.observedFrom), new Date(report.observedTo))
  const period = report.periodFrom && report.periodTo
    ? shortDate.formatRange(new Date(report.periodFrom), new Date(report.periodTo))
    : null
  const stats = [
    { label: '个选题', value: report.stories.length },
    { label: '个项目', value: report.items.length },
  ]
  if (report.coverageDays !== undefined) stats.push({ label: '个观测日', value: report.coverageDays })
  return <header className="daily-masthead">
    <div className="daily-dateline"><time dateTime={report.date}>{label}</time><span>{preview ? '采集预览' : '北京时间'}</span></div>
    <div className="daily-nameplate">
      <h1 id="daily-title" aria-label={'GitHub 雷达' + name}><span>GitHub 雷达</span><strong>{name}</strong></h1>
      <div className="report-masthead-panel">
        <div className="daily-issue-box">
          <span>{preview ? '未出刊' : '第 ' + report.number + ' 期'}</span>
          <strong>{report.date.slice(-2)}</strong>
          <span>{kind === 'daily' ? groupLabel(report.date.slice(0, 7)) : label}</span>
          <span>{kind === 'daily' ? weekday.format(new Date(report.date)) : preview ? null : period}</span>
        </div>
        <IssueCalendar kind={kind} date={report.date} current={preview ? '' : report.date} dates={calendar} />
      </div>
    </div>
    <div className="daily-edition-meta">
      <dl>{stats.map(({ label, value }) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      <p>实际采集 {coverage}（北京时间）{preview && report.requiredDays !== undefined && ('，出刊至少需 ' + report.requiredDays + ' 个观测日')}</p>
    </div>
  </header>
}

function IssueCalendar({ kind, date, current, dates }: { kind: ReportKind; date: string; current: string; dates: string[] }) {
  const { title, columns, heads, keys } = issueGrid(kind, date)
  const published = new Set(dates)
  return <nav className="report-calendar" aria-label={reportNames[kind] + '出刊日历'}>
    <div className="report-calendar-label">
      <strong>{title}</strong><span>{kind === 'daily' ? '本月' : '全年'} {published.size} 期</span>
    </div>
    <div className="report-calendar-grid" style={{ gridTemplateColumns: 'repeat(' + columns + ', minmax(0, 1fr))' }}>
      {heads.map((head) => <span key={head} className="report-calendar-weekday" aria-hidden>{head}</span>)}
      {keys.map((key, index) => published.has(key)
        ? <Link key={key} to={reportPath(kind, key)} title={issueLabel(kind, key)} aria-label={issueLabel(kind, key)}
          aria-current={key === current ? 'page' : undefined} />
        : <span key={key || 'empty-' + index} className={key ? 'report-calendar-empty' : undefined} aria-hidden />)}
    </div>
  </nav>
}

function groupKey(kind: ReportKind, issue: ArchiveItem) {
  return (issue.periodFrom ?? issue.date).slice(0, kind === 'monthly' ? 4 : 7)
}

function groupLabel(key: string) {
  const year = key.slice(0, 4) + ' 年'
  return key.length === 4 ? year : year + ' ' + Number(key.slice(5, 7)) + ' 月'
}

function groupArchive(kind: ReportKind, archive: ArchiveItem[]) {
  const groups = new Map<string, ArchiveItem[]>()
  for (const item of archive) {
    const key = groupKey(kind, item)
    const items = groups.get(key)
    if (items) items.push(item)
    else groups.set(key, [item])
  }
  return Array.from(groups)
}

function IssueMark({ kind, issue }: { kind: ReportKind; issue: ArchiveItem }) {
  const mark = issueMark(kind, issue)
  return <time dateTime={issue.date}>{issue.date.slice(-2)}{mark && <small>{mark}</small>}</time>
}

function Archive({ kind, archive, current, before, nextBefore, path }: {
  kind: ReportKind; archive: ArchiveItem[]; current: string; before: string; nextBefore: string | null; path: string
}) {
  const groups = groupArchive(kind, archive)
  const openGroup = groups.find(([, items]) => items.some((item) => item.date === current))?.[0] ?? groups[0]?.[0]
  return <nav aria-label={'往期' + reportNames[kind]} className="daily-archive-list">
    {groups.map(([key, items]) => <details key={key} open={key === openGroup}>
      <summary><ChevronRight size={14} aria-hidden /><span>{groupLabel(key)}</span><small>{items.length}</small></summary>
      <ul>{items.map((issue) => <li key={issue.date}>
        <Link to={reportPath(kind, issue.date) + (before ? '?' + new URLSearchParams({ before }) : '')}
          aria-label={issueLabel(kind, issue.date) + ' ' + issue.headline}
          aria-current={issue.date === current ? 'page' : undefined}>
          <IssueMark kind={kind} issue={issue} />
          <span>{issue.headline || issueLabel(kind, issue.date)}</span>
        </Link>
      </li>)}</ul>
    </details>)}
    {nextBefore && <Link className="daily-older" to={path + '?' + new URLSearchParams({ before: nextBefore })}>更早{reportNames[kind]}<ChevronRight size={14} aria-hidden /></Link>}
    {before && <Link className="daily-older" to={path}>回到最新</Link>}
  </nav>
}

function ArchiveBook({ archive, before, nextBefore, total }: {
  archive: ArchiveItem[]; before: string; nextBefore: string | null; total: number
}) {
  return <article className="daily-archive-book" aria-labelledby="daily-book-title">
    <header className="daily-book-head">
      <div className="daily-dateline"><span>GitHub 雷达</span><span>共 {total} 期</span></div>
      <h1 id="daily-book-title">日报<strong>合订本</strong></h1>
    </header>
    {groupArchive('daily', archive).map(([month, items]) => <section key={month} aria-labelledby={'archive-' + month} className="daily-book-month">
      <h2 id={'archive-' + month}>{groupLabel(month)}</h2>
      <ul>{items.map((issue) => <li key={issue.date}>
        <Link to={reportPath('daily', issue.date) + (before ? '?' + new URLSearchParams({ before }) : '')}
          aria-label={issueLabel('daily', issue.date) + ' ' + issue.headline + ' ' + issue.count + ' 个选题'}>
          <IssueMark kind="daily" issue={issue} />
          <span><strong>{issue.headline}</strong><small>{issue.count} 个选题</small></span>
        </Link>
      </li>)}</ul>
    </section>)}
    {archive.length === 0 && <Empty title="暂无往期日报" />}
    {(before || nextBefore) && <nav className="daily-neighbours" aria-label="合订本分页">
      {before && <Link to="/daily/archive">回到最新</Link>}
      {nextBefore && <Link to={'/daily/archive?' + new URLSearchParams({ before: nextBefore })}>更早日报<ChevronRight size={14} aria-hidden /></Link>}
    </nav>}
  </article>
}
