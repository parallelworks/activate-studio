// Where a new pw code chat starts, chosen in its composer before the first
// message (NewChatTarget.tsx). The first message carries it, and the Studio
// makes the chat's session there (server-go chat, pwcodestart.go); with
// nothing chosen, where the Studio always did.
import { NEW_CHAT } from './newChatPick'

/** The Studio's own pw code machine, as the server names it. */
export const STUDIO_MACHINE = 'studio'

/** What the models offered on a machine turn on. */
export interface MachineFacts { id?: string; claudeCodeAllowed?: boolean; claudeInstalled?: boolean }

export interface NewChatStart {
  /** Another of the person's machines; none is the Studio's own. */
  machine?: MachineFacts & { id: string }
  /** The folder; none is the default on the Studio's own machine. */
  folder?: string
  /** The permission mode, on another machine. */
  mode?: string
  /** Whether to add the Studio's tools there first, where it was asked. */
  tools?: boolean
}

const starts = new Map<string, NewChatStart>()
const listeners = new Set<() => void>()
const NONE: NewChatStart = {}

export function startOf(conversationId: string | null | undefined): NewChatStart {
  return starts.get(conversationId ?? NEW_CHAT) ?? (conversationId ? starts.get(NEW_CHAT) : undefined) ?? NONE
}

/** Sets where the chat starts, whole: {} is the default. */
export function setStart(conversationId: string | null | undefined, next: NewChatStart): void {
  const had = startOf(conversationId)
  if (JSON.stringify(had) === JSON.stringify(next)) return
  starts.set(conversationId ?? NEW_CHAT, next)
  for (const l of [...listeners]) l()
}

/** What a message to the Studio carries for the session it makes: the
 *  choice of a chat with no conversation yet moves to the one it gets. */
export function firstMessageStart(conversationId: string | null | undefined): { pwcodeMachine?: string; pwcodeFolder?: string; pwcodeMode?: string; pwcodeTools?: boolean } {
  const s = startOf(conversationId)
  if (conversationId && !starts.has(conversationId) && starts.has(NEW_CHAT)) {
    starts.set(conversationId, s)
    starts.delete(NEW_CHAT)
  }
  return {
    ...(s.machine ? { pwcodeMachine: s.machine.id, ...(s.mode ? { pwcodeMode: s.mode } : {}), ...(s.tools !== undefined ? { pwcodeTools: s.tools } : {}) } : {}),
    ...(s.folder ? { pwcodeFolder: s.folder } : {}),
  }
}

export function onStart(listener: () => void): () => void {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

/** Forgets every choice: someone else is the viewer, or their ACTIVATE
 *  sign-in changed (viewerCache.ts). */
export function forgetStarts(): void {
  starts.clear()
  for (const l of [...listeners]) l()
}
