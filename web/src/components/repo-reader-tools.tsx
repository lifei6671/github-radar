import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Download, ExternalLink, Image, Menu, Share2 } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import type { RepoDetail } from '../api/types'
import { ApiError } from '../api/client'
import { repoReadmeOptions } from '../api/queries'
import { githubLink } from '../lib/format'
import { downloadFile, repoExportName, repoMarkdown, repoShareUrl } from '../lib/repo-share'
import { FavoriteButton } from './favorite'
import { Dialog, Notice, Spinner } from './ui'

export function RepoReaderTools({ repo }: { repo: RepoDetail }) {
  const menuId = useId()
  const menu = useRef<HTMLDivElement>(null)
  const [posterOpen, setPosterOpen] = useState(false)
  const [message, setMessage] = useState('')
  const [toast, setToast] = useState<{ message: string } | null>(null)
  const client = useQueryClient()
  const source = githubLink(repo.htmlUrl)
  const url = repoShareUrl(repo)
  const markdown = useMutation({
    mutationFn: async () => {
      const readme = await client.fetchQuery(repoReadmeOptions(client, repo.id, false)).catch((error) => {
        if (error instanceof ApiError && error.status === 404) throw error
        return undefined
      })
      const content = await repoMarkdown(repo, url, readme)
      return new Blob([content], { type: 'text/markdown;charset=utf-8' })
    },
  })

  useEffect(() => {
    if (!toast) return
    const timer = window.setTimeout(() => setToast(null), 2500)
    return () => window.clearTimeout(timer)
  }, [toast])

  async function copyShareLink() {
    menu.current?.hidePopover()
    try {
      await navigator.clipboard.writeText(window.location.href)
      setToast({ message: '已复制链接' })
    } catch {
      setToast({ message: '复制失败，请从地址栏复制' })
    }
  }

  return <div className="repo-reader-tools" role="group" aria-label="项目操作">
    <div className="repo-reader-controls">
      {source && <a className="button repo-original" href={source} target="_blank" rel="noopener noreferrer">
        访问仓库<ExternalLink size={13} aria-hidden />
      </a>}
      <FavoriteButton item={{ id: repo.id, fullName: repo.fullName, excerpt: repo.summaryPreview || repo.description, language: repo.language }} />
      <button type="button" className="icon-button" aria-label="更多操作" title="更多操作" popoverTarget={menuId}>
        <Menu size={17} aria-hidden />
      </button>
    </div>
    <div ref={menu} id={menuId} popover="auto" className="panel repo-reader-menu" role="group" aria-label="更多项目操作"
      onToggle={(event) => {
        if (event.newState === 'open') {
          setMessage('')
          if (markdown.isError) markdown.reset()
          event.currentTarget.querySelector('button')?.focus({ preventScroll: true })
        }
      }}>
      <button type="button" onClick={() => { void copyShareLink() }}><Share2 size={16} aria-hidden />分享链接</button>
      <button type="button" onClick={() => { menu.current?.hidePopover(); setPosterOpen(true) }}><Image size={16} aria-hidden />生成分享海报</button>
      <button type="button" disabled={markdown.isPending} onClick={() => {
        menu.current?.hidePopover()
        markdown.mutate(undefined, { onSuccess: (blob) => {
          downloadFile(blob, repoExportName(repo, '.md'))
          setMessage('已生成 Markdown')
        } })
      }}><Download size={16} aria-hidden />导出 Markdown</button>
    </div>
    <div className="repo-reader-status minor" role="status">{markdown.isPending ? '正在导出 Markdown' : message}</div>
    {markdown.error && <Notice error>导出 Markdown 失败，请重试。</Notice>}
    {createPortal(<div className="repo-reader-toast" role="status">{toast && <span>{toast.message}</span>}</div>, document.body)}
    {posterOpen && <PosterDialog repo={repo} url={url} onClose={() => setPosterOpen(false)} />}
  </div>
}

function PosterDialog({ repo, url, onClose }: { repo: RepoDetail; url: string; onClose: () => void }) {
  const [source] = useState({ repo, url })
  const [preview, setPreview] = useState('')
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let canceled = false
    let objectUrl: string | undefined
    void import('../lib/repo-poster').then(({ createRepoPoster }) => createRepoPoster(source.repo, source.url)).then((blob) => {
      if (canceled) return
      objectUrl = URL.createObjectURL(blob)
      setPreview(objectUrl)
    }).catch(() => {
      if (!canceled) setFailed(true)
    })
    return () => {
      canceled = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [source])

  return <Dialog title="分享海报" onClose={onClose}>
    {preview ? <>
      <img className="repo-poster-preview" src={preview} alt={source.repo.fullName + ' 分享海报'} width={900} height={1120} />
      <div className="action-row">
        <a className="button primary" href={preview} download={repoExportName(source.repo, '-poster.png')}>
          <Download size={16} aria-hidden />下载 PNG
        </a>
      </div>
    </> : failed ? <Notice error>海报生成失败，请重新打开后重试。</Notice> :
      <span className="repo-poster-pending muted" role="status"><Spinner />正在生成海报</span>}
  </Dialog>
}
