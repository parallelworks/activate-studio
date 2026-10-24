import { useId, useRef, useState } from 'react'
import { Button, Input, Popover, Tooltip } from '@parallelworks/ui'
import { api } from '../api'
import { uploadBatched, UploadProgress } from '../upload'

export function UploadMenu({ targetDir, onTargetDir, onDone, onProgress, readOnly }: {
  /** Why nothing can be added here (a read-only library): the button is
   *  disabled and says so. */
  readOnly?: string | null
  targetDir: string
  onTargetDir: (dir: string) => void
  onDone: (savedPaths: string[]) => void
  onProgress: (p: UploadProgress | null) => void
}) {
  const [url, setUrl] = useState('')
  const [newDir, setNewDir] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const newDirId = useId()
  const urlId = useId()

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

  const makeDir = async () => {
    const rel = newDir.trim().replace(/^\/+|\/+$/g, '')
    if (!rel) return
    setBusy(true)
    setNote('')
    try {
      const r = await api.mkdir(rel)
      setNote(`Created ${r.created}`)
      setNewDir('')
      onTargetDir(r.created)
      window.dispatchEvent(new Event('ade-kb-changed'))
    } catch (e) {
      setNote(String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  if (readOnly) {
    return <Tooltip content={readOnly}><Button variant="primary" size="sm" disabled focusableWhenDisabled>+ Add</Button></Tooltip>
  }

  return (
    <Popover
      trigger={<Button variant="primary" size="sm">+ Add</Button>}
      align="start"
      initialFocus="popup"
      aria-label="Add to the library"
      className="upload-pop"
    >
      <Input
        label="Destination folder"
        size="sm"
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
      <label className="field-label" htmlFor={newDirId}>Or create a new folder</label>
      <div className="url-row">
        <Input
          id={newDirId}
          size="sm"
          value={newDir}
          onChange={e => setNewDir(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && makeDir()}
          placeholder="e.g. papers/2026"
        />
        <Button variant="primary" size="sm" onClick={makeDir} disabled={busy || !newDir.trim()}>Create</Button>
      </div>
      <label className="field-label" htmlFor={urlId}>Or add a web page / PDF by URL</label>
      <div className="url-row">
        <Input
          id={urlId}
          size="sm"
          value={url}
          onChange={e => setUrl(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && sendUrl()}
          placeholder="https://…"
        />
        <Button variant="primary" size="sm" onClick={sendUrl} disabled={busy}>Add</Button>
      </div>
      {note && <div className="upload-status">{note}</div>}
    </Popover>
  )
}
