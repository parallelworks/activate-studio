/**
 * On a touch screen, the page's chrome makes room for what is being read.
 * Scrolling down a chat or a file hides the page header and the chat's
 * controls, and on a tablet folds the navigation away; scrolling back up, or
 * a tap near the top edge, brings them back. A swipe in from the left edge
 * opens the navigation over the page, and a tap outside it or a pick closes
 * it. Only a person's own scrolling counts: a reply streaming in, which moves
 * the chat by script, leaves the chrome as it is. The composer is not part of
 * the chrome. A desktop window, or a tablet with a mouse or trackpad, keeps
 * its chrome; the CSS is under the same touch query, and motion is left out
 * where the person asks for less of it.
 *
 * The state is two attributes on the root element: data-chrome="hidden" and
 * data-nav="open".
 */

import { useSyncExternalStore } from 'react'

/** A touch screen with no mouse or trackpad, as in styles.css. */
export const CHROME_TOUCH_QUERY = '(hover: none) and (pointer: coarse)'

/** How far a person scrolls, in one direction, before the chrome follows. */
export const HIDE_AFTER = 48
export const SHOW_AFTER = 32
/** Near the top of a scroller the chrome always shows. */
export const TOP_ZONE = 16
/** A tap this close to the top of the screen brings the chrome back. */
export const TAP_ZONE = 44
/** A swipe that starts this close to the left edge, and travels this far
 *  sideways, opens the navigation. */
export const EDGE_ZONE = 24
export const EDGE_TRAVEL = 48
/** Momentum carries a fling on after the finger lifts, in steps of at most
 *  this much a frame. */
const FLING_MS = 1200
const FLING_STEP = 160

export interface ChromeState { hidden: boolean; navOpen: boolean }

/** The decisions, apart from the page: fed scrolls and touches, it says what
 *  the chrome should be. */
export function chromeWatch(apply: (s: ChromeState) => void, now: () => number = () => performance.now()) {
  const state: ChromeState = { hidden: false, navOpen: false }
  const last = new WeakMap<object, number>()
  let run = 0
  let touching = false
  let liftedAt = Number.NEGATIVE_INFINITY
  let edge: { x: number; y: number } | null = null
  const set = (patch: Partial<ChromeState>) => {
    const next = { ...state, ...patch }
    if (next.hidden === state.hidden && next.navOpen === state.navOpen) return
    Object.assign(state, next)
    apply({ ...state })
  }
  let fingerY: number | null = null
  /** Reading on (dy > 0) or back, by this much. */
  const feed = (dy: number) => {
    if (dy === 0) return
    run = Math.sign(dy) === Math.sign(run) ? run + dy : dy
    if (run >= HIDE_AFTER && !state.navOpen) set({ hidden: true })
    else if (run <= -SHOW_AFTER) set({ hidden: false })
  }
  return {
    state: () => ({ ...state }),
    /** A scroller moved to `top`. While a finger is down its own movement
     *  decides (touchMove): the chat moves itself by script as it loads rows
     *  above, so its scroll is not the person's. After the finger lifts, a
     *  fling's scrolling counts, leaving out a jump no fling makes in one step. */
    scroll(el: object, top: number) {
      const before = last.get(el)
      last.set(el, top)
      if (top <= TOP_ZONE) { run = 0; set({ hidden: false }); return }
      if (before === undefined || touching || now() - liftedAt >= FLING_MS) return
      const dy = top - before
      if (Math.abs(dy) <= FLING_STEP) feed(dy)
    },
    touchStart(x: number, y: number, inNav: boolean) {
      touching = true
      fingerY = inNav ? null : y
      edge = x <= EDGE_ZONE && state.hidden && !state.navOpen ? { x, y } : null
      if (state.navOpen && !inNav) { set({ navOpen: false }); return }
      if (state.hidden && y <= TAP_ZONE) { run = 0; set({ hidden: false }) }
    },
    touchMove(x: number, y: number) {
      if (edge) {
        const dx = x - edge.x
        if (dx >= EDGE_TRAVEL && Math.abs(y - edge.y) < dx) { edge = null; fingerY = null; set({ navOpen: true }) }
        return
      }
      // A finger moving up the screen reads on.
      if (fingerY !== null) { feed(fingerY - y); fingerY = y }
    },
    touchEnd() {
      touching = false
      fingerY = null
      liftedAt = now()
      edge = null
    },
    /** A pick in the navigation, or the layout changed: the overlay closes. */
    closeNav() { set({ navOpen: false }) },
    reset() { run = 0; set({ hidden: false, navOpen: false }) },
  }
}

let folded = false
const watchers = new Set<() => void>()
const subscribe = (fn: () => void) => { watchers.add(fn); return () => { watchers.delete(fn) } }
/** Whether the chrome is folded away now, for a page whose own layout
 *  should hold still while it is (the Library's tree). */
export function useChromeFolded(): boolean {
  return useSyncExternalStore(subscribe, () => folded, () => false)
}

/** Starts it on the page, while the window is a touch screen's. */
export function installTabletChrome(): void {
  if (typeof matchMedia !== 'function') return
  const root = document.documentElement
  const watch = chromeWatch(s => {
    if (s.hidden) root.dataset['chrome'] = 'hidden'
    else delete root.dataset['chrome']
    if (folded !== s.hidden) { folded = s.hidden; for (const fn of [...watchers]) fn() }
    if (s.navOpen) root.dataset['nav'] = 'open'
    else delete root.dataset['nav']
  })
  const touch = matchMedia(CHROME_TOUCH_QUERY)
  const inNav = (t: EventTarget | null) => t instanceof Element && !!t.closest('.sidenav')
  const point = (e: TouchEvent) => e.touches[0] ?? e.changedTouches[0]
  const onScroll = (e: Event) => {
    const t = e.target
    if (!(t instanceof Element) || t.closest('.sidenav')) return
    // Only a scroller that holds what is read: a chat, a file, a list.
    if (t.clientHeight < window.innerHeight / 3) return
    watch.scroll(t, t.scrollTop)
  }
  const onStart = (e: TouchEvent) => { const p = point(e); if (p) watch.touchStart(p.clientX, p.clientY, inNav(e.target)) }
  const onMove = (e: TouchEvent) => { const p = point(e); if (p) watch.touchMove(p.clientX, p.clientY) }
  const onEnd = () => watch.touchEnd()
  const onClick = (e: Event) => { if (inNav(e.target) && (e.target as Element).closest('a, button')) watch.closeNav() }
  const on = () => {
    document.addEventListener('scroll', onScroll, { capture: true, passive: true })
    document.addEventListener('touchstart', onStart, { capture: true, passive: true })
    document.addEventListener('touchmove', onMove, { capture: true, passive: true })
    document.addEventListener('touchend', onEnd, { capture: true, passive: true })
    document.addEventListener('touchcancel', onEnd, { capture: true, passive: true })
    document.addEventListener('click', onClick, { capture: true })
  }
  const off = () => {
    document.removeEventListener('scroll', onScroll, { capture: true })
    document.removeEventListener('touchstart', onStart, { capture: true })
    document.removeEventListener('touchmove', onMove, { capture: true })
    document.removeEventListener('touchend', onEnd, { capture: true })
    document.removeEventListener('touchcancel', onEnd, { capture: true })
    document.removeEventListener('click', onClick, { capture: true })
    watch.reset()
  }
  if (touch.matches) on()
  touch.addEventListener?.('change', () => (touch.matches ? on() : off()))
  // A new page starts with its chrome.
  window.addEventListener('hashchange', () => watch.reset())
}
