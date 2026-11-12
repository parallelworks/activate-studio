import { useMemo, useState } from 'react'
import { Button } from '@parallelworks/ui'

/**
 * A CSV or TSV file as a table: the first row is the header, which stays
 * in view while the rows scroll; numeric columns are right aligned and
 * sort as numbers; a filter keeps the rows holding its text. Rows are
 * drawn a page at a time, so a large file does not hold the page.
 */

export const DELIMITED_SUFFIXES = ['.csv', '.tsv']
const PAGE = 500
const CANDIDATES = [',', '\t', ';', '|']

/** RFC 4180 fields: quotes around a field allow the delimiter, line breaks
 *  and doubled quotes inside it. A file cut off mid row (a truncated
 *  preview) loses only that row, which `partial` reports. */
export function parseDelimited(text: string, delim: string, cut = false): { rows: string[][]; partial: boolean } {
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let quoted = false
  let i = 0
  const s = text.charCodeAt(0) === 0xfeff ? text.slice(1) : text
  while (i < s.length) {
    const c = s[i]
    if (quoted) {
      if (c === '"') {
        if (s[i + 1] === '"') { field += '"'; i += 2; continue }
        quoted = false; i++; continue
      }
      field += c; i++; continue
    }
    if (c === '"' && field === '') { quoted = true; i++; continue }
    if (c === delim) { row.push(field); field = ''; i++; continue }
    if (c === '\r' || c === '\n') {
      row.push(field); rows.push(row); row = []; field = ''
      i += c === '\r' && s[i + 1] === '\n' ? 2 : 1
      continue
    }
    field += c; i++
  }
  const tail = field !== '' || row.length > 0
  let partial = false
  if (tail) {
    if (cut) partial = true
    else { row.push(field); rows.push(row) }
  }
  // Blank lines carry no row.
  return { rows: rows.filter(r => r.length > 1 || r[0] !== ''), partial }
}

/** The delimiter a file uses: tab for .tsv, otherwise the candidate that
 *  splits the first lines into the same number of fields, more than one. */
export function detectDelimiter(text: string, suffix: string): string {
  if (suffix === '.tsv') return '\t'
  const lines = text.split(/\r?\n/).filter(l => l.trim()).slice(0, 20)
  let best = ',', bestScore = 0
  for (const d of CANDIDATES) {
    const counts = lines.map(l => l.split(d).length - 1)
    if (!counts.length || counts[0] === 0) continue
    const steady = counts.filter(n => n === counts[0]).length / counts.length
    const score = steady * 10 + Math.min(counts[0], 9)
    if (score > bestScore) { best = d; bestScore = score }
  }
  return best
}

const NUM = /^[-+]?(\d{1,3}(,\d{3})+|\d+)?(\.\d+)?([eE][-+]?\d+)?%?$/
function asNumber(v: string): number | null {
  const t = v.trim()
  if (!t || !NUM.test(t) || !/\d/.test(t)) return null
  const n = Number(t.replace(/[,%]/g, ''))
  return Number.isFinite(n) ? n : null
}

type Sort = { col: number; dir: 1 | -1 } | null

export function CsvTable({ text, suffix, truncated }: { text: string; suffix: string; truncated?: boolean }) {
  const { header, body, cols, partial, delim } = useMemo(() => {
    const delim = detectDelimiter(text, suffix)
    const { rows, partial } = parseDelimited(text, delim, truncated)
    const cols = rows.reduce((n, r) => Math.max(n, r.length), 0)
    return { header: rows[0] ?? [], body: rows.slice(1), cols, partial, delim }
  }, [text, suffix, truncated])
  const numeric = useMemo(() => Array.from({ length: cols }, (_, c) => {
    let seen = 0
    for (const r of body) {
      const v = r[c] ?? ''
      if (!v.trim()) continue
      if (asNumber(v) === null) return false
      if (++seen >= 200) break
    }
    return seen > 0
  }), [body, cols])
  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState<Sort>(null)
  const [limit, setLimit] = useState(PAGE)

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase()
    let rows = body.map((r, i) => ({ r, n: i + 1 }))
    if (q) rows = rows.filter(({ r }) => r.some(v => v.toLowerCase().includes(q)))
    if (sort) {
      const { col, dir } = sort
      const num = numeric[col]
      rows = [...rows].sort((a, b) => {
        const x = a.r[col] ?? '', y = b.r[col] ?? ''
        if (num) {
          const nx = asNumber(x), ny = asNumber(y)
          if (nx === null || ny === null) return nx === null ? (ny === null ? 0 : 1) : -1
          return (nx - ny) * dir
        }
        if (!x || !y) return !x ? (!y ? 0 : 1) : -1
        return x.localeCompare(y, undefined, { numeric: true, sensitivity: 'base' }) * dir
      })
    }
    return rows
  }, [body, filter, sort, numeric])

  if (!cols) return <p className="muted pad">This file has no rows.</p>

  const cycle = (c: number) => setSort(s => (!s || s.col !== c ? { col: c, dir: 1 } : s.dir === 1 ? { col: c, dir: -1 } : null))
  const delimName = delim === '\t' ? 'tab' : delim === ',' ? 'comma' : delim === ';' ? 'semicolon' : 'pipe'

  return (
    <div className="csv-view">
      <div className="csv-bar">
        <input
          className="csv-filter"
          type="search"
          placeholder="Filter rows"
          aria-label="Filter rows"
          value={filter}
          onChange={e => { setFilter(e.target.value); setLimit(PAGE) }}
        />
        <span className="muted csv-count">
          {filter.trim() ? `${shown.length.toLocaleString()} of ` : ''}{body.length.toLocaleString()} rows, {cols} columns, {delimName} separated
        </span>
      </div>
      <div className="csv-scroll">
        <table className="csv-table">
          <thead>
            <tr>
              <th className="csv-n" aria-label="Row" />
              {Array.from({ length: cols }, (_, c) => {
                const dir = sort?.col === c ? sort.dir : 0
                return (
                  <th key={c} className={numeric[c] ? 'num' : undefined} aria-sort={dir === 1 ? 'ascending' : dir === -1 ? 'descending' : 'none'}>
                    <button type="button" onClick={() => cycle(c)} title="Sort by this column">
                      {header[c] || `Column ${c + 1}`}
                      <span className="csv-sort" aria-hidden>{dir === 1 ? '▲' : dir === -1 ? '▼' : ''}</span>
                    </button>
                  </th>
                )
              })}
            </tr>
          </thead>
          <tbody>
            {shown.slice(0, limit).map(({ r, n }) => (
              <tr key={n}>
                <td className="csv-n">{n}</td>
                {Array.from({ length: cols }, (_, c) => <td key={c} className={numeric[c] ? 'num' : undefined}><div className="csv-c">{r[c] ?? ''}</div></td>)}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {shown.length > limit && (
        <div className="csv-more">
          <span className="muted">Showing {limit.toLocaleString()} of {shown.length.toLocaleString()} rows.</span>
          <Button variant="secondary" size="sm" onClick={() => setLimit(l => l + PAGE * 4)}>Show more</Button>
        </div>
      )}
      {(truncated || partial) && <p className="muted pad">The first part of the file is shown; use Download for all of it.</p>}
    </div>
  )
}
