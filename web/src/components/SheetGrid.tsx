import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Button } from '@parallelworks/ui'
import { useEffectiveTheme } from '../theme'

/**
 * A sheet as a spreadsheet draws it, read only: column letters and row
 * numbers that stay in view, the file's column widths, row heights,
 * frozen rows and columns, merged cells and cell styles, each value as
 * the file formats it. Only the rows and columns in view are drawn, so a
 * sheet of thousands of rows scrolls as a short one does.
 *
 * A cell selected shows its full value, and its formula, in the bar above
 * the grid; a range shows its count and sum, and copies as tab separated
 * text. Text longer than its column ends in an ellipsis, never silently:
 * selecting the cell, or Wrap, shows all of it.
 */

/** A cell style: only what differs from a plain cell (server: GridStyle). */
export interface GridStyle {
  b?: boolean; i?: boolean; u?: boolean; s?: boolean
  color?: string; fill?: string
  h?: 'left' | 'center' | 'right' | 'justify'; v?: 'top' | 'center'
  wrap?: boolean; size?: number; indent?: number
}

/** [column, shown text, style, formula, raw value, kind]; kind is n
 *  (number), d (date or time), b (true or false), e (error) or text. */
export type GridCell = [number, string, number?, string?, string?, string?]

/** One sheet, as GET /api/kb/sheet gives it or a delimited file makes it. */
export interface GridSheetData {
  name: string
  rows: number
  cols: number
  totalRows: number
  totalCols: number
  colWidths: number[]
  rowHeights?: Record<string, number>
  frozenRows?: number
  frozenCols?: number
  merges?: [number, number, number, number][]
  styles?: GridStyle[]
  cells: { r: number; c: GridCell[] }[]
  charts?: number
  pictures?: number
}

const HEAD_H = 24
const ROW_H = 22
const DEF_W = 72
const LINE_H = 17
const OVERSCAN_ROWS = 12
const OVERSCAN_COLS = 4
/** The most cells a copy takes. */
const COPY_MAX = 200_000

/** A column's letters: 0 is A, 26 is AA. */
export function colName(c: number): string {
  let s = ''
  for (let n = c + 1; n > 0; n = Math.floor((n - 1) / 26)) s = String.fromCharCode(65 + ((n - 1) % 26)) + s
  return s
}

/** The first index in sorted `a` whose value is above `v`, less one. */
function lastAtOrBelow(a: number[], v: number): number {
  let lo = 0, hi = a.length - 1
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1
    if (a[mid] <= v) lo = mid
    else hi = mid - 1
  }
  return lo
}

function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16)
  const ch = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map(v => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * ch[0] + 0.7152 * ch[1] + 0.0722 * ch[2]
}
const contrast = (a: number, b: number) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
function mix(hex: string, base: string, amount: number): string {
  const p = (h: string) => { const n = parseInt(h.slice(1), 16); return [(n >> 16) & 255, (n >> 8) & 255, n & 255] }
  const a = p(hex), b = p(base)
  return '#' + a.map((v, i) => Math.round(v * amount + b[i] * (1 - amount)).toString(16).padStart(2, '0')).join('')
}

/** The page behind the grid, light and dark (styles.css --theme-panel-bg). */
const PANEL = { light: '#ffffff', dark: '#282824' }

/**
 * A cell style's colours in the theme the page is painted in: in dark mode
 * a fill is laid at half strength over the page, and a text colour that
 * would not read against what is behind it gives way to one that does.
 */
function styleColors(st: GridStyle | undefined, theme: 'light' | 'dark'): { background?: string; color?: string } {
  if (!st || (!st.fill && !st.color)) return {}
  const page = PANEL[theme]
  const bg = st.fill ? (theme === 'dark' ? mix(st.fill, page, 0.45) : st.fill) : undefined
  const behind = luminance(bg ?? page)
  let color = st.color
  if (color && contrast(luminance(color), behind) < 2.6) color = undefined
  if (!color && bg) color = contrast(1, behind) >= contrast(0, behind) ? '#ffffff' : '#111111'
  return { background: bg, color }
}

interface Pos { r: number; c: number }

/** The sheet's values by row, then column. */
function cellIndex(data: GridSheetData): Map<number, Map<number, GridCell>> {
  const m = new Map<number, Map<number, GridCell>>()
  for (const row of data.cells) {
    const cols = new Map<number, GridCell>()
    for (const c of row.c) cols.set(c[0], c)
    m.set(row.r, cols)
  }
  return m
}

function numberOf(cell: GridCell | undefined): number | null {
  if (!cell) return null
  const kind = cell[5]
  if (kind !== 'n' && kind !== 'd') {
    if (kind) return null
    const t = cell[1].trim().replace(/[,$€£%\s]/g, '')
    if (!/^[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/.test(t)) return null
    const n = Number(t)
    return Number.isFinite(n) ? n : null
  }
  const n = Number(cell[4] || cell[1])
  return Number.isFinite(n) ? n : null
}

export function SheetGrid({ data, tabs, tools, note, sortable, label }: {
  data: GridSheetData
  /** The workbook's sheet tabs, drawn under the grid. */
  tabs?: ReactNode
  /** More buttons for the bar above the grid (Print view). */
  tools?: ReactNode
  /** A line under the grid: the sheet was cut, it has charts. */
  note?: ReactNode
  /** Rows can be sorted by a column (a delimited file, under its header). */
  sortable?: boolean
  label: string
}) {
  const theme = useEffectiveTheme()
  const cells = useMemo(() => cellIndex(data), [data])
  const styles = data.styles ?? []
  const FR = Math.min(data.frozenRows ?? 0, data.rows)
  const FC = Math.min(data.frozenCols ?? 0, data.cols)
  const [wrap, setWrap] = useState(false)
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<{ col: number; dir: 1 | -1 } | null>(null)
  const [sel, setSel] = useState<{ a: Pos; f: Pos }>({ a: { r: 0, c: 0 }, f: { r: 0, c: 0 } })
  const scroller = useRef<HTMLDivElement>(null)
  const [view, setView] = useState({ top: 0, left: 0, w: 800, h: 600 })

  // Column widths: the file's, or for a delimited file the width its
  // values ask for, within limits.
  const widths = useMemo(() => Array.from({ length: data.cols }, (_, c) => data.colWidths[c] ?? DEF_W), [data])
  const colX = useMemo(() => {
    const x = [0]
    for (const w of widths) x.push(x[x.length - 1] + w)
    return x
  }, [widths])

  // The rows shown under the frozen ones: all of them, or those holding
  // the filter's text, in the file's order or sorted by a column.
  const body = useMemo(() => {
    let rows = Array.from({ length: Math.max(0, data.rows - FR) }, (_, i) => i + FR)
    const q = filter.trim().toLowerCase()
    if (q) rows = rows.filter(r => { const m = cells.get(r); return !!m && [...m.values()].some(c => c[1].toLowerCase().includes(q)) })
    if (sort) {
      const { col, dir } = sort
      const key = (r: number) => cells.get(r)?.get(col)
      const numeric = rows.slice(0, 200).every(r => { const c = key(r); return !c || numberOf(c) !== null })
      rows = [...rows].sort((a, b) => {
        const x = key(a), y = key(b)
        if (!x || !y) return !x ? (!y ? 0 : 1) : -1
        if (numeric) return ((numberOf(x) ?? 0) - (numberOf(y) ?? 0)) * dir
        return x[1].localeCompare(y[1], undefined, { numeric: true, sensitivity: 'base' }) * dir
      })
    }
    return rows
  }, [data.rows, FR, filter, sort, cells])
  const rearranged = !!filter.trim() || !!sort

  // Row heights: the file's, or with Wrap on, as tall as the longest
  // value's lines in its column (estimated, up to ten lines).
  const heightOf = useCallback((r: number): number => {
    const set = data.rowHeights?.[r]
    if (!wrap) return set ?? ROW_H
    if (set === 0) return 0
    let lines = 1
    const m = cells.get(r)
    if (m) for (const c of m.values()) {
      const w = widths[c[0]] ?? DEF_W
      lines = Math.max(lines, ...c[1].split('\n').map(l => Math.ceil((l.length * 7 + 8) / Math.max(w, 1))))
    }
    return Math.max(set ?? ROW_H, Math.min(lines, 10) * LINE_H + 5)
  }, [data.rowHeights, wrap, cells, widths])
  const frozenY = useMemo(() => {
    const y = [0]
    for (let r = 0; r < FR; r++) y.push(y[r] + heightOf(r))
    return y
  }, [FR, heightOf])
  const bodyY = useMemo(() => {
    const y = [0]
    for (const r of body) y.push(y[y.length - 1] + heightOf(r))
    return y
  }, [body, heightOf])
  const frozenH = frozenY[FR]
  const frozenW = colX[FC]
  const totalW = colX[data.cols]
  const digits = String(Math.max(data.rows, 1)).length
  const RN = Math.max(36, digits * 8 + 16)

  // Where a row stands in the order shown: frozen rows first.
  const shownRows = useMemo(() => [...Array.from({ length: FR }, (_, i) => i), ...body], [FR, body])
  const place = useMemo(() => {
    const m = new Map<number, number>()
    shownRows.forEach((r, i) => m.set(r, i))
    return m
  }, [shownRows])

  // Merged cells, by their top left cell, and the cells they cover; not
  // drawn merged while rows are filtered or sorted.
  const merges = useMemo(() => {
    const anchors = new Map<string, [number, number, number, number]>()
    const covered = new Set<string>()
    // Merges in the frozen rows stay: those rows are neither filtered nor sorted.
    for (const m of data.merges ?? []) {
      if (rearranged && m[2] >= FR) continue
      anchors.set(`${m[0]}:${m[1]}`, m)
      for (let r = m[0]; r <= m[2]; r++) for (let c = m[1]; c <= m[3]; c++) if (r !== m[0] || c !== m[1]) covered.add(`${r}:${c}`)
    }
    return { anchors, covered }
  }, [data.merges, rearranged, FR])

  // What is in view, kept as the grid scrolls and resizes.
  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    let frame = 0
    const read = () => { frame = 0; setView({ top: el.scrollTop, left: el.scrollLeft, w: el.clientWidth, h: el.clientHeight }) }
    const onScroll = () => { if (!frame) frame = requestAnimationFrame(read) }
    read()
    el.addEventListener('scroll', onScroll, { passive: true })
    const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(onScroll) : null
    ro?.observe(el)
    return () => { el.removeEventListener('scroll', onScroll); ro?.disconnect(); if (frame) cancelAnimationFrame(frame) }
  }, [])
  // Another sheet starts at its top left.
  useEffect(() => {
    setSel({ a: { r: 0, c: 0 }, f: { r: 0, c: 0 } })
    setFilter(''); setSort(null)
    scroller.current?.scrollTo?.({ top: 0, left: 0 })
  }, [data])

  const visRows = useMemo(() => {
    if (!body.length) return [] as number[]
    const from = Math.max(0, lastAtOrBelow(bodyY, view.top) - OVERSCAN_ROWS)
    const to = Math.min(body.length - 1, lastAtOrBelow(bodyY, view.top + view.h) + OVERSCAN_ROWS)
    const out: number[] = []
    for (let i = from; i <= to; i++) out.push(i)
    return out
  }, [body, bodyY, view.top, view.h])
  const visCols = useMemo(() => {
    const out: number[] = []
    if (FC >= data.cols) return out
    const from = Math.max(FC, lastAtOrBelow(colX, frozenW + view.left) - OVERSCAN_COLS)
    const to = Math.min(data.cols - 1, lastAtOrBelow(colX, frozenW + view.left + view.w) + OVERSCAN_COLS)
    for (let c = from; c <= to; c++) out.push(c)
    return out
  }, [colX, FC, frozenW, data.cols, view.left, view.w])

  // The selection, as positions in the order shown.
  const range = useMemo(() => {
    const pa = place.get(sel.a.r) ?? 0, pf = place.get(sel.f.r) ?? 0
    return { p1: Math.min(pa, pf), p2: Math.max(pa, pf), c1: Math.min(sel.a.c, sel.f.c), c2: Math.max(sel.a.c, sel.f.c) }
  }, [sel, place])
  // A selection the filter hides moves to the first row still shown.
  useEffect(() => {
    if (place.has(sel.a.r) && place.has(sel.f.r)) return
    const r = shownRows[Math.min(FR, shownRows.length - 1)]
    if (r !== undefined) setSel({ a: { r, c: sel.a.c }, f: { r, c: sel.a.c } })
  }, [place]) // eslint-disable-line react-hooks/exhaustive-deps
  const inRange = (r: number, c: number) => {
    const p = place.get(r)
    return p !== undefined && p >= range.p1 && p <= range.p2 && c >= range.c1 && c <= range.c2
  }

  const spanOf = (r: number, c: number) => {
    const m = merges.anchors.get(`${r}:${c}`)
    if (!m) return { w: widths[c], h: heightOf(r) }
    let h = 0
    for (let rr = m[0]; rr <= Math.min(m[2], data.rows - 1); rr++) h += heightOf(rr)
    return { w: colX[Math.min(m[3], data.cols - 1) + 1] - colX[c], h }
  }

  const drawCell = (r: number, c: number, x: number, y: number, key?: string) => {
    if (merges.covered.has(`${r}:${c}`)) return null
    const { w, h } = spanOf(r, c)
    if (w <= 0 || h <= 0) return null
    const cell = cells.get(r)?.get(c)
    const st = cell?.[2] !== undefined && cell[2] >= 0 ? styles[cell[2]] : undefined
    const kind = cell?.[5]
    const align = st?.h ?? (kind === 'n' || kind === 'd' ? 'right' : kind === 'b' || kind === 'e' ? 'center' : 'left')
    const wraps = wrap || !!st?.wrap
    const active = sel.f.r === r && sel.f.c === c
    const text = cell?.[1] ?? ''
    const cls = 'sg-cell' + (inRange(r, c) ? ' sel' : '') + (active ? ' act' : '') + (wraps ? ' wrap' : '') +
      (st?.v === 'top' ? ' top' : st?.v === 'center' ? ' mid' : '')
    return (
      <div key={key ?? `${r}:${c}`} className={cls} data-r={r} data-c={c}
        title={!wraps && text.length > Math.max(8, w / 7) ? text : undefined}
        style={{
          left: x, top: y, width: w, height: h, textAlign: align,
          ...styleColors(st, theme),
          fontWeight: st?.b ? 600 : undefined, fontStyle: st?.i ? 'italic' : undefined,
          textDecoration: st?.u ? 'underline' : st?.s ? 'line-through' : undefined,
          fontSize: st?.size ? `${Math.round(Math.min(st.size, 28) * 12 / 11)}px` : undefined,
          paddingLeft: st?.indent ? 4 + st.indent * 9 : undefined,
        }}>
        <span className="sg-t">{text}</span>
      </div>
    )
  }

  // Merges whose top left cell is out of view but which reach into it.
  const reaching = (rowsShown: Set<number>, colsShown: Set<number>, rowOk: (r: number) => boolean, colOk: (c: number) => boolean) => {
    const out: [number, number][] = []
    for (const m of merges.anchors.values()) {
      if (!rowOk(m[0]) || !colOk(m[1])) continue
      if (rowsShown.has(m[0]) && colsShown.has(m[1])) continue
      let rowHit = false, colHit = false
      for (let r = m[0]; r <= m[2] && !rowHit; r++) rowHit = rowsShown.has(r)
      for (let c = m[1]; c <= m[3] && !colHit; c++) colHit = colsShown.has(c)
      if (rowHit && colHit) out.push([m[0], m[1]])
    }
    return out
  }

  const bodyRowsShown = visRows.map(i => body[i])
  const rowSet = new Set(bodyRowsShown)
  const colSet = new Set(visCols)
  const frozenRowSet = new Set(Array.from({ length: FR }, (_, i) => i))
  const frozenColSet = new Set(Array.from({ length: FC }, (_, i) => i))
  const yOfBody = (r: number) => bodyY[body.indexOf(r)] ?? 0

  // Selecting with the mouse: a cell, a column letter, a row number or
  // the corner; dragging or Shift widens it.
  const dragging = useRef(false)
  // Where the button went down: a drag widens the selection only once the
  // pointer has moved, not when the grid scrolls or shifts under it.
  const downAt = useRef({ x: 0, y: 0 })
  type Hit = { all: true } | { col: number } | { row: number } | { r: number; c: number }
  const target = (e: { target: EventTarget | null }): Hit | null => {
    const el = (e.target as HTMLElement | null)?.closest?.('[data-c],[data-col],[data-row],[data-all]') as HTMLElement | null
    if (!el) return null
    if (el.dataset.all !== undefined) return { all: true }
    if (el.dataset.col !== undefined) return { col: Number(el.dataset.col) }
    if (el.dataset.row !== undefined) return { row: Number(el.dataset.row) }
    return { r: Number(el.dataset.r), c: Number(el.dataset.c) }
  }
  const lastRow = shownRows[shownRows.length - 1] ?? 0
  const selectAt = (t: Hit, extend: boolean) => {
    const first = shownRows[0] ?? 0
    if ('all' in t) { setSel({ a: { r: first, c: 0 }, f: { r: lastRow, c: data.cols - 1 } }); return }
    if ('col' in t) {
      const col = t.col
      setSel(s => ({ a: { r: first, c: extend ? s.a.c : col }, f: { r: lastRow, c: col } }))
      return
    }
    if ('row' in t) {
      const row = t.row
      setSel(s => ({ a: { r: extend ? s.a.r : row, c: 0 }, f: { r: row, c: data.cols - 1 } }))
      return
    }
    const at = { r: t.r, c: t.c }
    setSel(s => extend ? { a: s.a, f: at } : { a: at, f: at })
  }
  const onMouseDown = (e: React.MouseEvent) => {
    if (e.button !== 0) return
    const t = target(e)
    if (!t) return
    scroller.current?.focus({ preventScroll: true })
    selectAt(t, e.shiftKey)
    dragging.current = true
    downAt.current = { x: e.clientX, y: e.clientY }
    const up = () => { dragging.current = false; window.removeEventListener('mouseup', up) }
    window.addEventListener('mouseup', up)
  }
  const onMouseOver = (e: React.MouseEvent) => {
    if (!dragging.current) return
    if (Math.abs(e.clientX - downAt.current.x) + Math.abs(e.clientY - downAt.current.y) < 3) return
    const t = target(e)
    if (t && 'r' in t) setSel(s => ({ a: s.a, f: { r: t.r, c: t.c } }))
  }

  // The selected values as tab separated text, as a spreadsheet copies.
  const rangeText = useCallback(() => {
    const lines: string[] = []
    const n = (range.p2 - range.p1 + 1) * (range.c2 - range.c1 + 1)
    if (n > COPY_MAX) return null
    for (let p = range.p1; p <= range.p2; p++) {
      const m = cells.get(shownRows[p])
      const out: string[] = []
      for (let c = range.c1; c <= range.c2; c++) {
        const t = m?.get(c)?.[1] ?? ''
        out.push(/[\t\n"]/.test(t) ? `"${t.replace(/"/g, '""')}"` : t)
      }
      lines.push(out.join('\t'))
    }
    return lines.join('\n')
  }, [range, cells, shownRows])
  const [copied, setCopied] = useState('')
  const onCopy = (e: React.ClipboardEvent) => {
    const t = rangeText()
    if (t === null) { setCopied(`Too many cells to copy at once (over ${COPY_MAX.toLocaleString()}).`); e.preventDefault(); return }
    e.clipboardData.setData('text/plain', t)
    e.preventDefault()
    setCopied('Copied')
  }
  const copyButton = async () => {
    const t = rangeText()
    if (t === null) { setCopied(`Too many cells to copy at once (over ${COPY_MAX.toLocaleString()}).`); return }
    try { await navigator.clipboard.writeText(t); setCopied('Copied') } catch { setCopied('The browser did not allow copying; use Ctrl+C or ⌘C in the grid.') }
  }
  useEffect(() => { if (!copied) return; const t = setTimeout(() => setCopied(''), 2500); return () => clearTimeout(t) }, [copied])

  // Bring a cell into view, past the frozen rows and columns.
  const reveal = (r: number, c: number) => {
    const el = scroller.current
    if (!el) return
    if (r >= FR) {
      const i = body.indexOf(r)
      if (i >= 0) {
        const y = bodyY[i], h = heightOf(r)
        const room = el.clientHeight - HEAD_H - frozenH
        if (y < el.scrollTop) el.scrollTop = y
        else if (y + h > el.scrollTop + room) el.scrollTop = y + h - room
      }
    }
    if (c >= FC) {
      const x = colX[c] - frozenW, w = widths[c]
      const room = el.clientWidth - RN - frozenW
      if (x < el.scrollLeft) el.scrollLeft = x
      else if (x + w > el.scrollLeft + room) el.scrollLeft = x + w - room
    }
  }
  const onKeyDown = (e: React.KeyboardEvent) => {
    const mod = e.ctrlKey || e.metaKey
    if (mod && e.key.toLowerCase() === 'c') {
      // A focused grid holds no text selection, so the copy is asked for.
      if (document.execCommand?.('copy')) e.preventDefault()
      return
    }
    if (mod && e.key.toLowerCase() === 'a') { e.preventDefault(); selectAt({ all: true }, false); return }
    const page = Math.max(1, Math.floor((scroller.current?.clientHeight ?? 400) / ROW_H) - 2)
    const moves: Record<string, [number, number]> = {
      ArrowUp: [-1, 0], ArrowDown: [1, 0], ArrowLeft: [0, -1], ArrowRight: [0, 1],
      Enter: [e.shiftKey ? -1 : 1, 0], Tab: [0, e.shiftKey ? -1 : 1], PageUp: [-page, 0], PageDown: [page, 0],
    }
    let to: Pos | null = null
    const p = place.get(sel.f.r) ?? 0
    if (e.key === 'Home') to = { r: mod ? shownRows[0] : sel.f.r, c: 0 }
    else if (e.key === 'End') to = { r: mod ? lastRow : sel.f.r, c: data.cols - 1 }
    else if (moves[e.key]) {
      const [dr, dc] = moves[e.key]
      const np = Math.min(shownRows.length - 1, Math.max(0, p + (mod ? dr * shownRows.length : dr)))
      let nc = Math.min(data.cols - 1, Math.max(0, sel.f.c + (mod ? dc * data.cols : dc)))
      // Hidden columns are stepped over.
      while (dc && widths[nc] === 0 && nc > 0 && nc < data.cols - 1) nc += dc > 0 ? 1 : -1
      to = { r: shownRows[np] ?? 0, c: nc }
    }
    if (!to) return
    if (e.key === 'Tab' && (to.c === sel.f.c)) return
    e.preventDefault()
    const extend = e.shiftKey && e.key !== 'Enter' && e.key !== 'Tab'
    setSel(s => extend ? { a: s.a, f: to! } : { a: to!, f: to! })
    reveal(to.r, to.c)
  }

  // The bar: where the selection is, and what the cell holds.
  const one = range.p1 === range.p2 && range.c1 === range.c2
  const merged = merges.anchors.get(`${sel.a.r}:${sel.a.c}`)
  const single = one || (!!merged && range.p2 - range.p1 === merged[2] - merged[0] && range.c2 - range.c1 === merged[3] - merged[1])
  const ref = (r: number, c: number) => `${colName(c)}${r + 1}`
  const where = single ? ref(sel.a.r, sel.a.c) : `${ref(shownRows[range.p1], range.c1)}:${ref(shownRows[range.p2], range.c2)}`
  const current = cells.get(sel.a.r)?.get(sel.a.c)
  const stats = useMemo(() => {
    if (single) return null
    let count = 0, sum = 0, nums = 0
    const n = (range.p2 - range.p1 + 1) * (range.c2 - range.c1 + 1)
    if (n > COPY_MAX) return { count: null, sum: 0, nums: 0 }
    for (let p = range.p1; p <= range.p2; p++) {
      const m = cells.get(shownRows[p])
      if (!m) continue
      for (let c = range.c1; c <= range.c2; c++) {
        const cell = m.get(c)
        if (!cell) continue
        count++
        const v = cell[5] === 'n' ? numberOf(cell) : null
        if (v !== null) { sum += v; nums++ }
      }
    }
    return { count, sum, nums }
  }, [single, range, cells, shownRows])

  const corner = (
    <div className="sg-corner" style={{ width: RN + frozenW, height: HEAD_H + frozenH }}>
      <div className="sg-head sg-all" data-all="" style={{ left: 0, top: 0, width: RN, height: HEAD_H }} title="Select all" />
      {Array.from({ length: FC }, (_, c) => widths[c] > 0 && (
        <div key={c} className={'sg-head' + (c >= range.c1 && c <= range.c2 ? ' on' : '')} data-col={c} style={{ left: RN + colX[c], top: 0, width: widths[c], height: HEAD_H }}>{colName(c)}</div>
      ))}
      {Array.from({ length: FR }, (_, r) => heightOf(r) > 0 && (
        <div key={r} className={'sg-head sg-n' + (r >= range.p1 && r <= range.p2 ? ' on' : '')} data-row={r} style={{ left: 0, top: HEAD_H + frozenY[r], width: RN, height: heightOf(r) }}>{r + 1}</div>
      ))}
      {Array.from({ length: FR }, (_, r) => Array.from({ length: FC }, (_, c) => drawCell(r, c, RN + colX[c], HEAD_H + frozenY[r])))}
      {reaching(frozenRowSet, frozenColSet, r => r < FR, c => c < FC).map(([r, c]) => drawCell(r, c, RN + colX[c], HEAD_H + frozenY[r], `m${r}:${c}`))}
    </div>
  )

  const shown = data.rows === 0 || data.cols === 0
  const cut = data.totalRows > data.rows || data.totalCols > data.cols

  return (
    <div className="sg" aria-label={label}>
      <div className="sg-bar">
        <span className="sg-ref" aria-label="Selected cells">{where}</span>
        <div className="sg-what" aria-live="polite">
          {single ? (
            <>
              {current?.[3] && <code className="sg-formula" title="Formula">{current[3]}</code>}
              <span className="sg-value">{current?.[1] ?? ''}</span>
              {current?.[4] && <span className="muted sg-raw" title="The value as stored">{current[4]}</span>}
            </>
          ) : stats && (
            <span className="muted">
              {stats.count === null ? 'A large range' : `${stats.count.toLocaleString()} ${stats.count === 1 ? 'value' : 'values'}`}
              {stats.nums > 1 && `, sum ${stats.sum.toLocaleString(undefined, { maximumFractionDigits: 6 })}, average ${(stats.sum / stats.nums).toLocaleString(undefined, { maximumFractionDigits: 6 })}`}
            </span>
          )}
        </div>
        <div className="sg-tools">
          {copied && <span className="muted sg-copied" role="status">{copied}</span>}
          <input className="sg-filter" type="search" placeholder="Filter rows" aria-label="Filter rows" value={filter} onChange={e => setFilter(e.target.value)} />
          {sortable && (
            <Button variant="secondary" size="sm" onClick={() => setSort(s => !s || s.col !== sel.f.c ? { col: sel.f.c, dir: 1 } : s.dir === 1 ? { col: sel.f.c, dir: -1 } : null)}
              title="Sort the rows under the header by the selected column; again for descending, a third time for the file's order">
              {sort ? `Sorted by ${colName(sort.col)} ${sort.dir === 1 ? '▲' : '▼'}` : 'Sort'}
            </Button>
          )}
          <Button variant="secondary" size="sm" onClick={() => void copyButton()} title="Copy the selected cells as tab separated text">Copy</Button>
          <Button variant={wrap ? 'primary' : 'secondary'} size="sm" aria-pressed={wrap} onClick={() => setWrap(w => !w)} title="Wrap long text inside its cells">Wrap</Button>
          {tools}
        </div>
      </div>
      {shown ? <p className="muted pad">This sheet is empty.</p> : (
        <div ref={scroller} className="sg-scroll" tabIndex={0} role="grid" aria-label={label} aria-rowcount={data.rows} aria-colcount={data.cols}
          onMouseDown={onMouseDown} onMouseOver={onMouseOver} onKeyDown={onKeyDown} onCopy={onCopy}>
          <div className="sg-canvas" style={{ width: RN + totalW, height: HEAD_H + frozenH + bodyY[body.length] }}>
            <div className="sg-top" style={{ width: RN + totalW, height: HEAD_H + frozenH }}>
              {corner}
              <div className="sg-layer" style={{ width: totalW - frozenW, height: HEAD_H + frozenH }}>
                {visCols.map(c => widths[c] > 0 && (
                  <div key={c} className={'sg-head' + (c >= range.c1 && c <= range.c2 ? ' on' : '')} data-col={c} style={{ left: colX[c] - frozenW, top: 0, width: widths[c], height: HEAD_H }}>{colName(c)}</div>
                ))}
                {Array.from({ length: FR }, (_, r) => visCols.map(c => drawCell(r, c, colX[c] - frozenW, HEAD_H + frozenY[r])))}
                {reaching(frozenRowSet, colSet, r => r < FR, c => c >= FC).map(([r, c]) => drawCell(r, c, colX[c] - frozenW, HEAD_H + frozenY[r], `m${r}:${c}`))}
              </div>
            </div>
            <div className="sg-main" style={{ width: RN + totalW, height: bodyY[body.length] }}>
              <div className="sg-left" style={{ width: RN + frozenW, height: bodyY[body.length] }}>
                {visRows.map(i => {
                  const r = body[i]
                  const p = place.get(r)!
                  return heightOf(r) > 0 && (
                    <div key={r} className={'sg-head sg-n' + (p >= range.p1 && p <= range.p2 ? ' on' : '')} data-row={r} style={{ left: 0, top: bodyY[i], width: RN, height: heightOf(r) }}>{r + 1}</div>
                  )
                })}
                {visRows.map(i => Array.from({ length: FC }, (_, c) => drawCell(body[i], c, RN + colX[c], bodyY[i])))}
                {reaching(rowSet, frozenColSet, r => r >= FR, c => c < FC).map(([r, c]) => drawCell(r, c, RN + colX[c], yOfBody(r), `m${r}:${c}`))}
              </div>
              <div className="sg-layer" style={{ width: totalW - frozenW, height: bodyY[body.length] }}>
                {visRows.map(i => visCols.map(c => drawCell(body[i], c, colX[c] - frozenW, bodyY[i])))}
                {reaching(rowSet, colSet, r => r >= FR, c => c >= FC).map(([r, c]) => drawCell(r, c, colX[c] - frozenW, yOfBody(r), `m${r}:${c}`))}
              </div>
            </div>
          </div>
          {!body.length && filter.trim() && <p className="muted sg-none">No rows hold “{filter.trim()}”.</p>}
        </div>
      )}
      {(tabs || note || cut || rearranged) && (
        <div className="sg-foot">
          {tabs}
          <span className="muted sg-note">
            {rearranged && `${body.length.toLocaleString()} of ${Math.max(0, data.rows - FR).toLocaleString()} rows${sort ? ', sorted' : ''}. `}
            {cut && `The first ${data.rows.toLocaleString()} of ${data.totalRows.toLocaleString()} rows${data.totalCols > data.cols ? ` and ${data.cols} of ${data.totalCols} columns` : ''} are shown; use Download for all of it. `}
            {note}
          </span>
        </div>
      )}
    </div>
  )
}
