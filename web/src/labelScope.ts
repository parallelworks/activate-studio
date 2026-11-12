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

/** The library the assistant searches and queries in this chat: an id, or
 *  "all"; null leaves it to the server, which means the knowledge base. */
let chatLibrary: string | null = null
export function setChatLibrary(id: string | null): void { chatLibrary = id || null }
export function getChatLibrary(): string | null { return chatLibrary }

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
