import { useCallback, useEffect, useMemo, useState } from 'react'

/**
 * The corpus over time: pick a moment on the scrubber and the panel shows
 * the knowledge base as the index found it then, with the changes that
 * arrived at that moment listed beside it.
 *
 * The receding stack behind the panel is the older snapshots. It is a
 * borrowed metaphor and it earns its place by making the direction of
 * travel obvious: the depth is time, and the scrubber moves through it.
 *
 * Snapshots hold the census, not the bytes, so a file can be inspected as
 * a name, a size, and a date, and opened only if it still exists today.
 */

interface Snap { id: string; takenAt: number; files: number; bytes: number; disk: number }
interface Check { at: number }
interface LevelDir { name: string; path: string; files: number; bytes: number }
interface LevelFile { name: string; path: string; size: number; mtime: number }
interface Level { available: boolean; dir?: string; dirs?: LevelDir[]; files?: LevelFile[]; totals?: { files: number; bytes: number } }
interface Diff {
  available: boolean
  counts?: { added: number; removed: number; modified: number; renamed: number }
  added?: { path: string }[]
  removed?: { path: string }[]
  modified?: { path: string }[]
  renamed?: { from: string; to: string }[]
}

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(2)} GB`
}

function when(ts: number, long = false): string {
  const d = new Date(ts * 1000)
  return d.toLocaleString(undefined, long
    ? { weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }
    : { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function HistoryView({ onOpen }: { onOpen: (path: string) => void }) {
  const [snaps, setSnaps] = useState<Snap[] | null>(null)
  const [stored, setStored] = useState(0)
  const [checks, setChecks] = useState<Check[]>([])
  const [idx, setIdx] = useState(0)
  const [dir, setDir] = useState('')
  const [level, setLevel] = useState<Level | null>(null)
  const [diff, setDiff] = useState<Diff | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => fetch('/api/kb/history')
    .then(r => r.json())
    .then((d: { snapshots?: Snap[]; storedBytes?: number; checks?: Check[] }) => {
      const list = d.snapshots ?? []
      setSnaps(list)
      setStored(d.storedBytes ?? 0)
      setChecks(d.checks ?? [])
      setIdx(Math.max(0, list.length - 1))
    })
    .catch(() => setSnaps([])), [])

  useEffect(() => { load() }, [load])

  const current = snaps && snaps.length > 0 ? snaps[Math.min(idx, snaps.length - 1)] : null

  useEffect(() => {
    if (!current) return
    fetch(`/api/kb/history/level?id=${current.id}&dir=${encodeURIComponent(dir)}`)
      .then(r => r.json()).then(setLevel).catch(() => setLevel(null))
  }, [current, dir])

  useEffect(() => {
    if (!snaps || !current || idx === 0) { setDiff(null); return }
    fetch(`/api/kb/history/diff?from=${snaps[idx - 1].id}&to=${current.id}`)
      .then(r => r.json()).then(setDiff).catch(() => setDiff(null))
  }, [snaps, current, idx])

  // Arrow keys travel through time, which is the whole point of a scrubber.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!snaps) return
      if (e.key === 'ArrowUp') { setIdx(i => Math.min(snaps.length - 1, i + 1)); e.preventDefault() }
      if (e.key === 'ArrowDown') { setIdx(i => Math.max(0, i - 1)); e.preventDefault() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [snaps])

  const capture = async () => {
    setBusy(true)
    try {
      await fetch('/api/kb/history/capture', { method: 'POST' })
      await load()
    } finally { setBusy(false) }
  }

  // Deleting the newest snapshot of a live corpus is not an error: the
  // reload's visit captures a fresh one, so the timeline stays truthful.
  const remove = (id: string) =>
    fetch(`/api/kb/history/snapshot?id=${id}`, { method: 'DELETE' }).then(load)

  const pruneTo = (keep: number) =>
    fetch('/api/kb/history/prune', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ keep }),
    }).then(load)

  const crumbs = useMemo(() => {
    const parts = dir ? dir.split('/') : []
    return parts.map((p, i) => ({ name: p, path: parts.slice(0, i + 1).join('/') }))
  }, [dir])

  if (snaps === null) return <div className="hist-view"><p className="muted pad">Loading history…</p></div>

  if (snaps.length === 0) {
    return (
      <div className="hist-view">
        <div className="empty-state">
          <h2>No history yet</h2>
          <p className="muted">
            A snapshot is recorded after each index pass, and the timeline needs at least one to start.
            Snapshots hold the list of files with their sizes and dates, not their contents.
          </p>
          <button className="btn-primary" onClick={capture} disabled={busy}>{busy ? 'Capturing…' : 'Capture the first snapshot'}</button>
        </div>
      </div>
    )
  }

  const changed = diff?.counts
    ? diff.counts.added + diff.counts.modified + diff.counts.renamed + diff.counts.removed
    : 0

  // The body is built from the same pieces as the Stats page (card ov-list
  // sections, 13px h3 headings, ov-row rows) so it matches the rest of the
  // app by construction; only the layout grid and the timeline ticks are
  // this view's own.
  return (
    <div className="hist-view">
      <div className="card ov-head">
        <h1 className="view-title">Corpus history</h1>
        <p className="muted view-sub">
          The knowledge base as the index found it at each pass. Pick a moment on the timeline, browse what the
          corpus held then, and see what that pass changed. Snapshots record the file list with sizes and
          dates, not file contents, so opening a file here shows it as it is today.
        </p>
      </div>

      <div className="ov-tiles hist-tiles">
        <div className="card ov-tile"><span className="stat-num">{snaps.length}</span><span className="stat-label">snapshots · {bytes(stored)} on disk</span></div>
        <div className="card ov-tile"><span className="stat-num">{current!.files.toLocaleString()}</span><span className="stat-label">files at this point</span></div>
        <div className="card ov-tile"><span className="stat-num">{bytes(current!.bytes)}</span><span className="stat-label">corpus size then</span></div>
        <div className="card ov-tile"><span className="stat-num">{idx === 0 ? '—' : changed}</span><span className="stat-label">changes in this pass</span></div>
      </div>

      <div className="hist-main">
        <section className="card ov-list">
          <div className="hist-head-row">
            <h3>
              Contents on {when(current!.takenAt, true)}
              <span className="muted hist-head-note">
                {idx === snaps.length - 1
                  ? ' · the most recent pass'
                  : ` · ${snaps.length - 1 - idx} pass${snaps.length - 1 - idx === 1 ? '' : 'es'} ago`}
              </span>
            </h3>
            <button className="btn-secondary" onClick={capture} disabled={busy}>{busy ? 'Capturing…' : 'Capture now'}</button>
          </div>
          <p className="hist-crumbs">
            <button className="link-button" onClick={() => setDir('')}>knowledge base</button>
            {crumbs.map(c => (
              <span key={c.path}> / <button className="link-button" onClick={() => setDir(c.path)}>{c.name}</button></span>
            ))}
          </p>
          {level?.dirs?.map(d => (
            <div key={d.path} className="ov-row" onClick={() => setDir(d.path)}>
              <span className="ov-row-path">{d.name}/</span>
              <span className="ov-row-meta">{d.files.toLocaleString()} files · {bytes(d.bytes)}</span>
            </div>
          ))}
          {level?.files?.map(f => (
            <div key={f.path} className="ov-row" onClick={() => onOpen(f.path)} title="Open the file as it is today">
              <span className="ov-row-path">{f.name}</span>
              <span className="ov-row-meta">{bytes(f.size)} · {when(f.mtime)}</span>
            </div>
          ))}
          {level && (level.dirs?.length ?? 0) + (level.files?.length ?? 0) === 0 && (
            <p className="muted">This directory held nothing at this point.</p>
          )}
        </section>

        <section className="card ov-list">
          <h3>What this pass changed</h3>
          {idx === 0 ? (
            <p className="muted">This is the earliest snapshot, so there is nothing before it to compare against.</p>
          ) : diff?.counts ? (
            <>
              <p className="hist-counts">
                <span className="hist-tally add">+{diff.counts.added} added</span>
                <span className="hist-tally mod">~{diff.counts.modified} changed</span>
                <span className="hist-tally move">→{diff.counts.renamed} moved</span>
                <span className="hist-tally del">−{diff.counts.removed} removed</span>
              </p>
              {[['Added', diff.added?.map(e => e.path)], ['Changed', diff.modified?.map(e => e.path)],
                ['Moved', diff.renamed?.map(e => e.to)], ['Removed', diff.removed?.map(e => e.path)]]
                .filter(([, list]) => (list as string[] | undefined)?.length)
                .map(([label, list]) => (
                  <div key={label as string} className="hist-changes">
                    <p className="hist-change-label">{label as string}</p>
                    {(list as string[]).slice(0, 12).map(p => (
                      <div key={p} className="ov-row" onClick={() => onOpen(p)}>
                        <span className="ov-row-path">{p}</span>
                      </div>
                    ))}
                    {(list as string[]).length > 12 && <p className="muted">and {(list as string[]).length - 12} more</p>}
                  </div>
                ))}
              {diff.counts.added + diff.counts.modified + diff.counts.renamed + diff.counts.removed === 0 && (
                <p className="muted">Nothing changed in this pass.</p>
              )}
            </>
          ) : <p className="muted">Comparing…</p>}
        </section>
      </div>

      <section className="card ov-list hist-rail" role="slider" aria-label="Snapshot" aria-valuenow={idx} aria-valuemin={0} aria-valuemax={snaps.length - 1}>
        <h3>Index passes</h3>
        <p className="muted hist-stored">{snaps.length} kept · {bytes(stored)} on disk</p>
        {/* Days the index was examined and found unchanged, shown above
            the snapshots so a quiet timeline reads as verified rather
            than as nothing watching. Only checks newer than the latest
            snapshot are informative; older ones are implied by it. */}
        {checks.filter(c => c.at > (snaps[snaps.length - 1]?.takenAt ?? 0)).reverse().map(c => (
          <span key={c.at} className="hist-check" title="The index was examined and nothing had changed.">
            <span className="hist-tick-label">{when(c.at)}</span> · checked, no change
          </span>
        ))}
        {[...snaps].reverse().map((s) => {
          const i = snaps.indexOf(s)
          return (
          <div key={s.id} className="hist-tick-row">
            <button
              className={`hist-tick${i === idx ? ' active' : ''}`}
              onClick={() => setIdx(i)}
              title={`${when(s.takenAt, true)} · ${s.files.toLocaleString()} files`}
            >
              <span className="hist-tick-label">{when(s.takenAt)}</span>
              {i === snaps.length - 1 && <span className="hist-now"> now</span>}
            </button>
            <button className="hist-tick-del" title={`Delete this snapshot (${bytes(s.disk)})`} onClick={() => remove(s.id)}>×</button>
          </div>
          )
        })}
        {snaps.length > 10 && (
          <button className="link-button hist-prune" onClick={() => pruneTo(10)}>Remove all but the last 10</button>
        )}
      </section>
    </div>
  )
}
