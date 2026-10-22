import { useEffect, useRef, useState } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { changelogApi, openWhatsNew, type ChangeVersion } from '../api'
import { isNewer, isVersion, useUpdateFlow } from '../updateFlow'

/** The newest version this browser has opened the Studio on. */
export const SEEN_KEY = 'ade-seen-version'

/**
 * Once, the first time the Studio opens on a newer version than this browser
 * last saw (in the macOS app and a clone): one line, "Updated to vX", with
 * how many changes came since, from the change log the build carries, so it
 * works offline, and What's new, which lists them. A first visit only records
 * the version. A hosted deployment does not show it; What's new is in Help
 * and the footer there.
 */
export function UpdatedNotice() {
  const flow = useUpdateFlow()
  const [shown, setShown] = useState<{ version: string; versions: ChangeVersion[] } | null>(null)
  const decided = useRef(false)
  const current = flow.status?.current.version ?? null
  const mode = flow.status?.mode ?? null

  useEffect(() => {
    if (decided.current || flow.none || !current || !mode || mode === 'hosted' || !isVersion(current)) return
    decided.current = true
    let seen: string | null
    try { seen = localStorage.getItem(SEEN_KEY) } catch { return }
    try { localStorage.setItem(SEEN_KEY, current) } catch { /* not remembered */ }
    if (!seen || !isVersion(seen) || !isNewer(current, seen)) return
    const since = seen
    changelogApi.get(since)
      .then(log => setShown({ version: current, versions: log.versions.filter(v => !isNewer(v.version, current)) }))
      .catch(() => setShown({ version: current, versions: [] }))
  }, [flow.none, current, mode])

  if (!shown) return null
  const changes = shown.versions.reduce((n, v) => n + v.entries.length, 0)
  return (
    <Callout variant="bar" tone="success" live className="update-notice" testId="updated-notice" onDismiss={() => setShown(null)}>
      <div className="update-notice-row">
        <span className="update-notice-text">
          <b>Updated to {shown.version}</b>
          {changes > 0 && <span className="update-notice-sub"> · {changes} change{changes === 1 ? '' : 's'} since you last opened it</span>}
        </span>
        <div className="update-notice-actions">
          <Button variant="secondary" size="sm" onClick={() => { setShown(null); openWhatsNew() }}>What's new</Button>
        </div>
      </div>
    </Callout>
  )
}
