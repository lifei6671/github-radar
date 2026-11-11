import { Bookmark, X } from 'lucide-react'
import { useState } from 'react'
import { toggleFavorite, useFavorites } from '../lib/favorites'
import type { FavoriteItem } from '../lib/favorites'
import { ActionButton } from './ui'

export function FavoriteButton({ item, removeIcon = false }: { item: FavoriteItem; removeIcon?: boolean }) {
  const saved = useFavorites().some(({ id }) => id === item.id)
  const [failed, setFailed] = useState(false)
  const label = saved ? '取消收藏' : '收藏'
  const Icon = removeIcon ? X : Bookmark
  return <span className="favorite-action">
    <ActionButton className={'icon-button favorite-button' + (saved && !removeIcon ? ' is-favorite' : '')}
      aria-label={label + ' ' + item.fullName} aria-pressed={saved} title={label}
      icon={<Icon size={removeIcon ? 14 : 16} fill={saved && !removeIcon ? 'currentColor' : 'none'} />}
      onClick={() => setFailed(!toggleFavorite(item))} />
    {failed && <span role="alert" className="repo-action-error">无法写入浏览器收藏</span>}
  </span>
}
