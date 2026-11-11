import { ArrowRight, Bookmark } from 'lucide-react'
import { Link } from 'react-router'
import { FavoriteButton } from '../components/favorite'
import { RepoCollection, RepoCollectionEmpty, RepoCollectionRow } from '../components/repo-collection'
import { useFavorites } from '../lib/favorites'
import { repoPath } from '../lib/format'

export function Starred() {
  const favorites = useFavorites()
  return <RepoCollection title="我的收藏" subtitle="收藏保存在此浏览器，清除浏览器数据后会丢失。">
    {favorites.length ? <ul className="repo-collection-rows" aria-label="收藏仓库">
      {favorites.map((item) => <RepoCollectionRow key={item.id} metadata={<span>{item.language ?? 'GitHub'}</span>}
        title={<Link className="repo-name" to={repoPath(item.fullName) + '?repoId=' + item.id} state={{ from: '/starred' }}>{item.fullName}</Link>}
        date={item.savedAt} dateLabel="收藏于" excerpt={item.excerpt} action={<FavoriteButton item={item} removeIcon />} />)}
    </ul> : <RepoCollectionEmpty icon={<Bookmark size={20} aria-hidden />} message="还没有收藏的仓库"
      action={<Link to="/repos">浏览全部项目<ArrowRight size={14} aria-hidden /></Link>} />}
  </RepoCollection>
}
