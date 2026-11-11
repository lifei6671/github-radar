import { lazy, Suspense } from 'react'

// 普通页面无需下载专题图标全集，只有真正展示专题图标时才加载。
const ThemeIconGlyph = lazy(() => import('./theme-icon-options').then(({ ThemeIcon }) => ({ default: ThemeIcon })))

export function ThemeIcon(props: { icon?: string; color?: string; size?: number }) {
  if (!props.icon) return null
  return <Suspense fallback={null}><ThemeIconGlyph {...props} /></Suspense>
}
