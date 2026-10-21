import { useSyncExternalStore } from 'react'

/**
 * Whether the status panel shows, shared by the toggles on every page and
 * the panel itself. The viewer's choice is remembered; with none, the panel
 * starts open on a wide screen and closed on a narrow one, and a phone is
 * not offered it at all.
 *
 * On a page with a side view (the chat's runs and sessions, the Workflows
 * page's), the two do not show together: while the side view is up the
 * panel steps aside, without changing the remembered choice, and the
 * page's toggle brings the panel back by hiding the side view.
 */

const KEY = 'ade-status-panel'
const PHONE = '(max-width: 760px)'
const WIDE_PX = 1200

type SideTarget = 'chat' | 'page'
interface PanelState { open: boolean; view: string; side: Record<SideTarget, boolean> }

function remembered(): boolean {
  try {
    const v = localStorage.getItem(KEY)
    if (v === '1' || v === '0') return v === '1'
  } catch { /* storage unavailable */ }
  return typeof window !== 'undefined' && window.innerWidth >= WIDE_PX
}

let state: PanelState = { open: remembered(), view: 'chat', side: { chat: false, page: false } }
const subs = new Set<() => void>()
const emit = () => { for (const fn of subs) fn() }
const set = (patch: Partial<PanelState>) => { state = { ...state, ...patch }; emit() }

/** The side view a page has: the chat's, or the Workflows page's. */
export function sideTargetOf(view: string): SideTarget | null {
  return view === 'chat' ? 'chat' : view === 'workflows' ? 'page' : null
}

export function setPanelOpen(open: boolean): void {
  try { localStorage.setItem(KEY, open ? '1' : '0') } catch { /* not remembered */ }
  const target = sideTargetOf(state.view)
  // Asked for while a side view has the room: the side view hides.
  if (open && target && state.side[target]) window.dispatchEvent(new CustomEvent('ade:side-view-hide', { detail: target }))
  set({ open })
}

/** The page on screen, which decides whose side view counts. */
export function setPanelView(view: string): void { if (view !== state.view) set({ view }) }

/** A side view reports whether it is showing. */
export function setSideShown(target: SideTarget, shown: boolean): void {
  if (state.side[target] !== shown) set({ side: { ...state.side, [target]: shown } })
}

/** Reads the remembered choice again; tests use it after changing storage. */
export function reloadPanelState(): void { set({ open: remembered() }) }

function subscribePhone(fn: () => void): () => void {
  if (typeof matchMedia !== 'function') return () => {}
  const mq = matchMedia(PHONE)
  mq.addEventListener('change', fn)
  return () => mq.removeEventListener('change', fn)
}
const isPhone = () => (typeof matchMedia === 'function' ? matchMedia(PHONE).matches : false)

export interface Panel {
  /** Not on a phone. */
  offered: boolean
  /** The viewer's choice. */
  open: boolean
  /** On screen now: offered, open, and not stepping aside for a side view. */
  visible: boolean
}

export function usePanel(): Panel {
  const s = useSyncExternalStore(fn => { subs.add(fn); return () => { subs.delete(fn) } }, () => state)
  const phone = useSyncExternalStore(subscribePhone, isPhone)
  const target = sideTargetOf(s.view)
  const offered = !phone
  return { offered, open: s.open, visible: offered && s.open && !(target && s.side[target]) }
}
