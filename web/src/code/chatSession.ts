import { sectionAllowed, useAppConfig } from '../config'
import { type Notices, useNotices } from '../notices'
import { sessionHash } from './openInStudio'

/** The session a chat's own record names, for one the notices do not have. */
export interface ChatLink { session?: string; machine?: string }

/**
 * The Code page's address for the pw code session a chat runs in, or
 * undefined for a chat with none, and for a session that is neither listed
 * nor known to be on a machine, which the Code page could not open.
 */
export function chatSessionHref(n: Notices, conversationId: string | null, link?: ChatLink): string | undefined {
  const chat = conversationId ? n.chats[conversationId] : undefined
  const session = link?.session ?? chat?.session
  if (!session) return undefined
  const listed = chat?.row?.id === session ? chat.row : n.sessions.find(s => s.id === session)
  const machine = listed?.machineId || link?.machine || (chat?.session === session ? chat.machine : undefined)
  return machine ? sessionHash(machine, session) : undefined
}

/** chatSessionHref for chats on the page, where the Code page shows. */
export function useChatSessionHref(): (conversationId: string | null, link?: ChatLink) => string | undefined {
  const cfg = useAppConfig()
  const notices = useNotices()
  return (id, link) => (sectionAllowed('code', cfg) ? chatSessionHref(notices, id, link) : undefined)
}
