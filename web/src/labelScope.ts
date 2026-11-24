/** Conversation label scope: the chat adapter reads this at request time, so
 *  it lives outside React state. Set from the ChatView scope chips. */
let scope: string[] = []

export function getLabelScope(): string[] {
  return scope
}

export function setLabelScope(tags: string[]): void {
  scope = tags
}


/** The persona chosen for the current conversation, read by the adapter
 *  when it sends a turn. Module state rather than a prop because the
 *  composer belongs to the chat package. */
let persona: string | null = null
export function setPersona(name: string | null): void { persona = name || null }
export function getPersona(): string | null { return persona }

/** The libraries the assistant searches and queries in this chat, as the
 *  request sends them: "all", one id, or a list of ids; null leaves it to
 *  the server, which means the knowledge base. Set from the chat's Scope
 *  menu (scope.ts keeps the choice). */
export type ChatLibraries = string | string[] | null
let chatLibrary: ChatLibraries = null
export function setChatLibrary(v: ChatLibraries): void { chatLibrary = (Array.isArray(v) ? (v.length ? v : null) : v || null) }
export function getChatLibrary(): ChatLibraries { return chatLibrary }

/** Reasoning effort for the chat's turns: '' leaves it to the model, else
 *  one of EFFORTS. Each conversation keeps its own, a new chat starts from
 *  the last one chosen, and both are kept in this browser. */
export const EFFORTS = [
  { value: '', label: 'Default', hint: "The model's own effort" },
  { value: 'low', label: 'Low', hint: 'Quick answers, little reasoning' },
  { value: 'medium', label: 'Medium', hint: 'Balanced' },
  { value: 'high', label: 'High', hint: 'Reasons longer before answering' },
  { value: 'max', label: 'Max', hint: 'The most reasoning the model allows' },
] as const
const EFFORT_LAST = 'ade-chat-effort'
const EFFORT_BY_CHAT = 'ade-chat-effort-by-chat'
const valid = (v: unknown): string => (typeof v === 'string' && EFFORTS.some(e => e.value === v) ? v : '')
function byChat(): Record<string, string> {
  try { return JSON.parse(localStorage.getItem(EFFORT_BY_CHAT) || '{}') as Record<string, string> } catch { return {} }
}
export function getEffort(conversationId?: string | null): string {
  try {
    if (conversationId) {
      const kept = byChat()[conversationId]
      if (kept !== undefined) return valid(kept)
    }
    return valid(localStorage.getItem(EFFORT_LAST))
  } catch { return '' }
}
/** Whether the conversation has chosen an effort of its own. */
export function hasEffort(conversationId: string): boolean {
  try { return byChat()[conversationId] !== undefined } catch { return false }
}
export function setEffort(value: string, conversationId?: string | null): void {
  const v = valid(value)
  try {
    localStorage.setItem(EFFORT_LAST, v)
    if (conversationId) {
      const all = byChat()
      all[conversationId] = v
      // Keep the map small: the newest 200 conversations.
      const keys = Object.keys(all)
      for (const k of keys.slice(0, Math.max(0, keys.length - 200))) delete all[k]
      localStorage.setItem(EFFORT_BY_CHAT, JSON.stringify(all))
    }
  } catch { /* storage unavailable: the choice lasts until reload */ }
  window.dispatchEvent(new CustomEvent('ade:chat-effort', { detail: { value: v, conversationId: conversationId ?? null } }))
}

/** What answers a conversation: 'pwcode', 'studio', or '' for the Studio's
 *  default (GET /api/chat/engine). The server keeps each conversation's
 *  own (the engine that answered it last, or the one the person switched
 *  it to), so a chat answers the same from any browser; this browser keeps
 *  only the last choice, which a new chat starts with. */
export type Engine = '' | 'pwcode' | 'studio'
const ENGINE_LAST = 'ade-chat-engine'
const validEngine = (v: unknown): Engine => (v === 'pwcode' || v === 'studio' ? v : '')
/** The engine last chosen in this browser, for new chats. */
export function getLastEngine(): Engine {
  try { return validEngine(localStorage.getItem(ENGINE_LAST)) } catch { return '' }
}
/** What is known here of each conversation's engine: the server's answer
 *  (the chat's record, a reply's stream, a switch), so the chat's parts
 *  agree on it until it is read again. */
const known = new Map<string, Engine>()
export function knownEngine(conversationId: string | null | undefined): Engine {
  return conversationId ? known.get(conversationId) ?? '' : ''
}
/** Records what answers a conversation, as the server says it now. */
export function knowEngine(conversationId: string, value: Engine): void {
  const v = validEngine(value)
  if (known.get(conversationId) === v) return
  known.set(conversationId, v)
  window.dispatchEvent(new CustomEvent('ade:chat-engine', { detail: { value: v, conversationId } }))
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
  else window.dispatchEvent(new CustomEvent('ade:chat-engine', { detail: { value, conversationId: null } }))
}
