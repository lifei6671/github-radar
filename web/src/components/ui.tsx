import { AlertCircle, ArrowUp, CircleHelp, LoaderCircle, X } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { errorText } from '../api/client'

export function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'ok' | 'hot' | 'warning' | 'accent' }) {
  return <span className={'badge badge-' + tone}>{children}</span>
}

export function Notice({ children, error = false }: { children: ReactNode; error?: boolean }) {
  return <div className={'notice ' + (error ? 'notice-error' : '')} role={error ? 'alert' : 'status'}>
    {error ? <AlertCircle size={16} /> : <CircleHelp size={16} />}
    <span>{children}</span>
  </div>
}

export function PageError({ error, retry }: { error: unknown; retry?: () => void }) {
  return <div className="empty-state">
    <AlertCircle size={24} className="tone-hot" aria-hidden />
    <p role="alert">{errorText(error)}</p>
    {retry && <button className="button" onClick={retry}>重试</button>}
  </div>
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return <div className="empty-state"><span className="empty-mark" aria-hidden /><strong>{title}</strong>{children}</div>
}

export function Skeleton({ rows = 3 }: { rows?: number }) {
  return <div className="skeleton-list" aria-label="正在加载" role="status">
    {Array.from({ length: rows }, (_, i) => <div className="skeleton" key={i}><span /><span /><span /></div>)}
  </div>
}

export function Spinner() { return <LoaderCircle size={15} className="spinner" aria-hidden /> }

export function More({ hasMore, pending, onClick }: { hasMore: boolean; pending: boolean; onClick: () => void }) {
  if (!hasMore) return null
  return <div className="load-more"><button className="button" disabled={pending} onClick={onClick}>
    {pending ? <><Spinner />加载中</> : '加载更多'}
  </button></div>
}

// 原生模态框提供焦点圈定和 Esc 关闭，关闭后恢复触发按钮焦点。
export function Dialog({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const titleId = useId()
  useEffect(() => {
    const dialog = ref.current
    dialog?.showModal()
    dialog?.querySelector<HTMLElement>('[data-autofocus]')?.focus()
    return () => { dialog?.close() }
  }, [])
  return <dialog ref={ref} className="dialog" aria-labelledby={titleId}
    onCancel={(event) => { event.preventDefault(); onClose() }}
    onClick={(event) => { if (event.target === event.currentTarget) onClose() }}>
    <div className="dialog-body">
      <header className="section-head">
        <h2 id={titleId}>{title}</h2>
        <button type="button" className="icon-button" aria-label="关闭对话框" onClick={onClose}><X size={19} /></button>
      </header>
      {children}
    </div>
  </dialog>
}

export function BackToTop() {
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    const update = () => setVisible(window.scrollY > 650)
    update()
    window.addEventListener('scroll', update, { passive: true })
    return () => window.removeEventListener('scroll', update)
  }, [])
  return visible && <button className="icon-button back-top" aria-label="返回顶部" onClick={() => window.scrollTo({ top: 0 })}><ArrowUp size={17} /></button>
}
