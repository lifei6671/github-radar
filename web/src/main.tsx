import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, Navigate } from 'react-router'
import { RouterProvider } from 'react-router/dom'
import { AdminLayout, AdminLogin, LegacyAdminPage, PageNotFound, PublicLayout, Root, Setup } from './app/shell'
import { Detail } from './pages/detail'
import { Categories, Discover, Ranking, Repos } from './pages/repos'
import { Starred } from './pages/starred'
import { Watchlist } from './pages/watchlist'
import { System } from './pages/system'
import { Topics } from './pages/topics'
import './styles/index.css'

const router = createBrowserRouter([{
  element: <Root />,
  children: [
    { element: <PublicLayout />, children: [
      { index: true, element: <Discover /> },
      { path: 'trending', element: <Ranking /> },
      { path: 'repos', element: <Repos /> },
      { path: 'repos/:id', element: <Detail /> },
      { path: 'starred', element: <Starred /> },
      { path: 'categories', element: <Categories /> },
      { path: 'categories/:slug', element: <Repos /> },
      { path: '*', element: <PageNotFound /> },
    ] },
    { path: 'admin/login', element: <AdminLogin /> },
    { path: 'admin', element: <AdminLayout />, children: [
      { index: true, element: <Navigate to="/admin/runs" replace /> },
      { path: 'runs', element: <System /> },
      { path: 'repos', element: <Repos /> },
      { path: 'repos/:id', element: <Detail /> },
      { path: 'watchlist', element: <Watchlist /> },
      { path: 'topics', element: <Topics /> },
      { path: 'setup', element: <Setup /> },
      { path: '*', element: <PageNotFound /> },
    ] },
    { path: 'watchlist', element: <LegacyAdminPage to="/admin/watchlist" /> },
    { path: 'topics', element: <LegacyAdminPage to="/admin/topics" /> },
    { path: 'system', element: <LegacyAdminPage to="/admin/runs" /> },
    { path: 'setup', element: <LegacyAdminPage to="/admin/setup" /> },
  ],
}])

createRoot(document.getElementById('root')!).render(
  <StrictMode><RouterProvider router={router} /></StrictMode>,
)
