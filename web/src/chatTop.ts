/** What floats over the top of the chat thread: the header buttons and
 *  the notices. ChatView measures; these decide. */

/** Drops the header buttons' labels to icons, the engine button's last,
 *  until the row fits the room it has. */
export function fitControls(controls: HTMLElement, room: number | null) {
  controls.classList.remove('tight', 'tighter')
  if (room == null) return
  for (const step of ['tight', 'tighter']) {
    if (controls.getBoundingClientRect().width <= room) return
    controls.classList.add(step)
  }
}

/** How far the start of the conversation sits below the thread's top so
 *  the notices over it cover none of it: their lowest foot, and a gap. */
export function noticePad(threadTop: number, feet: number[]) {
  const foot = Math.max(0, ...feet)
  return foot > threadTop ? Math.ceil(foot - threadTop) + 10 : 0
}

/** Keeps the canvas's notice offset and the thread's centre current
 *  (--chat-notice-pad, --chat-main-x, --chat-main-w; styles.css), and
 *  returns the cleanup. place() only measures and writes; what the
 *  ResizeObserver watches (the canvas, the thread and each notice) changes
 *  only when the DOM does (track, from the MutationObserver). Observing an
 *  element reports it once, so observing from the observer's own callback
 *  would run it on every frame. */
export function watchNotices(canvas: HTMLElement): () => void {
  const set = (name: string, value: string) => { if (canvas.style.getPropertyValue(name) !== value) canvas.style.setProperty(name, value) }
  const notices = () => Array.from(canvas.querySelectorAll<HTMLElement>(':scope > .cred-banner:not(.pwcode-approval)'))
  const place = () => {
    const box = canvas.getBoundingClientRect()
    const main = canvas.querySelector('main')?.getBoundingClientRect()
    if (main) {
      set('--chat-main-x', `${Math.round(main.left - box.left + main.width / 2)}px`)
      set('--chat-main-w', `${Math.round(main.width)}px`)
    }
    const pad = main ? noticePad(main.top, notices().map(n => n.getBoundingClientRect().bottom)) : 0
    set('--chat-notice-pad', `${pad}px`)
  }
  const ro = new ResizeObserver(place)
  const watched = new Set<Element>()
  const track = () => {
    const main = canvas.querySelector('main')
    const want = new Set<Element>([canvas, ...(main ? [main] : []), ...notices()])
    let changed = false
    for (const el of watched) if (!want.has(el)) { ro.unobserve(el); watched.delete(el); changed = true }
    for (const el of want) if (!watched.has(el)) { ro.observe(el); watched.add(el); changed = true }
    // A notice that left changes the pad without resizing anything watched.
    if (changed) place()
  }
  track()
  const mo = new MutationObserver(track)
  mo.observe(canvas, { childList: true, subtree: true })
  return () => { mo.disconnect(); ro.disconnect() }
}
