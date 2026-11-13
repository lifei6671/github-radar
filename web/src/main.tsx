import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, Navigate } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { AdminLayout, AdminLogin, LegacyAdminPage, PageNotFound, PublicLayout, Root, Setup } from './app/shell'
import { Skeleton } from './components/ui'
import { discoveryCategoryRoute, repoCategoryRoute, topicsPath } from './lib/format'
import { reportKinds } from './lib/report-period'
import { Categories, Discover, loadPublicRepos, ProjectThemeDetail, ProjectThemes, Ranking, Repos } from './pages/repos'
import { loadStarred, Starred } from './pages/starred'
import { Watchlist } from './pages/watchlist'
import { Topics } from './pages/topics'
import { loadPublicRepo, revalidatePublicRepo } from './api/queries'
import './styles/index.css'

const detailRoute = {
  path: 'repos/:owner/:name',
  HydrateFallback: Skeleton,
  lazy: async () => ({ Component: (await import('./pages/detail')).Detail }),
}

const publicReposRoute = { element: <Repos />, loader: loadPublicRepos, HydrateFallback: Skeleton }

const router = createBrowserRouter([{
  element: <Root />,
  children: [
    { element: <PublicLayout />, children: [
      { index: true, element: <Discover /> },
      { path: discoveryCategoryRoute, element: <Discover /> },
      { path: 'trending', element: <Ranking /> },
      ...reportKinds.map((kind) => ({ path: kind + '/:date?', HydrateFallback: Skeleton, lazy: async () => {
        const { ReportPage, loadReportPage } = await import('./pages/daily')
        return {
          Component: () => <ReportPage kind={kind} />,
          loader: (args: Parameters<typeof loadReportPage>[1]) => loadReportPage(kind, args),
        }
      } })),
      { path: 'repos', ...publicReposRoute },
      { ...detailRoute, loader: loadPublicRepo, shouldRevalidate: revalidatePublicRepo },
      { path: 'starred', element: <Starred />, loader: loadStarred, HydrateFallback: Skeleton },
      { path: 'categories', element: <Categories /> },
      { path: repoCategoryRoute, ...publicReposRoute },
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
      detailRoute,
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
