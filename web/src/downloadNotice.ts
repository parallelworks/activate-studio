import { toast } from '@parallelworks/ui'
import { inAppWindow } from './nativeShell'

/**
 * A short notice when the page saves a file: an exported conversation, an
 * Export CSV, a code block's download. In the Linux and Windows app window
 * the shell writes the file to the Downloads folder and reports the path it
 * wrote, or why it could not (studio-shell-saved), and the notice names that
 * path. The macOS app's window saves to Downloads and shows the file in the
 * Finder; a browser keeps it with its other downloads. A failure is shown as
 * an error with the reason.
 */

export const SAVED_EVENT = 'studio-shell-saved'

export interface SavedDetail { name: string; path?: string; error?: string }

/** The folder part of a path, for either separator. */
function folderOf(path: string): string {
  const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return i > 0 ? path.slice(0, i) : path
}
const baseOf = (path: string) => path.slice(Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\')) + 1)

/** What the notice says for a save the app window reported. */
export function savedMessage(d: SavedDetail): { ok: boolean; text: string } {
  if (d.error || !d.path) return { ok: false, text: `Could not save ${d.name || 'the file'}: ${d.error || 'no file was written'}.` }
  return { ok: true, text: `Saved ${baseOf(d.path)} to ${folderOf(d.path)}.` }
}

/** What the notice says for a file the page handed to the browser. */
export function downloadedMessage(name: string, appWindow: boolean): string {
  return appWindow ? `Saved ${name} to Downloads.` : `Downloaded ${name} to your browser's downloads folder.`
}

/** Starts the notices; returns a function that stops them. */
export function installDownloadNotices(): () => void {
  const onSaved = (e: Event) => {
    const m = savedMessage(((e as CustomEvent).detail ?? {}) as SavedDetail)
    if (m.ok) toast.success(m.text)
    else toast.error(m.text)
  }
  // Outside the Linux and Windows window nothing reports back, so the
  // notice follows the link the page clicked to save a file it made.
  const onClick = (e: MouseEvent) => {
    if ('__studioShell' in window) return
    const a = (e.target as Element | null)?.closest?.('a[download]') as HTMLAnchorElement | null
    if (!a || !/^(blob|data):/.test(a.href)) return
    toast.success(downloadedMessage(a.getAttribute('download') || 'the file', inAppWindow()))
  }
  window.addEventListener(SAVED_EVENT, onSaved)
  window.addEventListener('click', onClick)
  return () => {
    window.removeEventListener(SAVED_EVENT, onSaved)
    window.removeEventListener('click', onClick)
  }
}
