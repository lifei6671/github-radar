import { QueryClientProvider, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Activity, ArrowDownRight, ArrowLeft, ArrowRight, Bookmark, ChartNoAxesCombined, Crosshair, Ellipsis, Github, Grid2X2, LayoutDashboard,
  Layers3, ListFilter, LogOut, Monitor, Moon, Radar, Search, Sun, Workflow,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, Navigate, NavLink, Outlet, ScrollRestoration, useLocation, useMatch, useNavigate, useOutletContext, useSearchParams } from 'react-router'
import { api, ApiError, errorText, setAuthenticated, useSession } from '../api/client'
import { ActionButton, BackToTop, Dialog, Notice, PageError, Skeleton } from '../components/ui'
import { topicsPath } from '../lib/format'
import { SetupBackup, useBackupStatus, useRestoredData } from '../pages/backup'

const publicNav = [
  { path: '/', label: '发现', icon: Radar },
  { path: '/repos', label: '全部项目', icon: Layers3 },
  { path: '/trending', label: '热点榜', icon: ChartNoAxesCombined },
  { path: topicsPath, label: '主题', icon: Grid2X2 },
  { path: '/starred', label: '收藏', icon: Bookmark },
]
const adminNav = [
  { path: '/admin/repos', label: '仓库管理', icon: Layers3 },
  { path: '/admin/watchlist', label: '重点监控', icon: Crosshair },
  { path: '/admin/topics', label: '选题池', icon: Workflow },
  { path: '/admin/runs', label: '运行状态', icon: Activity },
]
const adminEntry = { path: '/admin', label: '后台管理', icon: LayoutDashboard }
const publicMobile = [publicNav[0], { ...publicNav[1], label: '项目' }, publicNav[2], publicNav[4]]
const publicMore = [publicNav[3], adminEntry]

function Brand({ admin = false }: { admin?: boolean }) {
  return <div className="brand">
    <img src="/radar.svg" alt="" width="34" height="34" />
    <span>{admin ? 'GitHub 雷达后台' : 'GitHub 雷达'}</span>
  </div>
}

function useTheme() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const [preference, setPreference] = useState(() => {
    try { return localStorage.getItem('radar.theme') } catch { return null }
  })
  useEffect(() => {
    const media = matchMedia('(prefers-color-scheme: dark)')
    const sync = () => {
      if (preference) return
      setDark(media.matches)
      document.documentElement.className = media.matches ? 'dark' : 'light'
    }
    media.addEventListener('change', sync)
    return () => media.removeEventListener('change', sync)
  }, [preference])
  function choose(mode: 'dark' | 'light' | null) {
    const nextDark = mode === null ? matchMedia('(prefers-color-scheme: dark)').matches : mode === 'dark'
    document.documentElement.className = nextDark ? 'dark' : 'light'
    try {
      if (mode === null) localStorage.removeItem('radar.theme')
      else localStorage.setItem('radar.theme', mode)
    } catch { /* 当前页面仍可切换主题。 */ }
    setPreference(mode)
    setDark(nextDark)
  }
  return { dark, preference, choose }
}

function ThemeButton() {
  return <ThemeControl theme={useTheme()} />
}

function ThemeControl({ theme: { dark, preference, choose }, segmented = false }: {
  theme: ReturnType<typeof useTheme>; segmented?: boolean
}) {
  const label = dark ? '切换浅色主题' : '切换深色主题'
  if (segmented) return <div className="side-theme" role="group" aria-label="显示主题">
    <button className="icon-button" aria-label="切换深色主题" title="深色" aria-pressed={preference === 'dark'} onClick={() => choose('dark')}><Moon size={15} /></button>
    <button className="icon-button" aria-label="跟随系统主题" title="跟随系统" aria-pressed={preference === null} onClick={() => choose(null)}><Monitor size={15} /></button>
    <button className="icon-button" aria-label="切换浅色主题" title="浅色" aria-pressed={preference === 'light'} onClick={() => choose('light')}><Sun size={15} /></button>
  </div>
  return <button className="icon-button" onClick={() => choose(dark ? 'light' : 'dark')} aria-label={label} title={label}>
    {dark ? <Sun size={18} /> : <Moon size={18} />}
  </button>
}

function adminReturn(raw: string | null, allowSetup = false) {
  if (!raw?.startsWith('/admin')) return '/admin/runs'
  const url = new URL(raw, window.location.origin)
  if (url.pathname !== '/admin' && !url.pathname.startsWith('/admin/')) return '/admin/runs'
  const path = url.pathname.replace(/\/+$/, '')
  if (path === '/admin/login' || (!allowSetup && path === '/admin/setup')) return '/admin/runs'
  return url.pathname + url.search + url.hash
}

export function AdminLogin() {
  const { authenticated } = useSession()
  const context = useOutletContext<SessionContext>()
  const [params] = useSearchParams()
  const destination = adminReturn(params.get('return'), true)
  const [input, setInput] = useState('')
  const auth = useMutation({
    mutationFn: (password: string) => api('/auth/login', { method: 'POST', body: { password } }),
    onSuccess: () => setAuthenticated(true),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (input && !auth.isPending) auth.mutate(input)
  }
  if (authenticated === null) return <SessionPending {...context} />
  if (authenticated) return <Navigate to={destination} replace />
  return <main className="login-page">
    <div className="login-top"><Link to="/"><Brand /></Link><ThemeButton /></div>
    <div className="login-layout">
      <form className="panel login-form" onSubmit={submit}>
        <h1>登录雷达后台</h1>
        <label className="field">
          管理员密码
          <input type="password" name="password" autoComplete="current-password" spellCheck={false}
            value={input} onChange={(event) => setInput(event.target.value)} required autoFocus />
        </label>
        {auth.error && <Notice error>{errorText(auth.error)}</Notice>}
        <ActionButton type="submit" className="button primary" disabled={!input}
          pending={auth.isPending} icon={<ArrowRight size={17} />}>登录</ActionButton>
        <Link to="/" className="admin-link">返回前台</Link>
      </form>
    </div>
  </main>
}

function LogoutButton() {
  const logout = useMutation({
    mutationFn: () => api('/auth/logout', { method: 'POST' }),
    onSuccess: () => setAuthenticated(false),
  })
  return <>
    <ActionButton className="icon-button" aria-label="退出会话" title="退出会话"
      pending={logout.isPending} icon={<LogOut size={17} />} onClick={() => logout.mutate()} />
    {logout.error && <Notice error>{errorText(logout.error)}</Notice>}
  </>
}

function SearchBox({ close, admin }: { close: () => void; admin: boolean }) {
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  function submit(event: FormEvent) {
    event.preventDefault()
    if (!q.trim()) return
    close()
    navigate((admin ? '/admin/repos?' : '/repos?') + new URLSearchParams({ q: q.trim() }))
  }
  return <Dialog title="搜索仓库" onClose={close}>
    <form className="dialog-form" onSubmit={submit}>
      <label className="field">仓库名或简介<input data-autofocus value={q} onChange={(e) => setQ(e.target.value)} /></label>
      <button className="button primary" disabled={!q.trim()}><Search size={16} />搜索</button>
    </form>
  </Dialog>
}

function Shell({ admin = false, maintenance = false }: { admin?: boolean; maintenance?: boolean }) {
  const [search, setSearch] = useState(false)
  const [more, setMore] = useState(false)
  const theme = useTheme()
  const location = useLocation()
  const pathname = location.pathname.replace(/\/+$/, '')
  const system = pathname === '/admin/runs'
  const repoDetail = pathname.startsWith(admin ? '/admin/repos/' : '/repos/')
  const topicDetail = !admin && pathname.startsWith(topicsPath + '/')
  const navigation = admin ? adminNav : publicNav
  const page = repoDetail ? '仓库详情' : navigation.find(({ path }) => path !== '/' && (location.pathname === path || location.pathname.startsWith(path + '/')))?.label
  const showMaintenance = maintenance && (!system || new URLSearchParams(location.search).get('tab') !== 'backup')
  useEffect(() => {
    function shortcut(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setSearch(true)
      }
    }
    window.addEventListener('keydown', shortcut)
    return () => window.removeEventListener('keydown', shortcut)
  }, [])
  return <>
    <a className="skip-link" href="#main">跳到主要内容</a>
    <aside className={'sidebar' + (admin ? ' admin-sidebar' : '')}>
      <Link to={admin ? '/admin/runs' : '/'} className="brand-link" aria-label={admin ? 'GitHub 雷达后台首页' : 'GitHub 雷达首页'}><Brand admin={admin} /></Link>
      <nav aria-label="主导航" className="side-nav">
        <div className="side-nav-group">
          {navigation.map(({ path, label, icon: Icon }) => <NavLink key={path} to={path} end={path === '/' || path.endsWith('/repos')} title={label}>
            <Icon size={17} strokeWidth={1.7} /><span>{label}</span>
          </NavLink>)}
        </div>
      </nav>
      <div className="side-bottom">
        <Link to={admin ? '/' : adminEntry.path} className="side-action" aria-label={admin ? '返回前台' : adminEntry.label} title={admin ? '返回前台' : adminEntry.label}>
          {admin ? <Radar size={16} /> : <LayoutDashboard size={16} />}<span>{admin ? '返回前台' : adminEntry.label}</span>
        </Link>
        <a href="https://github.com/lifei6671/github-radar" target="_blank" rel="noopener noreferrer" className="side-action"
          aria-label="GitHub 开源" title="GitHub 开源"><Github size={16} aria-hidden /><span>GitHub 开源</span></a>
        <div className="side-utility"><ThemeControl theme={theme} segmented />{admin && <LogoutButton />}</div>
      </div>
    </aside>
    <header className="mobile-bar">
      {topicDetail ? <Link to={topicsPath} className="project-topic-back"><ArrowLeft size={15} aria-hidden />返回全部主题</Link> :
        <Link to="/" aria-label={admin ? '返回前台' : 'GitHub 雷达首页'}><Brand admin={admin} /></Link>}
      <div className="mobile-actions">
        <button className="icon-button" aria-label="搜索仓库" onClick={() => setSearch(true)}><Search size={19} /></button>
        <ThemeControl theme={theme} />
        {admin && <LogoutButton />}
      </div>
    </header>
    <main id="main" className="main" aria-label={page ?? '发现'} tabIndex={-1}><div className="content">
      {showMaintenance ? <section className="panel system-panel">
        <Notice>恢复期间暂不可访问项目数据。</Notice>
        {admin && <Link className="button" to="/admin/runs?tab=backup">查看恢复进度</Link>}
      </section> : <Outlet />}
    </div></main>
    <nav aria-label="手机导航" className="mobile-nav">
      {(admin ? adminNav : publicMobile).map(({ path, label, icon: Icon }) => <NavLink key={path} to={path} end={path === '/' || path.endsWith('/repos')}>
        <Icon size={20} strokeWidth={1.7} /><span>{label}</span>
      </NavLink>)}
      {!admin && <button className={publicMore.some(({ path }) => location.pathname.startsWith(path)) ? 'active' : undefined}
        aria-haspopup="dialog" onClick={() => setMore(true)}><Ellipsis size={20} /><span>更多</span></button>}
    </nav>
    {more && <Dialog title="更多" onClose={() => setMore(false)}>
      <nav className="side-nav" aria-label="更多页面">
        {publicMore.map(({ path, label, icon: Icon }) => <NavLink key={path} to={path} onClick={() => setMore(false)}><Icon size={17} />{label}</NavLink>)}
      </nav>
    </Dialog>}
    {search && <SearchBox admin={admin} close={() => setSearch(false)} />}
    <BackToTop />
    <ScrollRestoration getKey={({ key, pathname, search }) =>
      // 仓库章节可以共享同一路径和查询，历史条目需要各自保存滚动位置。
      pathname.startsWith('/repos/') || pathname.startsWith('/admin/repos/') ? key : pathname + search
    } />
  </>
}

export function Root() {
  const { authenticated, client, generation } = useSession()
  return <QueryClientProvider key={generation} client={client}><SessionContent authenticated={authenticated} /></QueryClientProvider>
}

type SessionContext = { maintenance: boolean; sessionError: Error | null; retrySession: () => void }

function SessionContent({ authenticated }: { authenticated: boolean | null }) {
  const adminArea = useMatch('/admin/*')
  const repoDetail = useMatch('/repos/:owner/:name')
  // 前台仓库详情只查询是否显示管理入口，其余公开页面无需探测会话。
  const check = useQuery({
    queryKey: ['auth-session'],
    queryFn: ({ signal }) => api<{ authenticated: boolean }>('/auth/session', { signal }),
    enabled: authenticated === null && Boolean(adminArea || repoDetail),
  })
  const signedIn = check.data?.authenticated
  useEffect(() => {
    if (authenticated === null && signedIn !== undefined) setAuthenticated(signedIn)
  }, [authenticated, signedIn])
  const backup = useBackupStatus(authenticated === true)
  useRestoredData(backup.data)
  const context: SessionContext = {
    maintenance: Boolean(backup.data?.maintenance), sessionError: check.error, retrySession: () => { void check.refetch() },
  }
  return <Outlet context={context} />
}

function SessionPending({ sessionError, retrySession }: SessionContext) {
  return <main className="standalone"><Brand />
    {sessionError ? <PageError error={sessionError} retry={retrySession} /> : <Skeleton rows={1} />}
  </main>
}

export function PublicLayout() {
  return <Shell maintenance={useOutletContext<SessionContext>().maintenance} />
}

export function LegacyAdminPage({ to }: { to: string }) {
  const { search, hash } = useLocation()
  return <Navigate to={to + search + hash} replace />
}

export function AdminLayout() {
  const { authenticated } = useSession()
  const { pathname, search, hash } = useLocation()
  const context = useOutletContext<SessionContext>()
  const destination = pathname + search + hash
  const setup = pathname.replace(/\/+$/, '') === '/admin/setup'
  const status = useQuery({
    queryKey: ['setup'],
    queryFn: ({ signal }) => api<{ initialized: boolean }>('/setup/status', { signal }),
    enabled: authenticated === true,
    staleTime: 60_000,
  })
  // 管理页面只有通过会话检查后才挂载，并继续保留原来的深链接。
  if (authenticated === null) return <SessionPending {...context} />
  if (!authenticated) return <Navigate to={'/admin/login?' + new URLSearchParams({ return: destination })} replace />
  if (status.isPending) return <main className="standalone"><Brand /><Skeleton rows={1} /></main>
  if (status.error) return <main className="standalone"><Brand /><PageError error={status.error} retry={() => { void status.refetch() }} /></main>
  if (!status.data.initialized && !setup) {
    return <Navigate to={'/admin/setup?' + new URLSearchParams({ return: destination })} replace />
  }
  if (setup) return <Outlet />
  return <Shell admin maintenance={context.maintenance} />
}

export function Setup() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const state = queryClient.getQueryData<{ initialized: boolean }>(['setup'])
  const [restoring, setRestoring] = useState(false)
  const returnTo = params.get('return')
  const destination = returnTo ? adminReturn(returnTo) : '/'
  const setup = useMutation({
    mutationFn: () => api<{ initialized: boolean }>('/setup/initialize', { method: 'POST' }),
    onSuccess: () => {
      queryClient.setQueryData(['setup'], { initialized: true })
      void navigate(destination, { replace: true })
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'already_initialized') {
        void queryClient.invalidateQueries({ queryKey: ['setup'] })
      }
    },
  })
  return <main className="standalone">
    <div className="setup-top"><Brand /><ThemeButton /><LogoutButton /></div>
    <section className="panel setup-card">
      <Radar size={30} className="tone-accent" />
      <h1>{state?.initialized ? '系统已初始化' : '初始化雷达'}</h1>
      {state?.initialized ? <Link to="/" className="button primary">进入发现<ArrowRight size={16} /></Link> : <>
        {setup.error && <Notice error>{errorText(setup.error)}</Notice>}
        <ActionButton className="button primary" disabled={restoring} pending={setup.isPending}
          icon={<ArrowDownRight size={17} />} onClick={() => setup.mutate()}>初始化空库</ActionButton>
      </>}
    </section>
    <SetupBackup initialized={Boolean(state?.initialized)} onRestoring={setRestoring}
      onDone={() => { void navigate(destination, { replace: true }) }} />
  </main>
}

export function PageNotFound() {
  return <div className="empty-state"><ListFilter size={24} /><h1>页面不存在</h1><Link to="/" className="button">返回发现</Link></div>
}
