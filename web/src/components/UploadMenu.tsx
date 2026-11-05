import { useId, useRef, useState } from 'react'
import { Button, Input, Popover, Tooltip } from '@parallelworks/ui'
import { api } from '../api'
import { collectChosen, collectDropped, collectListed, type UploadItem } from '../upload'
import { chooseUpload, shellListsFolders } from '../nativeShell'
import { startUpload } from '../transfers'

export function UploadMenu({ targetDir, onTargetDir, onDone, readOnly }: {
  /** Why nothing can be added here (a read-only library): the button is
   *  disabled and says so. */
  readOnly?: string | null
  targetDir: string
  onTargetDir: (dir: string) => void
  onDone: (savedPaths: string[]) => void
}) {
  const [url, setUrl] = useState('')
  const [newDir, setNewDir] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  const newDirId = useId()
  const urlId = useId()

  /** Queued, not awaited here: the next upload can be added at once, and
   *  each shows in the panel below the tree. */
  const send = async (items: UploadItem[]) => {
    if (!items.length) return
    setNote('')
    const result = await startUpload(items, targetDir)
    if (result.done > 0) onDone([`${targetDir}/${items[0].rel}`])
  }

  /** The Linux window's file chooser cannot pick a folder, so it asks the
   *  window for its own. */
  const chooseFolder = async () => {
    if (!shellListsFolders()) { folderInput.current?.click(); return }
    await send(collectListed(await chooseUpload('Choose a folder to add')))
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
          void collectDropped(e.dataTransfer.items).then(send)
        }}
        onClick={() => fileInput.current?.click()}
      >
        Drop files or folders here, or click to choose files. Folders keep their structure; progress shows below the tree.
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          onChange={e => { void send(collectChosen([...(e.target.files ?? [])])); e.target.value = '' }}
        />
      </div>
      <Button variant="outline" size="sm" onClick={() => void chooseFolder()}>Choose a folder</Button>
      <input
        ref={el => { folderInput.current = el; el?.setAttribute('webkitdirectory', '') }}
        type="file"
        hidden
        onChange={e => { void send(collectChosen([...(e.target.files ?? [])])); e.target.value = '' }}
      />
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
