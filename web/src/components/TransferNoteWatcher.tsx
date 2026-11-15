import { useEffect, useRef } from 'react'
import { useChat } from '@parallelworks/ui/ai'
import { transferApi } from '../transferApi'

/** How often to look while one of the conversation's transfers runs, and
 *  otherwise. */
const RUNNING_MS = 5_000
const IDLE_MS = 30_000
/** The server writes the note just after the transfer leaves the list. */
const NOTE_WAIT_MS = 1_500

/**
 * A transfer the assistant started ends with a note the server adds to its
 * conversation. This reads the open conversation again when one of its
 * transfers ends, so the note shows without a reload. It looks only while
 * the page is shown and no reply is streaming, and at once when a reply
 * ends, since a transfer is approved inside one.
 */
export function TransferNoteWatcher() {
  const { currentConversation, isStreaming, loadConversation } = useChat()
  const id = currentConversation?.id ?? null
  const watched = useRef<{ conversation: string | null; ids: Set<string> }>({ conversation: null, ids: new Set() })

  useEffect(() => {
    if (!id || isStreaming) return
    if (watched.current.conversation !== id) watched.current = { conversation: id, ids: new Set() }
    let stop = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const later = (fn: () => void, ms: number) => { if (!stop) timer = setTimeout(fn, ms) }
    const look = async () => {
      if (stop) return
      if (document.visibilityState === 'hidden') { later(look, IDLE_MS); return }
      let running = false
      try {
        const { transfers } = await transferApi.list({ state: 'active', startedBy: 'assistant', limit: 200 })
        if (stop) return
        const mine = new Set(transfers.filter(t => t.conversationId === id).map(t => t.id))
        const ended = [...watched.current.ids].some(t => !mine.has(t))
        watched.current.ids = mine
        running = mine.size > 0
        if (ended) {
          later(() => { void loadConversation(id).finally(() => later(look, running ? RUNNING_MS : IDLE_MS)) }, NOTE_WAIT_MS)
          return
        }
      } catch { /* a server without transfers, or offline: look again later */ }
      later(look, running ? RUNNING_MS : IDLE_MS)
    }
    void look()
    return () => { stop = true; if (timer) clearTimeout(timer) }
  }, [id, isStreaming, loadConversation])

  return null
}
