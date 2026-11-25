import { cachedChat, forgetChat } from '../chatRecord'
import { useWatch } from '../live'

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
  void readNames().finally(() => { forgetChat(conversationId); changed() })
}

/** Reads what the person's sessions are called now, and their chats again
 *  when one changed. */
export async function readNames(): Promise<void> {
  const res = await fetch('/api/chat/pwcode/names', { method: 'POST' }).catch(() => null)
  const d = res?.ok ? await res.json().catch(() => null) as { changed?: number } | null : null
  if (d?.changed) changed()
}

/** Follows the names, which the Notices page shows as well. */
export function useNames(): void {
  useWatch<{ changed?: number }>('/api/chat/pwcode/names', 30_000, true, d => { if (d.changed) changed() }, undefined, true)
}
