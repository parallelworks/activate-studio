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

// The Studio's instructions after the first message to a session on
// another machine (relayInstructionsHead, pwcodestart.go).
const TOLD = '\n\nFrom ACTIVATE Studio, for this session (the person did not type this):\n\n'

/** What the person typed, from a message the Studio sent a session. */
export function typedPart(sent: string): string {
  const told = sent.indexOf(TOLD)
  const text = told < 0 ? sent : sent.slice(0, told)
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

/** The id a turn from the chat's record has when it is drawn in the
 *  session's view (withEarlier), and a Studio assistant's reply among
 *  them; styles.css marks those rows. */
export const EARLIER = 'earlier-'
export const EARLIER_STUDIO = 'earlier-studio-'

const textOf = (m: ChatMessage): string =>
  typeof m.content === 'string' && m.content ? m.content : (m.parts ?? []).flatMap(p => (p.kind === 'text' ? [p.text] : [])).join('')
const byPWCode = (m: ChatMessage): boolean =>
  m.id.startsWith('pwc-') || m.id.startsWith('pwh-') || String((m as { model?: string }).model ?? '').startsWith('pw code')

/** The first of the person's messages in the record, from index `from`,
 *  that reads `typed`. */
function typedAt(record: readonly ChatMessage[], typed: string, from: number): number {
  for (let i = from; i < record.length; i++) {
    if (record[i].role === 'user' && textOf(record[i]).trim() === typed) return i
  }
  return -1
}

/** The chat's turns a session has not been told: those after one message
 *  ("" for the chat's start), up to another. */
export interface Owed { after?: string; upto: string }

/** Where in a session's messages the turns it is owed came: after the last
 *  of them the chat's record has, and its answer. */
function owedAt(messages: readonly ChatMessage[], record: readonly ChatMessage[]): number {
  let from = 0, at = 0
  messages.forEach((m, i) => {
    if (m.role !== 'user') return
    const found = typedAt(record, typedPart(textOf(m)).trim(), from)
    if (found < 0) return
    from = found + 1
    at = messages.findIndex((x, j) => j > i && x.role === 'user')
    if (at < 0) at = messages.length
  })
  return at
}

/**
 * A session's messages with the chat's turns it was given as carried over
 * (pwcodeContext) drawn from the chat's record, where they came: the
 * chat's turns before the session joined it above its first message, and
 * the Studio assistant's turns since its last reply before the message
 * they came with. They are drawn only; the session has them already and
 * is never sent them again. Each such turn's id starts with EARLIER.
 * Then the session's messages are drawn as typed (withoutCarried).
 */
export function withEarlier<L extends readonly ChatMessage[] | undefined>(messages: L, record: readonly ChatMessage[] | undefined, owed?: Owed): L {
  if (!messages || !record?.length) return messages
  const out: ChatMessage[] = []
  let from = 0
  const draw = (turns: readonly ChatMessage[], all: boolean) => {
    for (const r of turns) {
      if ((r.role === 'user' || r.role === 'assistant') && !r.id.startsWith('note-') && (all || !byPWCode(r))) {
        out.push({ ...r, id: (r.role === 'assistant' && !byPWCode(r) ? EARLIER_STUDIO : EARLIER) + r.id })
      }
    }
  }
  const owes = owed ? record.slice(record.findIndex(r => r.id === owed.after) + 1, record.findIndex(r => r.id === owed.upto) + 1) : []
  const before = owes.length ? owedAt(messages, record) : -1
  let i = 0
  for (const m of messages) {
    if (i++ === before) draw(owes, !owed?.after)
    if (m.role === 'user') {
      const text = textOf(m)
      const head = HEADS.find(h => text.startsWith(h))
      const at = typedAt(record, typedPart(text).trim(), from)
      if (at >= 0) {
        // Before the session joined, every turn of the chat is new to its
        // view; since its last reply, only the Studio assistant's turns.
        if (head) draw(record.slice(from, at), head === HEADS[0])
        from = at + 1
      }
    }
    out.push(m)
  }
  if (i === before) draw(owes, !owed?.after)
  return (out.length === messages.length ? messages : out) as unknown as L
}
