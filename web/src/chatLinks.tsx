import type { ComponentProps } from 'react'
import { ExternalLink } from '@parallelworks/ui'
import { useEngine } from './chatEngine'

/**
 * Where a continued chat's messages go, in the note that opens it
 * (pwcodecontinue.go continueNote): to its pw code session while pw code
 * answers the chat. markNotes (adapter.ts) makes that sentence a link with
 * this address and the chat's id, which ChatMarkdownLink draws as text
 * that follows what answers the chat (chatEngine.ts).
 */
export const GOES_TO = '#note-goes-to='
export const GOES_TO_SESSION = 'Messages you send here go to that session.'
export const GOES_TO_STUDIO = 'The Studio assistant answers this chat now, so messages you send here do not go to that session.'

function GoesTo({ conversationId }: { conversationId: string }) {
  const { current } = useEngine(conversationId)
  return <>{current === 'studio' ? GOES_TO_STUDIO : GOES_TO_SESSION}</>
}

/**
 * A link in a chat message. One to a place in the Studio (#view=, #open=,
 * #chat=) opens here, in this tab (App.tsx routes it), so it is a plain
 * link and is not said to open a new tab; any other opens in a new tab,
 * as the chat package draws it, and says so.
 */
export function ChatMarkdownLink({ node: _node, href, className, children, ...props }: ComponentProps<'a'> & { node?: unknown }) {
  const cls = ['link wrap-break-word', className].filter(Boolean).join(' ')
  if (typeof href === 'string' && href.startsWith(GOES_TO)) return <GoesTo conversationId={href.slice(GOES_TO.length)} />
  if (typeof href === 'string' && href.startsWith('#')) {
    // The markdown hands every link a new tab's target; this one has none.
    const { target: _target, rel: _rel, ...here } = props
    return <a {...here} href={href} className={cls}>{children}</a>
  }
  return <ExternalLink {...props} href={href ?? ''} className={cls}>{children}</ExternalLink>
}
