import { useState } from 'react'
import { parseAllDocuments } from 'yaml'

/** Structured previews for JSON and YAML: the parsed document as a
 *  collapsible tree, so a reader can see the shape of a large config
 *  without scrolling its text. Parsing happens here rather than on the
 *  server so the raw file stays the thing on disk. */

export type ParsedDoc = { value: unknown } | { error: string }

export function parseStructured(text: string, format: 'json' | 'yaml'): ParsedDoc[] {
  if (format === 'json') {
    try {
      return [{ value: JSON.parse(text) }]
    } catch (e) {
      return [{ error: (e as Error).message }]
    }
  }
  try {
    const docs = parseAllDocuments(text)
    if (docs.length === 0) return [{ value: null }]
    return docs.map(d => {
      const fatal = d.errors[0]
      return fatal ? { error: fatal.message } : { value: d.toJS() }
    })
  } catch (e) {
    return [{ error: (e as Error).message }]
  }
}

function kindOf(v: unknown): string {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  return typeof v
}

/** One line of summary for a collapsed container, so collapsing does not
 *  hide what is inside. */
function summary(v: unknown): string {
  if (Array.isArray(v)) return `[${v.length}]`
  const keys = Object.keys(v as object)
  const head = keys.slice(0, 3).join(', ')
  return `{${keys.length}}${keys.length ? ' ' + head + (keys.length > 3 ? ', …' : '') : ''}`
}

function Scalar({ value }: { value: unknown }) {
  const kind = kindOf(value)
  const text = kind === 'string' ? String(value) : JSON.stringify(value)
  // Multi-line strings are the common case in a workflow yaml; show them
  // as a block instead of one long unreadable line.
  if (kind === 'string' && text.includes('\n')) {
    return <pre className="tree-block">{text}</pre>
  }
  return <span className={`tree-scalar tree-${kind}`}>{text}</span>
}

function Node({ name, value, depth }: { name: string | null; value: unknown; depth: number }) {
  // Deep trees open at the top and close further down, which keeps a big
  // document readable on arrival.
  const [open, setOpen] = useState(depth < 2)
  const kind = kindOf(value)
  const container = kind === 'object' || kind === 'array'

  if (!container) {
    return (
      <div className="tree-row" style={{ paddingLeft: depth * 14 }}>
        {name !== null && <span className="tree-key">{name}</span>}
        <Scalar value={value} />
      </div>
    )
  }

  const entries: [string, unknown][] = Array.isArray(value)
    ? value.map((v, i) => [String(i), v])
    : Object.entries(value as Record<string, unknown>)

  return (
    <div className="tree-node">
      <div className="tree-row" style={{ paddingLeft: depth * 14 }}>
        <button className="tree-toggle" onClick={() => setOpen(o => !o)} aria-expanded={open}>
          {open ? '▾' : '▸'}
        </button>
        {name !== null && <span className="tree-key">{name}</span>}
        <span className="tree-summary">{summary(value)}</span>
      </div>
      {open && entries.map(([k, v]) => <Node key={k} name={k} value={v} depth={depth + 1} />)}
    </div>
  )
}

export function DataTree({ text, format }: { text: string; format: 'json' | 'yaml' }) {
  const docs = parseStructured(text, format)
  return (
    <div className="viewer-body">
      {docs.map((doc, i) => (
        <div key={i} className="tree-doc">
          {docs.length > 1 && <p className="tree-doc-label">document {i + 1}</p>}
          {'error' in doc ? (
            <p className="error">This file did not parse as {format.toUpperCase()}: {doc.error}. The Source tab shows it as written.</p>
          ) : (
            <Node name={null} value={doc.value} depth={0} />
          )}
        </div>
      ))}
    </div>
  )
}
