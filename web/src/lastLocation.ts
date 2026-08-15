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

/** Must run before the first render: components read location.hash in their
 *  state initializers, and replaceState fires no event to correct them. */
export function restoreLastHash(): void {
  try {
    if (location.hash) return
    const last = localStorage.getItem(KEY)
    if (!last || !last.startsWith('#')) return
    history.replaceState(null, '', location.pathname + location.search + last)
  } catch { /* storage unavailable, or a hash we cannot restore */ }
}

export function rememberHashChanges(): void {
  const save = () => {
    try { localStorage.setItem(KEY, location.hash || '') } catch { /* storage unavailable */ }
  }
  // pushState and replaceState fire no event, so the app's own navigation is
  // caught on the way out as well as on hashchange.
  window.addEventListener('hashchange', save)
  window.addEventListener('pagehide', save)
  window.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') save() })
}
