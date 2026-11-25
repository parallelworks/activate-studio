import { useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { textBox } from './NameTip'

export const TREE_MIN = 220
/** As wide as a drag goes in a window too narrow for half of it to be more. */
export const TREE_MAX = 520
/** As narrow as a fit makes it: the buttons over the tree still show whole. */
const FIT_LEAST = 300
/** What the tree leaves for the file beside it, however wide it is asked to be. */
const FILE_LEAST = 120
/** The room a hovered row's label button and its fade take at the row's end. */
const BUTTON_ROOM = 34
/** Space after a name, so the last letter does not touch the edge. */
const NAME_PAD = 2

/** The widest the tree may be: half the window, or TREE_MAX where that is
 *  more, with room left for the file in the card (0: not measured). */
export function treeMax(cardWidth: number): number {
  const most = Math.max(TREE_MAX, Math.round(window.innerWidth / 2))
  return Math.max(TREE_MIN, cardWidth > 0 ? Math.min(most, cardWidth - FILE_LEAST) : most)
}

export const clampTree = (w: number, max: number): number => Math.round(Math.min(max, Math.max(TREE_MIN, w)))

/** The tree's width at which its widest listed name fits whole, given its
 *  width now; null with nothing listed. */
export function fitTreeWidth(rail: Element, width: number): number | null {
  let more: number | null = null
  for (const text of rail.querySelectorAll('.explorer .tree-text')) {
    const label = text.querySelector('.tree-label')
    const name = label && textBox(label)
    if (!name) continue
    const tags = text.querySelector<HTMLElement>('.tree-tags')
    // Labels follow the name, and the button covers them instead.
    const after = tags ? tags.getBoundingClientRect().width + 7 : BUTTON_ROOM
    const extra = name.width + NAME_PAD + after - text.getBoundingClientRect().width
    if (more === null || extra > more) more = extra
  }
  return more === null ? null : Math.ceil(width + more)
}

/**
 * The edge between the Library's tree and the file beside it: a drag resizes
 * the tree, a double click fits it to its widest name, up to half the window,
 * and the arrow keys move it once it has the focus. The width is the
 * caller's, which keeps it in this browser.
 */
export function TreeResize({ width, max, rail, onWidth }: {
  width: number
  max: number
  rail: () => HTMLElement | null
  onWidth: (w: number) => void
}) {
  const [dragging, setDragging] = useState(false)
  const [pressed, setPressed] = useState(false)
  const start = useRef<{ x: number; w: number; last: number } | null>(null)

  const down = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    start.current = { x: e.clientX, w: width, last: width }
    // Captured, so a drag over a PDF or an app shown in the file's place
    // still reaches the edge.
    e.currentTarget.setPointerCapture?.(e.pointerId)
    // As the sidebar's edge (NavResize): a press focuses it, for the keys,
    // and shows no ring until a key is used.
    e.currentTarget.focus({ preventScroll: true })
    setPressed(true)
    setDragging(true)
    document.documentElement.classList.add('nav-resizing')
    e.preventDefault()
  }
  const move = (e: PointerEvent<HTMLDivElement>) => {
    const s = start.current
    if (!s) return
    s.last = clampTree(s.w + e.clientX - s.x, max)
    const el = rail()
    if (el) el.style.width = `${s.last}px`
  }
  const up = () => {
    const s = start.current
    if (!s) return
    start.current = null
    setDragging(false)
    document.documentElement.classList.remove('nav-resizing')
    if (s.last !== s.w) onWidth(s.last)
  }
  const fit = () => {
    const el = rail()
    const w = el && fitTreeWidth(el, width)
    if (w !== null && el) onWidth(clampTree(Math.max(FIT_LEAST, w), Math.min(max, Math.round(window.innerWidth / 2))))
  }
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    setPressed(false)
    if (e.ctrlKey || e.metaKey || e.altKey) return
    if (e.key === 'Enter') { e.preventDefault(); fit(); return }
    const step = e.shiftKey ? 40 : 10
    const next = e.key === 'ArrowLeft' ? width - step : e.key === 'ArrowRight' ? width + step
      : e.key === 'Home' ? TREE_MIN : e.key === 'End' ? max : null
    if (next === null) return
    e.preventDefault()
    onWidth(clampTree(next, max))
  }

  return (
    <div className={`divider${dragging ? ' dragging' : ''}${pressed ? ' pressed' : ''}`} role="separator" aria-orientation="vertical"
      aria-label="Tree width" aria-valuemin={TREE_MIN} aria-valuemax={max} aria-valuenow={width}
      title="Drag to resize; double click to fit the names" tabIndex={0}
      onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up}
      onDoubleClick={fit} onKeyDown={key} onBlur={() => setPressed(false)} />
  )
}
