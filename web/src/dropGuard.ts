/** A field a person can drop text into, as the browser does itself. */
const takesText = (t: EventTarget | null): boolean =>
  t instanceof Element && !!t.closest('textarea:not([disabled]):not([readonly]), input:not([disabled]):not([readonly]), [contenteditable=""], [contenteditable="true"]')

/**
 * A file or a link dropped where no drop zone took it would otherwise
 * replace the page: a browser, or the desktop window, opens what it is
 * given. Text dragged into a field is left to the browser, which puts it in.
 */
export function guardDrops(target: EventTarget = window): void {
  for (const type of ['dragover', 'drop']) {
    target.addEventListener(type, e => {
      const files = (e as DragEvent).dataTransfer?.types.includes('Files')
      if (files || !takesText(e.target)) e.preventDefault()
    })
  }
}
