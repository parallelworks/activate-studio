import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent, type MutableRefObject, type ReactNode } from 'react'

/**
 * Zoom for a document in the Viewer: a PDF's or an Office file's pages, or
 * an image. The document is drawn wider or narrower inside its own pane,
 * which scrolls across as well as down, and the rest of the Studio keeps
 * its size. 100% fits the pane's width.
 *
 * Ctrl or Cmd with the scroll wheel zooms, as does a pinch on a trackpad,
 * keeping the point under the pointer where it is. With the document
 * focused (click it), + and - zoom and 0 fits it again; the window's own
 * zoom keys stay the window's. The Viewer's buttons zoom about the middle
 * of what is in view.
 */

export const ZOOM_MIN = 0.25
export const ZOOM_MAX = 4
const STEPS = [0.25, 0.33, 0.5, 0.67, 0.75, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3, 4]

export const clampZoom = (z: number) => Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, z))

/** The next step in (dir 1) or out (dir -1) from z. */
export function stepZoom(z: number, dir: 1 | -1): number {
  if (dir > 0) return STEPS.find(s => s > z + 0.001) ?? ZOOM_MAX
  return [...STEPS].reverse().find(s => s < z - 0.001) ?? ZOOM_MIN
}

/** A wheel event's zoom factor: about 1.26 for a mouse wheel's notch, and
 *  small steps for a trackpad's pinch, which comes as many small deltas. */
export function wheelFactor(deltaY: number, deltaMode = 0): number {
  const px = deltaMode === 1 ? deltaY * 16 : deltaMode === 2 ? deltaY * 400 : deltaY
  return Math.pow(2, -Math.max(-150, Math.min(150, px)) / 300)
}

/** The Viewer's handle on a ZoomView, for its buttons. */
export interface ZoomControl { zoomTo: (z: number) => void }

/** What the Viewer gives a document it can zoom: the zoom, its setter,
 *  and the handle its buttons use, with `onShown` told while a zoomable
 *  view is on screen, so the buttons show only then. */
export interface Zooming {
  zoom: number
  onZoom: (z: number) => void
  control: MutableRefObject<ZoomControl | null>
  onShown: (shown: boolean) => void
}

/** The nearest ancestor that scrolls down, which the pane scrolls in. */
function scrollerY(el: HTMLElement): Element {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const o = getComputedStyle(p).overflowY
    if ((o === 'auto' || o === 'scroll') && p.scrollHeight > p.clientHeight) return p
  }
  return document.scrollingElement ?? document.documentElement
}

/** A point in a page or image (by its share of the item's size), and where
 *  on the screen it is to stay. */
interface Anchor { el: Element; fx: number; fy: number; x: number; y: number }

export function ZoomView({ zooming, label, className, children }: {
  zooming: Zooming
  /** What is zoomed, for the focusable pane's name ("PDF pages"). */
  label: string
  className?: string
  /** Given the width the document fits at 100%, in CSS pixels; 0 until
   *  measured. */
  children: (fit: number) => ReactNode
}) {
  const { zoom, onZoom, control, onShown } = zooming
  const box = useRef<HTMLDivElement>(null)
  const zoomRef = useRef(zoom)
  zoomRef.current = zoom
  const anchor = useRef<Anchor | null>(null)
  const [fit, setFit] = useState(0)

  // The width to fit: the pane's, less its padding, as the pane resizes.
  useLayoutEffect(() => {
    const b = box.current
    if (!b) return
    const measure = () => {
      const s = getComputedStyle(b)
      setFit(Math.max(0, b.clientWidth - parseFloat(s.paddingLeft || '0') - parseFloat(s.paddingRight || '0')))
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(b)
    return () => ro.disconnect()
  }, [])

  const zoomAt = useCallback((next: number, x?: number, y?: number) => {
    const b = box.current
    const z = clampZoom(next)
    if (!b || Math.abs(z - zoomRef.current) < 0.001) return
    const r = b.getBoundingClientRect()
    const px = x ?? (Math.max(r.left, 0) + Math.min(r.right, window.innerWidth)) / 2
    const py = y ?? (Math.max(r.top, 0) + Math.min(r.bottom, window.innerHeight)) / 2
    // The page or image under the point, or the one nearest it.
    const items = [...b.querySelectorAll('[data-zoom-item]')]
    const hit = document.elementFromPoint?.(px, py)?.closest('[data-zoom-item]')
    let el: Element | undefined = hit && b.contains(hit) ? hit : undefined
    if (!el) {
      let best = Infinity
      for (const it of items) {
        const ir = it.getBoundingClientRect()
        const d = py < ir.top ? ir.top - py : py > ir.bottom ? py - ir.bottom : 0
        if (d < best) { best = d; el = it }
      }
    }
    if (el) {
      const er = el.getBoundingClientRect()
      if (er.width > 0 && er.height > 0) anchor.current = { el, fx: (px - er.left) / er.width, fy: (py - er.top) / er.height, x: px, y: py }
    }
    zoomRef.current = z
    onZoom(z)
  }, [onZoom])

  // After a zoom is drawn, scroll so the anchored point is back in place.
  useLayoutEffect(() => {
    const a = anchor.current
    anchor.current = null
    const b = box.current
    if (!a || !b || !a.el.isConnected) return
    const er = a.el.getBoundingClientRect()
    b.scrollLeft += er.left + a.fx * er.width - a.x
    scrollerY(b).scrollTop += er.top + a.fy * er.height - a.y
  }, [zoom])

  useEffect(() => {
    control.current = { zoomTo: z => zoomAt(z) }
    onShown(true)
    return () => { control.current = null; onShown(false) }
  }, [control, zoomAt, onShown])

  // Listened for directly: React's wheel listener is passive, and only an
  // active one keeps the browser from zooming the whole page.
  useEffect(() => {
    const b = box.current
    if (!b) return
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return
      e.preventDefault()
      zoomAt(zoomRef.current * wheelFactor(e.deltaY, e.deltaMode), e.clientX, e.clientY)
    }
    // Safari and the macOS window report a pinch as gesture events.
    type Gesture = Event & { scale: number; clientX: number; clientY: number }
    let start = 1
    const onStart = (e: Event) => { e.preventDefault(); start = zoomRef.current }
    const onChange = (e: Event) => { e.preventDefault(); const g = e as Gesture; zoomAt(start * g.scale, g.clientX, g.clientY) }
    const onEnd = (e: Event) => e.preventDefault()
    b.addEventListener('wheel', onWheel, { passive: false })
    b.addEventListener('gesturestart', onStart)
    b.addEventListener('gesturechange', onChange)
    b.addEventListener('gestureend', onEnd)
    return () => {
      b.removeEventListener('wheel', onWheel)
      b.removeEventListener('gesturestart', onStart)
      b.removeEventListener('gesturechange', onChange)
      b.removeEventListener('gestureend', onEnd)
    }
  }, [zoomAt])

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.altKey || e.ctrlKey || e.metaKey) return
    let z: number | null = null
    if (e.key === '+' || e.key === '=') z = stepZoom(zoomRef.current, 1)
    else if (e.key === '-' || e.key === '_') z = stepZoom(zoomRef.current, -1)
    else if (e.key === '0') z = 1
    if (z == null) return
    e.preventDefault()
    zoomAt(z)
  }

  return (
    <div
      ref={box}
      className={`zoom-view${className ? ` ${className}` : ''}`}
      tabIndex={0}
      role="region"
      aria-label={`${label}, ${Math.round(zoom * 100)}%`}
      onKeyDown={onKeyDown}
    >
      {children(fit)}
    </div>
  )
}

/** The widest a page is drawn at 100%, as the page CSS has it. */
export const PAGE_MAX = 1100
/** The page column's padding on each side (.pdf-pages). */
const PAGES_PAD = 16

/** A page's width on screen: the pane's width less the column's padding,
 *  no wider than PAGE_MAX, times the zoom; 0 until the pane is measured. */
export function pageWidth(fit: number, zoom: number): number {
  if (!fit) return 0
  return Math.round(Math.min(Math.max(fit - 2 * PAGES_PAD, 160), PAGE_MAX) * zoom)
}

/** A value that follows v once it has held still for ms: a page drawn
 *  again for a zoom waits until the zoom stops changing. */
export function useSettled<T>(v: T, ms = 250): T {
  const [settled, setSettled] = useState(v)
  useEffect(() => {
    const t = setTimeout(() => setSettled(v), ms)
    return () => clearTimeout(t)
  }, [v, ms])
  return settled
}
