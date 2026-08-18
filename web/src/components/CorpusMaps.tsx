import { useEffect, useMemo, useRef, useState } from 'react'

/**
 * Two views of the corpus from the index: a squarified treemap of the
 * directory topology (area = bytes) and a semantic map (per-file embedding
 * vectors projected to 2D, colored by cluster). Both are prototypes that
 * render from one fetch each and open files or directories on click.
 */

/** Categorical colors derived from the deployment's accent: analogous
 *  hues around it with alternating lightness, so the maps read as part of
 *  the theme instead of a foreign rainbow. */
function hexToHsl(hex: string): { h: number; s: number; l: number } {
  const m = hex.replace('#', '')
  const r = parseInt(m.slice(0, 2), 16) / 255, g = parseInt(m.slice(2, 4), 16) / 255, b = parseInt(m.slice(4, 6), 16) / 255
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

function accentPalette(): string[] {
  let accent = '#2563eb'
  try {
    const css = getComputedStyle(document.documentElement)
    accent = (css.getPropertyValue('--theme-accent') || css.getPropertyValue('--pw-link') || accent).trim() || accent
  } catch { /* server-side or headless */ }
  const { h, s } = hexToHsl(accent.startsWith('#') ? accent : '#2563eb')
  const sat = Math.min(68, Math.max(42, s))
  const out: string[] = []
  for (let i = 0; i < 14; i++) {
    const hue = (h + ((i % 7) - 3) * 19 + 360) % 360
    const light = i % 2 ? 62 : 44
    out.push(`hsl(${Math.round(hue)} ${Math.round(sat)}% ${light}%)`)
  }
  return out
}
const PALETTE = accentPalette()

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

export function TopologyMap() {
  const [dirs, setDirs] = useState<DirRow[] | null>(null)
  const [focus, setFocus] = useState('')
  useEffect(() => {
    fetch('/api/kb/topology').then(r => r.json())
      .then(d => { if (d.available) setDirs(d.dirs) })
      .catch(() => { /* stays empty */ })
  }, [])
  const tree = useMemo(() => (dirs ? buildTree(dirs) : null), [dirs])
  if (!tree) return null
  let node = tree
  if (focus) for (const part of focus.split('/')) { const c = node.children.get(part); if (!c) break; node = c }
  const kids = [...node.children.values()]
  const W = 600, H = 320
  const rects = squarify(kids, 0, 0, W, H)
  const topDirs = [...tree.children.values()].sort((a, b) => b.bytes - a.bytes)
  const topColor = (p: string) => PALETTE[[...tree.children.keys()].indexOf(p.split('/')[0]) % PALETTE.length]
  return (
    <section className="card ov-list">
      <h3>Corpus topology</h3>
      <p className="muted">Area is bytes on disk, one hue per top-level directory, shade separating siblings. Click a directory to zoom; the crumbs back out.</p>
      <p className="crumb-row">
        <button className="crumb" onClick={() => setFocus('')}>corpus</button>
        {focus.split('/').filter(Boolean).map((part, i, arr) => (
          <button key={i} className="crumb" onClick={() => setFocus(arr.slice(0, i + 1).join('/'))}>/{part}</button>
        ))}
      </p>
      <div className="treemap" style={{ position: 'relative', width: '100%', height: 320 }}>
        {rects.map((r, i) => (
          <div
            key={i}
            className="treemap-cell"
            title={`${r.node.path}\n${r.node.files} files, ${fmtB(r.node.bytes)}`}
            style={{
              position: 'absolute',
              left: `${(r.x / W) * 100}%`, top: `${(r.y / H) * 100}%`,
              width: `${(r.w / W) * 100}%`, height: `${(r.h / H) * 100}%`,
              background: topColor(r.node.path),
              filter: `brightness(${1.18 - (i % 4) * 0.11})`,
            }}
            onClick={() => { if (r.node.children.size) setFocus(r.node.path) }}
          >
            {(r.w / W) * 100 > 7 && (r.h / H) * 100 > 7 && (
              <span className="treemap-label">{r.node.name}<br />{r.node.files} files · {fmtB(r.node.bytes)}</span>
            )}
          </div>
        ))}
      </div>
      <p className="map-legend">
        {topDirs.map(d => (
          <button key={d.path} className="legend-chip" onClick={() => setFocus(d.path)}>
            <span className="legend-dot" style={{ background: topColor(d.path) }} />
            {d.name} <span className="muted">{fmtB(d.bytes)}</span>
          </button>
        ))}
      </p>
    </section>
  )
}

interface MapFile { path: string; x: number; y: number; c: number }

/** Andrew's monotone chain convex hull. */
function hull(pts: { x: number; y: number }[]): { x: number; y: number }[] {
  if (pts.length < 3) return pts
  const p = [...pts].sort((a, b) => a.x - b.x || a.y - b.y)
  const cross = (o: { x: number; y: number }, a: { x: number; y: number }, b: { x: number; y: number }) =>
    (a.x - o.x) * (b.y - o.y) - (a.y - o.y) * (b.x - o.x)
  const lower: typeof p = []
  for (const pt of p) {
    while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], pt) <= 0) lower.pop()
    lower.push(pt)
  }
  const upper: typeof p = []
  for (const pt of [...p].reverse()) {
    while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], pt) <= 0) upper.pop()
    upper.push(pt)
  }
  return [...lower.slice(0, -1), ...upper.slice(0, -1)]
}

export function SemanticMap({ onOpen }: { onOpen: (path: string) => void }) {
  const [files, setFiles] = useState<MapFile[] | null>(null)
  const [labels, setLabels] = useState<string[]>([])
  const [hover, setHover] = useState<MapFile | null>(null)
  const [resizeTick, setResizeTick] = useState(0)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  useEffect(() => {
    const cv = canvasRef.current
    if (!cv) return
    const ro = new ResizeObserver(() => setResizeTick(t => t + 1))
    ro.observe(cv)
    return () => ro.disconnect()
  }, [files])
  useEffect(() => {
    fetch('/api/kb/semantic-map').then(r => r.json())
      .then(d => { if (d.available) { setFiles(d.files); setLabels(d.clusterLabels ?? []) } })
      .catch(() => { /* stays empty */ })
  }, [])
  useEffect(() => {
    const cv = canvasRef.current
    if (!cv || !files) return
    const W = cv.clientWidth || 600, H = 320
    cv.width = W * devicePixelRatio
    cv.height = H * devicePixelRatio
    cv.style.height = `${H}px`
    const g = cv.getContext('2d')!
    g.scale(devicePixelRatio, devicePixelRatio)
    g.clearRect(0, 0, W, H)
    const pad = 26
    const px = (f: { x: number; y: number }) => ({ x: pad + f.x * (W - 2 * pad), y: pad + (1 - f.y) * (H - 2 * pad) })
    const k = Math.max(...files.map(f => f.c)) + 1
    // Soft hulls first so points and labels sit above them.
    for (let c = 0; c < k; c++) {
      const members = files.filter(f => f.c === c).map(px)
      if (members.length < 3) continue
      const hl = hull(members)
      g.beginPath()
      hl.forEach((pt, i) => (i ? g.lineTo(pt.x, pt.y) : g.moveTo(pt.x, pt.y)))
      g.closePath()
      g.fillStyle = PALETTE[c % PALETTE.length]
      g.globalAlpha = 0.08
      g.fill()
      g.globalAlpha = 0.35
      g.strokeStyle = PALETTE[c % PALETTE.length]
      g.stroke()
      g.globalAlpha = 1
    }
    for (const f of files) {
      const { x, y } = px(f)
      g.beginPath()
      g.arc(x, y, hover === f ? 7 : 4.5, 0, Math.PI * 2)
      g.fillStyle = PALETTE[f.c % PALETTE.length]
      g.globalAlpha = hover && hover !== f ? 0.4 : 0.92
      g.fill()
      g.globalAlpha = 1
    }
    // Cluster names at centroids; individual files stay hover-only.
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark'
    g.font = '600 12.5px system-ui, sans-serif'
    for (let c = 0; c < k; c++) {
      const members = files.filter(f => f.c === c)
      if (!members.length || !labels[c]) continue
      const cx = members.reduce((s, f) => s + px(f).x, 0) / members.length
      const cy = members.reduce((s, f) => s + px(f).y, 0) / members.length
      const short = (l: string) => l.split(' + ').map(part => part.split('/').slice(-2).join('/')).join(' + ')
      const text = short(labels[c])
      const wpx = g.measureText(text).width
      g.fillStyle = isDark ? 'rgba(15,23,42,0.85)' : 'rgba(255,255,255,0.85)'
      g.fillRect(cx - wpx / 2 - 5, cy - 20, wpx + 10, 17)
      g.fillStyle = PALETTE[c % PALETTE.length]
      g.fillText(text, cx - wpx / 2, cy - 7)
    }
  }, [files, hover, labels, resizeTick])
  if (!files) return null
  const locate = (e: React.MouseEvent) => {
    const cv = canvasRef.current!
    const rect = cv.getBoundingClientRect()
    const W = rect.width, H = rect.height, pad = 18
    const mx = e.clientX - rect.left, my = e.clientY - rect.top
    let best: MapFile | null = null, bd = 12 * 12
    for (const f of files) {
      const x = pad + f.x * (W - 2 * pad)
      const y = pad + (1 - f.y) * (H - 2 * pad)
      const d = (x - mx) ** 2 + (y - my) ** 2
      if (d < bd) { bd = d; best = f }
    }
    return best
  }
  return (
    <section className="card ov-list">
      <h3>Semantic map</h3>
      <p className="muted">Every embedded document, placed by content similarity and colored by cluster. Neighbors read alike even when they live in different folders. Hover for the file; click to open it.</p>
      <div style={{ position: 'relative' }}>
        <canvas
          ref={canvasRef}
          className="semantic-map"
          style={{ width: '100%', display: 'block', cursor: hover ? 'pointer' : 'default' }}
          onMouseMove={e => setHover(locate(e))}
          onMouseLeave={() => setHover(null)}
          onClick={e => { const f = locate(e); if (f) onOpen(f.path) }}
        />
        {hover && <div className="map-tip">{hover.path}</div>}
      </div>
      <p className="map-legend">
        {labels.map((l, c) => l && (
          <span key={c} className="legend-chip">
            <span className="legend-dot" style={{ background: PALETTE[c % PALETTE.length] }} />
            {l}
          </span>
        ))}
      </p>
    </section>
  )
}
