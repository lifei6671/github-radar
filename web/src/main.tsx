import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, Navigate } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { AdminLayout, AdminLogin, LegacyAdminPage, PageNotFound, PublicLayout, Root, Setup } from './app/shell'
import { Skeleton } from './components/ui'
import { topicsPath } from './lib/format'
import { Detail } from './pages/detail'
import { Categories, Discover, ProjectThemeDetail, ProjectThemes, Ranking, Repos } from './pages/repos'
import { Starred } from './pages/starred'
import { Watchlist } from './pages/watchlist'
import { Topics } from './pages/topics'
import './styles/index.css'

const router = createBrowserRouter([{
  element: <Root />,
  children: [
    { element: <PublicLayout />, children: [
      { index: true, element: <Discover /> },
      { path: 'trending', element: <Ranking /> },
      { path: 'repos', element: <Repos /> },
      { path: 'repos/:owner/:name', element: <Detail /> },
      { path: 'starred', element: <Starred /> },
      { path: 'categories', element: <Categories /> },
      { path: 'categories/:slug', element: <Repos /> },
      { path: topicsPath, element: <ProjectThemes /> },
      { path: topicsPath + '/:slug', element: <ProjectThemeDetail /> },
      { path: '*', element: <PageNotFound /> },
    ] },
    { path: 'admin/login', element: <AdminLogin /> },
    { path: 'admin', element: <AdminLayout />, children: [
      { index: true, element: <Navigate to="/admin/runs" replace /> },
      // 运行配置与拖动组件只在进入后台运行状态时加载。
      { path: 'runs', HydrateFallback: Skeleton, lazy: async () => ({ Component: (await import('./pages/system')).System }) },
      { path: 'repos', element: <Repos /> },
      { path: 'repos/:owner/:name', element: <Detail /> },
      { path: 'watchlist', element: <Watchlist /> },
      { path: 'topics', element: <Topics /> },
      { path: 'setup', element: <Setup /> },
      { path: '*', element: <PageNotFound /> },
    ] },
    { path: 'watchlist', element: <LegacyAdminPage to="/admin/watchlist" /> },
    { path: 'system', element: <LegacyAdminPage to="/admin/runs" /> },
    { path: 'setup', element: <LegacyAdminPage to="/admin/setup" /> },
  ],
}])

createRoot(document.getElementById('root')!).render(
  <StrictMode><RouterProvider router={router} /></StrictMode>,
)
