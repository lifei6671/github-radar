import { Bookmark, X } from 'lucide-react'
import { useState } from 'react'
import { toggleFavorite, useFavorites } from '../lib/favorites'
import type { FavoriteItem } from '../lib/favorites'

export function FavoriteButton({ item, removeIcon = false }: { item: FavoriteItem; removeIcon?: boolean }) {
  const saved = useFavorites().some(({ id }) => id === item.id)
  const [failed, setFailed] = useState(false)
  const label = saved ? '取消收藏' : '收藏'
  const Icon = removeIcon ? X : Bookmark
  return <span className="favorite-action">
    <button type="button" className={'icon-button favorite-button' + (saved && !removeIcon ? ' is-favorite' : '')}
      aria-label={label + ' ' + item.fullName} aria-pressed={saved} title={label}
      onClick={() => setFailed(!toggleFavorite(item))}>
      <Icon size={removeIcon ? 14 : 16} fill={saved && !removeIcon ? 'currentColor' : 'none'} aria-hidden />
    </button>
    {failed && <span role="alert" className="repo-action-error">无法写入浏览器收藏</span>}
  </span>
}
