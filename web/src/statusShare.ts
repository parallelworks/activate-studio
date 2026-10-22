import { useSyncExternalStore } from 'react'
import { api, type LibraryStats } from './api'

/**
 * What the sidebar footer and the chat already know, shared with the
 * status panel so it can show the same thing without asking the server
 * again: model health, the index counts, who is signed in, and the model
 * the chat has selected. The footer and the chat publish; the panel reads.
 */

export interface AiHealth {
  ok: boolean
  status: 'ok' | 'auth' | 'unreachable' | 'offline' | 'unconfigured' | 'key-required' | 'off'
  models: number
  gatewayHost: string
  message: string | null
  lastCallFailure: { at: string; message: string; auth: boolean } | null
  checkedAt: string
  unlockUrl?: string | null
  /** Whether the viewer's own key reaches the platform API; null when not known. */
  platformAccess?: boolean | null
}

export interface KbStats { files?: number; dirs?: number; totalBytes?: number; libraries?: LibraryStats[] }

export interface Shared {
  ai: AiHealth | null
  stats: KbStats | null
  /** Whether this deployment signs people in through the platform. */
  authEnabled: boolean | null
  /** The model the chat has selected (its default until one is picked), by
   *  the name its picker shows, and the provider beside it. */
  model: string | null
  modelProvider: string | null
}

let state: Shared = { ai: null, stats: null, authEnabled: null, model: null, modelProvider: null }
const subs = new Set<() => void>()

export function share(patch: Partial<Shared>): void {
  state = { ...state, ...patch }
  for (const fn of subs) fn()
}

export function useShared(): Shared {
  return useSyncExternalStore(
    fn => { subs.add(fn); return () => { subs.delete(fn) } },
    () => state,
  )
}

/** A model's name as the chat's picker shows it: its own name, or its id
 *  without the provider part. */
export function modelLabel(m: { id: string; name?: string; provider?: string }): string {
  const idName = m.id.includes('/') ? m.id.slice(m.id.indexOf('/') + 1) : m.id
  const name = m.name?.trim()
  return !name || name === `${m.provider} (${idName})` ? idName : name
}

/** Runs an index pass now and tells the views that read the index (the
 *  library drops its cached tree, the footer refreshes its counts). A
 *  failure shows in the index status, so it is not thrown. */
export async function syncIndex(): Promise<void> {
  try {
    await api.sweepNow()
    window.dispatchEvent(new Event('ade-kb-changed'))
  } catch { /* surfaced via the index status */ }
}
