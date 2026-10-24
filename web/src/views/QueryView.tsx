import { useEffect, useState } from 'react'
import {
  ActionMenu, Button, buttonClasses, Callout, Field, IconButton, Input, NumberInput, Select, Table, Tabs, Textarea,
  type SelectItem, type TabItem,
} from '@parallelworks/ui'
import { ChevronDownIcon, CloseIcon } from '@parallelworks/ui/icons'
import { TagMenu } from '../components/TagMenu'
import { RowCheck } from '../components/Explorer'
import { ALL_LIBRARIES, LibrarySwitch } from '../components/LibrarySwitch'
import { useAppConfig } from '../config'
import { currentLibrary } from '../api'
import { GufiCommands, type GufiCommand } from '../components/GufiCommands'
import { SavedQuerySql } from '../components/SavedQuerySql'
import { OmittedLine, type Omission } from '../components/OmittedLine'
import {
  BLANK, DIR_FIELDS, FIELDS, OPS, PLACEHOLDER, PRESETS, fromCanned, newFilter, payloadOf, toState, withField,
  type BuilderState, type Filter,
} from '../queryBuilder'

interface QueryResult {
  columns: string[]
  rows: string[][]
  elapsedMs: number
  truncated: boolean
  /** Across all libraries: each row's library id. */
  rowLibraries?: string[]
  /** Across all libraries: those that could not answer. */
  failed?: { library: string; error: string }[]
  /** Every library left out, or answered only in part, and why. */
  omitted?: Omission[]
  /** The GUFI commands that produced the rows. */
  gufi?: GufiCommand[]
}

interface SavedQuery { id: string; name: string; payload: any; createdAt: string }

type Mode = 'builder' | 'sql'
const MODES: TabItem<Mode>[] = [
  { value: 'builder', label: 'Builder' },
  { value: 'sql', label: 'SQL' },
]

const MATCHES: SelectItem<'all' | 'any'>[] = [
  { value: 'all', label: 'all' },
  { value: 'any', label: 'any' },
]

const SOURCES: SelectItem<'files' | 'directories'>[] = [
  { value: 'files', label: 'Files' },
  { value: 'directories', label: 'Directories' },
]
const GROUPINGS: SelectItem<BuilderState['groupBy']>[] = [
  { value: 'none', label: 'No grouping' },
  { value: 'extension', label: 'Extension' },
  { value: 'directory', label: 'Directory' },
  { value: 'owner', label: 'Owner' },
]
const FILE_SORTS: SelectItem<string>[] = [
  { value: 'size', label: 'size' },
  { value: 'modified', label: 'modified' },
  { value: 'name', label: 'name' },
]
const GROUP_SORTS: SelectItem<string>[] = [
  { value: 'bytes', label: 'bytes' },
  { value: 'files', label: 'file count' },
]
const ORDERS: SelectItem<'asc' | 'desc'>[] = [
  { value: 'desc', label: 'descending' },
  { value: 'asc', label: 'ascending' },
]

function fmtBytes(v: string): string {
  const n = Number(v)
  if (!Number.isFinite(n)) return v
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export function QueryView({ onOpen }: { onOpen: (path: string) => void }) {
  const [mode, setMode] = useState<Mode>('builder')
  const [b, setB] = useState<BuilderState>(BLANK)
  const set = (patch: Partial<BuilderState>) => setB(s => ({ ...s, ...patch }))
  // the SQL mode's own scope; the builder's is in b
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
      setB(toState(p.builder))
      void runQuery(p)
    }
    window.addEventListener('ade-run-query', onRun)
    return () => window.removeEventListener('ade-run-query', onRun)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const currentPayload = () => (mode === 'builder' ? payloadOf(b) : { sql, subtree })

  const runQuery = async (payload?: any) => {
    setBusy(true)
    setError(null)
    try {
      const res = await fetch('/api/query', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...(payload ?? currentPayload()), ...(currentLibrary() !== 'kb' ? { library: currentLibrary() } : {}) }),
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
    // A query saved from the former canned list opens as the builder form
    // that runs it.
    const canned = p.canned ? fromCanned(p) : null
    if (p.builder || canned) {
      const next = canned ?? toState(p.builder)
      setMode('builder')
      setB(next)
      void runQuery(canned ? payloadOf(next) : p)
    } else if (p.sql) {
      setMode('sql')
      setSql(p.sql)
      setSubtree(p.subtree ?? '')
      void runQuery(p)
    }
  }

  const deleteSaved = async (id: string) => {
    await fetch(`/api/query/saved/${encodeURIComponent(id)}`, { method: 'DELETE' })
    setSaved(s => s.filter(x => x.id !== id))
  }

  const setFilter = (i: number, next: Filter) => setB(s => ({ ...s, filters: s.filters.map((f, j) => (j === i ? next : f)) }))
  const choosePreset = (state: Omit<BuilderState, 'subtree'>) => {
    const next = { ...state, filters: state.filters.length ? state.filters : [newFilter()], subtree: b.subtree }
    setMode('builder')
    setB(next)
    void runQuery(payloadOf(next))
  }

  // Across all libraries a row opens in the library it came from.
  const maybeOpen = (cell: string, row?: number) => {
    if (!(cell.includes('/') || /\.\w{1,5}$/.test(cell))) return
    const lib = row != null ? result?.rowLibraries?.[row] : undefined
    if (currentLibrary() === ALL_LIBRARIES) { if (lib && lib !== 'all') onOpen(`${lib}/${cell}`) }
    else onOpen(cell)
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

  // A sort the current grouping does not offer shows as the first that it
  // does, which is the column the server sorts by in its place.
  const sortItems = b.groupBy === 'none' && b.source === 'files' ? FILE_SORTS : GROUP_SORTS
  const shownSort = sortItems.some(o => o.value === b.sort) ? b.sort : sortItems[0].value
  const fields = b.source === 'directories' ? FIELDS.filter(f => DIR_FIELDS.has(f.value)) : FIELDS

  const cfg = useAppConfig()
  return (
    <div className="query-view">
      <div className="query-controls card">
        {(cfg.libraries?.length ?? 0) > 1 && <div className="scope-bar"><span className="muted">Querying</span><LibrarySwitch libraries={cfg.libraries ?? []} /></div>}
        <Tabs
          label="Query mode"
          items={MODES}
          value={mode}
          onValueChange={setMode}
          className="query-tabs"
          end={saved.length > 0 && (
            <ActionMenu
              label="Saved queries"
              align="end"
              triggerClassName={buttonClasses({ variant: 'outline', size: 'sm' })}
              items={saved.map(s => ({ label: s.name, onClick: () => loadSavedQuery(s) }))}
            >
              Saved queries… <ChevronDownIcon aria-hidden="true" />
            </ActionMenu>
          )}
        />
        {mode === 'builder' && (
          <div className="query-form">
            <div className="query-presets" role="group" aria-label="Start from">
              <span className="muted">Start from</span>
              {PRESETS.map(p => (
                <Button key={p.id} size="sm" variant="outline" onClick={() => choosePreset(p.state)}>{p.label}</Button>
              ))}
            </div>
            <div className="query-row query-settings">
              <Field label="Source" size="sm" className="query-cell">
                <Select className="query-pick" items={SOURCES} value={b.source} onValueChange={v => {
                  if (v) set({ source: v, groupBy: 'none', filters: v === 'directories' ? b.filters.map(f => (DIR_FIELDS.has(f.field) ? f : newFilter())) : b.filters })
                }} />
              </Field>
              {b.source === 'files' && (
                <Field label="Group by" size="sm" className="query-cell">
                  <Select className="query-pick" items={GROUPINGS} value={b.groupBy} onValueChange={v => { if (v) set({ groupBy: v }) }} />
                </Field>
              )}
              <Field label="Sort by" size="sm" className="query-cell">
                <Select className="query-pick" items={sortItems} value={shownSort} onValueChange={v => { if (v) set({ sort: v }) }} />
              </Field>
              <Field label="Order" size="sm" className="query-cell">
                <Select className="query-pick" items={ORDERS} value={b.dir} onValueChange={v => { if (v) set({ dir: v }) }} />
              </Field>
              <Field label="Limit" size="sm" className="query-cell">
                <NumberInput className="query-num" min={1} max={2000} value={b.limit} onValueChange={limit => set({ limit })} />
              </Field>
            </div>
            <div className="query-match">
              <span>Match</span>
              <Select aria-label="Match" className="query-pick-narrow" items={MATCHES} value={b.match} onValueChange={v => { if (v) set({ match: v }) }} />
              <span>of these conditions</span>
            </div>
            {b.filters.map((f, i) => (
              <div className="query-row filter-row" key={i}>
                <Select aria-label="Field" className="query-pick query-pick-wide" items={fields} value={f.field}
                  onValueChange={field => { if (field) setFilter(i, withField(f, field)) }} />
                <Select aria-label="Condition" className="query-pick" items={OPS[f.field] ?? OPS.name}
                  value={f.op} onValueChange={op => { if (op) setFilter(i, { ...f, op }) }} />
                <Input aria-label="Value" className="grow" value={f.value} onChange={e => setFilter(i, { ...f, value: e.target.value })}
                  onKeyDown={e => { if (e.key === 'Enter') void runQuery() }}
                  placeholder={PLACEHOLDER[f.field] ?? 'value'} />
                <IconButton variant="outline" size="md" icon={<CloseIcon />} label="Remove condition"
                  onClick={() => set({ filters: b.filters.filter((_, j) => j !== i) })} />
              </div>
            ))}
            <Button className="add-filter" onClick={() => set({ filters: [...b.filters, newFilter()] })}>
              + Add condition
            </Button>
          </div>
        )}
        {mode === 'sql' && (
          <div className="query-form">
            <Field label="Your own SELECT, run in every directory over the GUFI tables (vrpentries, vrsummary, words, gchunks). Read only." size="sm">
              <Textarea className="sql-box" mono value={sql} onChange={e => setSql(e.target.value)} rows={4} spellCheck={false} />
            </Field>
          </div>
        )}
        <Field label="Scope (subtree, empty for whole corpus)" size="sm" className="query-scope">
          <Input value={mode === 'builder' ? b.subtree : subtree}
            onChange={e => (mode === 'builder' ? set({ subtree: e.target.value }) : setSubtree(e.target.value))} placeholder="e.g. proposals" />
        </Field>
        <div className="query-actions">
          <Button variant="primary" loading={busy} onClick={() => runQuery()}>{busy ? 'Running…' : 'Run query'}</Button>
          <Button
            onClick={() => {
              setResult(null); setError(null); setSel(new Set()); setTagMenuOpen(false)
              setB(BLANK); setSubtree('')
            }}
          >Reset</Button>
          {savingOpen ? (
            <span className="confirm-group">
              <Input aria-label="Query name" value={saveName} onChange={e => setSaveName(e.target.value)}
                onKeyDown={e => e.key === 'Enter' && saveQuery()} placeholder="Query name" autoFocus />
              <Button onClick={saveQuery}>Save</Button>
              <Button onClick={() => setSavingOpen(false)}>Cancel</Button>
            </span>
          ) : (
            <Button onClick={() => setSavingOpen(true)}>Save query</Button>
          )}
          {mode === 'builder' && <SavedQuerySql name="this query" payload={currentPayload()} button="Show SQL" />}
          {result && <span className="muted">{result.rows.length} {result.rows.length === 1 ? 'row' : 'rows'} in {result.elapsedMs} ms{result.truncated ? ' (truncated)' : ''}{!result.omitted && result.failed?.length ? `; not answered: ${result.failed.map(f => `${f.library} (${f.error})`).join(', ')}` : ''}</span>}
        </div>
        {saved.length > 0 && savingOpen === false && (
          <div className="saved-list">
            {saved.slice(0, 8).map(s => (
              <span key={s.id} className="saved-chip">
                <Button variant="ghost" size="xs" onClick={() => loadSavedQuery(s)}>{s.name}</Button>
                <SavedQuerySql name={s.name} payload={s.payload} />
                <IconButton size="xs" icon={<CloseIcon />} label="Delete saved query" onClick={() => deleteSaved(s.id)} />
              </span>
            ))}
          </div>
        )}
        {error && <Callout tone="error" live className="query-error">{error}</Callout>}
        <GufiCommands commands={result?.gufi} />
      </div>
      {result && (
        <div className={`query-results card ${sel.size ? 'has-sel' : ''}`}>
          <OmittedLine omitted={result.omitted} what="query" />
          {pathCol >= 0 && rowKeys.length > 0 && (
            <div className="results-head results-toolbar">
              <span>{result.columns[pathCol] === 'directory' ? 'Label directories' : 'Label files'} from these results</span>
              <span className="results-actions">
                <Button size="sm" onClick={() => setSel(allSelected ? new Set() : new Set(rowKeys))}>
                  {allSelected ? 'Clear selection' : 'Select all'}
                </Button>
                <Button size="sm" disabled={!sel.size} onClick={() => setTagMenuOpen(o => !o)}>
                  Labels…{sel.size ? ` (${sel.size})` : ''}
                </Button>
              </span>
            </div>
          )}
          <Table stickyHeader density="compact" borderless wrapperClassName="table-scroll">
            {pathCol >= 0 && <Table.Header className="sel-col" />}
            {result.columns.map(c => <Table.Header key={c}>{c}</Table.Header>)}
            {result.rows.map((r, i) => (
              <tr key={i} className={pathCol >= 0 && sel.has(r[pathCol]) ? 'multi' : ''}>
                {pathCol >= 0 && (
                  <Table.Item className="sel-col">
                    {r[pathCol] && <RowCheck checked={sel.has(r[pathCol])} onToggle={() => toggleSel(r[pathCol])} />}
                  </Table.Item>
                )}
                {r.map((cell, j) => {
                  const isBytes = result.columns[j] === 'bytes'
                  const looksPath = result.columns[j] !== 'library' && (result.columns[j] === 'path' || result.columns[j] === 'directory' || cell.includes('/'))
                  return (
                    <Table.Item key={j} className={looksPath ? 'cell-path' : ''}
                      onClick={looksPath ? () => maybeOpen(cell, i) : undefined}>
                      {isBytes ? fmtBytes(cell) : cell}
                    </Table.Item>
                  )
                })}
              </tr>
            ))}
          </Table>
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
