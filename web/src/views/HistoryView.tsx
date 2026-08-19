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

interface Snap { id: string; takenAt: number; files: number; bytes: number }
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
  const [idx, setIdx] = useState(0)
  const [dir, setDir] = useState('')
  const [level, setLevel] = useState<Level | null>(null)
  const [diff, setDiff] = useState<Diff | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => fetch('/api/kb/history')
    .then(r => r.json())
    .then((d: { snapshots?: Snap[] }) => {
      const list = d.snapshots ?? []
      setSnaps(list)
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

  const crumbs = useMemo(() => {
    const parts = dir ? dir.split('/') : []
    return parts.map((p, i) => ({ name: p, path: parts.slice(0, i + 1).join('/') }))
  }, [dir])

  // Ghost cards stand in for the snapshots behind this one, capped so a
  // long history does not build a wall.
  const behind = Math.min(4, idx)

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

  return (
    <div className="hist-view">
      <div className="card ov-head">
        <h1 className="view-title">Corpus history</h1>
        <p className="muted view-sub">
          The knowledge base as the index found it at each pass. Pick a moment on the right, browse what the
          corpus held then, and see what that pass changed. Snapshots record the file list with sizes and
          dates, not file contents, so opening a file here shows it as it is today.
        </p>
      </div>

      <div className="hist-tiles">
        <div className="card ov-tile"><span className="stat-num">{snaps.length}</span><span className="stat-label">snapshots kept</span></div>
        <div className="card ov-tile"><span className="stat-num">{current!.files.toLocaleString()}</span><span className="stat-label">files at this point</span></div>
        <div className="card ov-tile"><span className="stat-num">{bytes(current!.bytes)}</span><span className="stat-label">corpus size then</span></div>
        <div className="card ov-tile"><span className="stat-num">{idx === 0 ? '—' : changed}</span><span className="stat-label">changes in this pass</span></div>
      </div>

      <div className="hist-stage card">
        {Array.from({ length: behind }, (_, i) => (
          <div key={i} className="hist-ghost" style={{ '--depth': String(behind - i) } as React.CSSProperties} />
        ))}

        <div className="hist-card">
          <div className="hist-card-head">
            <div>
              <h3>{when(current!.takenAt, true)}</h3>
              <p className="muted">
                {idx === snaps.length - 1
                  ? 'The most recent index pass'
                  : `${snaps.length - 1 - idx} pass${snaps.length - 1 - idx === 1 ? '' : 'es'} ago`}
              </p>
            </div>
            <button className="btn-secondary" onClick={capture} disabled={busy}>{busy ? 'Capturing…' : 'Capture now'}</button>
          </div>

          <div className="hist-crumbs">
            <button className="link-button" onClick={() => setDir('')}>knowledge base</button>
            {crumbs.map(c => (
              <span key={c.path}> / <button className="link-button" onClick={() => setDir(c.path)}>{c.name}</button></span>
            ))}
          </div>

          <div className="hist-panes">
            <div className="hist-listing">
              <p className="hist-pane-label">Contents at this point</p>
              {level?.dirs?.map(d => (
                <button key={d.path} className="hist-row" onClick={() => setDir(d.path)}>
                  <span className="hist-name">{d.name}/</span>
                  <span className="muted">{d.files.toLocaleString()} files · {bytes(d.bytes)}</span>
                </button>
              ))}
              {level?.files?.map(f => (
                <button key={f.path} className="hist-row" onClick={() => onOpen(f.path)} title="Open the file as it is today">
                  <span className="hist-name">{f.name}</span>
                  <span className="muted">{bytes(f.size)} · {when(f.mtime)}</span>
                </button>
              ))}
              {level && (level.dirs?.length ?? 0) + (level.files?.length ?? 0) === 0 && (
                <p className="muted pad">This directory held nothing at this point.</p>
              )}
            </div>

            <div className="hist-side">
              <p className="hist-pane-label">What this pass changed</p>
              {idx === 0 ? (
                <p className="muted">This is the earliest snapshot, so there is nothing before it to compare against.</p>
              ) : diff?.counts ? (
                <>
                  <p className="hist-counts">
                    <span className="hist-tally add">+{diff.counts.added}</span>
                    <span className="hist-tally mod">~{diff.counts.modified}</span>
                    <span className="hist-tally move">→{diff.counts.renamed}</span>
                    <span className="hist-tally del">−{diff.counts.removed}</span>
                  </p>
                  {[['Added', diff.added?.map(e => e.path)], ['Changed', diff.modified?.map(e => e.path)],
                    ['Moved', diff.renamed?.map(e => e.to)], ['Removed', diff.removed?.map(e => e.path)]]
                    .filter(([, list]) => (list as string[] | undefined)?.length)
                    .map(([label, list]) => (
                      <div key={label as string} className="hist-changes">
                        <p className="hist-change-label">{label as string}</p>
                        <ul className="hist-change-list">
                          {(list as string[]).slice(0, 12).map(p => (
                            <li key={p}><button className="link-button" onClick={() => onOpen(p)}>{p}</button></li>
                          ))}
                          {(list as string[]).length > 12 && <li className="muted">and {(list as string[]).length - 12} more</li>}
                        </ul>
                      </div>
                    ))}
                  {diff.counts.added + diff.counts.modified + diff.counts.renamed + diff.counts.removed === 0 && (
                    <p className="muted">Nothing changed in this pass.</p>
                  )}
                </>
              ) : <p className="muted">Comparing…</p>}
            </div>
          </div>
        </div>
      </div>

      <div className="hist-scrubber card" role="slider" aria-label="Snapshot" aria-valuenow={idx} aria-valuemin={0} aria-valuemax={snaps.length - 1}>
        <p className="hist-scrub-title">Index passes</p>
        {[...snaps].reverse().map((s) => {
          const i = snaps.indexOf(s)
          return (
          <button
            key={s.id}
            className={`hist-tick${i === idx ? ' active' : ''}`}
            onClick={() => setIdx(i)}
            title={`${when(s.takenAt, true)} · ${s.files.toLocaleString()} files`}
          >
            <span className="hist-tick-label">{when(s.takenAt)}</span>
            {i === snaps.length - 1 && <span className="hist-now"> now</span>}
          </button>
          )
        })}
      </div>
    </div>
  )
}
