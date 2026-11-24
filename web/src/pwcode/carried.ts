import type { ChatMessage } from '@parallelworks/ui/ai'

/**
 * The turns a pw code session missed, which the Studio sends before the
 * person's message (pwcodeContext, server-go/internal/chat/pwcodechat.go):
 * a heading, the turns, then this line before what the person typed. The
 * session reads them all; the chat shows only what the person typed, so
 * a session's own transcript is drawn without them.
 */
const HEADS = ['Earlier in this conversation, before you joined it:', 'In this conversation since your last reply:']
const TYPED = "\n\nThe person's new message:\n\n"

/** What the person typed, from a message the Studio sent a session. */
export function typedPart(text: string): string {
  if (!HEADS.some(h => text.startsWith(h))) return text
  const at = text.indexOf(TYPED)
  return at < 0 ? text : text.slice(at + TYPED.length)
}

/** A session's messages with each of the person's as they typed it; the
 *  list itself when none carried turns. */
export function withoutCarried<L extends readonly ChatMessage[] | undefined>(messages: L): L {
  if (!messages) return messages
  let changed = false
  const out = messages.map(m => {
    if (m.role !== 'user') return m
    const content = typeof m.content === 'string' ? typedPart(m.content) : m.content
    const parts = m.parts?.map(p => (p.kind === 'text' && typedPart(p.text) !== p.text ? { ...p, text: typedPart(p.text) } : p))
    const partsChanged = !!parts && parts.some((p, i) => p !== m.parts?.[i])
    if (content === m.content && !partsChanged) return m
    changed = true
    return { ...m, content, ...(partsChanged ? { parts } : {}) }
  })
  return (changed ? out : messages) as L
}
