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

export function onShellScroll(e: Event): void {
  const t = e.target === document ? document.scrollingElement : e.target
  if (!(t instanceof Element)) return
  if (isShellBox(t)) {
    if (t.scrollTop || t.scrollLeft) { t.scrollTop = 0; t.scrollLeft = 0 }
  } else if (t.matches('.sidenav') && t.scrollLeft) {
    t.scrollLeft = 0
  }
}

export function keepShellInPlace(): void {
  document.addEventListener('scroll', onShellScroll, { capture: true, passive: true })
}
