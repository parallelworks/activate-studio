/** The model a chat's pw code session runs on now, as its session view
 *  reads it from the session's settings. The chat bar's engine button names
 *  it: the model the chat was linked with goes stale once the person picks
 *  another in the composer, on the Code page or with /model. */

const EVENT = 'ade-pwcode-model'
const last = new Map<string, string>()

/** The session view says its model; the engine button follows. */
export function announceModel(conversationId: string, model: string | null | undefined) {
  if (!model || last.get(conversationId) === model) return
  last.set(conversationId, model)
  window.dispatchEvent(new CustomEvent(EVENT, { detail: { conversationId, model } }))
}

/** The model last said for a chat, for a button that mounts after it. */
export function liveModel(conversationId: string): string | undefined {
  return last.get(conversationId)
}

/** Calls back with each model said for the chat; returns the cleanup. */
export function onModel(conversationId: string, cb: (model: string) => void): () => void {
  const on = (e: Event) => {
    const d = (e as CustomEvent).detail as { conversationId?: string; model?: string }
    if (d?.conversationId === conversationId && d.model) cb(d.model)
  }
  window.addEventListener(EVENT, on)
  return () => window.removeEventListener(EVENT, on)
}
