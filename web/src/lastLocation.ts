/**
 * Remember where the user was, so a refresh returns them there.
 *
 * All navigation state lives in the hash (#view=, #open=, #chat=), which
 * survives a refresh in a normal tab because the address bar holds it. In
 * the platform frame it does not: refreshing the page reloads this app from
 * its bare src, the hash is gone, and the app opens on the default view.
 * The last hash is kept here and put back before React reads it.
 */

const KEY = 'ade-last-hash'

/**
 * Session storage, not local: a refresh keeps the tab, so the restore does
 * what it is for, while a new tab starts on the default view. Remembering
 * across tabs meant the bare URL always reopened the last file, with no way
 * to get back to the top level, and a file deleted since then reopened as
 * an error every time.
 */
function store(): Storage | null {
  try { return window.sessionStorage } catch { return null }
}

/** Record the hash the app itself navigated to. */
export function rememberHash(hash: string): void {
  try { store()?.setItem(KEY, hash || '') } catch { /* storage unavailable */ }
}

/** Forget it, when what it points at is gone. */
export function forgetHash(): void {
  try { store()?.removeItem(KEY) } catch { /* storage unavailable */ }
}

/** Must run before the first render: components read location.hash in their
 *  state initializers, and replaceState fires no event to correct them. */
export function restoreLastHash(): void {
  try {
    if (location.hash) return
    const last = store()?.getItem(KEY)
    if (!last || !last.startsWith('#')) return
    history.replaceState(null, '', location.pathname + location.search + last)
  } catch { /* storage unavailable, or a hash we cannot restore */ }
}

export function rememberHashChanges(): void {
  const save = () => rememberHash(location.hash)
  // pushState and replaceState fire no event, so the app's own navigation is
  // caught on the way out as well as on hashchange.
  window.addEventListener('hashchange', save)
  window.addEventListener('pagehide', save)
  window.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') save() })
}
