import { useEffect, useState } from 'react'
import { cachedChat, readChat, type ChatRecord } from '../chatRecord'
import { type Engine, readEngine, useEngine } from '../chatEngine'
import { sessionMachine } from './api'

/** A chat's pw code session as the Studio keeps it (conversations.PWCodeLink). */
export interface ChatLink {
  session?: string
  relay?: boolean
  machine?: string
  machineName?: string
  continued?: boolean
  toolsError?: string
  /** On another machine: the session started with the Studio's tools. */
  hasTools?: boolean
  /** The Studio started it where the person chose. */
  picked?: boolean
  url?: string
}

/** The engine stored for the chat, as its record has it ('' for nothing
 *  yet): what is shown until the Studio says what answers it. */
function storedEngine(c: ChatRecord | null | undefined): Engine {
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
  const fromCache = (id: string | null) => ({ id, link: linkOf(cachedChat(id)), engine: storedEngine(cachedChat(id)), behind: behindOf(cachedChat(id)) })
  const [read, setRead] = useState(() => fromCache(conversationId))
  // On a switch, what is known of the new chat counts from the same render.
  const cur = read.id === conversationId ? read : fromCache(conversationId)
  const link = cur.link
  // What answers it: the Studio's word (chatEngine.ts), the one source the
  // chat's parts share. Until it comes, the engine its record stores; the
  // record never sets what answers it, which for a person pw code is not
  // for here is the Studio assistant whatever the record says.
  const engine = useEngine(conversationId)
  const chosen = engine.current || (engine.info === undefined ? cur.engine : '')
  useEffect(() => {
    setRead(fromCache(conversationId))
    if (!conversationId) return
    let gone = false
    void readEngine(conversationId)
    const reread = () => readChat(conversationId)
      .then(c => { if (!gone) setRead({ id: conversationId, link: linkOf(c), engine: storedEngine(c), behind: behindOf(c) }) })
      .catch(() => { if (!gone) setRead({ id: conversationId, link: null, engine: '', behind: false }) })
    void reread()
    // A new session, or a finished reply, changes what the record says of
    // the chat's session and its last reply. What answers it is not read
    // from the record, so a change of that leads to no read here.
    const onChange = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string; session?: string }
      if (d?.conversationId === conversationId && (e.type !== 'ade-pwcode-session' || d.session)) void reread()
    }
    window.addEventListener('ade-pwcode-session', onChange)
    window.addEventListener('ade:chat-answered', onChange)
    return () => {
      gone = true
      window.removeEventListener('ade-pwcode-session', onChange)
      window.removeEventListener('ade:chat-answered', onChange)
    }
  }, [conversationId])
  const shown = !!link?.session && chosen !== 'studio' && !cur.behind
  return { link, machine: link ? sessionMachine(link) : '', shown }
}
