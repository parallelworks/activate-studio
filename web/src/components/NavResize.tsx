import { useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { NAV_WIDTHS, NAV_WIDTH_MAX, NAV_WIDTH_MIN, applyNavWidth, clampNavWidth, saveNavWidth } from '../config'

/**
 * The navigation's right edge, as a handle: a drag resizes the sidebar
 * between Narrow and NAV_WIDTH_MAX and saves the width on release, a double
 * click returns it to Default, and the arrow keys move it in steps. It sits
 * beside the sidebar, not inside it, so the sidebar's own scrolling does
 * not carry it away; the collapsed sidebar and the phone layout have none.
 * A press focuses it, so the arrow keys work after a click or a drag.
 */
export function NavResize({ width }: { width: number }) {
  const [dragging, setDragging] = useState(false)
  const [pressed, setPressed] = useState(false)
  const start = useRef<{ x: number; w: number; last: number } | null>(null)
  const save = (w: number) => { void saveNavWidth(w).catch(() => applyNavWidth(width)) }

  const down = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    const nav = e.currentTarget.previousElementSibling as HTMLElement | null
    const w = nav?.getBoundingClientRect().width || width
    start.current = { x: e.clientX, w, last: w }
    e.currentTarget.setPointerCapture?.(e.pointerId)
    // preventDefault below also stops the press from focusing the edge, so
    // it is focused here: the arrow keys work after a click or a drag, and
    // Shift+Tab moves on from the edge. Without it the focus stayed where
    // the click landed, Shift+Tab reached the sidebar's last control, and
    // revealing that scrolled the sidebar. preventScroll keeps the focus
    // itself from moving anything; :focus-visible keeps a mouse press from
    // showing the ring.
    e.currentTarget.focus({ preventScroll: true })
    // A focus a script gives counts as :focus-visible in some browsers when
    // the last input was a key, so a press marks itself; a key clears it.
    setPressed(true)
    setDragging(true)
    document.documentElement.classList.add('nav-resizing')
    e.preventDefault()
  }
  const move = (e: PointerEvent<HTMLDivElement>) => {
    const s = start.current
    if (!s) return
    s.last = clampNavWidth(s.w + e.clientX - s.x)
    applyNavWidth(s.last)
  }
  const up = () => {
    const s = start.current
    start.current = null
    setDragging(false)
    document.documentElement.classList.remove('nav-resizing')
    if (s && Math.round(s.last) !== Math.round(width)) save(s.last)
  }
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    setPressed(false)
    const step = e.shiftKey ? 40 : 10
    const next = e.key === 'ArrowLeft' ? width - step : e.key === 'ArrowRight' ? width + step
      : e.key === 'Home' ? NAV_WIDTH_MIN : e.key === 'End' ? NAV_WIDTH_MAX : null
    if (next === null) return
    e.preventDefault()
    save(next)
  }

  return (
    <div className={`sidenav-resize${dragging ? ' dragging' : ''}${pressed ? ' pressed' : ''}`} role="separator" aria-orientation="vertical"
      aria-label="Sidebar width" aria-valuemin={NAV_WIDTH_MIN} aria-valuemax={NAV_WIDTH_MAX} aria-valuenow={width}
      title="Drag to resize the sidebar; double click for the default width" tabIndex={0}
      onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up}
      onDoubleClick={() => save(NAV_WIDTHS.default)} onKeyDown={key} onBlur={() => setPressed(false)} />
  )
}
