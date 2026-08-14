import { useRef, useState } from 'react'
import { api } from '../api'

type Phase = 'idle' | 'uploading' | 'indexing' | 'done' | 'error'

export function UploadMenu({ targetDir, onTargetDir, onDone }: {
  targetDir: string
  onTargetDir: (dir: string) => void
  onDone: (savedPaths: string[]) => void
}) {
  const [open, setOpen] = useState(false)
  const [phase, setPhase] = useState<Phase>('idle')
  const [message, setMessage] = useState('')
  const [url, setUrl] = useState('')
  const fileInput = useRef<HTMLInputElement>(null)

  const finish = (saved: string[], ms: number) => {
    setPhase('done')
    setMessage(`${saved.length} item${saved.length === 1 ? '' : 's'} indexed in ${(ms / 1000).toFixed(1)}s`)
    onDone(saved)
  }
  const fail = (e: unknown) => {
    setPhase('error')
    setMessage(String((e as Error).message ?? e))
  }

  const sendFiles = async (files: File[]) => {
    if (!files.length) return
    try {
      setPhase('uploading')
      setMessage(`Uploading ${files.length} file${files.length === 1 ? '' : 's'}…`)
      setPhase('indexing')
      const r = await api.uploadFiles(files, targetDir)
      finish(r.saved, r.indexMs)
    } catch (e) { fail(e) }
  }

  const sendUrl = async () => {
    if (!/^https?:\/\//i.test(url.trim())) { fail(new Error('enter an http(s) URL')); return }
    try {
      setPhase('indexing')
      setMessage('Fetching and indexing…')
      const r = await api.uploadUrl(url.trim(), targetDir)
      setUrl('')
      finish(r.saved, r.indexMs)
    } catch (e) { fail(e) }
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
            onDrop={e => { e.preventDefault(); void sendFiles([...e.dataTransfer.files]) }}
            onClick={() => fileInput.current?.click()}
          >
            Drop files here or click to choose
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
            <button className="btn-primary" onClick={sendUrl} disabled={phase === 'indexing'}>Add</button>
          </div>
          {phase !== 'idle' && (
            <div className={`upload-status ${phase}`}>
              {phase === 'indexing' && <span className="spinner" />}
              {message}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
