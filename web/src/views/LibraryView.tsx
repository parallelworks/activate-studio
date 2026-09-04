import { useCallback, useEffect, useRef, useState } from 'react'
import type { Display } from '../App'
import { api } from '../api'
import { Explorer, visibleRowPaths } from '../components/Explorer'
import { Viewer } from '../components/Viewer'
import { DagViewer } from '../components/DagViewer'
import { UploadMenu } from '../components/UploadMenu'
import { TagMenu } from '../components/TagMenu'
import { useAppConfig } from '../config'
import { collectDropped, uploadBatched, UploadProgress } from '../upload'

const RAIL_MIN = 220
const RAIL_MAX = 520

/** Human-sized byte counts for the upload panel. */
function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export function LibraryView({ display, onDisplay }: {
  display: Display | null
  onDisplay: (d: Display | null) => void
}) {
  const [targetDir, setTargetDir] = useState('uploads')
  const [refreshKey, setRefreshKey] = useState(0)
  const [railWidth, setRailWidth] = useState(() => Number(localStorage.getItem('ade-rail-width')) || 320)
  const [railCollapsed, setRailCollapsed] = useState(() => localStorage.getItem('ade-rail-collapsed') === '1')
  const [progress, setProgress] = useState<UploadProgress | null>(null)
  const [multiSel, setMultiSel] = useState<Set<string>>(new Set())
  const [tagMenuOpen, setTagMenuOpen] = useState(false)
  const [selectMode, setSelectMode] = useState(false)
  const [moveMenuOpen, setMoveMenuOpen] = useState(false)
  const [allDirs, setAllDirs] = useState<string[]>([])
  /** Proposed labels awaiting review; nothing is applied until accepted. */
  const [suggest, setSuggest] = useState<{
    dir: string
    busy: boolean
    error?: string
    considered?: number
    rows: { path: string; labels: string[]; why: string; accept: boolean }[]
  } | null>(null)
  const [showLabels, setShowLabels] = useState(() => localStorage.getItem('ade-tree-labels') === '1')
  const [tagsTick, setTagsTick] = useState(0)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [bulkNote, setBulkNote] = useState('')
  // Right-click context menu on tree rows.
  const [ctx, setCtx] = useState<{ path: string; isDir: boolean; x: number; y: number } | null>(null)
  const [ctxConfirm, setCtxConfirm] = useState(false)
  const [ctxNewDir, setCtxNewDir] = useState<string | null>(null)
  const [ctxBusy, setCtxBusy] = useState(false)
  const [ctxErr, setCtxErr] = useState('')
  const [ctxRename, setCtxRename] = useState<string | null>(null)
  /** Cut or copied paths, waiting for a destination. */
  const [clipboard, setClipboard] = useState<{ mode: 'cut' | 'copy'; paths: string[] } | null>(null)

  const closeCtx = () => { setCtx(null); setCtxConfirm(false); setCtxNewDir(null); setCtxRename(null); setCtxBusy(false); setCtxErr('') }

  const ctxDelete = async () => {
    if (!ctx) return
    setCtxBusy(true)
    setCtxErr('')
    try {
      if (ctx.isDir) await api.deleteDir(ctx.path)
      else await api.deleteFile(ctx.path)
      if (display?.kind === 'file' && (display.target === ctx.path || display.target.startsWith(ctx.path + '/'))) onDisplay(null)
      setRefreshKey(k => k + 1)
      window.dispatchEvent(new Event('ade-kb-changed'))
      closeCtx()
    } catch (e) {
      setCtxErr(String((e as Error).message ?? e))
      setCtxBusy(false)
    }
  }

  const ctxDoRename = async () => {
    if (!ctx || ctxRename === null || !ctxRename.trim()) return
    setCtxBusy(true)
    setCtxErr('')
    try {
      const r = await api.rename(ctx.path, ctxRename.trim())
      // A renamed file that is open in the viewer should follow its name.
      if (r.renamed && !ctx.isDir && selectedFile === ctx.path) onDisplay({ kind: 'file', target: r.renamed.to })
      window.dispatchEvent(new CustomEvent('ade-kb-changed'))
      closeCtx()
    } catch (e) {
      setCtxErr(String((e as Error).message ?? e))
      setCtxBusy(false)
    }
  }

  const ctxMakeDir = async () => {
    if (!ctx || !ctxNewDir?.trim()) return
    setCtxBusy(true)
    setCtxErr('')
    try {
      const name = ctxNewDir.trim().replace(/^\/+|\/+$/g, '')
      const r = await api.mkdir(ctx.path ? `${ctx.path}/${name}` : name)
      setTargetDir(r.created)
      setRefreshKey(k => k + 1)
      window.dispatchEvent(new Event('ade-kb-changed'))
      closeCtx()
    } catch (e) {
      setCtxErr(String((e as Error).message ?? e))
      setCtxBusy(false)
    }
  }

  const bulkDelete = async () => {
    const files = [...multiSel]
    setConfirmDelete(false)
    setBulkNote('Deleting…')
    try {
      const r = await api.deleteFiles(files)
      const skippedDirs = r.skipped.filter((x: { reason: string }) => x.reason.includes('directories'))
      setBulkNote(`${r.deleted.length} deleted${r.skipped.length ? `, ${r.skipped.length} skipped${skippedDirs.length ? ' (directories are not deletable)' : ''}` : ''}`)
      setMultiSel(new Set())
      setRefreshKey(k => k + 1)
      if (display?.kind === 'file' && r.deleted.includes(display.target)) onDisplay(null)
    } catch (e) {
      setBulkNote(String((e as Error).message ?? e))
    }
    setTimeout(() => setBulkNote(''), 6000)
  }
  const drag = useRef<{ startX: number; startWidth: number } | null>(null)
  const cfg = useAppConfig()

  useEffect(() => { localStorage.setItem('ade-rail-width', String(railWidth)) }, [railWidth])
  useEffect(() => { localStorage.setItem('ade-rail-collapsed', railCollapsed ? '1' : '0') }, [railCollapsed])

  useEffect(() => {
    const onChange = () => setRefreshKey(k => k + 1)
    const onTags = () => setTagsTick(k => k + 1)
    window.addEventListener('ade-kb-changed', onChange)
    window.addEventListener('ade-tags-changed', onTags)
    return () => {
      window.removeEventListener('ade-kb-changed', onChange)
      window.removeEventListener('ade-tags-changed', onTags)
    }
  }, [])

  useEffect(() => { localStorage.setItem('ade-tree-labels', showLabels ? '1' : '0') }, [showLabels])

  // Cut, copy and paste from the keyboard, ignored while typing so a
  // rename box or a search field keeps its own shortcuts.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey)) return
      const el = e.target as HTMLElement | null
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return
      const paths = [...multiSel]
      if (e.key === 'x' && paths.length) { setClipboard({ mode: 'cut', paths }); e.preventDefault() }
      else if (e.key === 'c' && paths.length) { setClipboard({ mode: 'copy', paths }); e.preventDefault() }
      else if (e.key === 'v' && clipboard?.paths.length) { void pasteInto(targetDir === 'uploads' ? '' : targetDir); e.preventDefault() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  // The row a range extends from: the last one clicked without shift.
  const anchor = useRef<string | null>(null)
  /** Live while an upload is in flight, so it can be stopped. */
  const uploadAbort = useRef<AbortController | null>(null)

  const toggleMulti = (p: string) => {
    anchor.current = p
    setMultiSel(s => {
      const next = new Set(s)
      if (next.has(p)) next.delete(p)
      else next.add(p)
      return next
    })
  }

  /** Shift-click selects everything between the anchor and this row, in the
   *  order the rows appear on screen. */
  const selectRange = (p: string) => {
    const rows = visibleRowPaths()
    const from = anchor.current && rows.includes(anchor.current) ? rows.indexOf(anchor.current) : rows.indexOf(p)
    const to = rows.indexOf(p)
    if (from < 0 || to < 0) return
    const [lo, hi] = from <= to ? [from, to] : [to, from]
    setSelectMode(true)
    setMultiSel(s => {
      const next = new Set(s)
      for (const row of rows.slice(lo, hi + 1)) next.add(row)
      return next
    })
    anchor.current ??= p
  }

  /** Paste whatever was cut or copied into a directory. */
  const pasteInto = async (dir: string) => {
    if (!clipboard?.paths.length) return
    if (clipboard.mode === 'cut') {
      await moveInto(dir, clipboard.paths)
      setClipboard(null)
      return
    }
    const paths = clipboard.paths
    const p: UploadProgress = {
      kind: 'move', phase: 'uploading', total: paths.length, done: 0, bytes: 0, totalBytes: 0,
      current: paths[0], failed: [], indexMs: 0, finished: false, dir,
    }
    setProgress({ ...p })
    try {
      const job = await api.copyJob(paths, dir)
      for (;;) {
        await new Promise(r => setTimeout(r, 500))
        const s = await api.job(job.id)
        p.done = s.done
        p.current = s.current
        p.phase = s.phase === 'indexing' ? 'indexing' : 'uploading'
        if (s.state === 'done') {
          const r = s.result as { moved: unknown[]; skipped: { path: string; reason: string }[] } | null
          p.done = r?.moved.length ?? s.done
          p.failed = (r?.skipped ?? []).map(x => ({ name: x.path, error: x.reason }))
          p.indexMs = s.ms ?? 0
          break
        }
        if (s.state === 'error') { p.indexError = s.error ?? 'copy failed'; break }
        setProgress({ ...p })
      }
    } catch (e) {
      p.indexError = String((e as Error).message ?? e)
    }
    p.phase = 'done'
    p.finished = true
    setProgress({ ...p })
    window.dispatchEvent(new CustomEvent('ade-kb-changed'))
  }

  /** Ask the model what this material should be labelled. */
  const runSuggest = async (dir: string) => {
    setSuggest({ dir, busy: true, rows: [] })
    try {
      const r = await api.labelSuggestions(dir, 40)
      setSuggest({
        dir, busy: false, considered: r.considered,
        rows: r.proposals.map(p2 => ({ ...p2, accept: true })),
      })
    } catch (e) {
      setSuggest({ dir, busy: false, rows: [], error: String((e as Error).message ?? e) })
    }
  }

  /** Apply the accepted proposals, grouped so identical label sets go in
   *  one call rather than one per file. */
  const applySuggestions = async () => {
    if (!suggest) return
    const groups = new Map<string, string[]>()
    for (const row of suggest.rows.filter(r => r.accept && r.labels.length)) {
      const key = row.labels.join(',')
      groups.set(key, [...(groups.get(key) ?? []), row.path])
    }
    setSuggest({ ...suggest, busy: true })
    for (const [labels, paths] of groups) {
      await api.applyTags(paths, labels.split(','), []).catch(() => {})
    }
    setSuggest(null)
    window.dispatchEvent(new CustomEvent('ade-tags-changed'))
    window.dispatchEvent(new CustomEvent('ade-kb-changed'))
  }

  /** Hand a path to the chat as a reference, the way an editor does. */
  const addToChat = (path: string) => {
    window.dispatchEvent(new CustomEvent('ade-chat-insert', { detail: { text: path } }))
    location.hash = '#view=chat'
  }

  /** Drag a row (or the whole selection) onto a directory to move it, or
   *  pick a destination for the selection. Runs as a job so a few hundred
   *  files report progress instead of freezing the panel. */
  const moveInto = async (dir: string, paths: string[]) => {
    const clean = paths.filter(p => p && p !== dir && !dir.startsWith(`${p}/`))
    if (!clean.length) return
    const p: UploadProgress = {
      kind: 'move', phase: 'uploading', total: clean.length, done: 0, bytes: 0, totalBytes: 0,
      current: clean[0], failed: [], indexMs: 0, finished: false, dir,
    }
    setProgress({ ...p })
    try {
      const job = await api.moveJob(clean, dir)
      for (;;) {
        await new Promise(r => setTimeout(r, 500))
        const s = await api.job(job.id)
        p.done = s.done
        p.current = s.current
        p.phase = s.phase === 'indexing' ? 'indexing' : 'uploading'
        if (s.state === 'done') {
          const r = s.result as { moved: unknown[]; skipped: { path: string; reason: string }[] } | null
          p.done = r?.moved.length ?? s.done
          p.failed = (r?.skipped ?? []).map(x => ({ name: x.path, error: x.reason }))
          p.indexMs = s.ms ?? 0
          break
        }
        if (s.state === 'error') { p.indexError = s.error ?? 'move failed'; break }
        setProgress({ ...p })
      }
    } catch (e) {
      p.indexError = String((e as Error).message ?? e)
    }
    p.phase = 'done'
    p.finished = true
    setProgress({ ...p })
    setMultiSel(new Set())
    window.dispatchEvent(new CustomEvent('ade-kb-changed'))
  }

  const railRef = useRef<HTMLElement>(null)

  const onDividerDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = railWidth
    let w = startW
    const onMove = (ev: MouseEvent) => {
      w = Math.min(RAIL_MAX, Math.max(RAIL_MIN, startW + (ev.clientX - startX)))
      if (railRef.current) railRef.current.style.width = `${w}px`
    }
    const onUp = () => {
      document.body.classList.remove('resizing')
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      setRailWidth(w)
    }
    document.body.classList.add('resizing')
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }, [railWidth])

  const uploadTo = useCallback(async (dir: string, items: DataTransferItemList) => {
    const files = await collectDropped(items)
    if (!files.length) return
    uploadAbort.current = new AbortController()
    const result = await uploadBatched(files, dir, setProgress, uploadAbort.current.signal)
    uploadAbort.current = null
    setRefreshKey(k => k + 1)
    if (result.done > 0 && files.length === 1) {
      onDisplay({ kind: 'file', target: (dir ? dir + '/' : '') + files[0].rel })
    }
  }, [onDisplay])

  const selectedFile = display?.kind === 'file' ? display.target : null
  const [dropOver, setDropOver] = useState(false)

  return (
    <div
      className={`library ${dropOver ? 'drop-over' : ''}`}
      onDragOver={e => { e.preventDefault(); setDropOver(true) }}
      onDragLeave={() => setDropOver(false)}
      onDrop={e => { e.preventDefault(); setDropOver(false); void uploadTo(targetDir, e.dataTransfer.items) }}
    >
      <div className="library-card card">
        {railCollapsed ? (
          <div className="rail-strip">
            <button className="rail-expander" title="Expand knowledge base tree" onClick={() => setRailCollapsed(false)}>»</button>
          </div>
        ) : (
          <>
            <aside ref={railRef} className="library-rail" style={{ width: railWidth }}>
              <div className="rail-head" title={cfg.kbLabel}>
                <span className="rail-title">Library</span>
                <span className="rail-actions">
                  <button
                    className={`rail-collapse select-toggle ${showLabels ? 'active' : ''}`}
                    title="Show applied labels in the tree"
                    onClick={() => setShowLabels(v => !v)}
                  >
                    <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2 2h5.2L14 8.8a1 1 0 0 1 0 1.4L10.2 14a1 1 0 0 1-1.4 0L2 7.2V2z"/><circle cx="5.2" cy="5.2" r="1" fill="currentColor" stroke="none"/></svg>
                  </button>
                  <button
                    className={`rail-collapse select-toggle ${selectMode ? 'active' : ''}`}
                    title="Select multiple items for labeling or deletion"
                    onClick={() => { setSelectMode(m => !m); setMultiSel(new Set()); setTagMenuOpen(false); setConfirmDelete(false) }}
                  >
                    <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><rect x="2" y="2" width="8" height="8" rx="2"/><path d="M4.5 6l1.6 1.6L9 4.7"/><path d="M12.5 6.5v5a1.5 1.5 0 0 1-1.5 1.5H6"/></svg>
                  </button>
                  <button
                    className="rail-collapse"
                    title="Refresh the tree (picks up files added outside the app, like chat-session exports)"
                    onClick={() => setRefreshKey(k => k + 1)}
                  >
                    <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9"/><path d="M13.5 1.5v3h-3"/></svg>
                  </button>
                  <UploadMenu
                    targetDir={targetDir}
                    onTargetDir={setTargetDir}
                    onDone={paths => {
                      setRefreshKey(k => k + 1)
                      if (paths[0]) onDisplay({ kind: 'file', target: paths[0] })
                    }}
                    onProgress={setProgress}
                  />
                  <button className="rail-collapse" title="Collapse tree" onClick={() => setRailCollapsed(true)}>«</button>
                </span>
              </div>
              {selectMode && (
                <div className="multi-bar">
                  <span>{multiSel.size} selected</span>
                  <button className="btn-secondary" disabled={!multiSel.size} onClick={() => setTagMenuOpen(o => !o)}>Labels…</button>
                  <button className="btn-secondary" disabled={!multiSel.size}
                    onClick={async () => {
                      setMoveMenuOpen(o => !o)
                      if (!allDirs.length) setAllDirs((await api.dirs().catch(() => ({ dirs: [] }))).dirs)
                    }}>Move to…</button>
                  {confirmDelete ? (
                    <span className="confirm-group">
                      <button className="btn-danger" onClick={bulkDelete}>Delete {multiSel.size}</button>
                      <button className="btn-secondary" onClick={() => setConfirmDelete(false)}>Cancel</button>
                    </span>
                  ) : (
                    <button className="btn-danger-outline" disabled={!multiSel.size} onClick={() => setConfirmDelete(true)}>Delete…</button>
                  )}
                  <button className="link-btn2" onClick={() => setMultiSel(new Set())}>clear</button>
                  {moveMenuOpen && (
                    <div className="move-menu">
                      <select className="field" defaultValue=""
                        onChange={e => {
                          const dest = e.target.value
                          if (dest === '') return
                          setMoveMenuOpen(false)
                          void moveInto(dest === '/' ? '' : dest, [...multiSel])
                        }}>
                        <option value="">Move {multiSel.size} {multiSel.size === 1 ? 'item' : 'items'} to…</option>
                        <option value="/">{cfg.kbLabel} (root)</option>
                        {allDirs.map(d => <option key={d} value={d}>{d}</option>)}
                      </select>
                    </div>
                  )}
                  {tagMenuOpen && (
                    <TagMenu
                      paths={[...multiSel]}
                      onClose={() => setTagMenuOpen(false)}
                      onApplied={() => window.dispatchEvent(new Event('ade-tags-changed'))}
                    />
                  )}
                </div>
              )}
              {bulkNote && <div className="drop-note">{bulkNote}</div>}
              <Explorer
                refreshTick={refreshKey}
                onOpen={p => { anchor.current = p; onDisplay({ kind: 'file', target: p }) }}
                onDirFocus={dir => { anchor.current = dir; setTargetDir(dir || 'uploads') }}
                selected={selectedFile}
                rootLabel={cfg.kbLabel}
                multiSelected={multiSel}
                onToggleMulti={toggleMulti}
                onDropInto={(dir, items) => void uploadTo(dir, items)}
                onSelectRange={selectRange}
                onMoveInto={(dir, paths) => void moveInto(dir, paths)}
                onLabel={p => { setMultiSel(new Set([p])); setTagMenuOpen(true); setSelectMode(true) }}
                onContext={(path, isDir, x, y) => { setCtxConfirm(false); setCtxNewDir(null); setCtxErr(''); setCtx({ path, isDir, x, y }) }}
                selectMode={selectMode}
                showLabels={showLabels || selectMode}
                tagsTick={tagsTick}
              />
              {suggest && (
                <div className="upload-panel suggest-panel">
                  <div className="upload-panel-head">
                    <span>
                      {suggest.busy && !suggest.rows.length
                        ? `Reading ${suggest.dir || 'the library'} and proposing labels…`
                        : suggest.error
                          ? 'Could not propose labels'
                          : `${suggest.rows.length} of ${suggest.considered ?? 0} unlabelled files have proposals`}
                    </span>
                    <button className="link-btn2" onClick={() => setSuggest(null)}>dismiss</button>
                  </div>
                  {suggest.error && <div className="upload-failed">{suggest.error}</div>}
                  {!suggest.busy && !suggest.error && !suggest.rows.length && (
                    <div className="muted suggest-empty">Nothing to propose here. Everything already carries a label, or there was no text to read.</div>
                  )}
                  {suggest.rows.length > 0 && (
                    <>
                      <div className="suggest-rows">
                        {suggest.rows.map((row, i) => (
                          <label className="suggest-row" key={row.path} title={row.why}>
                            <input
                              type="checkbox"
                              checked={row.accept}
                              onChange={e => setSuggest(s2 => s2 && {
                                ...s2,
                                rows: s2.rows.map((r, j) => (j === i ? { ...r, accept: e.target.checked } : r)),
                              })}
                            />
                            <span className="suggest-path">{row.path}</span>
                            <span className="suggest-tags">{row.labels.map(l => <code key={l}>{l}</code>)}</span>
                          </label>
                        ))}
                      </div>
                      <div className="suggest-actions">
                        <button className="btn-primary" disabled={suggest.busy || !suggest.rows.some(r => r.accept)}
                          onClick={() => void applySuggestions()}>
                          {suggest.busy ? 'Applying…' : `Apply ${suggest.rows.filter(r => r.accept).length}`}
                        </button>
                        <button className="btn-secondary" onClick={() => setSuggest(s2 => s2 && { ...s2, rows: s2.rows.map(r => ({ ...r, accept: false })) })}>
                          Clear all
                        </button>
                      </div>
                    </>
                  )}
                </div>
              )}
              {progress && (
                <div className="upload-panel">
                  <div className="upload-panel-head">
                    <span>
                      {progress.phase === 'done'
                        ? progress.cancelled
                          ? `Cancelled: ${progress.done} of ${progress.total} had already been added to ${progress.dir || 'root'}`
                          : `${progress.done} of ${progress.total} ${progress.kind === 'move' ? 'moved to' : 'added to'} ${progress.dir || 'root'}${progress.indexMs ? `, indexed in ${(progress.indexMs / 1000).toFixed(1)}s` : ''}`
                        : progress.phase === 'indexing'
                          ? `Indexing ${progress.done} ${progress.done === 1 ? 'item' : 'items'} in ${progress.dir || 'root'}…`
                          : progress.kind === 'move'
                            ? `Moving into ${progress.dir || 'root'}: ${progress.done} / ${progress.total}`
                            : `Uploading to ${progress.dir || 'root'}: ${progress.done} / ${progress.total}${progress.totalBytes ? ` (${fmtBytes(progress.bytes)} of ${fmtBytes(progress.totalBytes)})` : ''}`}
                    </span>
                    {progress.phase === 'uploading' && progress.kind !== 'move' && uploadAbort.current ? (
                      <button className="link-btn2" onClick={() => uploadAbort.current?.abort()}>cancel</button>
                    ) : (
                      <button className="link-btn2" onClick={() => setProgress(null)}>dismiss</button>
                    )}
                  </div>
                  {/* Bytes drive the bar while uploading, so one large file
                      still moves; indexing has no count to report, so the
                      bar runs indeterminate until the job comes back. */}
                  <div className={`upload-bar ${progress.phase === 'indexing' ? 'indeterminate' : ''}`}>
                    <div style={{ width: progress.phase === 'indexing' ? '100%' : `${(progress.totalBytes && progress.kind !== 'move' ? progress.bytes / progress.totalBytes : progress.done / Math.max(progress.total, 1)) * 100}%` }} />
                  </div>
                  {progress.phase !== 'done' && progress.current && (
                    <div className="upload-current">{progress.current}</div>
                  )}
                  {progress.indexError && (
                    <div className="upload-failed">Uploaded, but indexing failed, so these files are not searchable yet: {progress.indexError}</div>
                  )}
                  {progress.failed.length > 0 && (
                    <div className="upload-failed">{progress.failed.length} failed: {progress.failed.slice(0, 3).map(f => f.name).join(', ')}{progress.failed.length > 3 ? '…' : ''}</div>
                  )}
                </div>
              )}
            </aside>
            <div className="divider" onMouseDown={onDividerDown} title="Drag to resize" />
          </>
        )}
        <section className="library-main">
          {display?.kind === 'workflow_dag' ? (
            <DagViewer workflow={display.target} />
          ) : (
            <Viewer
              path={selectedFile}
              find={display?.kind === 'file' ? display.q : undefined}
              onDeleted={() => { onDisplay(null); setRefreshKey(k => k + 1) }}
            />
          )}
        </section>
      </div>
      {ctx && (
        <>
          <div className="scope-overlay" onClick={closeCtx} onContextMenu={e => { e.preventDefault(); closeCtx() }} />
          <div
            className="ctx-menu card"
            style={{ left: Math.min(ctx.x, window.innerWidth - 230), top: Math.min(ctx.y, window.innerHeight - 210) }}
          >
            <div className="ctx-title" title={ctx.path || cfg.kbLabel}>{ctx.path ? ctx.path.split('/').pop() : cfg.kbLabel}</div>
            {!ctx.isDir && (
              <button onClick={() => { onDisplay({ kind: 'file', target: ctx.path }); closeCtx() }}>Open</button>
            )}
            {ctx.isDir && (ctxNewDir === null ? (
              <button onClick={() => setCtxNewDir('')}>New folder</button>
            ) : (
              <div className="ctx-input">
                <input
                  className="field"
                  autoFocus
                  value={ctxNewDir}
                  placeholder="folder name"
                  onChange={e => setCtxNewDir(e.target.value)}
                  onKeyDown={e => { if (e.key === 'Enter') void ctxMakeDir(); if (e.key === 'Escape') setCtxNewDir(null) }}
                />
                <button className="btn-secondary" disabled={ctxBusy || !ctxNewDir.trim()} onClick={() => void ctxMakeDir()}>Create</button>
              </div>
            ))}
            {ctx.path && (ctxRename === null ? (
              <button onClick={() => setCtxRename(ctx.path.split('/').pop() ?? '')}>Rename…</button>
            ) : (
              <div className="ctx-input">
                <input
                  className="field"
                  autoFocus
                  value={ctxRename}
                  placeholder="new name"
                  onChange={e => setCtxRename(e.target.value)}
                  onFocus={e => {
                    // Select the stem, leaving the extension alone, which is
                    // what a rename usually changes.
                    const dot = e.target.value.lastIndexOf('.')
                    e.target.setSelectionRange(0, dot > 0 ? dot : e.target.value.length)
                  }}
                  onKeyDown={e => { if (e.key === 'Enter') void ctxDoRename(); if (e.key === 'Escape') setCtxRename(null) }}
                />
                <button className="btn-secondary" disabled={ctxBusy || !ctxRename.trim()} onClick={() => void ctxDoRename()}>
                  {ctxBusy ? 'Renaming…' : 'Rename'}
                </button>
              </div>
            ))}
            {ctx.isDir && (
              <button onClick={() => { void runSuggest(ctx.path); closeCtx() }}>Suggest labels…</button>
            )}
            {ctx.path && (
              <button onClick={() => { addToChat(ctx.path); closeCtx() }}>Add to chat</button>
            )}
            {ctx.path && (
              <button onClick={async () => {
                await navigator.clipboard.writeText(ctx.path).catch(() => {})
                setCtxErr('Path copied')
                setTimeout(closeCtx, 700)
              }}>Copy path</button>
            )}
            {ctx.path && (
              <button onClick={() => {
                const paths = multiSel.has(ctx.path) ? [...multiSel] : [ctx.path]
                setClipboard({ mode: 'cut', paths })
                closeCtx()
              }}>Cut{multiSel.has(ctx.path) && multiSel.size > 1 ? ` ${multiSel.size} items` : ''}</button>
            )}
            {ctx.path && (
              <button onClick={() => {
                const paths = multiSel.has(ctx.path) ? [...multiSel] : [ctx.path]
                setClipboard({ mode: 'copy', paths })
                closeCtx()
              }}>Copy{multiSel.has(ctx.path) && multiSel.size > 1 ? ` ${multiSel.size} items` : ''}</button>
            )}
            {ctx.isDir && clipboard?.paths.length && (
              <button onClick={() => { void pasteInto(ctx.path); closeCtx() }}>
                Paste {clipboard.paths.length} {clipboard.paths.length === 1 ? 'item' : 'items'}
              </button>
            )}
            {ctx.path && (
              <button onClick={() => { setMultiSel(new Set([ctx.path])); setTagMenuOpen(true); setSelectMode(true); closeCtx() }}>Labels…</button>
            )}
            {!ctx.path ? null : !ctxConfirm ? (
              <button className="ctx-danger" onClick={() => setCtxConfirm(true)}>{ctx.isDir ? 'Delete folder…' : 'Delete…'}</button>
            ) : (
              <button className="ctx-danger confirm" disabled={ctxBusy} onClick={() => void ctxDelete()}>
                {ctxBusy ? 'Deleting…' : ctx.isDir ? 'Really delete folder and all contents' : 'Really delete this file'}
              </button>
            )}
            {ctxErr && <div className="ctx-err">{ctxErr}</div>}
          </div>
        </>
      )}
    </div>
  )
}
