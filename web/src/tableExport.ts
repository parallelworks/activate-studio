/**
 * Results as a file or as text to paste: CSV for a file (RFC 4180, every
 * value as it came, so a script reads back what the query returned), and
 * tab-separated text for the clipboard, which a spreadsheet pastes into
 * columns.
 */

/** One CSV field: quoted when it holds a comma, a quote or a line break. */
function csvField(v: string): string {
  return /[",\r\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
}

/** A header row and rows as CSV, with CRLF line ends as RFC 4180 has them. */
export function toCsv(columns: string[], rows: string[][]): string {
  return [columns, ...rows].map(r => r.map(c => csvField(String(c ?? ''))).join(',')).join('\r\n') + '\r\n'
}

/** A header row and rows as tab-separated lines; a tab or line break inside
 *  a value becomes a space, since a paste has no quoting. */
export function toTsv(columns: string[], rows: string[][]): string {
  return [columns, ...rows].map(r => r.map(c => String(c ?? '').replace(/[\t\r\n]+/g, ' ')).join('\t')).join('\n') + '\n'
}

/** A file name for an export: the kind, a few words of what it was, and the
 *  local date and time, such as search-budget-report-2026-10-27-1405.csv. */
export function exportName(kind: string, words = '', at = new Date()): string {
  const slug = words.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/-+$/, '')
  const p = (n: number) => String(n).padStart(2, '0')
  const stamp = `${at.getFullYear()}-${p(at.getMonth() + 1)}-${p(at.getDate())}-${p(at.getHours())}${p(at.getMinutes())}`
  return `${[kind, slug, stamp].filter(Boolean).join('-')}.csv`
}

/** Saves text as a file: the browser's download, and in the macOS app's
 *  window a file in Downloads, shown in the Finder. */
export function saveText(name: string, text: string, type = 'text/csv;charset=utf-8'): void {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  // The download has its own reference by now.
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}
