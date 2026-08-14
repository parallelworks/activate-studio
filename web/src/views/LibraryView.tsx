import { useCallback, useEffect, useRef, useState } from 'react'
import type { Display } from '../App'
import { api } from '../api'
import { Explorer } from '../components/Explorer'
import { Viewer } from '../components/Viewer'
import { DagViewer } from '../components/DagViewer'
import { UploadMenu } from '../components/UploadMenu'
import { TagMenu } from '../components/TagMenu'
import { useAppConfig } from '../config'
import { collectDropped, uploadBatched, UploadProgress } from '../upload'

const RAIL_MIN = 220
const RAIL_MAX = 520

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
  const [showLabels, setShowLabels] = useState(() => localStorage.getItem('ade-tree-labels') === '1')
  const [tagsTick, setTagsTick] = useState(0)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [bulkNote, setBulkNote] = useState('')

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

  const toggleMulti = (p: string) => setMultiSel(s => {
    const next = new Set(s)
    if (next.has(p)) next.delete(p)
    else next.add(p)
    return next
  })

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
    const result = await uploadBatched(files, dir, setProgress)
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
              {/* The tree's root row already names the knowledge base, so the
                  head is just the action row (label hover carries the name). */}
              <div className="rail-head rail-head-actions" title={cfg.kbLabel}>
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
                  {confirmDelete ? (
                    <span className="confirm-group">
                      <button className="btn-danger" onClick={bulkDelete}>Delete {multiSel.size}</button>
                      <button className="btn-secondary" onClick={() => setConfirmDelete(false)}>Cancel</button>
                    </span>
                  ) : (
                    <button className="btn-danger-outline" disabled={!multiSel.size} onClick={() => setConfirmDelete(true)}>Delete…</button>
                  )}
                  <button className="link-btn2" onClick={() => setMultiSel(new Set())}>clear</button>
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
                key={refreshKey}
                onOpen={p => onDisplay({ kind: 'file', target: p })}
                onDirFocus={dir => setTargetDir(dir || 'uploads')}
                selected={selectedFile}
                rootLabel={cfg.kbLabel}
                multiSelected={multiSel}
                onToggleMulti={toggleMulti}
                onDropInto={(dir, items) => void uploadTo(dir, items)}
                onLabel={p => { setMultiSel(new Set([p])); setTagMenuOpen(true); setSelectMode(true) }}
                selectMode={selectMode}
                showLabels={showLabels || selectMode}
                tagsTick={tagsTick}
              />
              {progress && (
                <div className="upload-panel">
                  <div className="upload-panel-head">
                    <span>
                      {progress.finished
                        ? `${progress.done} of ${progress.total} indexed into ${progress.dir || 'root'}`
                        : `Uploading to ${progress.dir || 'root'}: ${progress.done} / ${progress.total}`}
                    </span>
                    <button className="link-btn2" onClick={() => setProgress(null)}>dismiss</button>
                  </div>
                  <div className="upload-bar"><div style={{ width: `${(progress.done / Math.max(progress.total, 1)) * 100}%` }} /></div>
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
              onDeleted={() => { onDisplay(null); setRefreshKey(k => k + 1) }}
            />
          )}
        </section>
      </div>
    </div>
  )
}
