import type { Display } from './App'

/**
 * The library's location in the URL hash: #open=file:<path> for a file,
 * #open=workflow_dag:<name> for a DAG, with an optional &q=<query> after
 * a file so a link can land on a match. Paths are percent-encoded except
 * for their slashes, which keeps the hash readable.
 */
export function parseOpenHash(hash: string): Display | null {
  const m = /^#open=(file|workflow_dag):(.+?)(?:&q=([^&]*))?(?:&lib=([a-z0-9_-]+))?$/.exec(hash)
  if (!m) return null
  const target = decodeURIComponent(m[2])
  if (m[1] === 'workflow_dag') return { kind: 'workflow_dag', target }
  const d: Display = { kind: 'file', target }
  if (m[3]) d.q = decodeURIComponent(m[3])
  // Which library the path belongs to; absent means the primary.
  if (m[4]) d.lib = m[4]
  return d
}

export function buildOpenHash(display: Display): string {
  const base = `#open=${display.kind}:${encodeURIComponent(display.target).replace(/%2F/gi, '/')}`
  if (display.kind !== 'file') return base
  const q = display.q ? `&q=${encodeURIComponent(display.q)}` : ''
  const lib = display.lib && display.lib !== 'kb' ? `&lib=${display.lib}` : ''
  return base + q + lib
}
