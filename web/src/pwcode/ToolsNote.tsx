import { useEffect, useState } from 'react'
import { NoticeCard } from '@parallelworks/ui/ai'
import type { ChatLink } from './useChatLink'
import { StudioToolsSetup } from '../components/StudioToolsSetup'

const SEEN = 'ade-pwcode-tools-note-seen'

function seen(conversationId: string): boolean {
  try { return (JSON.parse(localStorage.getItem(SEEN) ?? '[]') as string[]).includes(conversationId) } catch { return false }
}

function markSeen(conversationId: string): void {
  try {
    const all = (JSON.parse(localStorage.getItem(SEEN) ?? '[]') as string[]).filter(id => id !== conversationId)
    all.push(conversationId)
    localStorage.setItem(SEEN, JSON.stringify(all.slice(-200)))
  } catch { /* storage unavailable: the note shows again after a reload */ }
}

/**
 * Whether the chat's pw code session has the Studio's tools, said only
 * when it does not. A session the Studio drives directly gets them at every
 * message, and this shows only when that failed after a retry, with what
 * failed. A session on another machine gets them once the person adds the
 * Studio to pw code there, which the setup card shows, until ACTIVATE can
 * attach them itself.
 */
export function PWCodeToolsNote({ conversationId, link }: { conversationId: string; link: ChatLink }) {
  const [hidden, setHidden] = useState(() => !!link.relay && seen(conversationId))
  useEffect(() => { setHidden(!!link.relay && seen(conversationId)) }, [conversationId, link.relay])
  if (hidden) return null
  if (!link.relay) {
    if (!link.toolsError) return null
    return (
      <NoticeCard tone="warning" onDismiss={() => setHidden(true)}>
        This session does not have the Studio's tools: the Studio tried twice before your last message and could not connect them ({link.toolsError}). It tries again with your next message.
      </NoticeCard>
    )
  }
  // A session on another machine reaches the Studio's tools once its
  // machine's pw code has the Studio as an MCP server: the setup card shows
  // the one-time command and checks that it worked (/api/mcp/bridge).
  return <StudioToolsSetup machineName={link.machineName} conversationId={conversationId} onDismiss={() => { markSeen(conversationId); setHidden(true) }} />
}
