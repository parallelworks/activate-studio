import { useId, useRef, useState } from 'react'
import { ActionMenu, Button, buttonClasses, Input, Popover, type MenuEntry } from '@parallelworks/ui'
import { ChevronDownIcon } from '@parallelworks/ui/icons'
import { api } from '../api'
import { collectChosen, collectDropped, collectListed, type UploadItem } from '../upload'
import { chooseUpload, shellListsFolders } from '../nativeShell'
import { startUpload } from '../transfers'
import { LockedButton } from './LockedButton'

/** Settings > Indexing > Libraries with its Add a library form open. */
export const ADD_LIBRARY_HASH = '#view=settings:libraries?add=1'

/** Add files: the + Add button and the panel it opens, to choose or drop
 *  files and folders, make a folder, or fetch a page by URL. */
export function UploadMenu({ targetDir, onTargetDir, onDone, readOnly, open, onOpenChange, onAddLibrary }: {
  /** Why nothing can be added here (a read-only library): the button is
   *  disabled and says so. */
  readOnly?: string | null
  targetDir: string
  onTargetDir: (dir: string) => void
  onDone: (savedPaths: string[]) => void
  /** The panel's state, where the page opens it too (the Add menu's Add
   *  files). */
  open?: boolean
  onOpenChange?: (open: boolean) => void
  /** Offers Add a library at the foot of the panel. */
  onAddLibrary?: () => void
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
    return <LockedButton reason={readOnly} variant="primary" className={onAddLibrary ? 'add-split-main' : undefined}>+ Add</LockedButton>
  }

  return (
    <Popover
      trigger={<Button variant="primary" size="sm" className={onAddLibrary ? 'add-split-main' : undefined}>+ Add</Button>}
      open={open}
      onOpenChange={onOpenChange}
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
      {onAddLibrary && (
        <>
          <div className="field-label">Or add another library</div>
          <Button variant="outline" size="sm" onClick={() => { onOpenChange?.(false); onAddLibrary() }}>Add a library…</Button>
        </>
      )}
    </Popover>
  )
}

/**
 * The Library's + Add: a split button. + Add opens the Add files panel as
 * it always has; the arrow beside it opens a menu of Add files, Add a
 * library and Import a library list. The arrow is there only for a viewer
 * who may add libraries (`onAddLibrary` and `onImport` given); otherwise
 * + Add is the button alone.
 */
export function LibraryAddMenu({ onAddLibrary, onImport, readOnly, ...files }: Omit<Parameters<typeof UploadMenu>[0], 'open' | 'onOpenChange' | 'onAddLibrary'> & {
  /** Opens Add a library; absent where the viewer may not add one. */
  onAddLibrary?: () => void
  /** Opens Import a library list; absent where the viewer may not. */
  onImport?: () => void
}) {
  const [filesOpen, setFilesOpen] = useState(false)
  // A choice that opens a panel or dialog runs once the menu has closed and
  // handed focus back, so the panel keeps the focus it takes.
  const pending = useRef<(() => void) | null>(null)
  const later = (fn: () => void) => () => { pending.current = fn }
  const button = <UploadMenu {...files} readOnly={readOnly} open={filesOpen} onOpenChange={setFilesOpen} onAddLibrary={onAddLibrary} />
  if (!onAddLibrary && !onImport) return button
  const items: MenuEntry[] = [
    readOnly
      ? { label: 'Add files…', onClick: () => {}, disabled: true, disabledReason: readOnly }
      : { label: 'Add files…', description: 'Files, folders, or a page by URL', onClick: later(() => setFilesOpen(true)) },
    ...(onAddLibrary ? [{ label: 'Add a library…', description: 'An index or folder here or on another system', onClick: later(onAddLibrary) }] : []),
    ...(onImport ? [{ label: 'Import a library list…', description: 'A file exported from another Studio', onClick: later(onImport) }] : []),
  ]
  return (
    <span className="add-split">
      {button}
      <ActionMenu label="More ways to add" align="end" size="sm" items={items}
        triggerClassName={`${buttonClasses({ variant: 'primary', size: 'sm' })} add-split-more`}
        onOpenChange={o => {
          if (o || !pending.current) return
          const fn = pending.current
          pending.current = null
          window.setTimeout(fn, 0)
        }}>
        <ChevronDownIcon aria-hidden="true" />
      </ActionMenu>
    </span>
  )
}
