/**
 * The shell never scrolls; each pane scrolls itself. A scrollIntoView or
 * focus anywhere (a frame, the chat package) can still scroll the window
 * and boxes that only hide their overflow, which slid the Studio up and
 * sideways out of the user's reach. Whatever a script scrolls there is put
 * back at once.
 */
const SHELL = '.app, .app-body, main.content, .chat-canvas, .chat-wrap, .view'

/** Whether a box is part of the shell. Every box around the chat's thread
 *  counts, whatever the package calls it: the composer sits inside the
 *  thread, so moving one of them lifts the composer with it. The package's
 *  drawer layout adds such a box, which the list above did not name. */
export function isShellBox(t: Element): boolean {
  if (t === document.scrollingElement || t === document.body || t.id === 'root' || t.matches(SHELL)) return true
  return !!t.closest('.chat-canvas') && !!t.querySelector('.chat-thread')
}

/** Whether the page is pinched in or out (Safari on an iPad). The visible
 *  part is then not the layout, and Safari moves the page under the pinch
 *  itself: a script putting the page back fought the gesture, and the page
 *  was drawn in one place while taps landed in another. */
export function pinched(vv: { scale: number } | null = window.visualViewport): boolean {
  return !!vv && Math.abs(vv.scale - 1) > 0.01
}

const isRoot = (t: Element) => t === document.scrollingElement || t === document.body || t === document.documentElement

export function onShellScroll(e: Event): void {
  const t = e.target === document ? document.scrollingElement : e.target
  if (!(t instanceof Element)) return
  if (isRoot(t) && pinched()) return
  if (isShellBox(t)) {
    if (t.scrollTop || t.scrollLeft) { t.scrollTop = 0; t.scrollLeft = 0 }
  } else if (t.matches('.sidenav') && t.scrollLeft) {
    t.scrollLeft = 0
  }
}

export function keepShellInPlace(): void {
  document.addEventListener('scroll', onShellScroll, { capture: true, passive: true })
}

/** iPadOS pans the page to keep the composer above its on-screen keyboard
 *  and can leave it panned once the keyboard closes, which strands the
 *  composer mid screen over blank page. When the visible area grows back
 *  by a keyboard's height, the page goes back to the top. A pinch zooming
 *  out grows the visible area too, but it changes the scale, which a
 *  keyboard does not: the page is left to the pinch then. */
export function settleAfterKeyboard(vv: VisualViewport | null = window.visualViewport, toTop = () => window.scrollTo(0, 0)): void {
  if (!vv) return
  let last = vv.height
  let scale = vv.scale
  vv.addEventListener('resize', () => {
    const zooming = pinched(vv) || Math.abs(vv.scale - scale) > 0.01
    if (!zooming && vv.height > last + 80 && (window.scrollY || vv.offsetTop)) toTop()
    last = vv.height
    scale = vv.scale
  })
}

/** The layout viewport's size, which fixed layers and getBoundingClientRect
 *  measure in. Safari on an iPad shrinks innerWidth and innerHeight to the
 *  visible part while the page is pinched in, so a menu or tip kept inside
 *  them landed away from what was tapped. */
export function layoutSize(): { width: number; height: number } {
  const root = document.documentElement
  return { width: root.clientWidth || window.innerWidth, height: root.clientHeight || window.innerHeight }
}
