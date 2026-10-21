import { useEffect, useRef, useState } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { changelogApi, openWhatsNew, type ChangeVersion } from '../api'
import { isNewer, isVersion, useUpdateFlow } from '../updateFlow'

/** The newest version this browser has opened the Studio on. */
export const SEEN_KEY = 'ade-seen-version'

/**
 * Once, the first time the Studio opens on a newer version than this browser
 * last saw (in the macOS app and a clone): "Updated to vX" with the titles
 * of the changes since, from the change log the build carries, so it works
 * offline. A first visit only records the version. A hosted deployment does
 * not show it; What's new is in Help and the footer there.
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
  const { versions } = shown
  const many = versions.length > 1
  return (
    <Callout variant="bar" tone="success" live className="update-notice" testId="updated-notice"
      title={`Updated to ${shown.version}`} onDismiss={() => setShown(null)}>
      {versions.length > 0 && (
        <div className="update-notice-list updated-list">
          {versions.map(v => (
            <div key={v.version} className="updated-version">
              {many && <div className="changes-version-name">{v.version}</div>}
              <ul>{v.entries.map((e, i) => <li key={`${e.pr ?? 'n'}-${i}`}>{e.title}</li>)}</ul>
            </div>
          ))}
        </div>
      )}
      <div className="update-notice-actions">
        <Button variant="secondary" size="sm" onClick={() => { setShown(null); openWhatsNew() }}>What's new</Button>
      </div>
    </Callout>
  )
}
