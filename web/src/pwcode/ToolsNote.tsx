import { useEffect, useState } from 'react'
import { NoticeCard } from '@parallelworks/ui/ai'
import type { ChatLink } from './useChatLink'
import { StudioToolsSetup } from '../components/StudioToolsSetup'
import { autoSetupWaits } from '../components/studioTools'

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
 * failed. A session on another machine gets them once pw code there has the
 * Studio, until ACTIVATE can give them to the session itself.
 */
export function PWCodeToolsNote({ conversationId, link }: { conversationId: string; link: ChatLink }) {
  const [hidden, setHidden] = useState(() => !!link.relay && seen(conversationId))
  // The chat whose setup card is open.
  const [open, setOpen] = useState(() => (autoSetupWaits(conversationId) ? conversationId : ''))
  useEffect(() => { if (autoSetupWaits(conversationId)) setOpen(conversationId) }, [conversationId])
  useEffect(() => { setHidden(!!link.relay && seen(conversationId)) }, [conversationId, link.relay])
  if (hidden) return null
  if (!link.relay) {
    if (!link.toolsError) return null
    if (link.continued && link.picked) {
      return (
        <NoticeCard tone="warning" onDismiss={() => setHidden(true)}>
          This session started without the Studio's tools, which pw code could not connect to ({link.toolsError}). A session keeps the tools it started with: start a new one to try again.
        </NoticeCard>
      )
    }
    return (
      <NoticeCard tone="warning" onDismiss={() => setHidden(true)}>
        This session does not have the Studio's tools: the Studio tried twice before your last message and could not connect them ({link.toolsError}). It tries again with your next message.
      </NoticeCard>
    )
  }
  if (link.hasTools) return null
  // A session on another machine reaches the Studio's tools once its
  // machine's pw code has the Studio as an MCP server: one line, and behind
  // it the setup card, which shows the one-time command and checks that it
  // worked (/api/mcp/bridge). A session continued with the box ticked opens it.
  const dismiss = () => { markSeen(conversationId); setHidden(true) }
  if (open !== conversationId) {
    return (
      <NoticeCard tone="neutral" testId="pwcode-tools-line" onDismiss={dismiss}>
        This session on {link.machineName || 'its machine'} works without the Studio's tools.{' '}
        <button type="button" className="code-approval-more" onClick={() => setOpen(conversationId)}>Add them</button>
      </NoticeCard>
    )
  }
  return <>
    {link.toolsError && <NoticeCard tone="warning">This session started without the Studio's tools: {link.toolsError.replace(/\.$/, '')}.</NoticeCard>}
    <StudioToolsSetup machineName={link.machineName} conversationId={conversationId} onDismiss={dismiss} />
  </>
}
