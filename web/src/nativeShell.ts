/**
 * What the macOS app's own window offers the page, through the
 * `studioShell` message handler its web view installs
 * (deploy/mac/shell/StudioShell.swift): the system folder chooser,
 * following the Studio to another port, and quitting to open again after an
 * update. Elsewhere (a browser, or the app opened in Chrome) there is no
 * handler, and the page uses its own ways.
 */

interface ShellHandler { postMessage(message: unknown): Promise<unknown> }

function handler(): ShellHandler | null {
  const w = window as unknown as { webkit?: { messageHandlers?: { studioShell?: ShellHandler } } }
  return w.webkit?.messageHandlers?.studioShell ?? null
}

/** True in the macOS app's window, which has the system folder chooser. */
export function hasNativeFolderChooser(): boolean {
  return !!handler()
}

/** Opens the system folder chooser at `start`. Resolves with the folder
 *  chosen, or null when the person cancels; throws outside the app's window
 *  or when the window cannot open it. */
export async function chooseFolderNatively(opts: { start?: string; message?: string; prompt?: string } = {}): Promise<string | null> {
  const h = handler()
  if (!h) throw new Error('no system folder chooser here')
  const picked = await h.postMessage({ action: 'chooseFolder', ...opts })
  return typeof picked === 'string' && picked ? picked : null
}

/** Asks the app's window to follow the Studio to another port on this
 *  computer, opening `hash` there. False outside the app's window, where
 *  the page moves itself. */
export async function followStudioToPort(port: number, hash = ''): Promise<boolean> {
  const h = handler()
  if (!h) return false
  try {
    await h.postMessage({ action: 'studioMoved', port, hash })
    return true
  } catch { return false }
}

/** True in the macOS app's own window. */
export function inAppWindow(): boolean {
  return !!handler()
}

/** Asks the app's window to quit and open again, which runs the copy of the
 *  app an update put in place. False outside the app's window, and in a
 *  window from before the app could do this, which has to be reopened by
 *  hand. */
export async function reopenApp(): Promise<boolean> {
  const h = handler()
  if (!h) return false
  try {
    return (await h.postMessage({ action: 'relaunch' })) === true
  } catch { return false }
}
