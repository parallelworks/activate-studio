import { useCallback, useEffect, useRef, useState } from 'react'
import type { Display } from '../App'
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
  const drag = useRef<{ startX: number; startWidth: number } | null>(null)
  const cfg = useAppConfig()

  useEffect(() => { localStorage.setItem('ade-rail-width', String(railWidth)) }, [railWidth])
  useEffect(() => { localStorage.setItem('ade-rail-collapsed', railCollapsed ? '1' : '0') }, [railCollapsed])

  useEffect(() => {
    const onChange = () => setRefreshKey(k => k + 1)
    window.addEventListener('ade-kb-changed', onChange)
    return () => window.removeEventListener('ade-kb-changed', onChange)
  }, [])

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
              <div className="rail-head">
                <span className="rail-title">{cfg.kbLabel}</span>
                <span className="rail-actions">
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
              {multiSel.size > 0 && (
                <div className="multi-bar">
                  <span>{multiSel.size} selected</span>
                  <button className="btn-secondary" onClick={() => setTagMenuOpen(o => !o)}>Labels…</button>
                  <button className="link-btn2" onClick={() => { setMultiSel(new Set()); setTagMenuOpen(false) }}>clear</button>
                  {tagMenuOpen && (
                    <TagMenu
                      paths={[...multiSel]}
                      onClose={() => setTagMenuOpen(false)}
                      onApplied={() => window.dispatchEvent(new Event('ade-tags-changed'))}
                    />
                  )}
                </div>
              )}
              <Explorer
                key={refreshKey}
                onOpen={p => onDisplay({ kind: 'file', target: p })}
                onDirFocus={dir => setTargetDir(dir || 'uploads')}
                selected={selectedFile}
                rootLabel={cfg.kbLabel}
                multiSelected={multiSel}
                onToggleMulti={toggleMulti}
                onDropInto={(dir, items) => void uploadTo(dir, items)}
                onLabel={p => { setMultiSel(new Set([p])); setTagMenuOpen(true) }}
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
