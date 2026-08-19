import { useEffect, useMemo, useState } from 'react'

/**
 * A timeline of the corpus: one point per index pass, showing how many
 * files the knowledge base held, with the changes between any two points
 * listed underneath.
 *
 * Snapshots hold the census the index already carries, not file contents,
 * so this answers what changed and when. Restoring a file is a separate
 * problem and belongs to the filesystem.
 */

interface Snap { id: string; takenAt: number; files: number; bytes: number }
interface Entry { path: string; size: number; mtime: number }
interface Diff {
  available: boolean
  from?: Snap
  to?: Snap
  added?: Entry[]
  removed?: Entry[]
  modified?: { path: string; size: number; wasSize: number }[]
  renamed?: { from: string; to: string; size: number }[]
  counts?: { added: number; removed: number; modified: number; renamed: number }
}

function when(ts: number): string {
  const d = new Date(ts * 1000)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}

function ChangeList({ label, items, onOpen }: {
  label: string
  items: { path: string; note?: string }[]
  onOpen: (p: string) => void
}) {
  if (items.length === 0) return null
  return (
    <div className="hist-changes">
      <p className="hist-change-label">{label}</p>
      <ul className="hist-change-list">
        {items.slice(0, 40).map(i => (
          <li key={i.path}>
            <button className="link-button" onClick={() => onOpen(i.path)}>{i.path}</button>
            {i.note && <span className="muted"> {i.note}</span>}
          </li>
        ))}
        {items.length > 40 && <li className="muted">and {items.length - 40} more</li>}
      </ul>
    </div>
  )
}

export function HistoryTimeline({ onOpen }: { onOpen: (path: string) => void }) {
  const [snaps, setSnaps] = useState<Snap[] | null>(null)
  const [sel, setSel] = useState<string | null>(null)
  const [diff, setDiff] = useState<Diff | null>(null)
  const [busy, setBusy] = useState(false)

  const load = () => fetch('/api/kb/history')
    .then(r => r.json())
    .then((d: { snapshots?: Snap[] }) => setSnaps(d.snapshots ?? []))
    .catch(() => setSnaps([]))

  useEffect(() => { load() }, [])

  // The newest point is the interesting one on arrival: it shows what the
  // last index pass changed.
  useEffect(() => {
    if (!snaps || snaps.length < 2) return
    setSel(s => s ?? snaps[snaps.length - 1].id)
  }, [snaps])

  useEffect(() => {
    if (!sel || !snaps || snaps.length < 2) return
    const i = snaps.findIndex(s => s.id === sel)
    if (i <= 0) { setDiff(null); return }
    fetch(`/api/kb/history/diff?from=${snaps[i - 1].id}&to=${sel}`)
      .then(r => r.json())
      .then(setDiff)
      .catch(() => setDiff(null))
  }, [sel, snaps])

  const capture = async () => {
    setBusy(true)
    try {
      await fetch('/api/kb/history/capture', { method: 'POST' })
      await load()
    } finally {
      setBusy(false)
    }
  }

  const geom = useMemo(() => {
    if (!snaps || snaps.length === 0) return null
    const max = Math.max(...snaps.map(s => s.files), 1)
    const first = snaps[0].takenAt
    const span = Math.max(1, snaps[snaps.length - 1].takenAt - first)
    return snaps.map(s => ({
      snap: s,
      // A single snapshot sits at the right edge rather than at zero width.
      x: snaps.length === 1 ? 100 : ((s.takenAt - first) / span) * 100,
      h: (s.files / max) * 100,
    }))
  }, [snaps])

  if (snaps === null) return <div className="ov-card"><h3 className="ov-span">Corpus timeline</h3><p className="muted">Loading…</p></div>

  if (snaps.length === 0) {
    return (
      <div className="ov-card">
        <h3 className="ov-span">Corpus timeline</h3>
        <p className="muted">
          No snapshots yet. One is taken after each index pass; capture the first now to start the timeline.
        </p>
        <button className="btn-secondary" onClick={capture} disabled={busy}>{busy ? 'Capturing…' : 'Capture now'}</button>
      </div>
    )
  }

  return (
    <div className="ov-card">
      <h3 className="ov-span">Corpus timeline</h3>
      <div className="hist-axis">
        {geom!.map(g => (
          <button
            key={g.snap.id}
            className={`hist-point${sel === g.snap.id ? ' active' : ''}`}
            style={{ left: `${g.x}%`, height: `${Math.max(8, g.h)}%` }}
            title={`${when(g.snap.takenAt)} · ${g.snap.files.toLocaleString()} files · ${bytes(g.snap.bytes)}`}
            onClick={() => setSel(g.snap.id)}
          />
        ))}
      </div>
      <div className="hist-legend">
        <span className="muted">{when(snaps[0].takenAt)}</span>
        <span className="muted">{snaps.length} snapshot{snaps.length === 1 ? '' : 's'}</span>
        <span className="muted">{when(snaps[snaps.length - 1].takenAt)}</span>
      </div>

      {diff?.available && diff.counts ? (
        <div className="hist-detail">
          <p className="hist-summary">
            {when(diff.from!.takenAt)} to {when(diff.to!.takenAt)}:{' '}
            {diff.counts.added} added, {diff.counts.removed} removed, {diff.counts.modified} changed,{' '}
            {diff.counts.renamed} moved. {diff.to!.files.toLocaleString()} files, {bytes(diff.to!.bytes)}.
          </p>
          <ChangeList label="Added" items={(diff.added ?? []).map(e => ({ path: e.path, note: bytes(e.size) }))} onOpen={onOpen} />
          <ChangeList label="Changed" items={(diff.modified ?? []).map(e => ({ path: e.path, note: `${bytes(e.wasSize)} to ${bytes(e.size)}` }))} onOpen={onOpen} />
          <ChangeList label="Moved" items={(diff.renamed ?? []).map(e => ({ path: e.to, note: `from ${e.from}` }))} onOpen={onOpen} />
          <ChangeList label="Removed" items={(diff.removed ?? []).map(e => ({ path: e.path, note: bytes(e.size) }))} onOpen={onOpen} />
          {diff.counts.added + diff.counts.removed + diff.counts.modified + diff.counts.renamed === 0 && (
            <p className="muted">Nothing changed between these two passes.</p>
          )}
        </div>
      ) : (
        <p className="muted">
          {snaps.length < 2
            ? 'One snapshot so far. The next index pass adds a second and the changes between them appear here.'
            : 'Select a point to see what changed since the pass before it.'}
        </p>
      )}
      <button className="btn-secondary" onClick={capture} disabled={busy}>{busy ? 'Capturing…' : 'Capture now'}</button>
    </div>
  )
}
