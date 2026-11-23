import { useEffect, useState } from 'react'
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

/**
 * The open chat's pw code session, when it is shown with the session view:
 * the chat has a session and is not set to the Studio assistant. A first
 * reply's stream names the session as soon as pw code has one, so the view
 * takes over from its first events.
 */
export function useChatLink(conversationId: string | null): { link: ChatLink | null; machine: string; shown: boolean } {
  const [link, setLink] = useState<ChatLink | null>(null)
  const [engine, setEngineState] = useState(() => getEngine(conversationId))
  useEffect(() => {
    setLink(null)
    setEngineState(getEngine(conversationId))
    if (!conversationId) return
    let gone = false
    const read = () => fetch(`/api/chat/conversations/${encodeURIComponent(conversationId)}`)
      .then(r => (r.ok ? r.json() : null))
      .then(c => { if (!gone) setLink(c?.pwcode?.session ? c.pwcode as ChatLink : null) })
      .catch(() => {})
    void read()
    const onSession = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string; session?: string }
      if (d?.conversationId === conversationId && d.session) void read()
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
  const shown = !!link?.session && engine !== 'studio'
  return { link, machine: link ? sessionMachine(link) : '', shown }
}
