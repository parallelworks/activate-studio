import { useEffect, useState, useSyncExternalStore } from 'react'
import { CopyButton, Loader, Meter, RelativeTime, TagList, formatBytes } from '@parallelworks/ui'
import { api, currentLibrary, onLibraryChange, type EntryDetails } from '../api'

/**
 * The folder or file selected in the Library view's tree, and what the
 * status panel shows for it (GET /api/kb/details): its size, file and
 * folder counts, the latest change, the types taking the most space, its
 * labels and those below it, and which library and index it comes from.
 * With nothing selected the panel describes the library itself; with all
 * libraries showing, one has to be picked first.
 */

export interface Selection { path: string; isDir: boolean }

let selection: Selection | null = null
const subs = new Set<() => void>()
const emit = () => { for (const fn of subs) fn() }

/** The tree reports what was clicked; null clears it. */
export function selectLibraryEntry(next: Selection | null): void {
  if (next?.path === selection?.path && next?.isDir === selection?.isDir) return
  selection = next
  emit()
}

function useSelection(): Selection | null {
  return useSyncExternalStore(fn => { subs.add(fn); return () => { subs.delete(fn) } }, () => selection)
}

// Moving through the tree comes back to the same folders; their details
// are kept for a short while, and dropped when the library changes.
const KEEP_MS = 30_000
const kept = new Map<string, { at: number; value: EntryDetails }>()
const forget = () => kept.clear()
if (typeof window !== 'undefined') {
  window.addEventListener('ade-kb-changed', forget)
  window.addEventListener('ade-tags-changed', forget)
}

export interface DetailsState { selection: Selection | null; details: EntryDetails | null; loading: boolean; error: string; pick: boolean }

/** The details of what is selected, fetched a moment after the selection
 *  settles so a quick run of clicks asks once; nothing is asked while
 *  `active` is false (another page is showing). */
export function useEntryDetails(active = true): DetailsState {
  const sel = useSelection()
  const [lib, setLib] = useState(currentLibrary())
  useEffect(() => onLibraryChange(id => { setLib(id); forget(); selectLibraryEntry(null) }), [])
  const [state, setState] = useState<{ key: string; details: EntryDetails | null; error: string; loading: boolean }>({ key: '', details: null, error: '', loading: false })
  const all = lib === 'all'
  const path = !active ? null : sel?.path ?? (all ? null : '')
  const key = path === null ? '' : `${lib}|${path}`
  useEffect(() => {
    if (path === null) { setState({ key, details: null, error: '', loading: false }); return }
    const hit = kept.get(key)
    if (hit && Date.now() - hit.at < KEEP_MS) { setState({ key, details: hit.value, error: '', loading: false }); return }
    let live = true
    setState(s => ({ key, details: s.key === key ? s.details : null, error: '', loading: true }))
    const t = window.setTimeout(() => {
      api.details(path)
        .then(d => { kept.set(key, { at: Date.now(), value: d }); if (live) setState({ key, details: d, error: '', loading: false }) })
        .catch(e => { if (live) setState({ key, details: null, error: String((e as Error).message ?? e), loading: false }) })
    }, 120)
    return () => { live = false; window.clearTimeout(t) }
  }, [key, path])
  return { selection: sel, details: state.key === key ? state.details : null, loading: state.loading, error: state.error, pick: path === null }
}

/** The card's header line: what is selected, and how big it is. */
export function detailsSummary(s: DetailsState): string {
  if (s.pick) return 'nothing selected'
  if (!s.details) return s.loading ? 'reading…' : s.error ? 'not available' : ''
  const d = s.details
  return `${d.name} · ${formatBytes(d.size)}`
}

const count = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`

export function DetailsBody({ state }: { state: DetailsState }) {
  const { details: d, loading, error, pick } = state
  if (pick) return <p className="details-note">Select a folder or file in the tree to see its size, contents, and labels.</p>
  if (!d) {
    if (error) return <p className="details-note">{error}</p>
    return <div className="details-loading"><Loader inline size={14} label="Reading the index…" /></div>
  }
  const dir = d.type === 'dir'
  const largest = d.types[0]?.bytes || 0
  const labels = [...d.labels.own.map(l => ({ key: `own:${l}`, label: l })), ...d.labels.inherited.map(l => ({ key: `inh:${l}`, label: <span className="details-inherited" title="From a folder above">{l}</span> }))]
  return (
    <div className={`details${loading ? ' details-stale' : ''}`} aria-busy={loading}>
      <div className="details-head">
        <span className="details-name" title={d.path || d.name}>{d.name}</span>
        {d.path.includes('/') && <span className="details-path" title={d.path}>{d.path}</span>}
      </div>
      <dl className="details-stats">
        <div><dt>Size</dt><dd>{formatBytes(d.size)}</dd></div>
        {dir && <div><dt>Files</dt><dd>{d.files.toLocaleString()}</dd></div>}
        {dir && <div><dt>Folders</dt><dd>{d.dirs.toLocaleString()}</dd></div>}
        <div><dt>{dir ? 'Latest change' : 'Modified'}</dt><dd>{d.mtime ? <RelativeTime value={d.mtime * 1000} /> : '—'}</dd></div>
      </dl>
      {dir && d.types.length > 0 && (
        <div className="details-section">
          <div className="details-label">Largest types</div>
          {d.types.map(t => (
            <Meter key={t.ext} size="sm" tone="accent" value={largest ? t.bytes / largest : 0} max={1} showValue="end" minFill={2} trackClassName="details-track"
              label={<span className="details-type"><span>.{t.ext === '(none)' ? '—' : t.ext}</span><span className="details-type-count">{count(t.count, 'file')}</span></span>}
              valueText={() => formatBytes(t.bytes)} getAriaValueText={() => `${formatBytes(t.bytes)} in ${count(t.count, 'file')}`} />
          ))}
        </div>
      )}
      {(labels.length > 0 || d.labels.within.length > 0) && (
        <div className="details-section">
          {labels.length > 0 && <><div className="details-label">Labels</div><TagList tags={labels} /></>}
          {dir && d.labels.within.length > 0 && (
            <>
              <div className="details-label">Labels inside</div>
              <TagList tags={d.labels.within.map(l => ({ key: l.label, label: `${l.label} ${l.count.toLocaleString()}` }))} max={8} />
            </>
          )}
        </div>
      )}
      <div className="details-section details-source">
        <div className="details-label">Library</div>
        <div>{d.library.label} <span className="details-muted">· {d.library.kind}{d.library.writable ? ', read and write' : ', read only'}</span></div>
        {d.index && (
          <div className="details-index">
            <code title={d.index}>{d.index}</code>
            <CopyButton text={d.index} label="index path" size="xs" />
          </div>
        )}
        {!d.fullText && <div className="details-muted">Names and metadata only: this index has no text.</div>}
      </div>
    </div>
  )
}
