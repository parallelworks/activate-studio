import { useEffect, useState } from 'react'
import { cachedChat, readChat, type ChatRecord } from '../chatRecord'
import { type Engine, knowEngine, knownEngine } from '../labelScope'
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

/** What answers the chat, as its record has it ('' for nothing yet). */
function engineOf(c: ChatRecord | null | undefined): Engine {
  return c?.engine === 'pwcode' || c?.engine === 'studio' ? c.engine : ''
}

/** Whether the Studio assistant gave the chat's last reply: its pw code
 *  session has not seen it, so the chat's next message goes through the
 *  Studio, which gives the session the turns it missed, and not straight
 *  to the session from its own view. */
function behindOf(c: ChatRecord | null | undefined): boolean {
  const replies = ((c?.messages ?? []) as { id?: string; role?: string; model?: string | null }[])
    .filter(m => m.role === 'assistant' && !String(m.id ?? '').startsWith('note-'))
  const last = replies[replies.length - 1]
  if (!last) return false
  const id = String(last.id ?? '')
  return !(id.startsWith('pwc-') || id.startsWith('pwh-') || String(last.model ?? '').startsWith('pw code'))
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
  const fromCache = (id: string | null) => ({ id, link: linkOf(cachedChat(id)), engine: engineOf(cachedChat(id)), behind: behindOf(cachedChat(id)) })
  const [read, setRead] = useState(() => fromCache(conversationId))
  const [, bump] = useState(0)
  // On a switch, what is known of the new chat counts from the same render.
  const cur = read.id === conversationId ? read : fromCache(conversationId)
  const link = cur.link
  // What answers it: the server's word as last heard here (a switch, a
  // reply), else its record's.
  const chosen = knownEngine(conversationId) || cur.engine
  useEffect(() => {
    setRead(fromCache(conversationId))
    if (!conversationId) return
    let gone = false
    const reread = () => readChat(conversationId)
      .then(c => {
        if (gone) return
        setRead({ id: conversationId, link: linkOf(c), engine: engineOf(c), behind: behindOf(c) })
        if (engineOf(c)) knowEngine(conversationId, engineOf(c))
      })
      .catch(() => { if (!gone) setRead({ id: conversationId, link: null, engine: '', behind: false }) })
    void reread()
    const onSession = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string; session?: string }
      if (d?.conversationId === conversationId && d.session) void reread()
    }
    // A switch or a finished reply changes what the chat shows: read the
    // record again for its last reply.
    const onEngine = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string | null }
      if (d?.conversationId === conversationId) { bump(n => n + 1); void reread() }
    }
    window.addEventListener('ade-pwcode-session', onSession)
    window.addEventListener('ade:chat-engine', onEngine)
    window.addEventListener('ade:chat-answered', onEngine)
    return () => {
      gone = true
      window.removeEventListener('ade-pwcode-session', onSession)
      window.removeEventListener('ade:chat-engine', onEngine)
      window.removeEventListener('ade:chat-answered', onEngine)
    }
  }, [conversationId])
  const shown = !!link?.session && chosen !== 'studio' && !cur.behind
  return { link, machine: link ? sessionMachine(link) : '', shown }
}
