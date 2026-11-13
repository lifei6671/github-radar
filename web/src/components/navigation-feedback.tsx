import { useIsFetching } from '@tanstack/react-query'
import { useLayoutEffect, useRef } from 'react'
import type { ComponentPropsWithoutRef } from 'react'
import { useNavigation } from 'react-router'

export function NavigationProgress() {
  const navigation = useNavigation()
  const fetching = useIsFetching({ predicate: (query) =>
    query.isActive() && (query.meta?.pageLoad === 'revalidate' || (query.meta?.pageLoad === true && query.state.data === undefined)),
  })
  if (navigation.state !== 'loading' && fetching === 0) return null
  return <div className="navigation-progress" role="progressbar" aria-label="正在加载页面"><span /></div>
}

export function AnimatedPills({ className, children, ...props }: ComponentPropsWithoutRef<'div'>) {
  const group = useRef<HTMLDivElement>(null)
  const thumb = useRef<HTMLSpanElement>(null)
  useLayoutEffect(() => {
    const container = group.current
    const indicator = thumb.current
    if (!container || !indicator) return
    const tabs = [...container.querySelectorAll<HTMLElement>(':scope > button, :scope > a')]
    const active = tabs.filter((tab) => tab.matches('[aria-pressed="true"], [aria-current="page"], [aria-current="location"]'))
    const selected = tabs.find((tab) => tab.hasAttribute('data-pending')) ?? (active.length === 1 ? active[0] : undefined)
    function measure() {
      if (!container || !indicator) return
      const visible = Boolean(selected?.offsetWidth)
      container.toggleAttribute('data-sliding', visible)
      if (!visible || !selected) return
      indicator.style.width = selected.offsetWidth + 'px'
      indicator.style.height = selected.offsetHeight + 'px'
      indicator.style.transform = `translate(${selected.offsetLeft}px, ${selected.offsetTop}px)`
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(container)
    for (const tab of tabs) observer.observe(tab)
    return () => observer.disconnect()
  })
  return <div {...props} ref={group} className={['pills', 'animated-pills', className].filter(Boolean).join(' ')}>
    <span ref={thumb} className="pill-thumb" aria-hidden="true" />
    {children}
  </div>
}
