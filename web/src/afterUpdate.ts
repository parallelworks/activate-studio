import { useEffect, useRef, useState } from 'react'
import { reopenApp } from './nativeShell'
import { isNewer, isVersion, takeReopenRequest, type UpdateFlow } from './updateFlow'

/** The newest version this browser has opened the Studio on. */
export const SEEN_KEY = 'ade-seen-version'

export interface AfterUpdate {
  /** This page load is the first on a newer version than this browser last
   *  saw: the footer offers What's new until it is opened. */
  updated: boolean
  /** What's new was opened. */
  clear: () => void
  /** The app's window runs the old copy until the app reopens, and the page
   *  did not ask it to reopen on its own. */
  reopen: boolean
}

/**
 * What the footer says after an update, in the macOS app and a clone. The
 * first time the Studio opens on a newer version than this browser last saw,
 * `updated` is true for that page load. A first visit only records the
 * version, and a hosted deployment records nothing.
 *
 * An update that changed the app's own window runs the old window until the
 * app opens again. After an install the person started on the page before,
 * the app reopens on its own, and the reopened window is the one that says
 * it updated; otherwise `reopen` is true on every page load until the server
 * reports the window runs the new copy.
 */
export function useAfterUpdate(flow: UpdateFlow): AfterUpdate {
  const [updated, setUpdated] = useState(false)
  // Waits until the page has decided whether to ask the app to reopen.
  const [quiet, setQuiet] = useState(true)
  const decided = useRef(false)
  const current = flow.status?.current.version ?? null
  const mode = flow.status?.mode ?? null
  const reopen = mode === 'app' && (!!flow.status?.reopen || flow.status?.state === 'relaunch')

  useEffect(() => {
    if (decided.current || flow.none || !current || !mode || mode === 'hosted' || !isVersion(current)) return
    decided.current = true
    const record = () => {
      setQuiet(false)
      let seen: string | null
      try { seen = localStorage.getItem(SEEN_KEY) } catch { return }
      try { localStorage.setItem(SEEN_KEY, current) } catch { /* not remembered */ }
      if (seen && isVersion(seen) && isNewer(current, seen)) setUpdated(true)
    }
    // Not recorded as seen here: the reopened window says it updated.
    if (reopen && takeReopenRequest(current)) {
      void reopenApp().then(ok => { if (!ok) record() })
      return
    }
    record()
  }, [flow.none, current, mode, reopen])

  return { updated, clear: () => setUpdated(false), reopen: reopen && !quiet }
}
