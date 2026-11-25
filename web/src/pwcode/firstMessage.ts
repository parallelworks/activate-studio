// What a new chat's session is to do with the chat's first message, which
// the Studio made it for and did not send (server-go chat, pwcodefirst.go):
// a command, run in the session's view as a later one is, or text it
// refused, put back in the box with why.

export interface FirstMessage { text: string; refused: string }

const waiting = new Map<string, FirstMessage>()
// Chats whose message the chat package still shows as sent.
const handed = new Set<string>()
const EVENT = 'ade-pwcode-first'

export function handFirstMessage(conversationId: string, first: FirstMessage): void {
  waiting.set(conversationId, first)
  handed.add(conversationId)
  window.dispatchEvent(new CustomEvent(EVENT))
}

export function firstMessage(conversationId: string | undefined): FirstMessage | undefined {
  return conversationId ? waiting.get(conversationId) : undefined
}

/** Takes it: it is run, or put back, once. */
export function takeFirstMessage(conversationId: string | undefined): FirstMessage | undefined {
  const first = firstMessage(conversationId)
  if (first) {
    waiting.delete(conversationId!)
    window.dispatchEvent(new CustomEvent(EVENT))
  }
  return first
}

/** Whether the chat package's copy of the chat still holds the message;
 *  asking again for the same chat says no. */
export function takeHanded(conversationId: string): boolean {
  return handed.delete(conversationId)
}

export function onFirstMessage(cb: () => void): () => void {
  window.addEventListener(EVENT, cb)
  return () => window.removeEventListener(EVENT, cb)
}
