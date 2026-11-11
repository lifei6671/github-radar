import { ChevronDown, Search } from 'lucide-react'
import { useId, useRef, useState } from 'react'
import { ThemeIcon, themeColors, themeIcons } from './theme-icon-options'

export function ThemeIconPicker({ icon, color, onChange }: {
  icon: string; color: string; onChange: (icon: string, color: string) => void
}) {
  const id = useId()
  const trigger = useRef<HTMLButtonElement>(null)
  const searchBox = useRef<HTMLInputElement>(null)
  const [search, setSearch] = useState('')
  const [palette, setPalette] = useState(color || 'slate')
  const activeColor = icon ? color || 'slate' : palette
  const term = search.trim().toLowerCase()
  const selected = Object.hasOwn(themeIcons, icon) ? themeIcons[icon as keyof typeof themeIcons].label : '无图标'
  const options = Object.entries(themeIcons).filter(([key, { label }]) => label.includes(term) || key.includes(term))

  // 颜色可先于图标选择，只有选定图标后才把颜色写入表单。
  return <div className="field admin-field-wide">
    <span id={id + '-label'}>专题图标</span>
    <button type="button" className="button theme-picker-trigger" ref={trigger}
      aria-labelledby={id + '-label ' + id + '-selected'} popoverTarget={id}
      onClick={() => setPalette(color || 'slate')}>
      <ThemeIcon icon={icon} color={color} size={18} />
      <span id={id + '-selected'}>{selected}</span><ChevronDown size={14} aria-hidden />
    </button>
    {/* 原生浮层脱离表单布局，并支持点外关闭。 */}
    <div id={id} className="panel theme-picker" role="group" aria-labelledby={id + '-label'} popover="auto"
      onToggle={(event) => {
        if (event.newState === 'open') searchBox.current?.focus({ preventScroll: true })
        else setSearch('')
      }}
      onKeyDown={(event) => {
        if (event.key !== 'Escape') return
        event.preventDefault()
        event.stopPropagation()
        event.currentTarget.hidePopover()
        trigger.current?.focus()
      }}>
      <div className="theme-picker-search">
        <div><Search size={16} aria-hidden /><input ref={searchBox} type="search" aria-label="搜索图标" placeholder="搜索图标" value={search}
          onChange={(event) => setSearch(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') event.preventDefault() }} /></div>
        <button type="button" className="button" onClick={() => {
          setSearch('')
          setPalette('slate')
          onChange('', '')
        }}>清除</button>
      </div>
      <div className="theme-picker-colors" role="group" aria-label="图标颜色">
        {Object.entries(themeColors).map(([value, label]) =>
          <button key={value} type="button" data-color={value} aria-label={label} title={label}
            aria-pressed={activeColor === value} onClick={() => {
              setPalette(value)
              if (icon) onChange(icon, value)
            }} />)}
      </div>
      <div className="theme-picker-grid" role="group" aria-label="可选图标" data-color={activeColor}>
        {options.map(([value, { label, glyph: Icon }]) =>
          <button key={value} type="button" aria-label={label} title={label} aria-pressed={icon === value}
            onClick={() => onChange(value, activeColor)}><Icon size={22} aria-hidden /></button>)}
      </div>
      {options.length === 0 && <p className="minor muted" role="status">没有匹配的图标</p>}
    </div>
  </div>
}
