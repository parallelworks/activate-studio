import { useEffect, useState } from 'react'
import { cachedChat, readChat, type ChatRecord } from '../chatRecord'
import { getEngine } from '../labelScope'
import { sessionMachine } from './api'

/** A chat's pw code session as the Studio keeps it (conversations.PWCodeLink). */
export interface ChatLink {
  session?: string
  relay?: boolean
  machine?: string
  machineName?: string
  continued?: boolean
  toolsError?: string
  url?: string
}

/** The chat's session as its record has it, or null. */
function linkOf(c: ChatRecord | null | undefined): ChatLink | null {
  return c?.pwcode?.session ? c.pwcode as ChatLink : null
}

/**
 * The open chat's pw code session, when it is shown with the session view:
 * the chat has a session and is not set to the Studio assistant. A first
 * reply's stream names the session as soon as pw code has one, so the view
 * takes over from its first events. A chat opened before starts from its
 * kept record (chatRecord.ts), so its session shows at once, and its
 * record is read again behind it; a chat never opened waits for that read.
 */
export function useChatLink(conversationId: string | null): { link: ChatLink | null; machine: string; shown: boolean } {
  const [read, setRead] = useState<{ id: string | null; link: ChatLink | null }>(() => ({ id: conversationId, link: linkOf(cachedChat(conversationId)) }))
  const [engine, setEngineState] = useState(() => getEngine(conversationId))
  const [engineOf, setEngineOf] = useState(conversationId)
  // On a switch, what is known of the new chat counts from the same render.
  const link = read.id === conversationId ? read.link : linkOf(cachedChat(conversationId))
  const chosen = engineOf === conversationId ? engine : getEngine(conversationId)
  useEffect(() => {
    setEngineState(getEngine(conversationId))
    setEngineOf(conversationId)
    setRead({ id: conversationId, link: linkOf(cachedChat(conversationId)) })
    if (!conversationId) return
    let gone = false
    const reread = () => readChat(conversationId)
      .then(c => { if (!gone) setRead({ id: conversationId, link: linkOf(c) }) })
      .catch(() => { if (!gone) setRead({ id: conversationId, link: null }) })
    void reread()
    const onSession = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string; session?: string }
      if (d?.conversationId === conversationId && d.session) void reread()
    }
    const onEngine = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string | null }
      if (!d?.conversationId || d.conversationId === conversationId) setEngineState(getEngine(conversationId))
    }
    window.addEventListener('ade-pwcode-session', onSession)
    window.addEventListener('ade:chat-engine', onEngine)
    return () => {
      gone = true
      window.removeEventListener('ade-pwcode-session', onSession)
      window.removeEventListener('ade:chat-engine', onEngine)
    }
  }, [conversationId])
  const shown = !!link?.session && chosen !== 'studio'
  return { link, machine: link ? sessionMachine(link) : '', shown }
}
