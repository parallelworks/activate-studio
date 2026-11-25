import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Button, Callout, Modal } from '@parallelworks/ui'
import { api, type TextSaved } from '../api'
import { useShownPath } from '../libraryPath'
import { copyName, NO_BASE, textBytes, TextConflict, TEXT_MAX, toFile, type TextForm } from '../textFiles'
import { holdUnsaved } from '../unsaved'
import { TextNote } from './TextNote'

export interface OpenText {
  path: string
  text: string
  form: TextForm
  version: string
}

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const SAVE_KEYS = isMac ? 'Cmd+S' : 'Ctrl+S'

/**
 * A text file being edited: a plain monospace field, with Save and Cancel.
 * A save names the version the file was opened at, so one made on top of
 * somebody else's change is refused and the person says which is kept.
 */
export function TextEditor({ open, onClose, onSaved, onCopied }: {
  open: OpenText
  /** The editor is done: the file's view comes back. */
  onClose: () => void
  /** The file has these bytes now. */
  onSaved: (saved: TextSaved) => void | Promise<void>
  /** The text went into a file of its own beside this one. */
  onCopied: (saved: TextSaved) => void
}) {
  const shown = useShownPath()
  const area = useRef<HTMLTextAreaElement>(null)
  // The file's version and text as last opened or saved here.
  const base = useRef({ version: open.version, text: open.text })
  const [dirty, setDirty] = useState(false)
  const [busy, setBusy] = useState(false)
  const working = useRef(false)
  const [error, setError] = useState('')
  const [leaving, setLeaving] = useState<{ then: () => void } | null>(null)
  const [conflict, setConflict] = useState<{ state: 'changed' | 'missing'; version?: string; stay: boolean; then?: () => void } | null>(null)

  useEffect(() => { area.current?.focus({ preventScroll: true }) }, [])

  const text = () => area.current?.value ?? ''
  const typed = () => { setDirty(text() !== base.current.text); if (error) setError('') }

  // Nothing is in the way of leaving any more, at once: what is run next
  // may look before the next render.
  const release = useRef<() => void>(() => {})
  useLayoutEffect(() => {
    if (!dirty) return
    release.current = holdUnsaved({ path: open.path, ask: then => setLeaving({ then }) })
    return () => release.current()
  }, [dirty, open.path])
  useEffect(() => {
    if (!dirty) return
    const stop = (e: BeforeUnloadEvent) => { e.preventDefault(); e.returnValue = '' }
    window.addEventListener('beforeunload', stop)
    return () => window.removeEventListener('beforeunload', stop)
  }, [dirty])

  const close = (then?: () => void) => {
    release.current()
    setLeaving(null)
    setConflict(null)
    onClose()
    then?.()
  }

  /** One thing at a time, with its error shown. */
  const work = async (run: () => Promise<void>) => {
    if (working.current) return
    working.current = true
    setBusy(true)
    setError('')
    try { await run() } catch (e) {
      setError(String((e as Error).message ?? e))
    } finally {
      working.current = false
      setBusy(false)
    }
  }

  /** Save over `version` of the file. `stay` keeps the editor open. */
  const put = async (version: string, stay: boolean, then?: () => void) => {
    const now = text()
    const bytes = toFile(now, open.form)
    if (textBytes(bytes) > TEXT_MAX) throw new Error('This is more than 1 MB of text, the most a file edited here may hold. Nothing was saved.')
    let r: TextSaved
    try {
      r = await api.saveText(open.path, bytes, version)
    } catch (e) {
      if (!(e instanceof TextConflict)) throw e
      setLeaving(null)
      if (e.state === 'exists') {
        // Made again since it went: what is there now is what would be
        // written over.
        const there = await api.textFile(open.path)
        setConflict({ state: 'changed', version: there.version, stay, then })
      } else setConflict({ state: e.state, version: e.version, stay, then })
      return
    }
    base.current = { version: r.version, text: now }
    setConflict(null)
    setLeaving(null)
    const more = text() !== now
    if (!more) release.current()
    setDirty(more)
    await onSaved(r)
    if (more) return
    if (!stay) onClose()
    then?.()
  }
  const save = (stay: boolean, then?: () => void) => {
    if (text() === base.current.text) { if (!stay) close(then); return }
    void work(() => put(base.current.version, stay, then))
  }
  const overwrite = () => {
    const c = conflict
    if (c) void work(() => put(c.state === 'missing' ? NO_BASE : c.version ?? base.current.version, c.stay, c.then))
  }
  const saveCopy = () => {
    const c = conflict
    if (!c) return
    void work(async () => {
      for (let n = 1; ; n++) {
        try {
          const r = await api.saveText(copyName(open.path, n), toFile(text(), open.form), NO_BASE)
          release.current()
          onCopied(r)
          close(c.then)
          return
        } catch (e) {
          if (!(e instanceof TextConflict) || e.state !== 'exists' || n >= 50) throw e
        }
      }
    })
  }

  const saveRef = useRef(save)
  saveRef.current = save
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey || e.key.toLowerCase() !== 's') return
      // Not from another page, where the Library is there but hidden.
      if (area.current?.closest('.view.hidden')) return
      e.preventDefault()
      saveRef.current(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const name = shown(open.path)
  return (
    <div className="viewer text-editor">
      <div className="viewer-head">
        <span className="viewer-path">{name}</span>
        <span className="viewer-meta">
          <span role="status" className="text-editor-state">{busy ? 'Saving…' : dirty ? 'Not saved' : ''}</span>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => { if (dirty) setLeaving({ then: () => {} }); else close() }}>{dirty ? 'Cancel' : 'Close'}</Button>
          <Button variant="primary" size="sm" loading={busy} disabled={!dirty} title={`Save and show the file. ${SAVE_KEYS} saves and keeps editing.`} onClick={() => save(false)}>Save</Button>
        </span>
      </div>
      <TextNote path={open.path} />
      {error && !conflict && !leaving && <Callout tone="error" size="sm" className="text-editor-error">{error}</Callout>}
      <textarea
        ref={area}
        className="text-editor-area"
        aria-label={`Text of ${name}`}
        defaultValue={open.text}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        autoComplete="off"
        onChange={typed}
      />
      <Modal
        open={leaving !== null && !conflict}
        onClose={() => setLeaving(null)}
        alert
        size="sm"
        title="Save your changes?"
        description={name}
        footer={
          <>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setLeaving(null)}>Keep editing</Button>
            <Button variant="outline" size="sm" disabled={busy} onClick={() => close(leaving?.then)}>Discard</Button>
            <Button variant="primary" size="sm" loading={busy} onClick={() => save(false, leaving?.then)}>Save</Button>
          </>
        }
      >
        <p className="text-editor-ask">They are not saved yet.</p>
        {error && <Callout tone="error" size="sm">{error}</Callout>}
      </Modal>
      <Modal
        open={conflict !== null}
        onClose={() => { if (!busy) setConflict(null) }}
        alert
        size="md"
        title={conflict?.state === 'missing' ? 'This file was moved or deleted after you opened it' : 'This file changed after you opened it'}
        description={name}
        footer={
          <div className="text-editor-choices">
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConflict(null)}>Keep editing</Button>
            <Button variant="outline" size="sm" disabled={busy} onClick={() => close(conflict?.then)}>{conflict?.state === 'missing' ? 'Discard mine' : 'Keep theirs'}</Button>
            <Button variant="outline" size="sm" disabled={busy} onClick={overwrite}>{conflict?.state === 'missing' ? 'Make it again' : 'Overwrite'}</Button>
            <Button variant="primary" size="sm" disabled={busy} loading={busy} onClick={saveCopy}>Save mine as a copy</Button>
          </div>
        }
      >
        <p className="text-editor-ask">
          {conflict?.state === 'missing'
            ? 'Nothing was saved. Save what you have as a copy beside it, make the file again with what you have, or discard it.'
            : 'Nothing was saved, so what is there now is not lost. Save what you have as a copy beside it, keep what is there and discard yours, or overwrite it with yours.'}
        </p>
        {error && <Callout tone="error" size="sm">{error}</Callout>}
      </Modal>
    </div>
  )
}

