import { type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type ReactNode, useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Button, IconButton, Loader, SegmentedControl } from '@parallelworks/ui'
import { CloseIcon, ExpandIcon, ResetIcon, ZoomInIcon, ZoomOutIcon } from '@parallelworks/ui/icons'
import { api, type PublicLibrary } from '../api'
import { useMapLibrary } from '../mapLibrary'
import { openInLibrary } from '../scope'
import { LibraryScope } from './LibraryScope'

/**
 * Two views of the corpus from the index, on canvases that zoom, pan and
 * open full window: a squarified treemap of the directory topology (area is
 * bytes; zooming in shows the levels below), and the semantic map (files
 * placed by their embeddings, in clusters; a cluster opens to all of its
 * files, and the map turns in 3D). Each shows one library, picked on the
 * map, and remembers the choice: the topology the main folder until
 * changed, the semantic map the largest library that has one.
 */

/** Categorical colors derived from the deployment's accent: analogous
 *  hues around it with alternating lightness, so the maps read as part of
 *  the theme instead of a foreign rainbow. */
function rgbToHsl(r: number, g: number, b: number): { h: number; s: number; l: number } {
  r /= 255; g /= 255; b /= 255
  const max = Math.max(r, g, b), min = Math.min(r, g, b)
  const l = (max + min) / 2
  if (max === min) return { h: 215, s: 60, l: l * 100 }
  const d = max - min
  const sat = l > 0.5 ? d / (2 - max - min) : d / (max + min)
  let h = 0
  if (max === r) h = ((g - b) / d + (g < b ? 6 : 0))
  else if (max === g) h = (b - r) / d + 2
  else h = (r - g) / d + 4
  return { h: h * 60, s: sat * 100, l: l * 100 }
}

/** The accent as the browser resolves it, [r, g, b]. The theme defines it
 *  as light-dark(...) or another color form, so the variable's text is not
 *  parsed; a probe element's computed color is. */
function resolvedAccent(): [number, number, number] {
  try {
    const probe = document.createElement('span')
    probe.style.color = 'var(--theme-accent, var(--pw-link, #2563eb))'
    document.body.appendChild(probe)
    const color = getComputedStyle(probe).color
    probe.remove()
    const n = color.match(/[\d.]+/g)?.map(Number) ?? []
    // rgb(r, g, b) is 0-255; color(srgb r g b) is 0-1.
    if (n.length >= 3) return color.startsWith('color(') ? [n[0] * 255, n[1] * 255, n[2] * 255] : [n[0], n[1], n[2]]
  } catch { /* server-side or headless */ }
  return [37, 99, 235]
}

let cached: { key: string; colors: string[] } | null = null
/** The categorical colors for the current accent, recomputed when the theme
 *  (light or dark, or the accent) changes. */
function accentPalette(): string[] {
  const [r, g, b] = resolvedAccent()
  const key = `${Math.round(r)},${Math.round(g)},${Math.round(b)}`
  if (cached?.key === key) return cached.colors
  const { h, s } = rgbToHsl(r, g, b)
  const sat = Math.min(68, Math.max(42, s))
  const out: string[] = []
  for (let i = 0; i < 14; i++) {
    const hue = (h + ((i % 7) - 3) * 19 + 360) % 360
    const light = i % 2 ? 62 : 44
    out.push(`hsl(${Math.round(hue)} ${Math.round(sat)}% ${light}%)`)
  }
  cached = { key, colors: out }
  return out
}

/** Dark or white text for a palette color shown at a CSS brightness, by the
 *  higher contrast. */
export function inkOn(hsl: string, brightness = 1): string {
  const [h, s, l] = (hsl.match(/[\d.]+/g) ?? []).map(Number)
  const a = (s / 100) * Math.min(l / 100, 1 - l / 100)
  const f = (k: number) => l / 100 - a * Math.max(-1, Math.min((k + h / 30) % 12 - 3, 9 - ((k + h / 30) % 12), 1))
  const lin = (c: number) => { c = Math.min(1, c * brightness); return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4 }
  const lum = 0.2126 * lin(f(0)) + 0.7152 * lin(f(8)) + 0.0722 * lin(f(4))
  const onWhite = 1.05 / (lum + 0.05), onDark = (lum + 0.05) / 0.0592
  return onDark > onWhite ? '#0f172a' : '#fff'
}

interface DirRow { dir: string; files: number; bytes: number }

export interface TreeNode { name: string; path: string; bytes: number; files: number; children: Map<string, TreeNode> }

function buildTree(dirs: DirRow[]): TreeNode {
  const root: TreeNode = { name: '', path: '', bytes: 0, files: 0, children: new Map() }
  for (const d of dirs) {
    let node = root
    let acc = ''
    // '' is the top folder's own files.
    for (const part of d.dir.split('/').filter(Boolean)) {
      acc = acc ? `${acc}/${part}` : part
      if (!node.children.has(part)) node.children.set(part, { name: part, path: acc, bytes: 0, files: 0, children: new Map() })
      node = node.children.get(part)!
    }
    node.bytes += d.bytes
    node.files += d.files
  }
  const roll = (n: TreeNode): { bytes: number; files: number } => {
    let b = n.bytes, f = n.files
    for (const c of n.children.values()) { const r = roll(c); b += r.bytes; f += r.files }
    n.bytes = b; n.files = f
    return { bytes: b, files: f }
  }
  roll(root)
  return root
}

export interface Rect { x: number; y: number; w: number; h: number; node: TreeNode }

/**
 * Squarified layout (Bruls, Huizing and van Wijk): rows of children, the
 * largest first, each row added to while that keeps its cells nearer
 * square, laid along the shorter side of what is left. Area is bytes at one
 * scale for the whole rectangle, so the cells fill it exactly. (The area
 * was once taken as a share of the whole total from what was left, which
 * left most of the rectangle empty when one child was most of it.)
 */
export function squarify(children: TreeNode[], x: number, y: number, w: number, h: number): Rect[] {
  const items = children.filter(c => c.bytes > 0).sort((a, b) => b.bytes - a.bytes)
  const total = items.reduce((s, c) => s + c.bytes, 0)
  const rects: Rect[] = []
  if (!total || w <= 0 || h <= 0) return rects
  const scale = (w * h) / total
  // The worst aspect ratio of a row of `sum` bytes whose largest and
  // smallest cells are `max` and `min`, along a side of `side`.
  const worst = (sum: number, max: number, min: number, side: number) => {
    const area = sum * scale, s2 = side * side
    return Math.max((s2 * max * scale) / (area * area), (area * area) / (s2 * min * scale))
  }
  let i = 0
  while (i < items.length) {
    const horizontal = w >= h
    const side = horizontal ? h : w
    let j = i, sum = 0
    // The row grows while its worst cell gets no worse.
    while (j < items.length) {
      const next = sum + items[j].bytes
      if (j > i && worst(next, items[i].bytes, items[j].bytes, side) > worst(sum, items[i].bytes, items[j - 1].bytes, side)) break
      sum = next
      j++
    }
    const row = items.slice(i, j)
    // The last row takes what is left, so rounding leaves no sliver.
    const thickness = j === items.length ? (horizontal ? w : h) : Math.min((sum * scale) / side, horizontal ? w : h)
    let off = 0
    row.forEach((r, k) => {
      const len = k === row.length - 1 ? side - off : (r.bytes / sum) * side
      rects.push(horizontal
        ? { x, y: y + off, w: thickness, h: len, node: r }
        : { x: x + off, y, w: len, h: thickness, node: r })
      off += len
    })
    if (horizontal) { x += thickness; w -= thickness } else { y += thickness; h -= thickness }
    i = j
  }
  return rects
}

function fmtB(n: number): string {
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}


// ---- zoom and pan, shared by both maps ----

/** A 2D view: screen = base × k + t. */
export interface View { k: number; tx: number; ty: number }
export const VIEW0: View = { k: 1, tx: 0, ty: 0 }
const MAX_ZOOM = 40

/** The view kept over its content: a w × h base at the view's scale never
 *  leaves a gap at an edge, and never shrinks below its own size. */
export function clampView(v: View, w: number, h: number): View {
  const k = Math.min(MAX_ZOOM, Math.max(1, v.k))
  return { k, tx: Math.min(0, Math.max(w - w * k, v.tx)), ty: Math.min(0, Math.max(h - h * k, v.ty)) }
}

/** Zoom by `factor`, keeping the screen point (px, py) where it is. */
export function zoomAround(v: View, px: number, py: number, factor: number, w: number, h: number): View {
  const k = Math.min(MAX_ZOOM, Math.max(1, v.k * factor))
  const f = k / v.k
  return clampView({ k, tx: px - (px - v.tx) * f, ty: py - (py - v.ty) * f }, w, h)
}

/** The index of the screen point nearest (mx, my) within `radius`, or -1;
 *  where two are as near, the one drawn later (in front) wins. */
export function nearestIndex(xs: ArrayLike<number>, ys: ArrayLike<number>, mx: number, my: number, radius: number, order?: ArrayLike<number>): number {
  let best = -1, bd = radius * radius
  const n = order ? order.length : xs.length
  for (let j = 0; j < n; j++) {
    const i = order ? order[j] : j
    const d = (xs[i] - mx) ** 2 + (ys[i] - my) ** 2
    if (d <= bd) { bd = d; best = i }
  }
  return best
}

// ---- 3D ----

/** An orbit camera about a target point: yaw turns about the vertical axis,
 *  pitch tilts toward the viewer, dist is the camera's distance from the
 *  target in units of the map's own size (it sets how strong the perspective
 *  is, and stays put), k magnifies the picture (1 to MAX_ZOOM, as in 2D),
 *  and (tx, ty, tz) is the target, which sits at the centre of the view and
 *  which the map turns about. Zooming moves the target toward the pointer,
 *  so what is under the pointer stays there. */
export interface Camera { yaw: number; pitch: number; dist: number; k: number; tx: number; ty: number; tz: number }
export const CAMERA0: Camera = { yaw: 0.6, pitch: 0.35, dist: 2.6, k: 1, tx: 0, ty: 0, tz: 0 }

/** Where a point (centred on the origin, about a unit across) lands on
 *  screen: moved so the target is at the origin, rotated by yaw then pitch,
 *  then divided by its depth, so the target's plane keeps `scale × k`
 *  pixels per unit and nearer points grow. `f` is that growth (1 on the
 *  target's plane), `depth` the distance from the camera. */
export function project3d(p: readonly [number, number, number], cam: Camera, cx: number, cy: number, scale: number): { x: number; y: number; depth: number; f: number } {
  const x = p[0] - (cam.tx ?? 0), y = p[1] - (cam.ty ?? 0), z = p[2] - (cam.tz ?? 0)
  const cyaw = Math.cos(cam.yaw), syaw = Math.sin(cam.yaw)
  const x1 = x * cyaw + z * syaw
  const z1 = -x * syaw + z * cyaw
  const cp = Math.cos(cam.pitch), sp = Math.sin(cam.pitch)
  const y2 = y * cp - z1 * sp
  const z2 = y * sp + z1 * cp
  const depth = cam.dist + z2
  const f = cam.dist / Math.max(0.05, depth)
  const s = scale * (cam.k ?? 1)
  return { x: cx + x1 * f * s, y: cy - y2 * f * s, depth, f }
}

/** The map-space step that moves a point `a` to the right and `b` up on the
 *  screen, in the target's plane (the inverse of the camera's rotation). */
export function viewPlane(cam: Camera, a: number, b: number): [number, number, number] {
  const cyaw = Math.cos(cam.yaw), syaw = Math.sin(cam.yaw)
  const cp = Math.cos(cam.pitch), sp = Math.sin(cam.pitch)
  const z1 = -b * sp
  return [a * cyaw - z1 * syaw, b * cp, a * syaw + z1 * cyaw]
}

/** The target kept within the map, so zooming and moving never lose it. */
const TARGET_REACH = 0.75
const withTarget = (cam: Camera, t: [number, number, number]): Camera => {
  const c = (v: number) => Math.max(-TARGET_REACH, Math.min(TARGET_REACH, v))
  return { ...cam, tx: c(t[0]), ty: c(t[1]), tz: c(t[2]) }
}

/** Zoom by `factor` about the screen point (px, py): the picture grows and
 *  the target moves toward the pointer, so a point in the target's plane
 *  under the pointer stays under it. Back at k = 1 the map is centred again. */
export function zoom3d(cam: Camera, px: number, py: number, factor: number, cx: number, cy: number, scale: number): Camera {
  const k = Math.min(MAX_ZOOM, Math.max(1, cam.k * factor))
  if (k === cam.k) return cam
  if (k === 1) return { ...cam, k, tx: 0, ty: 0, tz: 0 }
  const s = scale * cam.k
  const t = 1 - cam.k / k
  const [dx, dy, dz] = viewPlane(cam, ((px - cx) / s) * t, (-(py - cy) / s) * t)
  return withTarget({ ...cam, k }, [cam.tx + dx, cam.ty + dy, cam.tz + dz])
}

/** Move the picture by (dx, dy) screen pixels: the target moves the other way. */
export function pan3d(cam: Camera, dx: number, dy: number, scale: number): Camera {
  const s = scale * cam.k
  const [mx, my, mz] = viewPlane(cam, -dx / s, dy / s)
  return withTarget(cam, [cam.tx + mx, cam.ty + my, cam.tz + mz])
}

/** A map filling the window stops at the classification banner, which stays
 *  on screen with whatever the page shows. */
const bannerBottom = () => document.querySelector('.cls-banner:not(.banner-preview)')?.getBoundingClientRect().bottom ?? 0

const reducedMotion = () => { try { return matchMedia('(prefers-reduced-motion: reduce)').matches } catch { return false } }

// ---- the frame both maps sit in ----

/** A map's canvas, inline or filling the window, with its controls. Filling
 *  the window it covers the map's title, so it carries the library picker and
 *  the busy mark itself, and stays open while another library loads. */
function MapFrame({ title, full, onFull, controls, picker, busy, onEscape, children }: {
  title: string
  full: boolean
  onFull: (full: boolean) => void
  controls: ReactNode
  picker?: ReactNode
  busy?: ReactNode
  /** Escape goes here first; true when it was used (a selection cleared),
   *  so the map stays filling the window. */
  onEscape?: () => boolean
  children: ReactNode
}) {
  const panel = useRef<HTMLDivElement>(null)
  const escape = useRef(onEscape)
  escape.current = onEscape
  useEffect(() => {
    if (!full) return
    const before = document.activeElement as HTMLElement | null
    panel.current?.focus()
    // Escape with the library list open closes the list, not the map.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented || document.querySelector('.library-scope-panel')) return
      e.preventDefault()
      if (escape.current?.()) return
      onFull(false)
    }
    window.addEventListener('keydown', onKey)
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { window.removeEventListener('keydown', onKey); document.body.style.overflow = overflow; before?.focus?.() }
  }, [full, onFull])
  const bar = (
    <div className="map-bar" role="toolbar" aria-label={`${title} view`}>
      {controls}
      {full
        ? <IconButton icon={<CloseIcon />} label="Close full screen" size="sm" onClick={() => onFull(false)} />
        : <IconButton icon={<ExpandIcon />} label="Full screen" size="sm" onClick={() => onFull(true)} />}
    </div>
  )
  if (!full) return <>{bar}{children}</>
  return createPortal(
    <div className="map-full" role="dialog" aria-modal="true" aria-label={title} tabIndex={-1} ref={panel} style={{ '--map-top': `${bannerBottom()}px` } as CSSProperties}>
      <div className="map-full-head"><div className="map-full-title"><h2>{title}</h2>{picker}{busy}</div>{bar}</div>
      <div className="map-full-body">{children}</div>
    </div>,
    document.body,
  )
}

function ZoomButtons({ onZoom, onReset }: { onZoom: (factor: number) => void; onReset: () => void }) {
  return (
    <>
      <IconButton icon={<ZoomInIcon />} label="Zoom in" size="sm" onClick={() => onZoom(1.6)} />
      <IconButton icon={<ZoomOutIcon />} label="Zoom out" size="sm" onClick={() => onZoom(1 / 1.6)} />
      <IconButton icon={<ResetIcon />} label="Reset view" size="sm" onClick={onReset} />
    </>
  )
}

/** A legend entry: a color, a name and a figure; hovering it shows its part
 *  of the map, clicking it does what clicking that part of the map does. */
export interface LegendItem { key: string; name: ReactNode; color: string; note: string; title?: string; ariaLabel?: string; on?: boolean; onClick: () => void }

/** Legend entries shown before "+ N more", largest first. */
export const LEGEND_SHOWN = 8

/** A map's legend: its largest LEGEND_SHOWN entries, and a button that
 *  shows the rest in place (and "Show fewer" folds them again). */
function Legend({ items, onHover, chipClass = '' }: { items: LegendItem[]; onHover: (key: string | null) => void; chipClass?: string }) {
  const [open, setOpen] = useState(false)
  const rest = items.length - LEGEND_SHOWN
  const shown = open || rest <= 0 ? items : items.slice(0, LEGEND_SHOWN)
  return (
    <p className="map-legend" onMouseLeave={() => onHover(null)}>
      {shown.map(it => (
        <button key={it.key} className={`legend-chip${chipClass ? ` ${chipClass}` : ''}${it.on ? ' on' : ''}`} title={it.title} aria-label={it.ariaLabel}
          onMouseEnter={() => onHover(it.key)} onFocus={() => onHover(it.key)} onBlur={() => onHover(null)} onClick={it.onClick}>
          <span className="legend-dot" style={{ background: it.color }} />
          <span className="legend-name">{it.name}</span> <span className="muted">{it.note}</span>
        </button>
      ))}
      {rest > 0 && (
        <button className="legend-more" aria-expanded={open} onClick={() => setOpen(o => !o)}>
          {open ? 'Show fewer' : `+ ${rest} more`}
        </button>
      )}
    </p>
  )
}

/** The canvas's size in CSS pixels, followed as it changes. */
function useCanvasSize(ref: React.RefObject<HTMLCanvasElement | null>, height: number | 'fill'): { w: number; h: number } {
  const [size, setSize] = useState({ w: 600, h: height === 'fill' ? 480 : height })
  useEffect(() => {
    const cv = ref.current
    if (!cv) return
    const measure = () => {
      const box = (height === 'fill' ? cv.parentElement : cv)?.getBoundingClientRect()
      const w = Math.max(200, Math.round(box?.width || 600))
      const h = height === 'fill' ? Math.max(200, Math.round(box?.height || 480)) : height
      setSize(s => (s.w === w && s.h === h ? s : { w, h }))
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(height === 'fill' ? cv.parentElement ?? cv : cv)
    return () => ro.disconnect()
  }, [ref, height])
  return size
}

/** The 2D drawing context, scaled for the screen, or null (a test's jsdom). */
function context2d(cv: HTMLCanvasElement, w: number, h: number): CanvasRenderingContext2D | null {
  let g: CanvasRenderingContext2D | null = null
  try { g = cv.getContext('2d') } catch { return null }
  if (!g) return null
  const dpr = window.devicePixelRatio || 1
  if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) {
    cv.width = Math.round(w * dpr)
    cv.height = Math.round(h * dpr)
  }
  g.setTransform(dpr, 0, 0, dpr, 0, 0)
  g.clearRect(0, 0, w, h)
  return g
}

/** An hsl() color at a lightness factor, for shading on a canvas. */
function shade(hsl: string, factor: number): string {
  const [h, s, l] = (hsl.match(/[\d.]+/g) ?? []).map(Number)
  return `hsl(${h} ${s}% ${Math.max(8, Math.min(92, l * factor))}%)`
}

const isDarkTheme = () => document.documentElement.getAttribute('data-theme') === 'dark'
  || (!document.documentElement.getAttribute('data-theme') && (() => { try { return matchMedia('(prefers-color-scheme: dark)').matches } catch { return false } })())

/** Pointer, wheel and keyboard on a map canvas: drag (and its distance, so a
 *  drag is not also a click), wheel or pinch, double-click, and keys. */
function useGestures(ref: React.RefObject<HTMLCanvasElement | null>, h: {
  drag: (dx: number, dy: number, e: PointerEvent) => void
  hover: (x: number, y: number) => void
  leave: () => void
  click: (x: number, y: number) => void
  wheel: (x: number, y: number, deltaY: number) => void
  dbl: (x: number, y: number) => void
  key: (e: KeyboardEvent) => boolean
}) {
  const handlers = useRef(h)
  handlers.current = h
  useEffect(() => {
    const cv = ref.current
    if (!cv) return
    let down: { x: number; y: number; moved: number; id: number } | null = null
    const at = (e: MouseEvent) => { const r = cv.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top } }
    const onDown = (e: PointerEvent) => {
      down = { ...at(e), moved: 0, id: e.pointerId }
      try { cv.setPointerCapture(e.pointerId) } catch { /* not in jsdom */ }
    }
    const onMove = (e: PointerEvent) => {
      const p = at(e)
      if (down && down.id === e.pointerId) {
        const dx = p.x - down.x, dy = p.y - down.y
        down.moved += Math.abs(dx) + Math.abs(dy)
        down.x = p.x; down.y = p.y
        if (down.moved > 3) handlers.current.drag(dx, dy, e)
      } else handlers.current.hover(p.x, p.y)
    }
    const onUp = (e: PointerEvent) => {
      const wasClick = down && down.moved <= 3 && e.button === 0
      down = null
      try { cv.releasePointerCapture(e.pointerId) } catch { /* not in jsdom */ }
      if (wasClick) { const p = at(e); handlers.current.click(p.x, p.y) }
    }
    const onWheel = (e: WheelEvent) => { e.preventDefault(); const p = at(e); handlers.current.wheel(p.x, p.y, e.deltaY * (e.deltaMode === 1 ? 33 : 1)) }
    const onDbl = (e: MouseEvent) => { const p = at(e); handlers.current.dbl(p.x, p.y) }
    const onKey = (e: KeyboardEvent) => { if (handlers.current.key(e)) e.preventDefault() }
    const onLeave = () => handlers.current.leave()
    const onMenu = (e: MouseEvent) => e.preventDefault()
    cv.addEventListener('pointerdown', onDown)
    cv.addEventListener('pointermove', onMove)
    cv.addEventListener('pointerup', onUp)
    cv.addEventListener('pointerleave', onLeave)
    cv.addEventListener('wheel', onWheel, { passive: false })
    cv.addEventListener('dblclick', onDbl)
    cv.addEventListener('keydown', onKey)
    cv.addEventListener('contextmenu', onMenu)
    return () => {
      cv.removeEventListener('pointerdown', onDown)
      cv.removeEventListener('pointermove', onMove)
      cv.removeEventListener('pointerup', onUp)
      cv.removeEventListener('pointerleave', onLeave)
      cv.removeEventListener('wheel', onWheel)
      cv.removeEventListener('dblclick', onDbl)
      cv.removeEventListener('keydown', onKey)
      cv.removeEventListener('contextmenu', onMenu)
    }
  }, [ref])
}

/** The keys both 2D maps share: + and - zoom about the centre, the arrows
 *  pan, 0 resets. True when the key was one of them. */
function viewKey(e: KeyboardEvent, zoom: (f: number) => void, pan: (dx: number, dy: number) => void, reset: () => void): boolean {
  if (e.key === '+' || e.key === '=') zoom(1.6)
  else if (e.key === '-' || e.key === '_') zoom(1 / 1.6)
  else if (e.key === 'ArrowLeft') pan(60, 0)
  else if (e.key === 'ArrowRight') pan(-60, 0)
  else if (e.key === 'ArrowUp') pan(0, 60)
  else if (e.key === 'ArrowDown') pan(0, -60)
  else if (e.key === '0') reset()
  else return false
  return true
}

// ---- the corpus topology ----

interface Cell { x: number; y: number; w: number; h: number; node: TreeNode; depth: number; color: string; parent: number }

/** The treemap of `node`, nested to `maxDepth` levels in one layout: each
 *  directory's children fill it below a header strip for its name. Zooming
 *  draws the deeper levels where they have room, with no new layout. */
function layoutCells(node: TreeNode, w: number, h: number, maxDepth: number, colorOf: (n: TreeNode) => string): Cell[] {
  const out: Cell[] = []
  const place = (n: TreeNode, x: number, y: number, cw: number, ch: number, depth: number, parent: number, color: string | null) => {
    squarify([...n.children.values()], x, y, cw, ch).forEach((r, i) => {
      const base = color ?? colorOf(r.node)
      const col = shade(base, (depth === 1 ? 1.18 : 1.1) - (i % 4) * 0.07 - (depth - 1) * 0.08)
      const me = out.push({ ...r, depth, color: col, parent }) - 1
      if (depth < maxDepth && r.node.children.size && r.w > 6 && r.h > 6) {
        const head = Math.min(15, r.h * 0.22), inset = Math.min(2, r.w * 0.04)
        place(r.node, r.x + inset, r.y + head, r.w - 2 * inset, r.h - head - inset, depth + 1, me, base)
      }
    })
  }
  place(node, 0, 0, w, h, 1, -1, null)
  return out
}

/** A library's map data comes from the main folder's own address for the
 *  main folder, as before libraries had maps, else the library's. */
const mainOrLibrary = (id: string, kind: 'topology' | 'semantic-map') =>
  id === 'kb' ? `/api/kb/${kind}` : `/api/libraries/${encodeURIComponent(id)}/${kind}`

/** The deepest folder fetched that holds `dir`: the topology of a folder
 *  reaches a fixed number of levels below it, and is asked for again below
 *  a folder near that depth as you open it. */
function reachOf(reach: Map<string, number>, dir: string): { root: string; levels: number } | null {
  let best: { root: string; levels: number } | null = null
  for (const [root, levels] of reach) {
    if (root && dir !== root && !dir.startsWith(`${root}/`)) continue
    if (!best || root.length > best.root.length) best = { root, levels }
  }
  return best
}
const depthOf = (p: string) => p.split('/').filter(Boolean).length

export function TopologyMap({ libraries = [] }: { libraries?: PublicLibrary[] }) {
  const PALETTE = accentPalette()
  const [libId, setLib] = useMapLibrary('topology', libraries, 'kb')
  const lib = libraries.length > 1 ? libId ?? 'kb' : 'kb'
  const label = libraries.find(l => l.id === lib)?.label ?? 'the main folder'
  const [dirs, setDirs] = useState<DirRow[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [empty, setEmpty] = useState(false)
  const reach = useRef(new Map<string, number>())
  const [focus, setFocus] = useState('')
  const [full, setFull] = useState(false)
  const [highlight, setHighlight] = useState<string | null>(null)
  const gen = useRef(0)
  // `fresh` asks the server past its own short-lived copy of the counts.
  const load = useCallback((dir: string, fresh = false): Promise<void> => {
    const my = gen.current
    if (reach.current.has(dir)) return Promise.resolve()
    reach.current.set(dir, 0)
    setLoading(true)
    const qs = new URLSearchParams({ ...(dir ? { dir } : {}), ...(fresh ? { refresh: '1' } : {}) }).toString()
    return fetch(`${mainOrLibrary(lib, 'topology')}${qs ? `?${qs}` : ''}`, { cache: 'no-store' })
      .then(async r => { const d = await r.json(); if (!r.ok) throw new Error(d?.error ?? `HTTP ${r.status}`); return d })
      .then((d: { available: boolean; dirs?: DirRow[]; levels?: number }) => {
        if (my !== gen.current) return
        if (!d.available) { if (!dir) { setDirs(null); setEmpty(true) } return }
        if (!dir) setEmpty(false)
        reach.current.set(dir, d.levels ?? 99)
        // A folder asked for again replaces its own row, which held the
        // totals of everything below it, with the rows below it.
        setDirs(prev => dir ? [...(prev ?? []).filter(r => r.dir !== dir), ...(d.dirs ?? [])] : (d.dirs ?? []))
      })
      .catch(e => { if (my === gen.current) { reach.current.delete(dir); if (!dir) setError(String((e as Error).message ?? e)) } })
      .finally(() => { if (my === gen.current) setLoading(false) })
  }, [lib])
  // Another library starts over.
  useEffect(() => {
    gen.current++
    reach.current = new Map()
    setDirs(null); setEmpty(false); setError(null); setFocus('')
    load('')
  }, [lib]) // eslint-disable-line react-hooks/exhaustive-deps
  // Files added, moved or deleted, or an index job ended (ade-kb-changed):
  // the folders are read again. The map shown stays until the new counts
  // arrive, and the folder open stays open, read again below it if it was
  // deeper than the first read reached.
  const focusRef = useRef(focus)
  focusRef.current = focus
  useEffect(() => {
    const onChange = () => {
      gen.current++
      reach.current = new Map()
      setError(null)
      void load('', true).then(() => {
        const f = focusRef.current
        const r = f ? reachOf(reach.current, f) : null
        if (r && r.levels && depthOf(f) - depthOf(r.root) >= r.levels - 2) void load(f, true)
      })
    }
    window.addEventListener('ade-kb-changed', onChange)
    return () => window.removeEventListener('ade-kb-changed', onChange)
  }, [load])
  // Opening a folder near the last level fetched asks for the levels below it.
  useEffect(() => {
    if (!focus) return
    const r = reachOf(reach.current, focus)
    if (r && r.levels && depthOf(focus) - depthOf(r.root) >= r.levels - 2) load(focus)
  }, [focus, load])
  const tree = useMemo(() => (dirs ? buildTree(dirs) : null), [dirs])
  const viewApi = useRef<{ zoom: (f: number) => void; reset: () => void } | null>(null)
  const paletteKey = PALETTE.join()
  const colorOf = useCallback((n: TreeNode) => {
    const topKeys = tree ? [...tree.children.keys()] : []
    return PALETTE[Math.max(0, topKeys.indexOf(n.path.split('/')[0])) % PALETTE.length]
  }, [tree, paletteKey]) // eslint-disable-line react-hooks/exhaustive-deps
  const picker = libraries.length > 1 && (
    <LibraryScope single libraries={libraries} value={[lib]} onChange={s => { if (s !== 'all' && s[0]) setLib(s[0]) }} hint="Corpus topology of" />
  )
  const busy = loading && <Loader inline size={14} label="Reading the folders…" />
  const head = (
    <div className="map-head">
      <h3>Corpus topology</h3>
      {!full && picker}
      {!full && busy}
    </div>
  )
  if (!tree || (!tree.children.size && !tree.bytes)) {
    if (!picker) return null
    const note = (
      <p className="muted">
        {error ? `The folders of ${label} could not be read: ${error}.`
          : empty ? `${label} has no index yet, so there is nothing to draw.`
            : loading ? `Reading the folders of ${label} from its index.`
              : `${label} has no files in its index.`}
      </p>
    )
    return (
      <section className="card ov-list">
        {head}
        {full
          ? <MapFrame key="frame" title="Corpus topology" full onFull={setFull} controls={null} picker={picker} busy={busy}><div className="map-empty">{note}</div></MapFrame>
          : note}
      </section>
    )
  }
  let node = tree
  if (focus) for (const part of focus.split('/')) { const c = node.children.get(part); if (!c) break; node = c }
  const topDirs = [...tree.children.values()].sort((a, b) => b.bytes - a.bytes)
  const topColor = (p: string) => colorOf({ path: p } as TreeNode)
  const map = (height: number | 'fill') => (
    <TopologyCanvas node={node} height={height} colorOf={colorOf} onDrill={setFocus} api={viewApi} highlight={highlight} />
  )
  const crumbs = (
    <p className="crumb-row">
      <button className="crumb" onClick={() => setFocus('')}>{libraries.length > 1 ? label : 'corpus'}</button>
      {focus.split('/').filter(Boolean).map((part, i, arr) => (
        <button key={i} className="crumb" onClick={() => setFocus(arr.slice(0, i + 1).join('/'))}>/{part}</button>
      ))}
    </p>
  )
  const legend = (
    <Legend onHover={setHighlight} items={topDirs.map(d => ({
      key: d.path, name: d.name, color: topColor(d.path), note: fmtB(d.bytes), title: `${d.path} · ${d.files.toLocaleString()} files`,
      on: focus === d.path || focus.startsWith(`${d.path}/`), onClick: () => setFocus(d.path),
    }))} />
  )
  const controls = <ZoomButtons onZoom={f => viewApi.current?.zoom(f)} onReset={() => viewApi.current?.reset()} />
  return (
    <section className="card ov-list">
      {head}
      <p className="muted">
        Area is bytes on disk, one hue per top-level directory. Zoom (wheel, pinch or +) to see the levels below; drag to
        move. Click a directory to open it on its own; the crumbs back out.
      </p>
      {crumbs}
      <MapFrame key="frame" title="Corpus topology" full={full} onFull={setFull} controls={controls} picker={picker} busy={busy}>
        {full ? <>{crumbs}{map('fill')}{legend}</> : map(320)}
      </MapFrame>
      {!full && legend}
    </section>
  )
}

function TopologyCanvas({ node, height, colorOf, onDrill, api, highlight = null }: {
  node: TreeNode
  height: number | 'fill'
  colorOf: (n: TreeNode) => string
  onDrill: (path: string) => void
  api: React.RefObject<{ zoom: (f: number) => void; reset: () => void } | null>
  /** A top-level directory hovered in the legend: the rest of the map dims. */
  highlight?: string | null
}) {
  const cv = useRef<HTMLCanvasElement>(null)
  const { w, h } = useCanvasSize(cv, height)
  const [view, setView] = useState<View>(VIEW0)
  const [hover, setHover] = useState<Cell | null>(null)
  const cells = useMemo(() => layoutCells(node, w, h, 4, colorOf), [node, w, h, colorOf])
  // A new directory, or new data, starts from the whole of it.
  useEffect(() => { setView(VIEW0); setHover(null) }, [node])
  const shown = useRef<number[]>([])

  useEffect(() => {
    const c = cv.current
    if (!c) return
    c.dataset.zoom = view.k.toFixed(2)
    c.dataset.highlight = highlight ?? ''
    const g = context2d(c, w, h)
    if (!g) return
    const sx = (x: number) => x * view.k + view.tx, sy = (y: number) => y * view.k + view.ty
    // A directory hovered in the legend stays bright; the others dim.
    const dimmed = (cl: Cell) => !!highlight && cl.node.path.split('/')[0] !== highlight
    // A level below the first is drawn where its parent has room on screen.
    const roomy = (i: number) => {
      const p = cells[i].parent
      if (p < 0) return true
      const pc = cells[p]
      return view.k > 1.05 && pc.w * view.k > 110 && pc.h * view.k > 70 && drawn[p]
    }
    const drawn: boolean[] = new Array(cells.length).fill(false)
    const order: number[] = []
    for (let i = 0; i < cells.length; i++) {
      if (!roomy(i)) continue
      const cl = cells[i]
      const x = sx(cl.x), y = sy(cl.y), cw = cl.w * view.k, ch = cl.h * view.k
      if (x > w || y > h || x + cw < 0 || y + ch < 0) { drawn[i] = cl.depth === 1 || drawn[cl.parent]; continue }
      drawn[i] = true
      order.push(i)
      g.globalAlpha = dimmed(cl) ? 0.22 : 1
      g.fillStyle = cl.color
      g.fillRect(x, y, cw, ch)
      g.strokeStyle = 'rgba(255,255,255,0.55)'
      g.lineWidth = 1
      g.strokeRect(x + 0.5, y + 0.5, cw - 1, ch - 1)
    }
    g.globalAlpha = 1
    shown.current = order
    const withKids = new Set(order.map(j => cells[j].parent))
    g.textBaseline = 'top'
    for (const i of order) {
      const cl = cells[i]
      const x = sx(cl.x), y = sy(cl.y), cw = cl.w * view.k, ch = cl.h * view.k
      const hasKids = withKids.has(i)
      // The part of the cell on screen, so a name stays in view when the
      // cell's corner is scrolled off. A directory whose children are drawn
      // keeps its name in its own header strip, above theirs.
      const head = hasKids ? Math.min(15, cl.h * 0.22) * view.k : ch
      const vx = Math.max(x, 0), vy = hasKids ? y : Math.max(y, 0)
      const vw = Math.min(x + cw, w) - vx, vh = Math.min(y + head, y + ch, h) - Math.max(vy, 0)
      if (vw < 44 || vh < 13 || vy + 12 < 0 || dimmed(cl)) continue
      g.save()
      g.beginPath()
      g.rect(vx, Math.max(vy, 0), vw, vh)
      g.clip()
      g.fillStyle = inkOn(cl.color)
      g.font = '600 11.5px system-ui, sans-serif'
      g.fillText(cl.node.name, vx + 5, vy + 3)
      if (!hasKids && vw > 90 && vh > 34) {
        g.font = '11px system-ui, sans-serif'
        g.fillText(`${cl.node.files.toLocaleString()} files · ${fmtB(cl.node.bytes)}`, vx + 5, vy + 17)
      }
      g.restore()
    }
    if (hover) {
      g.strokeStyle = isDarkTheme() ? '#fff' : '#0f172a'
      g.lineWidth = 2
      g.strokeRect(sx(hover.x) + 1, sy(hover.y) + 1, hover.w * view.k - 2, hover.h * view.k - 2)
    }
  }, [cells, view, w, h, hover, highlight]) // eslint-disable-line react-hooks/exhaustive-deps

  const cellAt = (x: number, y: number): Cell | null => {
    let hit: Cell | null = null
    for (const i of shown.current) {
      const cl = cells[i]
      const cx = cl.x * view.k + view.tx, cy = cl.y * view.k + view.ty
      if (x >= cx && y >= cy && x < cx + cl.w * view.k && y < cy + cl.h * view.k && (!hit || cl.depth >= hit.depth)) hit = cl
    }
    return hit
  }
  const zoom = useCallback((f: number, px = w / 2, py = h / 2) => setView(v => zoomAround(v, px, py, f, w, h)), [w, h])
  const reset = useCallback(() => setView(VIEW0), [])
  const pan = (dx: number, dy: number) => setView(v => clampView({ ...v, tx: v.tx + dx, ty: v.ty + dy }, w, h))
  api.current = { zoom: f => zoom(f), reset }
  useGestures(cv, {
    drag: (dx, dy) => pan(dx, dy),
    hover: (x, y) => setHover(cellAt(x, y)),
    leave: () => setHover(null),
    click: (x, y) => {
      // The directory under the pointer opens on its own, as before; a file
      // area (no directories below it) does nothing.
      let cl = cellAt(x, y)
      while (cl && !cl.node.children.size && cl.parent >= 0) cl = cells[cl.parent]
      if (cl && cl.node.children.size) onDrill(cl.node.path)
    },
    wheel: (x, y, d) => zoom(Math.exp(-d * 0.0015), x, y),
    dbl: (x, y) => zoom(2, x, y),
    key: e => viewKey(e, f => zoom(f), pan, reset),
  })
  return (
    <div className={`map-canvas${height === 'fill' ? ' fill' : ''}`}>
      <canvas ref={cv} className="treemap-canvas" tabIndex={0} aria-label="Corpus topology: zoom with + and -, move with the arrow keys, 0 resets"
        style={{ width: '100%', height: height === 'fill' ? '100%' : height, display: 'block', cursor: hover?.node.children.size ? 'pointer' : 'default' }} />
      {hover && <div className="map-tip">{hover.node.path} · {hover.node.files.toLocaleString()} files · {fmtB(hover.node.bytes)}</div>}
    </div>
  )
}

// ---- the semantic map ----

/** A cluster: `label` is its distinctive words, or its folders where its
 *  files have no text; `folder` its folder name; `terms` the words in its
 *  label. Maps made before names came from words have the label only. */
export interface MapCluster { c: number; label: string; count: number; x: number; y: number; z: number; folder?: string; terms?: string[] }
export interface MapPoint { p: string; x: number; y: number; z: number; c: number }
/** Two clusters near each other by meaning; `sim` is the cosine of their mean vectors. */
export interface MapLink { a: number; b: number; sim: number }
/** A file near another by meaning, with its place on the map where it has one. */
export interface MapNeighbor { p: string; sim: number; x?: number; y?: number; z?: number; c?: number }
/** The library a map is of, as the server describes it; `host` is the
 *  system a library on another system is on. */
export interface MapLibrary { id: string; label: string; kind: 'main' | 'built' | 'connected' | 'remote'; depth: string | null; vectors: boolean; host?: string }
/** How far the map of a library on another system has got. */
export interface MapProgress { phase: 'counting' | 'fetching' | 'drawing'; files?: number; of?: number }
/** Where a remote library's map was fetched from, and when. */
export interface RemoteMapInfo { host: string; fetchedAt: string | null; checkedAt: string | null; bytes?: number; fetchMs?: number; models?: { model: string; files: number }[] }
export interface MapOverview {
  available: boolean
  reason?: 'off' | 'none' | 'failed' | 'connected' | 'remote-none'
  library?: MapLibrary
  building?: boolean
  progress?: MapProgress | null
  error?: string | null
  builtAt?: string
  model?: string
  files?: number
  shown?: number
  /** Where `files` is a sample: how many files have vectors there. */
  of?: number
  remote?: RemoteMapInfo
  clusters?: MapCluster[]
  links?: MapLink[]
  points?: MapPoint[]
  fallback?: boolean
}

/** Points a cluster loads as you zoom into the overview, per cluster and in
 *  all, and in a cluster on its own; 3D keeps fewer so it turns smoothly. */
const DETAIL_PER_CLUSTER = 4000
const DETAIL_TOTAL = 20000
const CLUSTER_CAP_2D = 20000
const CLUSTER_CAP_3D = 6000
const DETAIL_PER_CLUSTER_3D = 2000
const PAGE = 2000

/**
 * The names drawn on the map and in the legend, by cluster. A cluster named
 * for its words keeps them, cut at 30 characters. One named for its folders
 * shows each folder's own name, with as many parents as it takes for two
 * names not to read the same, within one cluster's name ("pw/docs +
 * gs/docs", not "docs + docs") or across clusters.
 */
export function shortLabels(clusters: MapCluster[]): Map<number, string> {
  const cut = (s: string) => (s.length > 30 ? `${s.slice(0, 29)}…` : s)
  const out = new Map<number, string>()
  for (const cl of clusters) if (cl.terms?.length) out.set(cl.c, cut(cl.label))
  const byFolder = clusters.filter(cl => !cl.terms?.length)
  const parts = new Map(byFolder.map(cl => [cl.c, cl.label.split(' + ').map(p => p.split('/').filter(Boolean))]))
  const depth = new Map(byFolder.map(cl => [cl.c, parts.get(cl.c)!.map(() => 1)]))
  const tails = (c: number) => parts.get(c)!.map((segs, i) => segs.slice(-depth.get(c)![i]).join('/'))
  const show = (c: number) => {
    const t = tails(c)
    return t.slice(0, 2).join(' + ') + (t.length > 2 ? ' +…' : '')
  }
  const deeper = (c: number, which: number[]) => {
    let moved = false
    for (const i of which) if (depth.get(c)![i] < parts.get(c)![i].length) { depth.get(c)![i]++; moved = true }
    return moved
  }
  for (let round = 0; round < 6; round++) {
    let moved = false
    for (const cl of byFolder) {
      const t = tails(cl.c)
      for (let i = 0; i < t.length; i++) for (let j = i + 1; j < t.length; j++) if (t[i] === t[j]) moved = deeper(cl.c, [i, j]) || moved
    }
    const named = new Map<string, number[]>()
    for (const cl of byFolder) named.set(show(cl.c), [...(named.get(show(cl.c)) ?? []), cl.c])
    const words = new Set(out.values())
    for (const [name, cs] of named) {
      if (cs.length < 2 && !words.has(name)) continue
      for (const c of cs) moved = deeper(c, parts.get(c)!.map((_, i) => i)) || moved
    }
    if (!moved) break
  }
  for (const cl of byFolder) out.set(cl.c, cut(show(cl.c)))
  return out
}

/** Whether the map draws its links, kept per viewer and per view (2D or
 *  3D); on in 3D and off in 2D until changed. */
const LINKS_KEY = 'studio.semanticMap.links'
function linksPref(mode: '2d' | '3d'): boolean {
  try {
    const v = JSON.parse(localStorage.getItem(LINKS_KEY) ?? '{}') as Record<string, unknown>
    if (typeof v[mode] === 'boolean') return v[mode] as boolean
  } catch { /* nothing kept, or no storage */ }
  return mode === '3d'
}
function keepLinksPref(mode: '2d' | '3d', on: boolean): void {
  try {
    let v: Record<string, unknown> = {}
    try { v = JSON.parse(localStorage.getItem(LINKS_KEY) ?? '{}') as Record<string, unknown> } catch { /* replaced */ }
    localStorage.setItem(LINKS_KEY, JSON.stringify({ ...v, [mode]: on }))
  } catch { /* no storage: the choice lasts this page */ }
}

/** A file's nearest files by meaning, asked once per file per map. `base`
 *  is the map's address, which names its library. */
const neighborCache = new Map<string, Promise<MapNeighbor[]>>()
function nearestFiles(base: string, p: string): Promise<MapNeighbor[]> {
  const key = `${base}\0${p}`
  let hit = neighborCache.get(key)
  if (!hit) {
    hit = fetch(`${base}/neighbors?file=${encodeURIComponent(p)}&k=5`)
      .then(r => (r.ok ? r.json() : {}))
      .then((d: { neighbors?: MapNeighbor[] }) => d.neighbors ?? [])
      .catch(() => [])
    neighborCache.set(key, hit)
    if (neighborCache.size > 200) neighborCache.delete(neighborCache.keys().next().value!)
  }
  return hit
}

// ---- the file selected on the semantic map ----

/** A file selected on the map, with its place there where it has one (a
 *  nearest file not on the map yet may have none). */
export interface MapSelection { p: string; x?: number; y?: number; z?: number; c?: number }

/** What changes the selection: a file on the map, one of the selected file's
 *  nearest files (from the card or the map), or nothing. */
export type SelectionAction =
  | { type: 'select'; file: MapSelection }
  | { type: 'neighbor'; nb: MapNeighbor }
  | { type: 'clear' }

export function selectionReducer(sel: MapSelection | null, a: SelectionAction): MapSelection | null {
  if (a.type === 'clear') return null
  const file = a.type === 'select' ? a.file : { p: a.nb.p, x: a.nb.x, y: a.nb.y, z: a.nb.z, c: a.nb.c }
  // The same file again keeps the one held, so the view does not move again.
  return sel && sel.p === file.p ? sel : file
}

/** Opens a file from the map: a map filling the window closes first, so the
 *  Library it opens in is what shows. */
export function openFromMap(path: string, full: boolean, setFull: (full: boolean) => void, onOpen: (path: string) => void): void {
  if (full) setFull(false)
  onOpen(path)
}

/** The zoom a selection moves to, unless the view is already nearer. */
export const SELECT_ZOOM = 3
/** How long the view takes to move to a selection, in milliseconds. */
export const SELECT_MS = 350

/** The 2D view with the base point `b` at screen point `at` (the centre of
 *  a w × h canvas unless given), at zoom `k` or the view's own where that is
 *  nearer; kept over the content, so a point near an edge comes as near as
 *  it can. */
export function centreView(v: View, b: { x: number; y: number }, w: number, h: number, k = SELECT_ZOOM, at = { x: w / 2, y: h / 2 }): View {
  const kk = Math.min(MAX_ZOOM, Math.max(v.k, k))
  return clampView({ k: kk, tx: at.x - b.x * kk, ty: at.y - b.y * kk }, w, h)
}

/** The 3D camera with the point `t` (centred map units) at zoom `k` or its
 *  own where that is nearer, and `off` screen pixels from the centre of the
 *  view (none unless given); its turn and tilt stay as they are. `scale` is
 *  the pixels per unit at k = 1. */
export function centreCamera(cam: Camera, t: readonly [number, number, number], k = SELECT_ZOOM, off = { x: 0, y: 0 }, scale = 1): Camera {
  const kk = Math.min(MAX_ZOOM, Math.max(cam.k, k))
  const [a, b, c] = viewPlane(cam, off.x / (scale * kk), -off.y / (scale * kk))
  return withTarget({ ...cam, k: kk }, [t[0] - a, t[1] - b, t[2] - c])
}

/** Where on a w × h canvas a selection is shown: its centre, or the middle
 *  of the part the selection's card leaves free (to its left where the card
 *  sits at the right, below it where the card spans the canvas). `card` is
 *  the card's box relative to the canvas. */
export function freeCentre(w: number, h: number, card: { left: number; bottom: number; width: number } | null): { x: number; y: number } {
  if (!card || card.width <= 0) return { x: w / 2, y: h / 2 }
  if (card.left > w * 0.4) return { x: card.left / 2, y: h / 2 }
  if (card.bottom < h * 0.8) return { x: w / 2, y: (card.bottom + h) / 2 }
  return { x: w / 2, y: h / 2 }
}

/** Part way (0 to 1) from one view or camera to another, eased in and out. */
export function tweenView<T extends View | Camera>(a: T, b: T, t: number): T {
  const e = t <= 0 ? 0 : t >= 1 ? 1 : t < 0.5 ? 4 * t * t * t : 1 - (-2 * t + 2) ** 3 / 2
  const out = { ...b } as Record<string, number>
  for (const key of Object.keys(b) as (keyof T)[]) {
    const x = a[key] as unknown as number, y = b[key] as unknown as number
    // Zoom moves evenly in its logarithm, so the picture grows at an even pace.
    out[key as string] = key === 'k' ? Math.exp(Math.log(x) + (Math.log(y) - Math.log(x)) * e) : x + (y - x) * e
  }
  return out as unknown as T
}

/** A line from a file to one of its nearest files, on screen: to its dot
 *  where it is on the map, else to its place there with a ring, else none. */
export interface NeighborLine { p: string; x: number; y: number; sim: number; c: number | undefined; ring: boolean }

export function neighborLines(list: MapNeighbor[], dotAt: (p: string) => { x: number; y: number } | null,
  placeAt: (x: number, y: number, z: number) => { x: number; y: number }): NeighborLine[] {
  const out: NeighborLine[] = []
  for (const nb of list) {
    const dot = dotAt(nb.p)
    const to = dot ?? (nb.x !== undefined && nb.y !== undefined && nb.z !== undefined ? placeAt(nb.x, nb.y, nb.z) : null)
    if (to) out.push({ p: nb.p, x: to.x, y: to.y, sim: nb.sim, c: nb.c, ring: !dot })
  }
  return out
}

/** A file's own name and its folder ('' at the top). */
export function splitPath(p: string): { name: string; folder: string } {
  const i = p.lastIndexOf('/')
  return i < 0 ? { name: p, folder: '' } : { name: p.slice(i + 1), folder: p.slice(0, i) }
}

/** How long ago a time was, in words. */
export function ago(iso: string | null | undefined, now = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN
  if (!Number.isFinite(t)) return ''
  const m = Math.round((now - t) / 60_000)
  if (m < 1) return 'just now'
  if (m < 90) return `${m} minute${m === 1 ? '' : 's'} ago`
  const h = Math.round(m / 60)
  if (h < 36) return `${h} hours ago`
  return `${Math.round(h / 24)} days ago`
}

/** What the map of a library on another system is doing while there is none
 *  to show, or while a new one is made. */
export function remoteProgress(p: MapProgress | null | undefined, host: string): string {
  const where = host || 'its system'
  if (p?.phase === 'counting') return `Counting the files with meaning vectors on ${where}.`
  if (p?.phase === 'fetching') {
    const n = p.files ?? 0, of = p.of ?? n
    return n < of ? `Fetching the vectors of a sample of ${n.toLocaleString()} of ${of.toLocaleString()} files from ${where}.`
      : `Fetching the vectors of ${of.toLocaleString()} files from ${where}.`
  }
  if (p?.phase === 'drawing') return `Drawing the map from ${(p.files ?? 0).toLocaleString()} files.`
  return `Making the map from the vectors on ${where}.`
}

/** The same map: a poll that finds it unchanged keeps the one on screen,
 *  with its open cluster and what was loaded of it. */
const sameMap = (a: MapOverview, b: MapOverview) => a.available && b.available && a.builtAt === b.builtAt && a.files === b.files

/** The library the semantic map starts on, without a choice kept: the one
 *  whose map has the most files, else one with vectors, else the main folder. */
export function defaultMapLibrary(maps: { id: string; available: boolean; files: number }[], libraries: Pick<PublicLibrary, 'id' | 'caps'>[]): string {
  const ids = new Set(libraries.map(l => l.id))
  const best = maps.filter(m => m.available && ids.has(m.id)).sort((a, b) => b.files - a.files)[0]
  return best?.id ?? libraries.find(l => l.caps?.vectors)?.id ?? 'kb'
}

export function SemanticMap({ onOpen, libraries = [] }: { onOpen: (path: string) => void; libraries?: PublicLibrary[] }) {
  const PALETTE = accentPalette()
  const several = libraries.length > 1
  // Without a choice kept, the largest map: asked for once.
  const [fallback, setFallback] = useState<string | null>(several ? null : 'kb')
  useEffect(() => {
    if (!several) return
    let alive = true
    fetch('/api/semantic-maps').then(r => r.json())
      .then((d: { maps?: { id: string; available: boolean; files: number }[] }) => { if (alive) setFallback(defaultMapLibrary(d.maps ?? [], libraries)) })
      .catch(() => { if (alive) setFallback('kb') })
    return () => { alive = false }
  }, [several]) // eslint-disable-line react-hooks/exhaustive-deps
  const [chosen, setLib] = useMapLibrary('semantic', libraries, fallback)
  const lib = several ? chosen : 'kb'
  const base = lib ? mainOrLibrary(lib, 'semantic-map') : null
  const [data, setData] = useState<MapOverview | null>(null)
  // What changes from one look to the next while a map stays the same.
  const [live, setLive] = useState<{ building: boolean; progress: MapProgress | null; error: string | null }>({ building: false, progress: null, error: null })
  const [tick, setTick] = useState(0)
  const [depthAsked, setDepthAsked] = useState(false)
  const [depthError, setDepthError] = useState('')
  const [mode, setMode] = useState<'2d' | '3d'>('2d')
  const [focus, setFocus] = useState<number | null>(null)
  const [pages, setPages] = useState<Map<number, MapPoint[]>>(new Map())
  const [full, setFull] = useState(false)
  const [spin, setSpin] = useState(false)
  const [links, setLinks] = useState(() => linksPref('2d'))
  const [highlight, setHighlight] = useState<number | null>(null)
  const loading = useRef(new Set<number>())
  const viewApi = useRef<{ zoom: (f: number) => void; reset: () => void } | null>(null)
  // The file selected on the map, and its nearest files once they come.
  const [selected, select] = useReducer(selectionReducer, null)
  const [selNear, setSelNear] = useState<{ p: string; list: MapNeighbor[] } | null>(null)

  useEffect(() => { setData(null); setLive({ building: false, progress: null, error: null }) }, [base])
  useEffect(() => {
    if (!base) return
    let alive = true, timer: ReturnType<typeof setTimeout> | undefined
    const load = () => fetch(base).then(r => r.json()).then((d: MapOverview) => {
      if (!alive) return
      setData(prev => (prev && sameMap(prev, d) ? prev : d))
      setLive({ building: !!d.building, progress: d.progress ?? null, error: d.error ?? null })
      // A map being made (a first one, or a new one over the one shown), or
      // one waiting for its embeddings, shows when ready.
      if (d.building || (!d.available && d.reason === 'none')) timer = setTimeout(load, d.building ? 5000 : 30_000)
    }).catch(() => { /* stays empty */ })
    void load()
    return () => { alive = false; if (timer) clearTimeout(timer) }
  }, [base, depthAsked, tick])
  // Another library starts with no depth asked for.
  useEffect(() => { setDepthAsked(false); setDepthError('') }, [lib])
  // A new map starts over: no cluster open, nothing extra loaded, no answers kept.
  useEffect(() => { setPages(new Map()); setFocus(null); neighborCache.clear(); select({ type: 'clear' }) }, [data])
  // A cluster opened on its own keeps a selection among its files only.
  useEffect(() => { if (focus !== null && selected && selected.c !== focus) select({ type: 'clear' }) }, [focus]) // eslint-disable-line react-hooks/exhaustive-deps
  // The selected file's nearest files, asked for at once.
  const selPath = selected?.p ?? null
  useEffect(() => {
    if (!selPath || !base) return
    let alive = true
    void nearestFiles(base, selPath).then(list => { if (alive) setSelNear({ p: selPath, list }) })
    return () => { alive = false }
  }, [selPath, base])
  useEffect(() => { setLinks(linksPref(mode)) }, [mode])

  const loadMore = useCallback((c: number, cap: number) => {
    const cl = data?.clusters?.find(x => x.c === c)
    const have = pages.get(c)?.length ?? 0
    if (!cl || have >= Math.min(cl.count, cap) || loading.current.has(c)) return
    loading.current.add(c)
    fetch(`${base}/cluster/${c}?offset=${have}&limit=${PAGE}`).then(r => r.json())
      .then((r: { points?: Omit<MapPoint, 'c'>[] }) => {
        setPages(m => {
          const next = new Map(m)
          next.set(c, [...(m.get(c) ?? []), ...(r.points ?? []).map(p => ({ ...p, c }))])
          return next
        })
      })
      .catch(() => { /* the sample stays */ })
      .finally(() => { loading.current.delete(c) })
  }, [data, pages, base])

  // A cluster opened on its own: its files, a page at a time.
  useEffect(() => { if (focus !== null) loadMore(focus, PAGE) }, [focus]) // eslint-disable-line react-hooks/exhaustive-deps

  const points = useMemo(() => {
    const sample = data?.points ?? []
    if (focus !== null) return pages.get(focus) ?? sample.filter(p => p.c === focus)
    if (!pages.size) return sample
    // A cluster's loaded pages start with its sample, so they stand in for it.
    return [...sample.filter(p => !pages.has(p.c)), ...[...pages.values()].flat()]
  }, [data, pages, focus])
  const labels = useMemo(() => shortLabels(data?.clusters ?? []), [data])

  const picker = several && lib && (
    <LibraryScope single libraries={libraries} value={[lib]} onChange={sc => { if (sc !== 'all' && sc[0]) setLib(sc[0]) }} hint="Semantic map of" />
  )
  const busy = (!data || (live.building && !(data.available && data.library?.kind === 'remote'))) && several && <Loader inline size={14} label={data && live.building ? 'Making the map…' : 'Loading…'} />
  const head = (
    <div className="map-head">
      <h3>Semantic map</h3>
      {!full && picker}
      {!full && busy}
    </div>
  )
  if (!data) {
    if (!several || !lib) return null
    return (
      <section className="card ov-list">
        {head}
        {full && <MapFrame key="frame" title="Semantic map" full onFull={setFull} controls={null} picker={picker} busy={busy}><div className="map-empty"><p className="muted">Loading the map.</p></div></MapFrame>}
      </section>
    )
  }
  const clusters = data.clusters ?? []
  const open = focus === null ? null : clusters.find(c => c.c === focus) ?? null
  const extraLoaded = [...pages.values()].reduce((s, p) => s + p.length, 0)
  // Zooming into the overview fetches more of the clusters in view (fewer in
  // 3D, so it turns smoothly); a cluster on its own fetches more as you zoom.
  const onDetail = (inView: number[], k: number) => {
    if (focus !== null) { if (k >= 1.8 || mode === '3d') loadMore(focus, mode === '3d' ? CLUSTER_CAP_3D : CLUSTER_CAP_2D); return }
    if (k < 2.2) return
    if (mode === '3d') {
      if (extraLoaded < CLUSTER_CAP_3D) for (const c of inView.slice(0, 2)) loadMore(c, DETAIL_PER_CLUSTER_3D)
      return
    }
    if (extraLoaded >= DETAIL_TOTAL) return
    for (const c of inView.slice(0, 4)) loadMore(c, DETAIL_PER_CLUSTER)
  }
  const label = libraries.find(l => l.id === lib)?.label ?? data.library?.label ?? 'the main folder'
  const Label = several ? label : 'The main folder'
  const owner = several ? label : 'the main folder'
  const remote = data.library?.kind === 'remote'
  const host = data.library?.host ?? data.remote?.host ?? ''
  // Files there whose vectors come from a model other than the map's.
  const otherModels = (data.remote?.models ?? []).filter(m => m.model !== data.model).reduce((n, m) => n + m.files, 0)
  // The Studio builds it: here (its depth applies at once) or on another
  // system (at its next Rebuild).
  const canSetDepth = data.library?.kind === 'main' || data.library?.kind === 'built' || (remote && !!data.library?.depth) || (!data.library && lib === 'kb')
  const setDepth = () => {
    if (!lib) return
    setDepthError('')
    api.setLibraryDepth(lib, 'meaning-documents').then(() => setDepthAsked(true)).catch(e => setDepthError(String((e as Error).message ?? e)))
  }
  // Made again now; for a library on another system, its vectors fetched again.
  const refresh = () => {
    if (!base) return
    setLive(l => ({ ...l, building: true, error: null }))
    fetch(`${base}/refresh`, { method: 'POST' }).then(r => r.json())
      .then((d: MapOverview & { error?: string }) => setLive({ building: !!d.building, progress: d.progress ?? null, error: d.error ?? null }))
      .catch(e => setLive({ building: false, progress: null, error: String((e as Error).message ?? e) }))
      .finally(() => setTick(t => t + 1))
  }
  const unavailable = !data.available && (
    <div className="map-empty">
      <p className="muted">
        {data.reason === 'remote-none' ? (data.of
          ? `${label} has only ${data.of} file${data.of === 1 ? '' : 's'} with meaning vectors on ${host || 'its system'}, too few for a map.`
          : `${label} has no meaning vectors on ${host || 'its system'}, so there is no map of it.${data.library?.depth ? ' Its last build there made none.' : ''}`)
          : data.reason === 'connected' ? 'This index was built elsewhere and has no meaning vectors the Studio can read.'
            : data.reason === 'off' && remote ? (depthAsked ? `${label} reads its documents for search by meaning at its next Rebuild, in Libraries; the map appears after that.`
              : `${Label} has no search by meaning. Set its depth to Meaning on documents to draw it here; it applies at its next Rebuild.`)
              : data.reason === 'off' ? `${Label} has no search by meaning. Set its depth to Meaning on documents to draw it here; that reads its documents once, in the background.`
                : (live.building || data.building) && remote ? remoteProgress(live.progress ?? data.progress, host)
                  : data.reason === 'failed' || live.error ? (remote ? `The vectors could not be fetched from ${host || 'its system'}: ${live.error ?? data.error ?? 'unknown error'}.`
                    : `The map could not be made: ${live.error ?? data.error ?? 'unknown error'}.`)
                    : data.building ? `The map is being made from the embeddings of ${owner}.`
                      : depthAsked ? `Reading the documents of ${owner} for search by meaning; the map appears when that is done.`
                        : `The map appears once ${owner} has embeddings for search by meaning.`}
      </p>
      {data.reason === 'off' && canSetDepth && !depthAsked && (
        <Button variant="secondary" size="sm" onClick={setDepth}>Set to Meaning on documents</Button>
      )}
      {(data.reason === 'failed' || live.error) && !live.building && <Button variant="secondary" size="sm" onClick={refresh}>Try again</Button>}
      {depthError && <p className="muted" role="alert">{depthError}</p>}
    </div>
  )
  // A library on another system: the files shown are of all those with
  // vectors there, and the sample fetched is said with them.
  const sampled = remote && (data.of ?? 0) > (data.files ?? 0)
  const fetched = remote
    ? ` (${sampled ? `a sample of ${(data.files ?? 0).toLocaleString()}` : 'all'} fetched from ${host || 'its system'}` +
      `${data.model ? `, made with ${data.model}` : ''}${data.remote?.fetchedAt ? `, ${ago(data.remote.fetchedAt)}` : ''})`
    : ''
  const status = open
    ? `${(pages.get(open.c)?.length ?? 0).toLocaleString()} of ${open.count.toLocaleString()} files in ${open.label}${(pages.get(open.c)?.length ?? 0) < open.count ? '; zoom in for more' : ''}.`
    : `${points.length.toLocaleString()} of ${((sampled ? data.of : data.files) ?? 0).toLocaleString()} files shown${fetched}; click a cluster for all of its files.`
  const remoteNotes = remote
    ? `${otherModels ? ` ${otherModels.toLocaleString()} file${otherModels === 1 ? '' : 's'} made with another model ${otherModels === 1 ? 'is' : 'are'} left out.` : ''}` +
      `${live.building ? ` ${remoteProgress(live.progress, host)}` : live.error ? ` The last refresh failed: ${live.error}.` : ''}`
    : ''
  // A file opens in the Library, in its own library where there are several;
  // a map filling the window closes first, so the Library is what shows.
  const openFile = (p: string) => openFromMap(p, full, setFull, several && lib ? (q => openInLibrary(onOpen, lib, q)) : onOpen)
  const nearList = selected && selNear?.p === selected.p ? selNear.list : null
  const card = selected && (
    <SelectionCard selected={selected} near={nearList} library={several ? label : null} palette={PALETTE}
      onNeighbor={nb => select({ type: 'neighbor', nb })} onOpen={() => openFile(selected.p)} onClear={() => select({ type: 'clear' })} />
  )
  const selName = selected ? splitPath(selected.p) : null
  const announce = selected && selName
    ? `Selected ${selName.name}${selName.folder ? ` in ${selName.folder}` : ''}.${nearList ? ` ${nearList.length} nearest file${nearList.length === 1 ? '' : 's'} shown.` : ''} Enter opens it in the Library.`
    : ''
  const canvas = (height: number | 'fill') => (
    <SemanticCanvas clusters={clusters} links={data.links ?? []} points={points} mode={mode} focus={open} height={height} palette={PALETTE} spin={spin}
      labels={labels} showLinks={links} highlight={highlight} selected={selected} selectedNear={nearList}
      onSelect={file => select(file ? { type: 'select', file } : { type: 'clear' })} onOpenFile={openFile}
      onFocus={setFocus} onDetail={onDetail} api={viewApi} mapBase={base ?? '/api/kb/semantic-map'}
      overlay={<>{card}<p className="sr-only" aria-live="polite">{announce}</p></>} />
  )
  const controls = (
    <>
      {open && <Button variant="secondary" size="sm" onClick={() => setFocus(null)}>Back to all clusters</Button>}
      <SegmentedControl label="View" options={[{ value: '2d', label: '2D' }, { value: '3d', label: '3D' }]} value={mode}
        onValueChange={v => { if (v) setMode(v as '2d' | '3d') }} />
      {mode === '3d' && !reducedMotion() && (
        <Button variant="ghost" size="sm" aria-pressed={spin} onClick={() => setSpin(s => !s)}>{spin ? 'Stop turning' : 'Turn slowly'}</Button>
      )}
      <Button variant="ghost" size="sm" aria-pressed={links} onClick={() => { keepLinksPref(mode, !links); setLinks(!links) }}>
        {links ? 'Hide links' : 'Show links'}
      </Button>
      <ZoomButtons onZoom={f => viewApi.current?.zoom(f)} onReset={() => viewApi.current?.reset()} />
    </>
  )
  const legend = (
    <Legend chipClass="map-cluster-chip" onHover={k => setHighlight(k === null ? null : Number(k))} items={clusters.map(cl => ({
      key: String(cl.c), name: labels.get(cl.c) ?? cl.label, color: PALETTE[cl.c % PALETTE.length], note: cl.count.toLocaleString(),
      title: cl.folder && cl.folder !== cl.label ? `${cl.label} · ${cl.folder}` : cl.label,
      ariaLabel: `${cl.label}, ${cl.count.toLocaleString()} files`, on: focus === cl.c,
      onClick: () => setFocus(focus === cl.c ? null : cl.c),
    }))} />
  )
  const statusLine = remote ? (
    <div className="map-remote">
      <p className="map-status muted">{status}{remoteNotes}{data.fallback ? ' This map was drawn from a sample, since numpy is not installed here.' : ''}</p>
      {live.building
        ? <Loader inline size={14} label="Refreshing…" />
        : <Button variant="ghost" size="sm" onClick={refresh}>Refresh</Button>}
    </div>
  ) : <p className="map-status muted">{status}{data.fallback ? ' This map was drawn from a sample, since numpy is not installed here.' : ''}</p>
  return (
    <section className="card ov-list">
      {head}
      <p className="muted">
        Files placed by what they say, from the embeddings search by meaning uses: files that read alike sit near each other
        whatever folder they are in. Only distance means anything. Clusters are named for the words that set them apart, and
        links join each to the two that read most alike; hovering a file draws lines to its five nearest. Click a cluster for
        all of its files. Click a file to select it. The map moves to it, keeps the lines to its five nearest files and lists
        them in a card with Open in Library; a double-click or Enter opens it too. Zoom and drag to move, or turn it in 3D.
      </p>
      {!full && (unavailable || statusLine)}
      {(full || !unavailable) && (
        <MapFrame key="frame" title="Semantic map" full={full} onFull={setFull} controls={unavailable ? null : controls} picker={picker} busy={busy}
          onEscape={() => { if (!selected) return false; select({ type: 'clear' }); return true }}>
          {unavailable || (full ? <>{statusLine}{canvas('fill')}{legend}</> : canvas(360))}
        </MapFrame>
      )}
      {!full && !unavailable && legend}
    </section>
  )
}

/** The selected file: its name, folder and library, its nearest files (each
 *  selects that file), Open in Library, and a button that clears it. Escape
 *  in it clears it too, and the map keeps the keyboard. */
function SelectionCard({ selected, near, library, palette, onNeighbor, onOpen, onClear }: {
  selected: MapSelection
  near: MapNeighbor[] | null
  library: string | null
  palette: string[]
  onNeighbor: (nb: MapNeighbor) => void
  onOpen: () => void
  onClear: () => void
}) {
  const { name, folder } = splitPath(selected.p)
  const ref = useRef<HTMLDivElement>(null)
  // Clearing from the card hands the keyboard back to the map.
  const clear = () => {
    ref.current?.closest('.map-canvas')?.querySelector('canvas')?.focus()
    onClear()
  }
  const onKey = (e: ReactKeyboardEvent) => {
    if (e.key !== 'Escape') return
    e.preventDefault()
    clear()
  }
  return (
    <div className="map-select" ref={ref} role="group" aria-label={`Selected file ${name}`} onKeyDown={onKey}>
      <div className="map-select-head">
        <div className="map-select-title">
          <strong title={selected.p}>{name}</strong>
          <span className="muted" title={folder}>{folder || 'Top folder'}{library ? ` · ${library}` : ''}</span>
        </div>
        <IconButton icon={<CloseIcon />} label="Clear selection" size="sm" onClick={clear} />
      </div>
      <p className="map-select-sub muted">Nearest by meaning</p>
      {near === null ? <div className="map-select-wait"><Loader inline size={12} label="Finding nearest files…" /></div>
        : near.length === 0 ? <p className="map-select-none muted">None found.</p>
          : (
            <ul className="map-select-list">
              {near.map(nb => (
                <li key={nb.p}>
                  <button type="button" className="map-select-nb" title={nb.p} onClick={() => onNeighbor(nb)}
                    aria-label={`${splitPath(nb.p).name}, similarity ${nb.sim.toFixed(2)}`}>
                    <span className="legend-dot" style={{ background: palette[(nb.c ?? selected.c ?? 0) % palette.length] }} />
                    <span className="map-select-name">{splitPath(nb.p).name}</span>
                    <span className="muted">{nb.sim.toFixed(2)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
      <Button variant="primary" size="sm" onClick={onOpen}>Open in Library</Button>
    </div>
  )
}

function SemanticCanvas({ clusters, links, points, mode, focus, height, palette, spin, labels, showLinks, highlight, selected, selectedNear, onSelect, onOpenFile, onFocus, onDetail, api, mapBase, overlay }: {
  /** The map's address, for its files' nearest files. */
  mapBase: string
  clusters: MapCluster[]
  links: MapLink[]
  points: MapPoint[]
  mode: '2d' | '3d'
  focus: MapCluster | null
  height: number | 'fill'
  palette: string[]
  spin: boolean
  labels: Map<number, string>
  showLinks: boolean
  /** A cluster hovered in the legend: it stays bright and the rest dims. */
  highlight: number | null
  /** The file selected, and its nearest files once they are known. */
  selected: MapSelection | null
  selectedNear: MapNeighbor[] | null
  /** A file clicked (or nothing, on an empty part of the map). */
  onSelect: (file: MapSelection | null) => void
  /** A file double-clicked, or Enter with one selected. */
  onOpenFile: (path: string) => void
  onFocus: (c: number) => void
  onDetail: (inView: number[], k: number) => void
  api: React.RefObject<{ zoom: (f: number) => void; reset: () => void } | null>
  /** Shown over the canvas: the selected file's card. */
  overlay?: ReactNode
}) {
  const cv = useRef<HTMLCanvasElement>(null)
  const { w, h } = useCanvasSize(cv, height)
  const [view, setView] = useState<View>(VIEW0)
  const [cam, setCam] = useState<Camera>(CAMERA0)
  const viewRef = useRef(view)
  viewRef.current = view
  const camRef = useRef(cam)
  camRef.current = cam
  const [hover, setHover] = useState<{ kind: 'point'; i: number } | { kind: 'cluster'; c: number } | null>(null)
  const [near, setNear] = useState<{ p: string; list: MapNeighbor[] } | null>(null)
  const PAD = 26
  // Screen positions from the last draw, for hit testing.
  // `rings` are the selected file's nearest files that are not dots on the map.
  const screen = useRef<{ xs: Float32Array; ys: Float32Array; order: Int32Array; cx: Map<number, { x: number; y: number; r: number }>; rings: NeighborLine[] }>(
    { xs: new Float32Array(0), ys: new Float32Array(0), order: new Int32Array(0), cx: new Map(), rings: [] })
  const pathIndex = useMemo(() => new Map(points.map((p, i) => [p.p, i])), [points])

  // A cluster on its own fills the view: the box of its points, with a margin.
  const frame = useMemo(() => {
    if (!focus || !points.length) return { x0: 0, x1: 1, y0: 0, y1: 1, z0: 0, z1: 1 }
    let x0 = 1, x1 = 0, y0 = 1, y1 = 0, z0 = 1, z1 = 0
    for (const p of points) { x0 = Math.min(x0, p.x); x1 = Math.max(x1, p.x); y0 = Math.min(y0, p.y); y1 = Math.max(y1, p.y); z0 = Math.min(z0, p.z); z1 = Math.max(z1, p.z) }
    const m = (a: number, b: number) => { const s = Math.max(b - a, 0.02) * 0.08; return [a - s, b + s] as const }
    const [ax, bx] = m(x0, x1), [ay, by] = m(y0, y1), [az, bz] = m(z0, z1)
    return { x0: ax, x1: bx, y0: ay, y1: by, z0: az, z1: bz }
  }, [focus, points.length > 0 ? points[0] : null]) // eslint-disable-line react-hooks/exhaustive-deps
  // A new map, a cluster opened or closed, or the other view starts afresh.
  useEffect(() => { viewRef.current = VIEW0; camRef.current = CAMERA0; setView(VIEW0); setCam(CAMERA0); setHover(null) }, [focus?.c, mode, clusters])
  // The view moves to a file selected: what was under the pointer is not
  // there any more, so its label goes until the pointer moves.
  useEffect(() => { setHover(null) }, [selected?.p])

  const base2d = (x: number, y: number) => ({
    x: PAD + ((x - frame.x0) / (frame.x1 - frame.x0)) * (w - 2 * PAD),
    y: PAD + (1 - (y - frame.y0) / (frame.y1 - frame.y0)) * (h - 2 * PAD),
  })
  const centred = (x: number, y: number, z: number): [number, number, number] => {
    const span = Math.max(frame.x1 - frame.x0, frame.y1 - frame.y0, frame.z1 - frame.z0)
    return [(x - (frame.x0 + frame.x1) / 2) / span, (y - (frame.y0 + frame.y1) / 2) / span, (z - (frame.z0 + frame.z1) / 2) / span]
  }
  // How far the points reach from the centre in 3D (the 95th percentile),
  // so the cloud fills the view whatever its spread.
  const reach = useMemo(() => {
    if (mode !== '3d' || !points.length) return 0.5
    const step = Math.max(1, Math.floor(points.length / 2000))
    const d: number[] = []
    for (let i = 0; i < points.length; i += step) { const [x, y, z] = centred(points[i].x, points[i].y, points[i].z); d.push(Math.hypot(x, y, z)) }
    d.sort((a, b) => a - b)
    return Math.max(0.05, d[Math.floor(d.length * 0.95)] ?? 0.5)
  }, [mode, frame, points.length > 0 ? points[0] : null]) // eslint-disable-line react-hooks/exhaustive-deps
  // Pixels per map unit in 3D at k = 1.
  const scale0 = (Math.min(w, h) * 0.42) / reach

  // Hovering a file, with links on: its nearest files by meaning, once the
  // pointer rests on it, from the map's own answers where it has them.
  const hoveredPath = hover?.kind === 'point' ? points[hover.i]?.p ?? null : null
  useEffect(() => {
    if (!showLinks || !hoveredPath) { setNear(null); return }
    let alive = true
    const t = setTimeout(() => { void nearestFiles(mapBase, hoveredPath).then(list => { if (alive) setNear({ p: hoveredPath, list }) }) }, 220)
    return () => { alive = false; clearTimeout(t) }
  }, [hoveredPath, showLinks, mapBase])

  // A file selected: the view moves to it and zooms in a little, at once
  // where motion is not welcome. Its turn and tilt in 3D stay as they are.
  const anim = useRef(0)
  useEffect(() => {
    if (!selected || selected.x === undefined || selected.y === undefined) return
    const sel = { x: selected.x, y: selected.y, z: selected.z ?? 0 }
    // The card is drawn with the selection: the file goes where it leaves room.
    const box = cv.current?.getBoundingClientRect()
    const cardBox = cv.current?.parentElement?.querySelector('.map-select')?.getBoundingClientRect()
    const at = freeCentre(w, h, box && cardBox ? { left: cardBox.left - box.left, bottom: cardBox.bottom - box.top, width: cardBox.width } : null)
    let step: (t: number) => void
    if (mode === '2d') {
      const a = viewRef.current, b = centreView(a, base2d(sel.x, sel.y), w, h, SELECT_ZOOM, at)
      step = t => { const v = tweenView(a, b, t); viewRef.current = v; setView(v) }
    } else {
      const a = camRef.current, b = centreCamera(a, centred(sel.x, sel.y, sel.z), SELECT_ZOOM, { x: at.x - w / 2, y: at.y - h / 2 }, scale0)
      step = t => {
        const c = tweenView(a, b, t)
        const next = { ...camRef.current, k: c.k, tx: c.tx, ty: c.ty, tz: c.tz }
        camRef.current = next
        setCam(next)
      }
    }
    if (reducedMotion() || typeof requestAnimationFrame !== 'function') { step(1); return }
    const t0 = performance.now()
    const tick = (now: number) => {
      const f = Math.min(1, (now - t0) / SELECT_MS)
      step(f)
      if (f < 1) anim.current = requestAnimationFrame(tick)
    }
    anim.current = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(anim.current)
  }, [selected?.p, mode, focus?.c, frame, w, h]) // eslint-disable-line react-hooks/exhaustive-deps

  const draw = useCallback(() => {
    const c = cv.current
    if (!c) return
    const t0 = performance.now()
    const camNow = camRef.current
    c.dataset.zoom = (mode === '2d' ? view.k : camNow.k).toFixed(2)
    c.dataset.mode = mode
    c.dataset.links = showLinks && !focus ? String(links.length) : '0'
    c.dataset.highlight = highlight === null ? '' : String(highlight)
    const g = context2d(c, w, h)
    const n = points.length
    const xs = new Float32Array(n), ys = new Float32Array(n), depth = new Float32Array(n), grow = new Float32Array(n)
    const cx = new Map<number, { x: number; y: number; r: number }>()
    const maxCount = Math.max(1, ...clusters.map(cl => cl.count))
    const to2d = (x: number, y: number) => { const b = base2d(x, y); return { x: b.x * view.k + view.tx, y: b.y * view.k + view.ty } }
    const to3d = (x: number, y: number, z: number) => project3d(centred(x, y, z), camNow, w / 2, h / 2, scale0)
    if (mode === '2d') {
      for (let i = 0; i < n; i++) { const s = to2d(points[i].x, points[i].y); xs[i] = s.x; ys[i] = s.y }
      if (!focus) for (const cl of clusters) {
        const s = to2d(cl.x, cl.y)
        cx.set(cl.c, { x: s.x, y: s.y, r: Math.max(12, Math.sqrt(cl.count / maxCount) * Math.min(w, h) * 0.17) * Math.sqrt(view.k) })
      }
    } else {
      for (let i = 0; i < n; i++) {
        const q = to3d(points[i].x, points[i].y, points[i].z)
        xs[i] = q.x; ys[i] = q.y; depth[i] = q.depth; grow[i] = q.f
      }
      if (!focus) for (const cl of clusters) {
        const q = to3d(cl.x, cl.y, cl.z)
        cx.set(cl.c, { x: q.x, y: q.y, r: 22 })
      }
    }
    // Far points first, so near ones are drawn over them.
    const order = Int32Array.from({ length: n }, (_, i) => i)
    if (mode === '3d') order.sort((a, b) => depth[b] - depth[a])
    // The selected file's nearest files, drawn whatever is hovered.
    const dotAt = (p: string) => { const i = pathIndex.get(p); return i === undefined ? null : { x: xs[i], y: ys[i] } }
    const placeAt = (x: number, y: number, z: number) => (mode === '2d' ? to2d(x, y) : to3d(x, y, z))
    const selAt = !selected ? null : dotAt(selected.p)
      ?? (selected.x !== undefined && selected.y !== undefined ? placeAt(selected.x, selected.y, selected.z ?? 0) : null)
    const selLines = selected && selectedNear ? neighborLines(selectedNear, dotAt, placeAt) : []
    c.dataset.selected = selected?.p ?? ''
    c.dataset.selectedLines = String(selAt ? selLines.length : 0)
    screen.current = { xs, ys, order, cx, rings: selAt ? selLines.filter(l => l.ring) : [] }
    if (!g) return
    const dark = isDarkTheme()
    const ink = dark ? '#cbd5e1' : '#475569'
    // The cluster in play: hovered on the map, or in the legend.
    const active = hover?.kind === 'cluster' ? hover.c : highlight
    // Clusters as regions sized by their file counts, under the files.
    if (mode === '2d' && !focus) {
      for (const cl of clusters) {
        const s = cx.get(cl.c)!
        g.beginPath()
        g.arc(s.x, s.y, s.r, 0, Math.PI * 2)
        g.fillStyle = palette[cl.c % palette.length]
        g.globalAlpha = active === cl.c ? 0.2 : active !== null ? 0.04 : 0.09
        g.fill()
        g.globalAlpha = active !== null && active !== cl.c ? 0.12 : 0.35
        g.strokeStyle = palette[cl.c % palette.length]
        g.stroke()
      }
      g.globalAlpha = 1
    }
    // Links between clusters near each other by meaning: a light skeleton
    // under the files, heavier for the nearer pairs; the cluster in play
    // shows its own in its color.
    if (showLinks && !focus && links.length) {
      let lo = Infinity, hi = -Infinity
      for (const l of links) { lo = Math.min(lo, l.sim); hi = Math.max(hi, l.sim) }
      for (const l of links) {
        const a = cx.get(l.a), b = cx.get(l.b)
        if (!a || !b) continue
        const s = hi > lo ? (l.sim - lo) / (hi - lo) : 1
        const mine = active !== null && (l.a === active || l.b === active)
        g.beginPath()
        g.moveTo(a.x, a.y)
        g.lineTo(b.x, b.y)
        g.strokeStyle = mine ? palette[active! % palette.length] : ink
        g.globalAlpha = mine ? 0.9 : active !== null ? 0.06 : 0.18 + 0.32 * s
        g.lineWidth = mine ? 1.8 + 1.4 * s : 0.8 + 1.6 * s
        g.stroke()
      }
      g.globalAlpha = 1
    }
    let fmin = Infinity, fmax = -Infinity
    if (mode === '3d') for (let i = 0; i < n; i++) { fmin = Math.min(fmin, grow[i]); fmax = Math.max(fmax, grow[i]) }
    const hovered = hover?.kind === 'point' ? hover.i : -1
    const dotR = mode === '2d' ? Math.min(6, 3.2 + Math.log2(view.k) * 0.6) : 3
    // Dots grow a little as the 3D view zooms in, up to twice their size.
    const zoomGrow = mode === '3d' ? Math.min(2, 1 + Math.log2(camNow.k) * 0.25) : 1
    for (let j = 0; j < n; j++) {
      const i = order[j]
      const x = xs[i], y = ys[i]
      if (x < -8 || y < -8 || x > w + 8 || y > h + 8) continue
      let r = dotR, a = hovered >= 0 && hovered !== i ? 0.45 : 0.9
      if (mode === '3d') {
        const t = fmax > fmin ? (grow[i] - fmin) / (fmax - fmin) : 1
        r = (1.6 + 3.2 * t) * zoomGrow
        a = (0.3 + 0.65 * t) * (hovered >= 0 && hovered !== i ? 0.6 : 1)
      }
      if (active !== null && points[i].c !== active && i !== hovered) a *= 0.2
      g.beginPath()
      g.arc(x, y, i === hovered ? r + 2.5 : r, 0, Math.PI * 2)
      g.fillStyle = palette[points[i].c % palette.length]
      g.globalAlpha = a
      g.fill()
    }
    g.globalAlpha = 1
    // A file's nearest files by meaning: a line to each, and a ring where
    // the file is not among the dots loaded.
    const drawLines = (from: { x: number; y: number }, lines: NeighborLine[], c0: number, width: number) => {
      for (const l of lines) {
        g.beginPath()
        g.moveTo(from.x, from.y)
        g.lineTo(l.x, l.y)
        g.strokeStyle = palette[c0 % palette.length]
        g.globalAlpha = 0.45 + 0.4 * Math.max(0, Math.min(1, l.sim))
        g.lineWidth = width
        g.stroke()
        if (l.ring) {
          g.beginPath()
          g.arc(l.x, l.y, 5, 0, Math.PI * 2)
          g.strokeStyle = palette[(l.c ?? c0) % palette.length]
          g.globalAlpha = 0.9
          g.lineWidth = 1.6
          g.stroke()
        }
      }
      g.globalAlpha = 1
    }
    if (selAt) {
      drawLines(selAt, selLines, selected!.c ?? 0, 2)
      // The selected file: a ring in the page's ink around its dot.
      g.beginPath()
      g.arc(selAt.x, selAt.y, (mode === '2d' ? dotR : 3 * zoomGrow) + 4, 0, Math.PI * 2)
      g.strokeStyle = dark ? '#fff' : '#0f172a'
      g.lineWidth = 2
      g.stroke()
    }
    // The hovered file's, as a preview, with links on.
    if (showLinks && hovered >= 0 && near && near.p === points[hovered]?.p && near.p !== selected?.p) {
      drawLines({ x: xs[hovered], y: ys[hovered] }, neighborLines(near.list, dotAt, placeAt), points[hovered].c, 1.4)
    }
    // Cluster names in screen space, largest first, none over another.
    if (!focus) {
      g.font = '600 12.5px system-ui, sans-serif'
      const drawn: { x0: number; y0: number; x1: number; y1: number }[] = []
      for (const cl of [...clusters].sort((a, b) => b.count - a.count)) {
        const s = cx.get(cl.c)
        const text = labels.get(cl.c) ?? cl.label
        if (!s || !text || s.x < 0 || s.y < 0 || s.x > w || s.y > h) continue
        if (active !== null && active !== cl.c) continue
        const wpx = g.measureText(text).width
        // Kept inside the canvas, so a cluster at the edge keeps its name.
        const x0 = Math.max(2, Math.min(w - wpx - 12, s.x - wpx / 2 - 5)), y0 = Math.max(2, Math.min(h - 19, s.y - 20))
        const box = { x0, y0, x1: x0 + wpx + 10, y1: y0 + 17 }
        if (drawn.some(d => box.x0 < d.x1 && box.x1 > d.x0 && box.y0 < d.y1 && box.y1 > d.y0)) continue
        drawn.push(box)
        g.fillStyle = dark ? 'rgba(15,23,42,0.85)' : 'rgba(255,255,255,0.88)'
        g.fillRect(box.x0, box.y0, wpx + 10, 17)
        g.fillStyle = dark ? palette[cl.c % palette.length] : shade(palette[cl.c % palette.length], 0.8)
        g.fillText(text, box.x0 + 5, box.y0 + 13)
      }
    }
    c.dataset.drawMs = (performance.now() - t0).toFixed(2)
  }, [points, clusters, links, mode, focus, frame, view, w, h, hover, palette, reach, showLinks, highlight, near, labels, pathIndex, selected, selectedNear]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { draw() }, [draw, cam])

  // A slow turn in 3D, when asked for and motion is welcome.
  useEffect(() => {
    if (mode !== '3d' || !spin || reducedMotion()) return
    let raf = 0, last = performance.now()
    const tick = (t: number) => {
      const dt = Math.min(64, t - last)
      last = t
      camRef.current = { ...camRef.current, yaw: camRef.current.yaw + dt * 0.00025 }
      draw()
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => { cancelAnimationFrame(raf); setCam(camRef.current) }
  }, [mode, spin, draw])

  // After the view settles, the clusters in it may bring more of their files.
  useEffect(() => {
    const t = setTimeout(() => {
      const inView = [...screen.current.cx.entries()]
        .filter(([, s]) => s.x > -s.r && s.y > -s.r && s.x < w + s.r && s.y < h + s.r)
        .map(([c]) => c)
        .sort((a, b) => {
          const sa = screen.current.cx.get(a)!, sb = screen.current.cx.get(b)!
          return Math.hypot(sa.x - w / 2, sa.y - h / 2) - Math.hypot(sb.x - w / 2, sb.y - h / 2)
        })
      onDetail(inView, mode === '2d' ? view.k : cam.k)
    }, 300)
    return () => clearTimeout(t)
  }, [view, cam, focus?.c, mode]) // eslint-disable-line react-hooks/exhaustive-deps

  const hitAt = (x: number, y: number): typeof hover => {
    const s = screen.current
    const i = nearestIndex(s.xs, s.ys, x, y, 10, s.order)
    if (i >= 0) return { kind: 'point', i }
    if (focus) return null
    let best: number | null = null, bd = Infinity
    for (const [c, p] of s.cx) {
      const d = Math.hypot(p.x - x, p.y - y)
      if (d <= p.r && d < bd) { bd = d; best = c }
    }
    return best === null ? null : { kind: 'cluster', c: best }
  }
  const zoom = useCallback((f: number, px = w / 2, py = h / 2) => {
    if (mode === '2d') setView(v => zoomAround(v, px, py, f, w, h))
    else setCam(cm => zoom3d(cm, px, py, f, w / 2, h / 2, scale0))
  }, [mode, w, h, scale0])
  const reset = useCallback(() => { setView(VIEW0); setCam(CAMERA0) }, [])
  api.current = { zoom: f => zoom(f), reset }
  const pan = (dx: number, dy: number) => {
    if (mode === '2d') setView(v => clampView({ ...v, tx: v.tx + dx, ty: v.ty + dy }, w, h))
    else setCam(cm => pan3d(cm, dx, dy, scale0))
  }
  useGestures(cv, {
    drag: (dx, dy, e) => {
      if (mode === '2d' || e.shiftKey || e.buttons === 2 || e.ctrlKey) pan(dx, dy)
      else setCam(cm => ({ ...cm, yaw: cm.yaw + dx * 0.008, pitch: Math.max(-1.45, Math.min(1.45, cm.pitch + dy * 0.008)) }))
    },
    hover: (x, y) => setHover(hitAt(x, y)),
    leave: () => setHover(null),
    // A file selects it, as does one of the selected file's nearest files
    // shown as a ring; a cluster opens on its own; an empty spot clears.
    click: (x, y) => {
      const hit = hitAt(x, y)
      const ring = hit?.kind === 'point' ? null : screen.current.rings.find(r => Math.hypot(r.x - x, r.y - y) <= 10)
      if (hit?.kind === 'point') { const p = points[hit.i]; onSelect({ p: p.p, x: p.x, y: p.y, z: p.z, c: p.c }) }
      else if (ring) { const nb = selectedNear?.find(n => n.p === ring.p); if (nb) onSelect({ p: nb.p, x: nb.x, y: nb.y, z: nb.z, c: nb.c }) }
      else if (hit?.kind === 'cluster') onFocus(hit.c)
      else onSelect(null)
    },
    wheel: (x, y, d) => zoom(Math.exp(-d * 0.0015), x, y),
    // A file opens; anywhere else zooms in.
    dbl: (x, y) => {
      const hit = hitAt(x, y)
      if (hit?.kind === 'point') onOpenFile(points[hit.i].p)
      else zoom(2, x, y)
    },
    key: e => {
      if (e.key === 'Enter' && selected) { onOpenFile(selected.p); return true }
      if (e.key === 'Escape' && selected) { onSelect(null); return true }
      if (mode === '3d' && e.key.startsWith('Arrow')) {
        const step = 0.12
        setCam(cm => e.key === 'ArrowLeft' ? { ...cm, yaw: cm.yaw - step } : e.key === 'ArrowRight' ? { ...cm, yaw: cm.yaw + step }
          : { ...cm, pitch: Math.max(-1.45, Math.min(1.45, cm.pitch + (e.key === 'ArrowUp' ? -step : step))) })
        return true
      }
      return viewKey(e, f => zoom(f), pan, reset)
    },
  })
  const hoverCluster = hover?.kind === 'cluster' ? clusters.find(x => x.c === hover.c) : null
  const tip = hover?.kind === 'point' ? points[hover.i]?.p
    : hoverCluster ? `${hoverCluster.label}${hoverCluster.folder && hoverCluster.folder !== hoverCluster.label ? ` (${hoverCluster.folder})` : ''} · ${hoverCluster.count.toLocaleString()} files · click for all of them`
      : null
  const nearNote = showLinks && hover?.kind === 'point' && near?.p === points[hover.i]?.p && near.list.length
    ? `lines to its ${near.list.length} nearest files by meaning` : null
  return (
    <div className={`map-canvas${height === 'fill' ? ' fill' : ''}`}>
      <canvas ref={cv} className="semantic-map" tabIndex={0}
        aria-label={`Semantic map, ${mode === '2d' ? '2D' : '3D'}: zoom with + and -, ${mode === '2d' ? 'move' : 'turn'} with the arrow keys, 0 resets`}
        style={{ width: '100%', height: height === 'fill' ? '100%' : height, display: 'block', cursor: hover ? 'pointer' : mode === '3d' ? 'grab' : 'default' }} />
      {tip && <div className="map-tip">{tip}{nearNote && <span className="map-tip-note"> · {nearNote}</span>}</div>}
      {overlay}
    </div>
  )
}
