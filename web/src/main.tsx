import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router'
import { PageNotFound, Root, Setup } from './app/shell'
import { Detail } from './pages/detail'
import { Categories, Discover, Ranking, Repos } from './pages/repos'
import { System } from './pages/system'
import { Topics } from './pages/topics'
import './styles/index.css'

const router = createBrowserRouter([{
  element: <Root />,
  children: [
    { index: true, element: <Discover /> },
    { path: 'trending', element: <Ranking /> },
    { path: 'repos', element: <Repos /> },
    { path: 'repos/:id', element: <Detail /> },
    { path: 'categories', element: <Categories /> },
    { path: 'categories/:slug', element: <Repos /> },
    { path: 'watchlist', element: <Repos watching /> },
    { path: 'topics', element: <Topics /> },
    { path: 'system', element: <System /> },
    { path: 'setup', element: <Setup /> },
    { path: '*', element: <PageNotFound /> },
  ],
}])

createRoot(document.getElementById('root')!).render(
  <StrictMode><RouterProvider router={router} /></StrictMode>,
)
