import { useMemo, useRef, useState } from 'react'
import { useChat } from '@parallelworks/ui/ai'
import { deleteConversations, isOwnConversation } from '../adapter'

/**
 * Selecting and deleting past conversations in bulk. The chat package's
 * rail deletes one at a time; until it can select several, this panel
 * stands in, in the main pane like the package's own Attachments page.
 */

type Row = { id: string; title?: string; preview?: string; updatedAt?: string; createdAt: string; messageCount: number; owner?: string | null }

const when = (iso?: string) => {
  if (!iso) return ''
  const d = new Date(iso)
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: d.getFullYear() === new Date().getFullYear() ? undefined : 'numeric' })
}

export function ChatsManager({ activeId, onOpen, onActiveDeleted, onClose }: {
  activeId: string | null
  onOpen: (id: string) => void
  onActiveDeleted: () => void
  onClose: () => void
}) {
  const { conversations, loadConversations } = useChat()
  const rows = conversations as unknown as Row[]
  const [filter, setFilter] = useState('')
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const lastClicked = useRef<number | null>(null)

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return rows.filter(c => !q || (c.title ?? '').toLowerCase().includes(q) || (c.preview ?? '').toLowerCase().includes(q))
  }, [rows, filter])
  const deletable = shown.filter(isOwnConversation)
  const pickedShown = deletable.filter(c => picked.has(c.id))
  const allShownPicked = deletable.length > 0 && pickedShown.length === deletable.length

  const toggle = (index: number, shift: boolean) => {
    const c = shown[index]
    if (!isOwnConversation(c)) return
    setConfirming(false)
    // Read before the update: React runs the updater later, after the ref moves.
    const anchor = shift ? lastClicked.current : null
    setPicked(prev => {
      const next = new Set(prev)
      const on = !prev.has(c.id)
      // Shift-click sets every deletable row between the last click and this one.
      const from = anchor !== null ? Math.min(anchor, index) : index
      const to = anchor !== null ? Math.max(anchor, index) : index
      for (let i = from; i <= to; i++) {
        const r = shown[i]
        if (!isOwnConversation(r)) continue
        if (on) next.add(r.id); else next.delete(r.id)
      }
      return next
    })
    lastClicked.current = index
  }
  const toggleAll = () => {
    setConfirming(false)
    setPicked(prev => {
      const next = new Set(prev)
      if (allShownPicked) deletable.forEach(c => next.delete(c.id))
      else deletable.forEach(c => next.add(c.id))
      return next
    })
  }

  const remove = async () => {
    const ids = [...picked]
    setBusy(true); setNote('')
    try {
      const r = await deleteConversations(ids)
      if (activeId && r.deleted.includes(activeId)) onActiveDeleted()
      setPicked(new Set())
      setConfirming(false)
      await loadConversations()
      setNote(`Deleted ${r.deleted.length} conversation${r.deleted.length === 1 ? '' : 's'}.${r.skipped.length ? ` ${r.skipped.length} could not be deleted.` : ''}`)
    } catch (e) {
      setNote(`Delete failed: ${(e as Error).message}`)
    } finally { setBusy(false) }
  }

  return (
    <div className="chats-manager">
      <div className="chats-manager-head">
        <h2>Conversations</h2>
        <span className="muted">{rows.length}</span>
        <button className="btn-secondary chats-manager-close" onClick={onClose}>Done</button>
      </div>
      <div className="chats-manager-bar">
        <label className="chats-check-all">
          <input type="checkbox" checked={allShownPicked} disabled={!deletable.length} onChange={toggleAll} />
          <span>{filter ? 'All shown' : 'All'}</span>
        </label>
        <input className="field chats-filter" placeholder="Filter by title" value={filter} onChange={e => setFilter(e.target.value)} />
        {!confirming
          ? <button className="btn-danger" disabled={!picked.size || busy} onClick={() => setConfirming(true)}>
              Delete{picked.size ? ` ${picked.size}` : ''}
            </button>
          : <span className="chats-confirm">
              <span>Delete {picked.size} conversation{picked.size === 1 ? '' : 's'} and their transcripts? This cannot be undone.</span>
              <button className="btn-danger" disabled={busy} onClick={() => void remove()}>{busy ? 'Deleting…' : 'Delete'}</button>
              <button className="btn-secondary" disabled={busy} onClick={() => setConfirming(false)}>Cancel</button>
            </span>}
      </div>
      {note && <p className="muted chats-note">{note}</p>}
      <ul className="chats-list" role="list">
        {shown.map((c, i) => {
          const own = isOwnConversation(c)
          return (
            <li key={c.id} className={`chats-row${picked.has(c.id) ? ' picked' : ''}${own ? '' : ' locked'}`}>
              <input
                type="checkbox"
                aria-label={`Select ${c.title || 'Untitled'}`}
                checked={picked.has(c.id)}
                disabled={!own}
                onChange={() => { /* handled on click, which carries the shift key */ }}
                onClick={e => toggle(i, e.shiftKey)}
              />
              <button className="chats-row-open" onClick={() => onOpen(c.id)} title="Open">
                <span className="chats-row-title">{c.title || c.preview || 'Untitled'}</span>
                <span className="chats-row-meta">
                  {when(c.updatedAt ?? c.createdAt)} · {c.messageCount} message{c.messageCount === 1 ? '' : 's'}
                </span>
              </button>
            </li>
          )
        })}
        {!shown.length && <li className="muted chats-empty">{rows.length ? 'No conversation matches.' : 'No conversations yet.'}</li>}
      </ul>
    </div>
  )
}
