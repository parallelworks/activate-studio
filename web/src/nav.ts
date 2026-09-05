import type { Display } from './App'

/**
 * The library's location in the URL hash: #open=file:<path> for a file,
 * #open=workflow_dag:<name> for a DAG, with an optional &q=<query> after
 * a file so a link can land on a match. Paths are percent-encoded except
 * for their slashes, which keeps the hash readable.
 */
export function parseOpenHash(hash: string): Display | null {
  const m = /^#open=(file|workflow_dag):(.+?)(?:&q=([^&]*))?$/.exec(hash)
  if (!m) return null
  const target = decodeURIComponent(m[2])
  if (m[1] === 'workflow_dag') return { kind: 'workflow_dag', target }
  return m[3] ? { kind: 'file', target, q: decodeURIComponent(m[3]) } : { kind: 'file', target }
}

export function buildOpenHash(display: Display): string {
  const base = `#open=${display.kind}:${encodeURIComponent(display.target).replace(/%2F/gi, '/')}`
  return display.kind === 'file' && display.q ? `${base}&q=${encodeURIComponent(display.q)}` : base
}
