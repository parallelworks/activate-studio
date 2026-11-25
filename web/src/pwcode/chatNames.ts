import { cachedChat, forgetChat } from '../chatRecord'

/**
 * A pw code chat and its session keep one name. The Studio's server does
 * the keeping (server-go/internal/chat/pwcodenames.go); this has the page
 * read its chats again when a name changed.
 */

const said = new Map<string, string>()

const changed = () => window.dispatchEvent(new CustomEvent('ade:chats-changed'))

/** The chat's session is called name now: when this page has another title,
 *  the Studio reads the name too, and the chats are read again. */
export function announceName(conversationId: string, name: string): void {
  const before = said.get(conversationId) ?? (cachedChat(conversationId) as { title?: string } | undefined)?.title
  said.set(conversationId, name)
  if (before === name) return
  void readNames(0).finally(() => { forgetChat(conversationId); changed() })
}

let read = 0

/** Reads what the person's sessions are called now, at most every half
 *  minute, and their chats again when one changed. */
export async function readNames(every = 30_000): Promise<void> {
  if (Date.now() - read < every) return
  read = Date.now()
  const res = await fetch('/api/chat/pwcode/names', { method: 'POST' }).catch(() => null)
  const d = res?.ok ? await res.json().catch(() => null) as { changed?: number } | null : null
  if (d?.changed) changed()
}
