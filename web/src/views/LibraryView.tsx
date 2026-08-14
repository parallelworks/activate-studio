import { useCallback, useEffect, useRef, useState } from 'react'
import type { Display } from '../App'
import { api } from '../api'
import { Explorer } from '../components/Explorer'
import { Viewer } from '../components/Viewer'
import { DagViewer } from '../components/DagViewer'
import { UploadMenu } from '../components/UploadMenu'
import { useAppConfig } from '../config'

const RAIL_MIN = 220
const RAIL_MAX = 520

/** Walk dropped items, descending directories, into {file, rel} pairs. */
async function collectDropped(items: DataTransferItemList): Promise<{ file: File; rel: string }[]> {
  const out: { file: File; rel: string }[] = []
  const walkEntry = async (entry: any, prefix: string): Promise<void> => {
    if (!entry) return
    if (entry.isFile) {
      const file: File = await new Promise((res, rej) => entry.file(res, rej))
      out.push({ file, rel: prefix + file.name })
    } else if (entry.isDirectory) {
      const reader = entry.createReader()
      for (;;) {
        const batch: any[] = await new Promise(res => reader.readEntries(res, () => res([])))
        if (!batch.length) break
        for (const child of batch) await walkEntry(child, prefix + entry.name + '/')
      }
    }
  }
  // Entries must be captured synchronously before any await invalidates the list.
  const entries = [...items].map(i => (i as any).webkitGetAsEntry?.()).filter(Boolean)
  for (const e of entries) await walkEntry(e, '')
  return out
}

export function LibraryView({ display, onDisplay }: {
  display: Display | null
  onDisplay: (d: Display | null) => void
}) {
  const [targetDir, setTargetDir] = useState('uploads')
  const [refreshKey, setRefreshKey] = useState(0)
  const [railWidth, setRailWidth] = useState(() => Number(localStorage.getItem('ade-rail-width')) || 320)
  const [railCollapsed, setRailCollapsed] = useState(() => localStorage.getItem('ade-rail-collapsed') === '1')
  const drag = useRef<{ startX: number; startWidth: number } | null>(null)
  const cfg = useAppConfig()

  useEffect(() => { localStorage.setItem('ade-rail-width', String(railWidth)) }, [railWidth])
  useEffect(() => { localStorage.setItem('ade-rail-collapsed', railCollapsed ? '1' : '0') }, [railCollapsed])

  useEffect(() => {
    const onChange = () => setRefreshKey(k => k + 1)
    window.addEventListener('ade-kb-changed', onChange)
    return () => window.removeEventListener('ade-kb-changed', onChange)
  }, [])

  const onDividerDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    drag.current = { startX: e.clientX, startWidth: railWidth }
    const onMove = (ev: MouseEvent) => {
      if (!drag.current) return
      setRailWidth(Math.min(RAIL_MAX, Math.max(RAIL_MIN, drag.current.startWidth + (ev.clientX - drag.current.startX))))
    }
    const onUp = () => {
      drag.current = null
      document.body.classList.remove('resizing')
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
    document.body.classList.add('resizing')
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }, [railWidth])

  const selectedFile = display?.kind === 'file' ? display.target : null
  const [dropState, setDropState] = useState<'idle' | 'over' | 'busy'>('idle')
  const [dropNote, setDropNote] = useState('')

  const onDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault()
    setDropState('busy')
    try {
      const files = await collectDropped(e.dataTransfer.items)
      if (!files.length) { setDropState('idle'); return }
      setDropNote(`Indexing ${files.length} file${files.length === 1 ? '' : 's'}…`)
      const r = await api.uploadFiles(files, targetDir)
      setDropNote(`${r.saved.length} indexed in ${(r.indexMs / 1000).toFixed(1)}s`)
      setRefreshKey(k => k + 1)
      if (r.saved[0]) onDisplay({ kind: 'file', target: r.saved[0] })
    } catch (err) {
      setDropNote(String((err as Error).message ?? err))
    } finally {
      setDropState('idle')
      setTimeout(() => setDropNote(''), 6000)
    }
  }, [targetDir, onDisplay])

  return (
    <div
      className={`library ${dropState === 'over' ? 'drop-over' : ''}`}
      onDragOver={e => { e.preventDefault(); setDropState(s => (s === 'busy' ? s : 'over')) }}
      onDragLeave={() => setDropState(s => (s === 'busy' ? s : 'idle'))}
      onDrop={onDrop}
    >
      {railCollapsed ? (
        <button className="rail-expander" title="Expand knowledge base tree" onClick={() => setRailCollapsed(false)}>»</button>
      ) : (
        <>
          <aside className="library-rail card" style={{ width: railWidth }}>
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
                />
                <button className="rail-collapse" title="Collapse tree" onClick={() => setRailCollapsed(true)}>«</button>
              </span>
            </div>
            {dropNote && <div className="drop-note">{dropNote}</div>}
            <Explorer
              key={refreshKey}
              onOpen={p => onDisplay({ kind: 'file', target: p })}
              onDirFocus={dir => setTargetDir(dir || 'uploads')}
              selected={selectedFile}
              rootLabel={cfg.kbLabel}
            />
          </aside>
          <div className="divider" onMouseDown={onDividerDown} title="Drag to resize" />
        </>
      )}
      <section className="library-main card">
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
  )
}
