import { useSyncExternalStore } from 'react'
import { bindLibraryStart, startOf } from './pwcode/newChatStart'

/**
 * What answers each conversation, on this page: the one place the chat's
 * parts learn it from. It holds the Studio's answers to GET
 * /api/chat/engine, and what a reply's stream or a switch said since, both
 * the server's word. Nothing here reads a chat's record: the record names
 * the engine stored for the chat, which is not what answers it for a
 * person pw code is not for here (the Studio does), so taking
 * both made the chat's parts set it back and forth, each change asking
 * the server again.
 *
 * A change reaches the parts through useEngine; no part reads the server
 * again because of one, so a read never leads to another.
 */

/** What answers a conversation: 'pwcode', 'studio', or '' when that is not
 *  known yet (a new chat takes the Studio's default). */
export type Engine = '' | 'pwcode' | 'studio'

/** GET /api/chat/engine: what answers by default, and whether pw code is
 *  offered to this person (with why not). */
export interface EngineInfo {
  setting: 'auto' | 'pwcode' | 'studio'
  /** What a new chat starts with. */
  default: 'pwcode' | 'studio'
  /** What this conversation answers with (its own, or a new chat's). */
  current?: 'pwcode' | 'studio'
  /** Something has answered the conversation. */
  answered?: boolean
  pwcode: boolean
  reason: string | null
  toolsDown?: string | null
  /** The model Settings names for pw code. */
  model?: string
}

const ENGINE_LAST = 'ade-chat-engine'
const validEngine = (v: unknown): Engine => (v === 'pwcode' || v === 'studio' ? v : '')

/** The engine last chosen in this browser, for new chats. The server keeps
 *  each conversation's own, so a chat answers the same from any browser. */
export function getLastEngine(): Engine {
  try { return validEngine(localStorage.getItem(ENGINE_LAST)) } catch { return '' }
}

/** A conversation's entry ('' for a new chat): the Studio's last answer
 *  (undefined until one comes, null when the read failed), and what
 *  answers it now. */
export interface EngineEntry { info?: EngineInfo | null; current: Engine }
const NONE: EngineEntry = { current: '' }
const entries = new Map<string, EngineEntry>()
// When each conversation was last told what answers it (a stream, a
// switch): a read that started before that says what it said before.
const told = new Map<string, number>()
let clock = 0
const listeners = new Set<() => void>()
const reads = new Map<string, Promise<EngineInfo | null>>()

function put(key: string, next: EngineEntry): void {
  const had = entries.get(key)
  if (had && had.current === next.current && JSON.stringify(had.info) === JSON.stringify(next.info)) return
  entries.set(key, next)
  for (const l of [...listeners]) l()
}

/**
 * Asks the Studio what answers a conversation (null: a new chat, which
 * starts with the last choice made here), by the rule its next message is
 * sent by. One read for every part that asks while it is on its way.
 */
export function readEngine(conversationId: string | null): Promise<EngineInfo | null> {
  const key = conversationId ?? ''
  const q = new URLSearchParams()
  if (conversationId) q.set('conversation', conversationId)
  const last = getLastEngine()
  if (last) q.set('last', last)
  const url = `/api/chat/engine?${q}`
  const going = reads.get(url)
  if (going) return going
  const started = ++clock
  const read = fetch(url)
    .then(r => (r.ok ? r.json() as Promise<EngineInfo> : null))
    .catch(() => null)
    .then(d => {
      const had = entries.get(key)
      // What the server says answers a conversation something has answered;
      // what it was told since this read started, if it was.
      const fresh = (told.get(key) ?? 0) < started
      const current = conversationId && fresh && d?.answered && d.current ? d.current : had?.current ?? ''
      put(key, { info: d, current })
      return d
    })
    .finally(() => { if (reads.get(url) === read) reads.delete(url) })
  reads.set(url, read)
  return read
}

/** The Studio's answer for a conversation as the page has it, read only
 *  when it has none yet: for a part that appears once the chat is open,
 *  which the chat bar's menu keeps current. */
export function engineInfo(conversationId: string | null): Promise<EngineInfo | null> {
  const had = entries.get(conversationId ?? '')
  return had?.info !== undefined ? Promise.resolve(had.info) : readEngine(conversationId)
}

/** What answers a conversation, as last heard here. */
export function knownEngine(conversationId: string | null | undefined): Engine {
  return conversationId ? entries.get(conversationId)?.current ?? '' : ''
}

/** Records what answers a conversation, as the server says it now (a
 *  reply's stream, a switch). */
export function knowEngine(conversationId: string, value: Engine): void {
  told.set(conversationId, ++clock)
  const had = entries.get(conversationId)
  put(conversationId, { info: had?.info, current: validEngine(value) })
}

/** Chats whose first message asks for what they were started on. */
const asking = new Set<string>()

/** Sets what answers the new chat being written, without making it the
 *  choice new chats start with ('': the default again). */
export function setNewChatEngine(value: Engine): void {
  if ((entries.get('')?.current ?? '') === value) return
  told.set('', ++clock)
  put('', { info: entries.get('')?.info, current: value })
}

/** A conversation was made for the new chat: one started from the Library
 *  takes where it starts and what answers it along. */
export function newConversation(conversationId: string): void {
  const value = entries.get('')?.current ?? ''
  bindLibraryStart(conversationId)
  if (!value) return
  setNewChatEngine('')
  knowEngine(conversationId, value)
  asking.add(conversationId)
}

/** What a message asks for, in a chat that was started on pw code and that
 *  pw code has not answered: it answers, or says why not. */
export function askedEngine(conversationId: string | null | undefined): { engine?: 'pwcode' } {
  return conversationId && asking.has(conversationId) && knownEngine(conversationId) === 'pwcode' ? { engine: 'pwcode' } : {}
}

/** pw code answers the chat: the server keeps that from here on. */
export function pwcodeAnswers(conversationId: string): void {
  asking.delete(conversationId)
  knowEngine(conversationId, 'pwcode')
}

/** Calls back on every change; returns the way to stop. */
export function onEngine(listener: () => void): () => void {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

/** A conversation's entry, kept current. */
export function useEngine(conversationId: string | null): EngineEntry {
  const key = conversationId ?? ''
  return useSyncExternalStore(onEngine, () => entries.get(key) ?? NONE)
}

/**
 * The person switches what answers a conversation, between turns: the
 * server keeps it for that conversation (PUT .../engine), and it becomes
 * the last choice, which new chats start with. A chat with no
 * conversation yet keeps it here until its first message. Rejects with
 * the server's reason (a reply being written, say).
 */
export async function switchEngine(value: 'pwcode' | 'studio', conversationId?: string | null): Promise<void> {
  if (conversationId) {
    const res = await fetch(`/api/chat/conversations/${encodeURIComponent(conversationId)}/engine`, {
      method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ engine: value }),
    })
    if (!res.ok) throw new Error(((await res.json().catch(() => ({}))) as { error?: string }).error ?? `${res.status}`)
  }
  try { localStorage.setItem(ENGINE_LAST, value) } catch { /* storage unavailable: the choice lasts until reload */ }
  if (conversationId) knowEngine(conversationId, value)
  else {
    // A chat started from the Library keeps its own choice.
    setNewChatEngine(startOf(null).library ? value : '')
    await readEngine(null)
  }
}

/** Forgets what is known: someone else is the viewer, or their ACTIVATE
 *  sign-in changed (viewerCache.ts). */
export function forgetEngines(): void {
  entries.clear(); told.clear(); reads.clear(); asking.clear()
  for (const l of [...listeners]) l()
}
