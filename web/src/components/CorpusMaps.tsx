import { useEffect, useMemo, useRef, useState } from 'react'

/**
 * Two views of the corpus from the index: a squarified treemap of the
 * directory topology (area = bytes) and a semantic map (per-file embedding
 * vectors projected to 2D, colored by cluster). Both are prototypes that
 * render from one fetch each and open files or directories on click.
 */

const PALETTE = [
  '#2563eb', '#d97706', '#059669', '#dc2626', '#7c3aed',
  '#0891b2', '#be185d', '#65a30d', '#9333ea', '#ea580c',
  '#0d9488', '#b91c1c', '#4f46e5', '#a16207',
]

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
  const W = 960, H = 320
  const rects = squarify(kids, 0, 0, W, H)
  const topColor = (p: string) => PALETTE[[...tree.children.keys()].indexOf(p.split('/')[0]) % PALETTE.length]
  return (
    <section className="card ov-list">
      <h3>Corpus topology</h3>
      <p className="muted">Area is bytes on disk. Click a directory to zoom; click the crumb to back out.</p>
      <p className="crumb-row">
        <button className="crumb" onClick={() => setFocus('')}>corpus</button>
        {focus.split('/').filter(Boolean).map((part, i, arr) => (
          <button key={i} className="crumb" onClick={() => setFocus(arr.slice(0, i + 1).join('/'))}>/{part}</button>
        ))}
      </p>
      <div className="treemap" style={{ position: 'relative', width: '100%', aspectRatio: `${W}/${H}` }}>
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
            }}
            onClick={() => { if (r.node.children.size) setFocus(r.node.path) }}
          >
            {(r.w / W) * 100 > 8 && (r.h / H) * 100 > 8 && (
              <span className="treemap-label">{r.node.name}<br />{fmtB(r.node.bytes)}</span>
            )}
          </div>
        ))}
      </div>
    </section>
  )
}

interface MapFile { path: string; x: number; y: number; c: number }

export function SemanticMap({ onOpen }: { onOpen: (path: string) => void }) {
  const [files, setFiles] = useState<MapFile[] | null>(null)
  const [hover, setHover] = useState<MapFile | null>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  useEffect(() => {
    fetch('/api/kb/semantic-map').then(r => r.json())
      .then(d => { if (d.available) setFiles(d.files) })
      .catch(() => { /* stays empty */ })
  }, [])
  useEffect(() => {
    const cv = canvasRef.current
    if (!cv || !files) return
    const W = cv.clientWidth, H = 340
    cv.width = W * devicePixelRatio
    cv.height = H * devicePixelRatio
    cv.style.height = `${H}px`
    const g = cv.getContext('2d')!
    g.scale(devicePixelRatio, devicePixelRatio)
    g.clearRect(0, 0, W, H)
    const pad = 18
    for (const f of files) {
      const x = pad + f.x * (W - 2 * pad)
      const y = pad + (1 - f.y) * (H - 2 * pad)
      g.beginPath()
      g.arc(x, y, hover === f ? 7 : 4.5, 0, Math.PI * 2)
      g.fillStyle = PALETTE[f.c % PALETTE.length]
      g.globalAlpha = hover && hover !== f ? 0.45 : 0.9
      g.fill()
      g.globalAlpha = 1
    }
  }, [files, hover])
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
    </section>
  )
}
