import { useRef, useState } from 'react'
import { api } from '../api'
import { uploadBatched, UploadProgress } from '../upload'

export function UploadMenu({ targetDir, onTargetDir, onDone, onProgress }: {
  targetDir: string
  onTargetDir: (dir: string) => void
  onDone: (savedPaths: string[]) => void
  onProgress: (p: UploadProgress | null) => void
}) {
  const [open, setOpen] = useState(false)
  const [url, setUrl] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)

  const sendFiles = async (files: File[]) => {
    if (!files.length) return
    setBusy(true)
    setNote('')
    const items = files.map(f => ({ file: f, rel: f.name }))
    const result = await uploadBatched(items, targetDir, onProgress)
    setBusy(false)
    if (result.done > 0) onDone([`${targetDir}/${items[0].rel}`])
  }

  const sendUrl = async () => {
    if (!/^https?:\/\//i.test(url.trim())) { setNote('enter an http(s) URL'); return }
    setBusy(true)
    setNote('Fetching and indexing…')
    try {
      const r = await api.uploadUrl(url.trim(), targetDir)
      setUrl('')
      setNote(`Indexed in ${(r.indexMs / 1000).toFixed(1)}s`)
      onDone(r.saved)
    } catch (e) {
      setNote(String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="upload-menu">
      <button className="btn-primary" onClick={() => setOpen(o => !o)}>+ Add</button>
      {open && (
        <div className="upload-pop card-pop">
          <label className="field-label">Destination folder</label>
          <input
            className="field"
            value={targetDir}
            onChange={e => onTargetDir(e.target.value.replace(/^\/+/, ''))}
            placeholder="uploads"
          />
          <div
            className="dropzone"
            onDragOver={e => e.preventDefault()}
            onDrop={e => {
              e.preventDefault()
              e.stopPropagation()
              void sendFiles([...e.dataTransfer.files])
            }}
            onClick={() => fileInput.current?.click()}
          >
            Drop files here or click to choose. Any number of files works; progress shows below the tree.
            <input
              ref={fileInput}
              type="file"
              multiple
              hidden
              onChange={e => { void sendFiles([...(e.target.files ?? [])]); e.target.value = '' }}
            />
          </div>
          <label className="field-label">Or add a web page / PDF by URL</label>
          <div className="url-row">
            <input
              className="field"
              value={url}
              onChange={e => setUrl(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && sendUrl()}
              placeholder="https://…"
            />
            <button className="btn-primary" onClick={sendUrl} disabled={busy}>Add</button>
          </div>
          {note && <div className="upload-status">{note}</div>}
        </div>
      )}
    </div>
  )
}
