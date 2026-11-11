import { closestCenter, DndContext, DragOverlay, KeyboardSensor, PointerSensor, useSensor, useSensors } from '@dnd-kit/core'
import type { CollisionDetection } from '@dnd-kit/core'
import { SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { GripVertical } from 'lucide-react'
import { useState } from 'react'
import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'

const withinSettingsList: CollisionDetection = (args) => {
  const point = args.pointerCoordinates
  if (!point) return closestCenter(args)
  const rows = [...args.droppableRects.values()]
  // 行间留白也能吸附，移出整份列表松手则取消排序。
  const inside = point.x >= Math.min(...rows.map((row) => row.left)) && point.x <= Math.max(...rows.map((row) => row.right)) &&
    point.y >= Math.min(...rows.map((row) => row.top)) && point.y <= Math.max(...rows.map((row) => row.bottom))
  return inside ? closestCenter(args) : []
}

// 拖影跟随指针，列表在松手前显示预计插入的位置。
export function SortableSettings({ entries, onMove, children }: {
  entries: { id: string; label: string }[]; onMove: (from: string, to: string) => void; children: ReactNode
}) {
  const [active, setActive] = useState<string | null>(null)
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }))
  const current = entries.find(({ id }) => id === active)
  return <DndContext sensors={sensors} collisionDetection={withinSettingsList}
    accessibility={{ screenReaderInstructions: { draggable: '按空格开始拖动，使用方向键移动，再按空格放下。按 Escape 取消。' } }}
    onDragStart={({ active }) => setActive(String(active.id))} onDragCancel={() => setActive(null)}
    onDragEnd={({ active, over }) => {
      setActive(null)
      if (over && active.id !== over.id) onMove(String(active.id), String(over.id))
    }}>
    <SortableContext items={entries.map(({ id }) => id)} strategy={verticalListSortingStrategy}>{children}</SortableContext>
    {createPortal(<DragOverlay>{current && <div className="settings-drag-preview" aria-hidden="true">
      <GripVertical size={16} />{current.label}
    </div>}</DragOverlay>, document.body)}
  </DndContext>
}

export function SortableSettingsRow({ as: Tag, id, label, disabled, children }: {
  as: 'tr' | 'li'; id: string; label: string; disabled?: boolean; children: (handle: ReactNode) => ReactNode
}) {
  const { attributes, listeners, setActivatorNodeRef, setNodeRef, transform, transition, isDragging, isOver } = useSortable({ id, disabled })
  const handle = <button type="button" className="icon-button settings-grip" ref={setActivatorNodeRef}
    {...attributes} {...listeners} disabled={disabled} aria-label={'拖动 ' + label} title="拖动排序"><GripVertical size={16} aria-hidden /></button>
  return <Tag ref={setNodeRef} style={{ transform: CSS.Translate.toString(transform), transition }}
    data-dragging={isDragging || undefined} data-drop-target={isOver && !isDragging || undefined}>{children(handle)}</Tag>
}
