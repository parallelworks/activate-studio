import type { usePwCodeComposer } from '@parallelworks/code'
import type { SessionViewHandle } from '@parallelworks/ui/ai'
import { type RefObject, useCallback, useEffect, useMemo, useRef, useSyncExternalStore } from 'react'
import { firstMessage, onFirstMessage, takeFirstMessage } from '../firstMessage'

type Recall = ReturnType<typeof usePwCodeComposer>['recall']

/** How long a session that takes nothing, such as one on a machine gone out
 *  of reach, is waited for before the text goes to its box. */
const LOCKED_MS = 8_000

/**
 * The chat's first message, where the Studio left it to the session's view
 * (firstMessage.ts): sent from its composer once the session is read, and
 * in its box where the session does not take it. `pending` is the text
 * until then, and `recall` the composer's, with what goes back in it.
 */
export function useFirstMessage({ conversationId, view, read, open, send, recall, notify }: {
  conversationId: string | undefined
  view: RefObject<SessionViewHandle | null>
  /** The session is read, or could not be. */
  read: boolean
  /** Its composer takes a message. */
  open: boolean
  send: (text: string) => Promise<boolean>
  recall: Recall
  notify: (text: string) => void
}): { pending: string | undefined; recall: Recall } {
  const first = useSyncExternalStore(onFirstMessage, () => firstMessage(conversationId))
  const back = useRef('')
  const putBack = useCallback((text: string) => {
    back.current = text
    void view.current?.recall()
  }, [view])
  const latest = useRef({ send, notify })
  latest.current = { send, notify }
  useEffect(() => {
    if (!first || !read) return
    const giveBack = () => {
      const taken = takeFirstMessage(conversationId)
      if (!taken) return
      putBack(taken.text)
      if (taken.refused) latest.current.notify(taken.refused)
    }
    if (first.refused) { giveBack(); return }
    if (!open) {
      const timer = window.setTimeout(giveBack, LOCKED_MS)
      return () => window.clearTimeout(timer)
    }
    const taken = takeFirstMessage(conversationId)
    if (taken) void latest.current.send(taken.text).then(sent => { if (!sent) putBack(taken.text) })
  }, [first, read, open, conversationId, putBack])
  const wrapped = useMemo<Recall>(() => ({
    ...recall,
    take: async () => {
      if (!back.current) return recall.take()
      const text = back.current
      back.current = ''
      return [{ text }]
    },
  }), [recall])
  return { pending: first && !first.refused ? first.text : undefined, recall: wrapped }
}
