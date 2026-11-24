// The model a new pw code chat runs on, picked in its composer before the
// first message (NewChatModel.tsx). The first message carries it, and the
// Studio makes the chat's session with it (server-go chat, pwcodeSession);
// a chat whose session exists picks in the session's own composer.

export interface NewChatPick { model: string; allocation?: string | null }

/** The key of a chat that has no conversation yet. */
export const NEW_CHAT = ''

const picks = new Map<string, NewChatPick>()
const EVENT = 'ade-pwcode-new-model'

export function pickedModel(conversationId: string | null | undefined): NewChatPick | undefined {
  return picks.get(conversationId ?? NEW_CHAT) ?? (conversationId ? picks.get(NEW_CHAT) : undefined)
}

/** Sets the pick, or a part of it (the allocation alone, say). */
export function pickModel(conversationId: string | null | undefined, change: Partial<NewChatPick>): void {
  const key = conversationId ?? NEW_CHAT
  picks.set(key, { model: '', ...picks.get(key), ...change })
  window.dispatchEvent(new CustomEvent(EVENT))
}

/** Forgets the pick: the chat is to start on another machine, whose models
 *  are its own. */
export function clearModelPick(conversationId: string | null | undefined): void {
  picks.delete(conversationId ?? NEW_CHAT)
  window.dispatchEvent(new CustomEvent(EVENT))
}

/** What a message to the Studio carries for the session it makes: the
 *  pick of a chat with no conversation yet moves to the one it gets. */
export function firstMessagePick(conversationId: string | null | undefined): { pwcodeModel?: string; pwcodeAllocation?: string | null } {
  const p = pickedModel(conversationId)
  if (!p?.model) return {}
  if (conversationId && !picks.has(conversationId)) {
    picks.set(conversationId, p)
    picks.delete(NEW_CHAT)
  }
  return { pwcodeModel: p.model, ...(p.allocation !== undefined ? { pwcodeAllocation: p.allocation } : {}) }
}

export function onPick(cb: () => void): () => void {
  window.addEventListener(EVENT, cb)
  return () => window.removeEventListener(EVENT, cb)
}
