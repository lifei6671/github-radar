import { useSyncExternalStore } from 'react'

export interface FavoriteItem {
  id: number
  fullName: string
  excerpt: string | null
  language: string | null
}

type Favorite = FavoriteItem & { savedAt: string }
const key = 'radar.favorites'
const listeners = new Set<() => void>()

function readFavorites(): Favorite[] {
  try {
    const saved: unknown = JSON.parse(localStorage.getItem(key) ?? '[]')
    return Array.isArray(saved) ? saved.filter((item) => item && Number.isSafeInteger(item.id) && item.id > 0 &&
      typeof item.fullName === 'string' && typeof item.savedAt === 'string' &&
      (item.excerpt === null || typeof item.excerpt === 'string') && (item.language === null || typeof item.language === 'string')) : []
  } catch {
    return []
  }
}

let favorites = readFavorites()

function publish(items: Favorite[]) {
  favorites = items
  for (const listener of listeners) listener()
}

// 浏览器同源标签页共享收藏，后台登录和退出不影响此处的数据。
window.addEventListener('storage', (event) => {
  if (event.key === key || event.key === null) publish(readFavorites())
})

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function getFavorites() { return favorites }

export function useFavorites() { return useSyncExternalStore(subscribe, getFavorites) }

export function toggleFavorite(item: FavoriteItem) {
  const current = readFavorites()
  const next = current.some(({ id }) => id === item.id)
    ? current.filter(({ id }) => id !== item.id)
    : [{ ...item, savedAt: new Date().toISOString() }, ...current]
  try {
    localStorage.setItem(key, JSON.stringify(next))
  } catch {
    return false
  }
  publish(next)
  return true
}
