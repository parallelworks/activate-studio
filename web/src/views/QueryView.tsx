import { useEffect, useState } from 'react'
import { TagMenu } from '../components/TagMenu'
import { RowCheck } from '../components/Explorer'

interface QueryResult {
  columns: string[]
  rows: string[][]
  elapsedMs: number
  truncated: boolean
}

interface Filter { field: string; op: string; value: string }
interface SavedQuery { id: string; name: string; payload: any; createdAt: string }

const CANNED = [
  { id: 'largest', label: 'Largest files' },
  { id: 'newest', label: 'Most recently modified' },
  { id: 'oldest', label: 'Oldest files' },
  { id: 'recent', label: 'Changed in the last N days' },
  { id: 'by_extension', label: 'Totals by file extension' },
  { id: 'big_directories', label: 'Biggest directories' },
]

const FIELDS = [
  { id: 'name', label: 'File name' },
  { id: 'path', label: 'Path' },
  { id: 'extension', label: 'Extension' },
  { id: 'size', label: 'Size (bytes)' },
  { id: 'modified', label: 'Modified (YYYY-MM-DD)' },
  { id: 'text', label: 'Text content' },
  { id: 'label', label: 'Label' },
]
const OPS: Record<string, { id: string; label: string }[]> = {
  default: [
    { id: 'contains', label: 'contains' },
    { id: 'not_contains', label: 'does not contain' },
    { id: 'eq', label: 'equals' },
    { id: 'gt', label: 'greater than' },
    { id: 'lt', label: 'less than' },
  ],
  text: [{ id: 'match', label: 'matches' }],
}

function fmtBytes(v: string): string {
  const n = Number(v)
  if (!Number.isFinite(n)) return v
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export function QueryView({ onOpen }: { onOpen: (path: string) => void }) {
  const [mode, setMode] = useState<'builder' | 'canned' | 'sql'>('canned')
  // builder state
  const [source, setSource] = useState<'files' | 'directories'>('files')
  const [filters, setFilters] = useState<Filter[]>([{ field: 'name', op: 'contains', value: '' }])
  const [groupBy, setGroupBy] = useState<'none' | 'extension' | 'directory'>('none')
  const [sort, setSort] = useState('size')
  const [dir, setDir] = useState<'asc' | 'desc'>('desc')
  const [limit, setLimit] = useState(50)
  // canned state
  const [canned, setCanned] = useState('largest')
  const [n, setN] = useState(25)
  const [days, setDays] = useState(7)
  // shared
  const [subtree, setSubtree] = useState('')
  const [sql, setSql] = useState("SELECT rpath(sname, sroll)||'/'||vrpentries.name, size FROM vrpentries WHERE size > 1000000")
  const [result, setResult] = useState<QueryResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // saved
  const [saved, setSaved] = useState<SavedQuery[]>([])
  const [saveName, setSaveName] = useState('')
  const [savingOpen, setSavingOpen] = useState(false)
  // multi-select labeling over result rows that carry a path
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [tagMenuOpen, setTagMenuOpen] = useState(false)

  useEffect(() => {
    fetch('/api/query/saved').then(r => r.json()).then(setSaved).catch(() => {})
  }, [])

  // Other views (Stats label/extension clicks) hand a ready-to-run builder
  // payload over this event; adopt it into the form and run it.
  useEffect(() => {
    const onRun = (e: Event) => {
      const p = (e as CustomEvent).detail
      if (!p?.builder) return
      setMode('builder')
      setSource(p.builder.source ?? 'files')
      setFilters(p.builder.filters?.length ? p.builder.filters : [{ field: 'name', op: 'contains', value: '' }])
      setGroupBy(p.builder.groupBy ?? 'none')
      setSort(p.builder.sort ?? 'size')
      setDir(p.builder.dir ?? 'desc')
      setLimit(p.builder.limit ?? 50)
      setSubtree(p.builder.subtree ?? '')
      void runQuery(p)
    }
    window.addEventListener('ade-run-query', onRun)
    return () => window.removeEventListener('ade-run-query', onRun)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const currentPayload = () => {
    if (mode === 'builder') return { builder: { source, filters: filters.filter(f => f.value !== ''), groupBy, sort, dir, limit, subtree } }
    if (mode === 'canned') return { canned, n, days, subtree }
    return { sql, subtree }
  }

  const runQuery = async (payload?: any) => {
    setBusy(true)
    setError(null)
    try {
      const res = await fetch('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload ?? currentPayload()),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.message ?? data.error ?? `${res.status}`)
      setResult(data)
      setSel(new Set())
      setTagMenuOpen(false)
    } catch (e) {
      setError(String((e as Error).message ?? e))
      setResult(null)
    } finally {
      setBusy(false)
    }
  }

  const saveQuery = async () => {
    if (!saveName.trim()) return
    const res = await fetch('/api/query/saved', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: saveName.trim(), payload: currentPayload() }),
    })
    if (res.ok) {
      const entry = await res.json()
      setSaved(s => [entry, ...s])
      setSaveName('')
      setSavingOpen(false)
    }
  }

  const loadSavedQuery = (s: SavedQuery) => {
    const p = s.payload
    if (p.builder) {
      setMode('builder')
      setSource(p.builder.source ?? 'files')
      setFilters(p.builder.filters?.length ? p.builder.filters : [{ field: 'name', op: 'contains', value: '' }])
      setGroupBy(p.builder.groupBy ?? 'none')
      setSort(p.builder.sort ?? 'size')
      setDir(p.builder.dir ?? 'desc')
      setLimit(p.builder.limit ?? 50)
      setSubtree(p.builder.subtree ?? '')
    } else if (p.canned) {
      setMode('canned')
      setCanned(p.canned)
      setN(p.n ?? 25)
      setDays(p.days ?? 7)
      setSubtree(p.subtree ?? '')
    } else if (p.sql) {
      setMode('sql')
      setSql(p.sql)
      setSubtree(p.subtree ?? '')
    }
    void runQuery(p)
  }

  const deleteSaved = async (id: string) => {
    await fetch(`/api/query/saved/${encodeURIComponent(id)}`, { method: 'DELETE' })
    setSaved(s => s.filter(x => x.id !== id))
  }

  const setFilter = (i: number, patch: Partial<Filter>) => {
    setFilters(fs => fs.map((f, j) => (j === i ? { ...f, ...patch } : f)))
  }

  const maybeOpen = (cell: string) => {
    if (cell.includes('/') || /\.\w{1,5}$/.test(cell)) onOpen(cell)
  }

  // Labeling is offered only when the result set has an explicit path or
  // directory column; heuristic path-shaped cells are fine for opening a
  // viewer but too loose for writing xattrs.
  const pathCol = result ? result.columns.findIndex(c => c === 'path' || c === 'directory') : -1
  const rowKeys = pathCol >= 0 && result ? [...new Set(result.rows.map(r => r[pathCol]).filter(Boolean))] : []
  const allSelected = rowKeys.length > 0 && sel.size === rowKeys.length

  const toggleSel = (key: string) => {
    setSel(s => {
      const next = new Set(s)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <div className="query-view">
      <div className="query-controls card">
        <h1 className="view-title">Query the file index</h1>
        <p className="muted view-sub">Structured questions about the corpus itself: sizes, ages, types, labels, and locations, answered from the index in milliseconds.</p>
        <div className="query-mode">
          <button className={mode === 'canned' ? 'active' : ''} onClick={() => setMode('canned')}>Canned queries</button>
          <button className={mode === 'builder' ? 'active' : ''} onClick={() => setMode('builder')}>Builder</button>
          <button className={mode === 'sql' ? 'active' : ''} onClick={() => setMode('sql')}>SQL</button>
          {saved.length > 0 && (
            <select
              className="field saved-select"
              value=""
              onChange={e => {
                const s = saved.find(x => x.id === e.target.value)
                if (s) loadSavedQuery(s)
              }}
            >
              <option value="">Saved queries…</option>
              {saved.map(s => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          )}
        </div>
        {mode === 'builder' && (
          <div className="query-form">
            <div className="query-row">
              <div>
                <label className="field-label">Source</label>
                <select className="field" value={source} onChange={e => setSource(e.target.value as any)}>
                  <option value="files">Files</option>
                  <option value="directories">Directories</option>
                </select>
              </div>
              {source === 'files' && (
                <div>
                  <label className="field-label">Group by</label>
                  <select className="field" value={groupBy} onChange={e => setGroupBy(e.target.value as any)}>
                    <option value="none">No grouping</option>
                    <option value="extension">Extension</option>
                    <option value="directory">Directory</option>
                  </select>
                </div>
              )}
              <div>
                <label className="field-label">Sort by</label>
                <select className="field" value={sort} onChange={e => setSort(e.target.value)}>
                  {groupBy === 'none' && source === 'files'
                    ? <><option value="size">size</option><option value="modified">modified</option><option value="name">name</option></>
                    : <><option value="bytes">bytes</option><option value="files">file count</option></>}
                </select>
              </div>
              <div>
                <label className="field-label">Order</label>
                <select className="field" value={dir} onChange={e => setDir(e.target.value as any)}>
                  <option value="desc">descending</option>
                  <option value="asc">ascending</option>
                </select>
              </div>
              <div>
                <label className="field-label">Limit</label>
                <input className="field" type="number" min={1} max={2000} value={limit} onChange={e => setLimit(Number(e.target.value))} />
              </div>
            </div>
            <label className="field-label">Filters (all must match)</label>
            {filters.map((f, i) => (
              <div className="query-row filter-row" key={i}>
                <select className="field" value={f.field} onChange={e => {
                  const field = e.target.value
                  setFilter(i, { field, op: field === 'text' ? 'match' : 'contains' })
                }}>
                  {FIELDS.map(fd => <option key={fd.id} value={fd.id}>{fd.label}</option>)}
                </select>
                <select className="field" value={f.op} onChange={e => setFilter(i, { op: e.target.value })}>
                  {(f.field === 'text' ? OPS.text : OPS.default).map(o => <option key={o.id} value={o.id}>{o.label}</option>)}
                </select>
                <input className="field grow" value={f.value} onChange={e => setFilter(i, { value: e.target.value })}
                  placeholder={f.field === 'modified' ? '2026-01-01' : 'value'} />
                <button className="btn-secondary" onClick={() => setFilters(fs => fs.filter((_, j) => j !== i))}>×</button>
              </div>
            ))}
            <button className="btn-secondary add-filter" onClick={() => setFilters(fs => [...fs, { field: 'name', op: 'contains', value: '' }])}>
              + Add filter
            </button>
          </div>
        )}
        {mode === 'canned' && (
          <div className="query-form">
            <label className="field-label">Query</label>
            <select className="field" value={canned} onChange={e => setCanned(e.target.value)}>
              {CANNED.map(c => <option key={c.id} value={c.id}>{c.label}</option>)}
            </select>
            <div className="query-row">
              <div>
                <label className="field-label">Limit</label>
                <input className="field" type="number" min={1} max={2000} value={n} onChange={e => setN(Number(e.target.value))} />
              </div>
              {canned === 'recent' && (
                <div>
                  <label className="field-label">Days</label>
                  <input className="field" type="number" min={1} value={days} onChange={e => setDays(Number(e.target.value))} />
                </div>
              )}
            </div>
          </div>
        )}
        {mode === 'sql' && (
          <div className="query-form">
            <label className="field-label">
              Per-directory SELECT over the GUFI tables (vrpentries, vrsummary, words, gchunks). Read-only.
            </label>
            <textarea className="field sql-box" value={sql} onChange={e => setSql(e.target.value)} rows={4} spellCheck={false} />
          </div>
        )}
        <div className="query-row">
          <div className="grow">
            <label className="field-label">Scope (subtree, empty for whole corpus)</label>
            <input className="field" value={subtree} onChange={e => setSubtree(e.target.value)} placeholder="e.g. proposals" />
          </div>
        </div>
        <div className="query-actions">
          <button className="btn-primary" onClick={() => runQuery()} disabled={busy}>{busy ? 'Running…' : 'Run query'}</button>
          <button
            className="btn-secondary"
            onClick={() => {
              setResult(null); setError(null); setSel(new Set()); setTagMenuOpen(false)
              setFilters([{ field: 'name', op: 'contains', value: '' }])
              setGroupBy('none'); setSort('size'); setDir('desc'); setLimit(50)
              setSubtree(''); setCanned('largest'); setN(25); setDays(7)
            }}
          >Reset</button>
          {savingOpen ? (
            <span className="confirm-group">
              <input className="field" value={saveName} onChange={e => setSaveName(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && saveQuery()} placeholder="Query name" autoFocus />
              <button className="btn-secondary" onClick={saveQuery}>Save</button>
              <button className="btn-secondary" onClick={() => setSavingOpen(false)}>Cancel</button>
            </span>
          ) : (
            <button className="btn-secondary" onClick={() => setSavingOpen(true)}>Save query</button>
          )}
          {result && <span className="muted">{result.rows.length} rows in {result.elapsedMs} ms{result.truncated ? ' (truncated)' : ''}</span>}
        </div>
        {saved.length > 0 && savingOpen === false && (
          <div className="saved-list">
            {saved.slice(0, 8).map(s => (
              <span key={s.id} className="saved-chip">
                <button className="saved-load" onClick={() => loadSavedQuery(s)}>{s.name}</button>
                <button className="saved-del" title="Delete saved query" onClick={() => deleteSaved(s.id)}>×</button>
              </span>
            ))}
          </div>
        )}
        {error && <p className="error">{error}</p>}
      </div>
      {result && (
        <div className={`query-results card ${sel.size ? 'has-sel' : ''}`}>
          {pathCol >= 0 && rowKeys.length > 0 && (
            <div className="results-head results-toolbar">
              <span>{result.columns[pathCol] === 'directory' ? 'Label directories' : 'Label files'} from these results</span>
              <span className="results-actions">
                <button
                  className="btn-secondary"
                  onClick={() => setSel(allSelected ? new Set() : new Set(rowKeys))}
                >{allSelected ? 'Clear selection' : 'Select all'}</button>
                <button
                  className="btn-secondary"
                  disabled={!sel.size}
                  onClick={() => setTagMenuOpen(o => !o)}
                >Labels…{sel.size ? ` (${sel.size})` : ''}</button>
              </span>
            </div>
          )}
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  {pathCol >= 0 && <th className="sel-col" />}
                  {result.columns.map(c => <th key={c}>{c}</th>)}
                </tr>
              </thead>
              <tbody>
                {result.rows.map((r, i) => (
                  <tr key={i} className={pathCol >= 0 && sel.has(r[pathCol]) ? 'multi' : ''}>
                    {pathCol >= 0 && (
                      <td className="sel-col">
                        {r[pathCol] && <RowCheck checked={sel.has(r[pathCol])} onToggle={() => toggleSel(r[pathCol])} />}
                      </td>
                    )}
                    {r.map((cell, j) => {
                      const isBytes = result.columns[j] === 'bytes'
                      const looksPath = result.columns[j] === 'path' || result.columns[j] === 'directory' || cell.includes('/')
                      return (
                        <td key={j} className={looksPath ? 'cell-path' : ''}
                          onClick={looksPath ? () => maybeOpen(cell) : undefined}>
                          {isBytes ? fmtBytes(cell) : cell}
                        </td>
                      )
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      {tagMenuOpen && sel.size > 0 && (
        <TagMenu
          paths={[...sel]}
          onClose={() => setTagMenuOpen(false)}
          onApplied={() => {}}
        />
      )}
    </div>
  )
}
