import { useMemo } from 'react'
import { type GridCell, type GridSheetData, SheetGrid } from './SheetGrid'


export const DELIMITED_SUFFIXES = ['.csv', '.tsv']
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

/**
 * A CSV or TSV file in the spreadsheet grid (SheetGrid.tsx), as a workbook
 * sheet is: its first row is the header, frozen and bold; numbers sit to
 * the right; columns are as wide as their values ask, within limits. Rows
 * filter, and sort by the selected column under the header.
 */
export function CsvTable({ text, suffix, truncated }: { text: string; suffix: string; truncated?: boolean }) {
  const { data, partial, delim, body } = useMemo(() => {
    const delim = detectDelimiter(text, suffix)
    const { rows, partial } = parseDelimited(text, delim, truncated)
    const cols = rows.reduce((n, r) => Math.max(n, r.length), 0)
    const widths = Array.from({ length: cols }, (_, c) => {
      let longest = 0
      for (let r = 0; r < Math.min(rows.length, 300); r++) longest = Math.max(longest, Math.min(60, (rows[r][c] ?? '').length))
      return Math.max(56, Math.min(400, longest * 7 + 18))
    })
    const data: GridSheetData = {
      name: suffix.slice(1).toUpperCase(),
      rows: rows.length,
      cols,
      totalRows: rows.length,
      totalCols: cols,
      colWidths: widths,
      frozenRows: rows.length > 1 ? 1 : 0,
      styles: [{ b: true }],
      cells: rows.map((row, r) => ({
        r,
        c: row.flatMap((v, c): GridCell[] => v === '' ? [] : [r === 0 ? [c, v, 0] : asNumber(v) !== null ? [c, v, -1, '', '', 'n'] : [c, v]]),
      })),
    }
    return { data, partial, delim, body: Math.max(0, rows.length - 1) }
  }, [text, suffix, truncated])

  if (!data.cols) return <p className="muted pad">This file has no rows.</p>
  const delimName = delim === '\t' ? 'tab' : delim === ',' ? 'comma' : delim === ';' ? 'semicolon' : 'pipe'
  return (
    <SheetGrid
      data={data}
      label="Table"
      sortable
      note={<>
        {`${body.toLocaleString()} rows, ${data.cols} columns, ${delimName} separated. `}
        {(truncated || partial) && 'The first part of the file is shown; use Download for all of it.'}
      </>}
    />
  )
}
