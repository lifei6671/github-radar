import { QueryClientProvider, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Activity, ArrowDownRight, ArrowRight, ChartNoAxesCombined, CircleUserRound, Command, FolderOpen,
  Layers3, ListFilter, LogOut, Moon, Radar, Search, Star, Sun, Workflow,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, Navigate, NavLink, Outlet, ScrollRestoration, useLocation, useNavigate } from 'react-router'
import { api, ApiError, errorText, setToken, useSession } from '../api/client'
import { BackToTop, Dialog, Notice, PageError, Skeleton, Spinner } from '../components/ui'
import { SetupBackup, useBackupStatus, useRestoredData } from '../pages/backup'

const navigation = [
  { path: '/', label: '发现', icon: Radar },
  { path: '/trending', label: '热点榜', icon: ChartNoAxesCombined },
  { path: '/repos', label: '全部项目', icon: Layers3 },
  { path: '/categories', label: '技术主题', icon: FolderOpen },
  { path: '/topics', label: '选题池', icon: Workflow },
  { path: '/watchlist', label: '我的关注', icon: Star },
  { path: '/system', label: '运行状态', icon: Activity },
]

function Brand() {
  return <div className="brand">
    <img src="/radar.svg" alt="" width="34" height="34" />
    <span>GitHub 雷达</span>
  </div>
}

function ThemeButton() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const label = dark ? '切换浅色主题' : '切换深色主题'
  useEffect(() => {
    const media = matchMedia('(prefers-color-scheme: dark)')
    const sync = () => {
      try { if (localStorage.getItem('radar.theme')) return } catch { /* 按系统偏好显示。 */ }
      setDark(media.matches)
      document.documentElement.className = media.matches ? 'dark' : 'light'
    }
    media.addEventListener('change', sync)
    return () => media.removeEventListener('change', sync)
  }, [])
  function toggle() {
    const theme = dark ? 'light' : 'dark'
    document.documentElement.className = theme
    try { localStorage.setItem('radar.theme', theme) } catch { /* 当前页面仍切换主题。 */ }
    setDark(!dark)
  }
  return <button className="icon-button" onClick={toggle} aria-label={label} title={label}>
    {dark ? <Sun size={18} /> : <Moon size={18} />}
  </button>
}

function Login() {
  const [input, setInput] = useState('')
  const auth = useMutation({
    mutationFn: (credential: string) => api<{ initialized: boolean }>('/setup/status', { token: credential }),
    onSuccess: (_status, credential) => setToken(credential),
  })
  function submit(event: FormEvent) {
    event.preventDefault()
    if (input.trim() && !auth.isPending) auth.mutate(input.trim())
  }
  return <main className="login-page">
    <div className="login-top"><Brand /><ThemeButton /></div>
    <div className="login-layout">
      <div className="login-visual" aria-hidden="true">
        <div className="radar-grid"><div className="radar-ring" /><div className="radar-ring" /><div className="radar-ring" /><div className="radar-ring" />
          <i className="radar-point radar-point-a" /><i className="radar-point radar-point-b" /><i className="radar-point radar-point-c" />
          <div className="radar-axis" /><div className="radar-ray" />
        </div>
      </div>
      <form className="login-form" onSubmit={submit}>
        <h1>连接 GitHub 雷达</h1>
        <label className="field">
          管理员令牌
          <input type="password" name="adminToken" autoComplete="off" spellCheck={false}
            value={input} onChange={(event) => setInput(event.target.value)} required autoFocus />
        </label>
        {auth.error && <Notice error>{errorText(auth.error)}</Notice>}
        <button className="button primary" disabled={auth.isPending || !input.trim()}>
          {auth.isPending ? <Spinner /> : <ArrowRight size={17} />}连接
        </button>
      </form>
    </div>
  </main>
}

function SearchBox({ close }: { close: () => void }) {
  const navigate = useNavigate()
  const [q, setQ] = useState('')
  function submit(event: FormEvent) {
    event.preventDefault()
    if (!q.trim()) return
    close()
    navigate('/repos?' + new URLSearchParams({ q: q.trim() }))
  }
  return <Dialog title="搜索仓库" onClose={close}>
    <form className="dialog-form" onSubmit={submit}>
      <label className="field">仓库名或简介<input data-autofocus value={q} onChange={(e) => setQ(e.target.value)} /></label>
      <button className="button primary" disabled={!q.trim()}><Search size={16} />搜索</button>
    </form>
  </Dialog>
}

function Shell({ maintenance }: { maintenance: boolean }) {
  const [search, setSearch] = useState(false)
  const location = useLocation()
  const mobile = [navigation[0], navigation[1], navigation[4], navigation[5], { ...navigation[6], label: '更多', icon: CircleUserRound }]
  const page = navigation.find(({ path }) => path !== '/' && location.pathname.startsWith(path))?.label
  const showMaintenance = maintenance && (location.pathname !== '/system' || new URLSearchParams(location.search).get('tab') !== 'backup')
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
    <aside className="sidebar">
      <Link to="/" className="brand-link" aria-label="GitHub 雷达首页"><Brand /></Link>
      <button className="side-search" onClick={() => setSearch(true)} aria-label="搜索仓库" title="搜索仓库">
        <Search size={17} /><span>搜索仓库</span><kbd><Command size={12} />K</kbd>
      </button>
      <nav aria-label="主导航" className="side-nav">
        {navigation.map(({ path, label, icon: Icon }) => <NavLink key={path} to={path} end={path === '/'} title={label}>
          <Icon size={19} strokeWidth={1.7} /><span>{label}</span>
        </NavLink>)}
      </nav>
      <div className="side-bottom"><ThemeButton /><button className="icon-button" aria-label="退出会话" title="退出会话" onClick={() => setToken(null)}><LogOut size={18} /></button></div>
    </aside>
    <header className="mobile-bar">
      <Link to="/" aria-label="GitHub 雷达首页"><Brand /></Link>
      <div className="mobile-actions">
        <button className="icon-button" aria-label="搜索仓库" onClick={() => setSearch(true)}><Search size={19} /></button>
        <ThemeButton />
        {location.pathname === '/system' && <button className="icon-button" aria-label="退出会话" onClick={() => setToken(null)}><LogOut size={18} /></button>}
      </div>
    </header>
    <main id="main" className="main" aria-label={page ?? '发现'} tabIndex={-1}><div className="content">
      {showMaintenance ? <section className="panel system-panel">
        <Notice>恢复期间暂不可访问项目数据。</Notice>
        <Link className="button" to="/system?tab=backup">查看恢复进度</Link>
      </section> : <Outlet />}
    </div></main>
    <nav aria-label="手机导航" className="mobile-nav">
      {mobile.map(({ path, label, icon: Icon }) => <NavLink key={path} to={path} end={path === '/'}>
        <Icon size={20} strokeWidth={1.7} /><span>{label}</span>
      </NavLink>)}
    </nav>
    {search && <SearchBox close={() => setSearch(false)} />}
    <BackToTop />
    <ScrollRestoration getKey={({ pathname, search }) => pathname + search} />
  </>
}

export function Root() {
  const { token, client, generation } = useSession()
  return <QueryClientProvider key={generation} client={client}>{token ? <Authenticated /> : <Login />}</QueryClientProvider>
}

function Authenticated() {
  const location = useLocation()
  const backup = useBackupStatus()
  useRestoredData(backup.data)
  const status = useQuery({
    queryKey: ['setup'],
    queryFn: ({ signal }) => api<{ initialized: boolean }>('/setup/status', { signal }),
    staleTime: 60_000,
  })
  if (status.isPending) return <main className="standalone"><Brand /><Skeleton rows={1} /></main>
  if (status.error) return <main className="standalone"><Brand /><PageError error={status.error} retry={() => { void status.refetch() }} /></main>
  if (!status.data.initialized && location.pathname !== '/setup') {
    return <Navigate to="/setup" replace state={{ from: location.pathname + location.search }} />
  }
  if (location.pathname === '/setup') return <Outlet />
  return <Shell maintenance={Boolean(backup.data?.maintenance)} />
}

export function Setup() {
  const location = useLocation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const state = queryClient.getQueryData<{ initialized: boolean }>(['setup'])
  const [restoring, setRestoring] = useState(false)
  const from: unknown = location.state?.from
  const destination = typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') && from !== '/setup' ? from : '/'
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
    <div className="setup-top"><Brand /><ThemeButton /><button className="icon-button" aria-label="退出会话" onClick={() => setToken(null)}><LogOut size={18} /></button></div>
    <section className="panel setup-card">
      <Radar size={30} className="tone-accent" />
      <h1>{state?.initialized ? '系统已初始化' : '初始化雷达'}</h1>
      {state?.initialized ? <Link to="/" className="button primary">进入发现<ArrowRight size={16} /></Link> : <>
        {setup.error && <Notice error>{errorText(setup.error)}</Notice>}
        <button className="button primary" disabled={setup.isPending || restoring} onClick={() => setup.mutate()}>
          {setup.isPending ? <Spinner /> : <ArrowDownRight size={17} />}初始化空库
        </button>
      </>}
    </section>
    <SetupBackup initialized={Boolean(state?.initialized)} onRestoring={setRestoring}
      onDone={() => { void navigate(destination, { replace: true }) }} />
  </main>
}

export function PageNotFound() {
  return <div className="empty-state"><ListFilter size={24} /><h1>页面不存在</h1><Link to="/" className="button">返回发现</Link></div>
}
