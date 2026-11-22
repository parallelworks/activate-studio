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
