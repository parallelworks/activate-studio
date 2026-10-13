/**
 * Opening a page in a window beside the Studio's, so the two sit side by
 * side the way a desktop app's built-in browser sits beside its chat. The
 * page takes the right half of the screen. Where the Studio is an installed
 * app its own window moves to the left half; a browser tab cannot be moved
 * or resized by its page, so there the new window is placed and the Studio
 * stays where it is.
 */

interface ScreenLike { availWidth: number; availHeight: number; availLeft?: number; availTop?: number }

/** The window features that put a page on the right half of the screen. */
export function besideFeatures(s: ScreenLike): { features: string; left: { x: number; y: number; w: number; h: number } } {
  const x = s.availLeft ?? 0, y = s.availTop ?? 0
  const half = Math.floor(s.availWidth / 2)
  return {
    features: `popup,left=${x + half},top=${y},width=${s.availWidth - half},height=${s.availHeight}`,
    left: { x, y, w: half, h: s.availHeight },
  }
}

/** Whether the Studio runs as an installed app, whose window it may move. */
export function installedApp(): boolean {
  try { return matchMedia('(display-mode: standalone), (display-mode: window-controls-overlay)').matches } catch { return false }
}

const opened = new Map<string, Window>()

/** Opens `url` beside the Studio, or focuses the window `key` already has.
 *  Returns false when the browser refused the window. */
export function openBeside(url: string, key: string): boolean {
  const had = opened.get(key)
  if (had && !had.closed) { had.focus(); return true }
  const { features, left } = besideFeatures(window.screen as unknown as ScreenLike)
  if (installedApp()) {
    try { window.moveTo(left.x, left.y); window.resizeTo(left.w, left.h) } catch { /* the browser decides */ }
  }
  const w = window.open('', '_blank', features)
  if (!w) return false
  // The page gets no handle back to the Studio.
  try { w.opener = null } catch { /* not ours to change */ }
  w.location.href = url
  opened.set(key, w)
  return true
}
