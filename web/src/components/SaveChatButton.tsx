import { useEffect, useState } from 'react'
import { Button, Popover, PopoverClose } from '@parallelworks/ui'

/** Where a chat is saved, as GET and POST /api/chat/conversations/{id}/save give it. */
export interface ChatDest {
  library: string
  label: string
  /** The transcript's path in the library, under chat-sessions/. */
  path: string
  /** The person's own space, closed to other accounts. */
  space: boolean
  /** The main library of a Studio others sign in to. */
  shared: boolean
}

async function call(id: string, method: 'GET' | 'POST'): Promise<ChatDest> {
  const r = await fetch(`/api/chat/conversations/${encodeURIComponent(id)}/save`, { method })
  const body = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(typeof body?.error === 'string' ? body.error : `the Studio answered ${r.status}`)
  return body as ChatDest
}

/** Who can read the saved copy, in a sentence. */
export function readersNote(d: ChatDest): string {
  if (d.space) return 'Only you can read it there.'
  if (d.shared) return 'Everyone who uses this Studio can read it there.'
  return ''
}

/**
 * Save to library: chats are not added to a library on their own, so this
 * saves a copy of the open one as Markdown in chat-sessions/ of the
 * person's own space, or of the main library when they have none. The
 * popover says where before anything is written. Saving again replaces
 * the copy.
 */
export function SaveChatButton({ conversationId }: { conversationId: string }) {
  const [open, setOpen] = useState(false)
  const [dest, setDest] = useState<ChatDest | null>(null)
  const [saved, setSaved] = useState<ChatDest | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    let live = true
    setDest(null); setSaved(null); setError('')
    call(conversationId, 'GET')
      .then(d => { if (live) setDest(d) })
      .catch(e => { if (live) setError(`Cannot save this chat: ${(e as Error).message}.`) })
    return () => { live = false }
  }, [open, conversationId])

  const save = async () => {
    setBusy(true); setError('')
    try {
      setSaved(await call(conversationId, 'POST'))
    } catch (e) {
      setError(`Not saved: ${(e as Error).message}.`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      title="Save this chat to a library"
      align="end"
      initialFocus="popup"
      className="scope-menu save-chat-menu"
      trigger={
        <button className="scope-btn" title="Save a copy of this chat to a library">
          <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2.5 2.5h8.5l2.5 2.5v8.5h-11z"/><path d="M5 2.5v3.5h5V2.5M5 13.5V9.5h6v4"/></svg>
          <span className="persona-label">Save</span>
        </button>
      }
    >
      <div className="save-chat-body" aria-live="polite">
        {saved ? (
          <p>Saved to {saved.label} as <code>{saved.path}</code>. {readersNote(saved)}</p>
        ) : dest ? (
          <>
            <p>Saves a copy of this chat to {dest.label} as <code>{dest.path}</code>. {readersNote(dest)}</p>
            <p className="muted">Chats are not added to a library unless you save them. Saving again replaces the copy.</p>
          </>
        ) : !error ? <p className="muted">Finding where it goes…</p> : null}
        {error && <p role="alert">{error}</p>}
      </div>
      <div className="scope-menu-foot">
        {saved || !dest
          ? <span />
          : <Button size="sm" disabled={busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</Button>}
        <PopoverClose render={<Button variant="secondary" size="sm" />}>{saved ? 'Done' : 'Cancel'}</PopoverClose>
      </div>
    </Popover>
  )
}
