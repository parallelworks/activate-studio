import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Button, IconButton, SegmentedControl } from '@parallelworks/ui'
import { CloseIcon, ExpandIcon, ResetIcon, ZoomInIcon, ZoomOutIcon } from '@parallelworks/ui/icons'

/**
 * Two views of the corpus from the index, on canvases that zoom, pan and
 * open full window: a squarified treemap of the directory topology (area is
 * bytes; zooming in shows the levels below), and the semantic map (files
 * placed by their embeddings, in clusters; a cluster opens to all of its
 * files, and the map turns in 3D).
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

interface TreeNode { name: string; path: string; bytes: number; files: number; children: Map<string, TreeNode> }

function buildTree(dirs: DirRow[]): TreeNode {
  const root: TreeNode = { name: '', path: '', bytes: 0, files: 0, children: new Map() }
  for (const d of dirs) {
    let node = root
    let acc = ''
    for (const part of d.dir.split('/')) {
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

interface Rect { x: number; y: number; w: number; h: number; node: TreeNode }

/** Squarified layout: rows of children packed to keep aspect ratios sane. */
function squarify(children: TreeNode[], x: number, y: number, w: number, h: number): Rect[] {
  const items = children.filter(c => c.bytes > 0).sort((a, b) => b.bytes - a.bytes)
  const total = items.reduce((s, c) => s + c.bytes, 0) || 1
  const rects: Rect[] = []
  let i = 0
  while (i < items.length) {
    const horizontal = w >= h
    const side = horizontal ? h : w
    let row: TreeNode[] = []
    let rowSum = 0
    let bestWorst = Infinity
    for (let j = i; j < items.length; j++) {
      const trySum = rowSum + items[j].bytes
      const rowArea = (trySum / total) * w * h
      const thickness = rowArea / side || 1
      let worst = 0
      for (const r of [...row, items[j]]) {
        const len = (r.bytes / trySum) * side || 1
        worst = Math.max(worst, Math.max(thickness / len, len / thickness))
      }
      if (worst > bestWorst && row.length) break
      row = [...row, items[j]]
      rowSum = trySum
      bestWorst = worst
    }
    const rowArea = (rowSum / total) * w * h
    const thickness = Math.min(rowArea / side || 1, horizontal ? w : h)
    let off = 0
    for (const r of row) {
      const len = (r.bytes / rowSum) * side || 1
      rects.push(horizontal
        ? { x, y: y + off, w: thickness, h: len, node: r }
        : { x: x + off, y, w: len, h: thickness, node: r })
      off += len
    }
    if (horizontal) { x += thickness; w -= thickness } else { y += thickness; h -= thickness }
    i += row.length
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

/** An orbit camera about the origin: yaw turns about the vertical axis,
 *  pitch tilts toward the viewer, dist is the camera's distance in units of
 *  the map's own size, and pan moves the picture on screen. */
export interface Camera { yaw: number; pitch: number; dist: number; panX: number; panY: number }
export const CAMERA0: Camera = { yaw: 0.6, pitch: 0.35, dist: 2.6, panX: 0, panY: 0 }

/** Where a point (centred on the origin, about a unit across) lands on
 *  screen: rotated by yaw then pitch, then divided by its depth, so the
 *  centre plane keeps `scale` pixels per unit and nearer points grow.
 *  `f` is that growth (1 on the centre plane), `depth` the distance from
 *  the camera. */
export function project3d(p: readonly [number, number, number], cam: Camera, cx: number, cy: number, scale: number): { x: number; y: number; depth: number; f: number } {
  const [x, y, z] = p
  const cyaw = Math.cos(cam.yaw), syaw = Math.sin(cam.yaw)
  const x1 = x * cyaw + z * syaw
  const z1 = -x * syaw + z * cyaw
  const cp = Math.cos(cam.pitch), sp = Math.sin(cam.pitch)
  const y2 = y * cp - z1 * sp
  const z2 = y * sp + z1 * cp
  const depth = cam.dist + z2
  const f = cam.dist / Math.max(0.05, depth)
  return { x: cx + cam.panX + x1 * f * scale, y: cy + cam.panY - y2 * f * scale, depth, f }
}

const reducedMotion = () => { try { return matchMedia('(prefers-reduced-motion: reduce)').matches } catch { return false } }

// ---- the frame both maps sit in ----

/** A map's canvas, inline or filling the window, with its controls. */
function MapFrame({ title, full, onFull, controls, children }: {
  title: string
  full: boolean
  onFull: (full: boolean) => void
  controls: ReactNode
  children: ReactNode
}) {
  const panel = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!full) return
    const before = document.activeElement as HTMLElement | null
    panel.current?.focus()
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.preventDefault(); onFull(false) } }
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
    <div className="map-full" role="dialog" aria-modal="true" aria-label={title} tabIndex={-1} ref={panel}>
      <div className="map-full-head"><h2>{title}</h2>{bar}</div>
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

export function TopologyMap() {
  const PALETTE = accentPalette()
  const [dirs, setDirs] = useState<DirRow[] | null>(null)
  const [focus, setFocus] = useState('')
  const [full, setFull] = useState(false)
  useEffect(() => {
    fetch('/api/kb/topology').then(r => r.json())
      .then(d => { if (d.available) setDirs(d.dirs) })
      .catch(() => { /* stays empty */ })
  }, [])
  const tree = useMemo(() => (dirs ? buildTree(dirs) : null), [dirs])
  const viewApi = useRef<{ zoom: (f: number) => void; reset: () => void } | null>(null)
  const paletteKey = PALETTE.join()
  const colorOf = useCallback((n: TreeNode) => {
    const topKeys = tree ? [...tree.children.keys()] : []
    return PALETTE[Math.max(0, topKeys.indexOf(n.path.split('/')[0])) % PALETTE.length]
  }, [tree, paletteKey]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!tree) return null
  let node = tree
  if (focus) for (const part of focus.split('/')) { const c = node.children.get(part); if (!c) break; node = c }
  const topDirs = [...tree.children.values()].sort((a, b) => b.bytes - a.bytes)
  const topColor = (p: string) => colorOf({ path: p } as TreeNode)
  const map = (height: number | 'fill') => (
    <TopologyCanvas node={node} height={height} colorOf={colorOf} onDrill={setFocus} api={viewApi} />
  )
  const crumbs = (
    <p className="crumb-row">
      <button className="crumb" onClick={() => setFocus('')}>corpus</button>
      {focus.split('/').filter(Boolean).map((part, i, arr) => (
        <button key={i} className="crumb" onClick={() => setFocus(arr.slice(0, i + 1).join('/'))}>/{part}</button>
      ))}
    </p>
  )
  const legend = (
    <p className="map-legend">
      {topDirs.map(d => (
        <button key={d.path} className="legend-chip" onClick={() => setFocus(d.path)}>
          <span className="legend-dot" style={{ background: topColor(d.path) }} />
          {d.name} <span className="muted">{fmtB(d.bytes)}</span>
        </button>
      ))}
    </p>
  )
  const controls = <ZoomButtons onZoom={f => viewApi.current?.zoom(f)} onReset={() => viewApi.current?.reset()} />
  return (
    <section className="card ov-list">
      <h3>Corpus topology</h3>
      <p className="muted">
        Area is bytes on disk, one hue per top-level directory. Zoom (wheel, pinch or +) to see the levels below; drag to
        move. Click a directory to open it on its own; the crumbs back out.
      </p>
      {crumbs}
      <MapFrame title="Corpus topology" full={full} onFull={setFull} controls={controls}>
        {full ? <>{crumbs}{map('fill')}{legend}</> : map(320)}
      </MapFrame>
      {!full && legend}
    </section>
  )
}

function TopologyCanvas({ node, height, colorOf, onDrill, api }: {
  node: TreeNode
  height: number | 'fill'
  colorOf: (n: TreeNode) => string
  onDrill: (path: string) => void
  api: React.RefObject<{ zoom: (f: number) => void; reset: () => void } | null>
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
    const g = context2d(c, w, h)
    if (!g) return
    const sx = (x: number) => x * view.k + view.tx, sy = (y: number) => y * view.k + view.ty
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
      g.fillStyle = cl.color
      g.fillRect(x, y, cw, ch)
      g.strokeStyle = 'rgba(255,255,255,0.55)'
      g.lineWidth = 1
      g.strokeRect(x + 0.5, y + 0.5, cw - 1, ch - 1)
    }
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
      if (vw < 44 || vh < 13 || vy + 12 < 0) continue
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
  }, [cells, view, w, h, hover])

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

export interface MapCluster { c: number; label: string; count: number; x: number; y: number; z: number }
export interface MapPoint { p: string; x: number; y: number; z: number; c: number }
export interface MapOverview {
  available: boolean
  reason?: 'off' | 'none' | 'failed'
  building?: boolean
  error?: string | null
  files?: number
  shown?: number
  clusters?: MapCluster[]
  points?: MapPoint[]
  fallback?: boolean
}

/** Points a cluster loads as you zoom into the overview, per cluster and in
 *  all, and in a cluster on its own; 3D keeps fewer so it turns smoothly. */
const DETAIL_PER_CLUSTER = 4000
const DETAIL_TOTAL = 20000
const CLUSTER_CAP_2D = 20000
const CLUSTER_CAP_3D = 6000
const PAGE = 2000

/** A cluster's name as drawn on the map: the last folder of at most two
 *  parts; the legend carries the whole name. */
const shortLabel = (l: string) => {
  const parts = l.split(' + ').map(part => part.split('/').filter(Boolean).slice(-1)[0] ?? part)
  const head = parts.slice(0, 2).join(' + ') + (parts.length > 2 ? ' +…' : '')
  return head.length > 30 ? head.slice(0, 29) + '…' : head
}

export function SemanticMap({ onOpen }: { onOpen: (path: string) => void }) {
  const PALETTE = accentPalette()
  const [data, setData] = useState<MapOverview | null>(null)
  const [mode, setMode] = useState<'2d' | '3d'>('2d')
  const [focus, setFocus] = useState<number | null>(null)
  const [pages, setPages] = useState<Map<number, MapPoint[]>>(new Map())
  const [full, setFull] = useState(false)
  const [spin, setSpin] = useState(false)
  const loading = useRef(new Set<number>())
  const viewApi = useRef<{ zoom: (f: number) => void; reset: () => void } | null>(null)

  useEffect(() => {
    let alive = true, timer: ReturnType<typeof setTimeout> | undefined
    const load = () => fetch('/api/kb/semantic-map').then(r => r.json()).then((d: MapOverview) => {
      if (!alive) return
      setData(d)
      // A map being made shows when it is ready.
      if (!d.available && d.building) timer = setTimeout(load, 8000)
    }).catch(() => { /* stays empty */ })
    void load()
    return () => { alive = false; if (timer) clearTimeout(timer) }
  }, [])
  // A new map starts over: no cluster open, nothing extra loaded.
  useEffect(() => { setPages(new Map()); setFocus(null) }, [data])

  const loadMore = useCallback((c: number, cap: number) => {
    const cl = data?.clusters?.find(x => x.c === c)
    const have = pages.get(c)?.length ?? 0
    if (!cl || have >= Math.min(cl.count, cap) || loading.current.has(c)) return
    loading.current.add(c)
    fetch(`/api/kb/semantic-map/cluster/${c}?offset=${have}&limit=${PAGE}`).then(r => r.json())
      .then((r: { points?: Omit<MapPoint, 'c'>[] }) => {
        setPages(m => {
          const next = new Map(m)
          next.set(c, [...(m.get(c) ?? []), ...(r.points ?? []).map(p => ({ ...p, c }))])
          return next
        })
      })
      .catch(() => { /* the sample stays */ })
      .finally(() => { loading.current.delete(c) })
  }, [data, pages])

  // A cluster opened on its own: its files, a page at a time.
  useEffect(() => { if (focus !== null) loadMore(focus, PAGE) }, [focus]) // eslint-disable-line react-hooks/exhaustive-deps

  const points = useMemo(() => {
    const sample = data?.points ?? []
    if (focus !== null) return pages.get(focus) ?? sample.filter(p => p.c === focus)
    if (!pages.size) return sample
    // A cluster's loaded pages start with its sample, so they stand in for it.
    return [...sample.filter(p => !pages.has(p.c)), ...[...pages.values()].flat()]
  }, [data, pages, focus])

  if (!data) return null
  const clusters = data.clusters ?? []
  const open = focus === null ? null : clusters.find(c => c.c === focus) ?? null
  const extraLoaded = [...pages.values()].reduce((s, p) => s + p.length, 0)
  // Zooming into the overview fetches more of the clusters in view; a cluster
  // on its own fetches more as you zoom into it.
  const onDetail = (inView: number[], k: number) => {
    if (mode === '3d' && focus === null) return
    if (focus !== null) { if (k >= 1.8 || mode === '3d') loadMore(focus, mode === '3d' ? CLUSTER_CAP_3D : CLUSTER_CAP_2D); return }
    if (k < 2.2 || extraLoaded >= DETAIL_TOTAL) return
    for (const c of inView.slice(0, 4)) loadMore(c, DETAIL_PER_CLUSTER)
  }
  const unavailable = !data.available && (
    <p className="muted">
      {data.reason === 'off' ? <>The main folder&apos;s index depth stops before search by meaning, so there is no map. Set it to Meaning on documents or Meaning on all files in <a href="#view=settings:libraries">Settings, Libraries</a>.</>
        : data.reason === 'failed' ? `The map could not be made: ${data.error ?? 'unknown error'}.`
          : data.building ? 'The map is being made from the main folder\'s embeddings.'
            : 'The map appears once the main folder has embeddings for search by meaning.'}
    </p>
  )
  const status = open
    ? `${(pages.get(open.c)?.length ?? 0).toLocaleString()} of ${open.count.toLocaleString()} files in ${open.label}${(pages.get(open.c)?.length ?? 0) < open.count ? '; zoom in for more' : ''}.`
    : `${points.length.toLocaleString()} of ${(data.files ?? 0).toLocaleString()} files shown; click a cluster for all of its files.`
  const canvas = (height: number | 'fill') => (
    <SemanticCanvas clusters={clusters} points={points} mode={mode} focus={open} height={height} palette={PALETTE} spin={spin}
      onOpen={onOpen} onFocus={setFocus} onDetail={onDetail} api={viewApi} />
  )
  const controls = (
    <>
      {open && <Button variant="secondary" size="sm" onClick={() => setFocus(null)}>Back to all clusters</Button>}
      <SegmentedControl label="View" options={[{ value: '2d', label: '2D' }, { value: '3d', label: '3D' }]} value={mode}
        onValueChange={v => { if (v) setMode(v as '2d' | '3d') }} />
      {mode === '3d' && !reducedMotion() && (
        <Button variant="ghost" size="sm" aria-pressed={spin} onClick={() => setSpin(s => !s)}>{spin ? 'Stop turning' : 'Turn slowly'}</Button>
      )}
      <ZoomButtons onZoom={f => viewApi.current?.zoom(f)} onReset={() => viewApi.current?.reset()} />
    </>
  )
  const legend = (
    <p className="map-legend">
      {clusters.map(cl => (
        <button key={cl.c} className={`legend-chip map-cluster-chip${focus === cl.c ? ' on' : ''}`} title={cl.label}
          aria-label={`${cl.label}, ${cl.count.toLocaleString()} files`} onClick={() => setFocus(focus === cl.c ? null : cl.c)}>
          <span className="legend-dot" style={{ background: PALETTE[cl.c % PALETTE.length] }} />
          <span className="legend-name">{shortLabel(cl.label)}</span> <span className="muted">{cl.count.toLocaleString()}</span>
        </button>
      ))}
    </p>
  )
  return (
    <section className="card ov-list">
      <h3>Semantic map</h3>
      <p className="muted">
        Files placed by what they say, from the embeddings search by meaning uses: files that read alike sit near each other
        whatever folder they are in. Only distance means anything. Click a cluster for all of its files, hover for a file,
        click to open it; zoom and drag to move, or turn it in 3D.
      </p>
      {unavailable || (
        <>
          <p className="map-status muted">{status}{data.fallback ? ' This map was drawn from a sample, since numpy is not installed here.' : ''}</p>
          <MapFrame title="Semantic map" full={full} onFull={setFull} controls={controls}>
            {full ? <><p className="map-status muted">{status}</p>{canvas('fill')}{legend}</> : canvas(360)}
          </MapFrame>
          {!full && legend}
        </>
      )}
    </section>
  )
}

function SemanticCanvas({ clusters, points, mode, focus, height, palette, spin, onOpen, onFocus, onDetail, api }: {
  clusters: MapCluster[]
  points: MapPoint[]
  mode: '2d' | '3d'
  focus: MapCluster | null
  height: number | 'fill'
  palette: string[]
  spin: boolean
  onOpen: (path: string) => void
  onFocus: (c: number) => void
  onDetail: (inView: number[], k: number) => void
  api: React.RefObject<{ zoom: (f: number) => void; reset: () => void } | null>
}) {
  const cv = useRef<HTMLCanvasElement>(null)
  const { w, h } = useCanvasSize(cv, height)
  const [view, setView] = useState<View>(VIEW0)
  const [cam, setCam] = useState<Camera>(CAMERA0)
  const [hover, setHover] = useState<{ kind: 'point'; i: number } | { kind: 'cluster'; c: number } | null>(null)
  const PAD = 26
  // Screen positions from the last draw, for hit testing.
  const screen = useRef<{ xs: Float32Array; ys: Float32Array; order: Int32Array; cx: Map<number, { x: number; y: number; r: number }> }>(
    { xs: new Float32Array(0), ys: new Float32Array(0), order: new Int32Array(0), cx: new Map() })

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
  useEffect(() => { setView(VIEW0); setCam(CAMERA0); setHover(null) }, [focus?.c, mode, clusters])

  const base2d = (x: number, y: number) => ({
    x: PAD + ((x - frame.x0) / (frame.x1 - frame.x0)) * (w - 2 * PAD),
    y: PAD + (1 - (y - frame.y0) / (frame.y1 - frame.y0)) * (h - 2 * PAD),
  })
  const centred = (x: number, y: number, z: number): [number, number, number] => {
    const span = Math.max(frame.x1 - frame.x0, frame.y1 - frame.y0, frame.z1 - frame.z0)
    return [(x - (frame.x0 + frame.x1) / 2) / span, (y - (frame.y0 + frame.y1) / 2) / span, (z - (frame.z0 + frame.z1) / 2) / span]
  }
  const camRef = useRef(cam)
  camRef.current = cam
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

  const draw = useCallback(() => {
    const c = cv.current
    if (!c) return
    c.dataset.zoom = mode === '2d' ? view.k.toFixed(2) : (CAMERA0.dist / camRef.current.dist).toFixed(2)
    c.dataset.mode = mode
    const g = context2d(c, w, h)
    const n = points.length
    const xs = new Float32Array(n), ys = new Float32Array(n), depth = new Float32Array(n), grow = new Float32Array(n)
    const cx = new Map<number, { x: number; y: number; r: number }>()
    const maxCount = Math.max(1, ...clusters.map(cl => cl.count))
    const camNow = camRef.current
    const scale = (Math.min(w, h) * 0.42) / reach
    if (mode === '2d') {
      for (let i = 0; i < n; i++) { const b = base2d(points[i].x, points[i].y); xs[i] = b.x * view.k + view.tx; ys[i] = b.y * view.k + view.ty }
      if (!focus) for (const cl of clusters) {
        const b = base2d(cl.x, cl.y)
        cx.set(cl.c, { x: b.x * view.k + view.tx, y: b.y * view.k + view.ty, r: Math.max(12, Math.sqrt(cl.count / maxCount) * Math.min(w, h) * 0.17) * Math.sqrt(view.k) })
      }
    } else {
      for (let i = 0; i < n; i++) {
        const q = project3d(centred(points[i].x, points[i].y, points[i].z), camNow, w / 2, h / 2, scale)
        xs[i] = q.x; ys[i] = q.y; depth[i] = q.depth; grow[i] = q.f
      }
      if (!focus) for (const cl of clusters) {
        const q = project3d(centred(cl.x, cl.y, cl.z), camNow, w / 2, h / 2, scale)
        cx.set(cl.c, { x: q.x, y: q.y, r: 22 })
      }
    }
    // Far points first, so near ones are drawn over them.
    const order = Int32Array.from({ length: n }, (_, i) => i)
    if (mode === '3d') order.sort((a, b) => depth[b] - depth[a])
    screen.current = { xs, ys, order, cx }
    if (!g) return
    const dark = isDarkTheme()
    // Clusters as regions sized by their file counts, under the files.
    if (mode === '2d' && !focus) {
      for (const cl of clusters) {
        const s = cx.get(cl.c)!
        g.beginPath()
        g.arc(s.x, s.y, s.r, 0, Math.PI * 2)
        g.fillStyle = palette[cl.c % palette.length]
        g.globalAlpha = hover?.kind === 'cluster' && hover.c === cl.c ? 0.2 : 0.09
        g.fill()
        g.globalAlpha = 0.35
        g.strokeStyle = palette[cl.c % palette.length]
        g.stroke()
      }
      g.globalAlpha = 1
    }
    let fmin = Infinity, fmax = -Infinity
    if (mode === '3d') for (let i = 0; i < n; i++) { fmin = Math.min(fmin, grow[i]); fmax = Math.max(fmax, grow[i]) }
    const hovered = hover?.kind === 'point' ? hover.i : -1
    const dotR = mode === '2d' ? Math.min(6, 3.2 + Math.log2(view.k) * 0.6) : 3
    for (let j = 0; j < n; j++) {
      const i = order[j]
      const x = xs[i], y = ys[i]
      if (x < -8 || y < -8 || x > w + 8 || y > h + 8) continue
      let r = dotR, a = hovered >= 0 && hovered !== i ? 0.45 : 0.9
      if (mode === '3d') {
        const t = fmax > fmin ? (grow[i] - fmin) / (fmax - fmin) : 1
        r = 1.6 + 3.2 * t
        a = (0.3 + 0.65 * t) * (hovered >= 0 && hovered !== i ? 0.6 : 1)
      }
      g.beginPath()
      g.arc(x, y, i === hovered ? r + 2.5 : r, 0, Math.PI * 2)
      g.fillStyle = palette[points[i].c % palette.length]
      g.globalAlpha = a
      g.fill()
    }
    g.globalAlpha = 1
    // Cluster names in screen space, largest first, none over another.
    if (!focus) {
      g.font = '600 12.5px system-ui, sans-serif'
      const drawn: { x0: number; y0: number; x1: number; y1: number }[] = []
      for (const cl of [...clusters].sort((a, b) => b.count - a.count)) {
        const s = cx.get(cl.c)
        if (!s || !cl.label || s.x < 0 || s.y < 0 || s.x > w || s.y > h) continue
        const text = shortLabel(cl.label)
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
  }, [points, clusters, mode, focus, frame, view, w, h, hover, palette, reach]) // eslint-disable-line react-hooks/exhaustive-deps

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
      onDetail(inView, mode === '2d' ? view.k : CAMERA0.dist / cam.dist)
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
    else setCam(cm => ({ ...cm, dist: Math.min(8, Math.max(1.05, cm.dist / f)) }))
  }, [mode, w, h])
  const reset = useCallback(() => { setView(VIEW0); setCam(CAMERA0) }, [])
  api.current = { zoom: f => zoom(f), reset }
  const pan = (dx: number, dy: number) => {
    if (mode === '2d') setView(v => clampView({ ...v, tx: v.tx + dx, ty: v.ty + dy }, w, h))
    else setCam(cm => ({ ...cm, panX: cm.panX + dx, panY: cm.panY + dy }))
  }
  useGestures(cv, {
    drag: (dx, dy, e) => {
      if (mode === '2d' || e.shiftKey || e.buttons === 2 || e.ctrlKey) pan(dx, dy)
      else setCam(cm => ({ ...cm, yaw: cm.yaw + dx * 0.008, pitch: Math.max(-1.45, Math.min(1.45, cm.pitch + dy * 0.008)) }))
    },
    hover: (x, y) => setHover(hitAt(x, y)),
    leave: () => setHover(null),
    click: (x, y) => {
      const hit = hitAt(x, y)
      if (hit?.kind === 'point') onOpen(points[hit.i].p)
      else if (hit?.kind === 'cluster') onFocus(hit.c)
    },
    wheel: (x, y, d) => zoom(Math.exp(-d * 0.0015), x, y),
    dbl: (x, y) => zoom(2, x, y),
    key: e => {
      if (mode === '3d' && e.key.startsWith('Arrow')) {
        const step = 0.12
        setCam(cm => e.key === 'ArrowLeft' ? { ...cm, yaw: cm.yaw - step } : e.key === 'ArrowRight' ? { ...cm, yaw: cm.yaw + step }
          : { ...cm, pitch: Math.max(-1.45, Math.min(1.45, cm.pitch + (e.key === 'ArrowUp' ? -step : step))) })
        return true
      }
      return viewKey(e, f => zoom(f), pan, reset)
    },
  })
  const tip = hover?.kind === 'point' ? points[hover.i]?.p
    : hover?.kind === 'cluster' ? (() => { const cl = clusters.find(x => x.c === hover.c); return cl ? `${cl.label} · ${cl.count.toLocaleString()} files · click for all of them` : null })()
      : null
  return (
    <div className={`map-canvas${height === 'fill' ? ' fill' : ''}`}>
      <canvas ref={cv} className="semantic-map" tabIndex={0}
        aria-label={`Semantic map, ${mode === '2d' ? '2D' : '3D'}: zoom with + and -, ${mode === '2d' ? 'move' : 'turn'} with the arrow keys, 0 resets`}
        style={{ width: '100%', height: height === 'fill' ? '100%' : height, display: 'block', cursor: hover ? 'pointer' : mode === '3d' ? 'grab' : 'default' }} />
      {tip && <div className="map-tip">{tip}</div>}
    </div>
  )
}
