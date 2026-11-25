import { useEffect, useRef } from 'react'
import { useChat } from '@parallelworks/ui/ai'
import { watch } from '../live'
import type { Transfer } from '../transferApi'

/** How often to look while one of the conversation's transfers runs, and
 *  otherwise. */
const RUNNING_MS = 5_000
const IDLE_MS = 30_000
/** The server writes the note just after the transfer leaves the list. */
const NOTE_WAIT_MS = 1_500
const PATH = '/api/transfers?state=active&startedBy=assistant&limit=200'

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
    let timer: ReturnType<typeof setTimeout> | undefined
    // Every 30 seconds until one of them runs, for as long as one does.
    const w = watch<{ transfers?: Transfer[] }>(PATH, IDLE_MS, ({ transfers }) => {
      const mine = new Set((transfers ?? []).filter(t => t.conversationId === id).map(t => t.id))
      const ended = [...watched.current.ids].some(t => !mine.has(t))
      watched.current.ids = mine
      w.every(mine.size > 0 ? RUNNING_MS : IDLE_MS)
      if (ended) { clearTimeout(timer); timer = setTimeout(() => void loadConversation(id), NOTE_WAIT_MS) }
    })
    return () => { w.stop(); clearTimeout(timer) }
  }, [id, isStreaming, loadConversation])

  return null
}
