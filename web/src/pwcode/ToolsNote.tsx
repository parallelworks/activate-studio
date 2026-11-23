import { useEffect, useState } from 'react'
import { NoticeCard } from '@parallelworks/ui/ai'
import type { ChatLink } from './useChatLink'

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
 * failed. A session on another machine cannot get them until ACTIVATE can
 * attach them, and the chat says so once.
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
  const where = link.machineName || 'its machine'
  return (
    <NoticeCard onDismiss={() => { markSeen(conversationId); setHidden(true) }}>
      This session runs on {where}, so it does not have this Studio's tools: library search, find by topic, reading library files,
      and the Studio's platform and delegation tools. pw code's own tools work as usual. Giving a session on another machine the
      Studio's tools is pending a platform change in ACTIVATE.
    </NoticeCard>
  )
}
