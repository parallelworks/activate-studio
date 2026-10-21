import { useEffect, useState } from 'react'
import { Button, Callout, CopyCodeBlock } from '@parallelworks/ui'
import { openUpdates } from '../api'
import { bare, setUpdateFlowOptions, underWay, updateActions, useUpdateFlow } from '../updateFlow'
import { ReleaseList } from './ChangeList'
import { UpdateProgress } from './UpdatesSection'

/** The version Later was pressed for, in this browser. */
const LATER_KEY = 'ade-update-later'

function readLater(): string | null {
  try { return localStorage.getItem(LATER_KEY) } catch { return null }
}

/**
 * The notice at the top of every page while a newer version is available,
 * in the macOS app and a clone (a hosted deployment is updated by its
 * operator and never shows it). It lists what every newer version brings,
 * newest first, each change opening to its description, and offers Update
 * now (the app, when it can install), the commands for a clone, and Later,
 * which hides it until a version newer than that one appears. While an
 * install runs it shows the progress instead; the footer's "update
 * available" and Settings, Updates stay either way.
 */
export function UpdateNotice({ pollMs, giveUpMs, reload }: {
  /** For tests: the update flow's timings and reload. */
  pollMs?: number
  giveUpMs?: number
  reload?: () => void
} = {}) {
  setUpdateFlowOptions({ pollMs, giveUpMs, reload })
  const flow = useUpdateFlow()
  const [later, setLater] = useState<string | null>(readLater)

  // Later in another tab of the same Studio applies here too.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => { if (e.key === LATER_KEY) setLater(e.newValue) }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  const { status } = flow
  if (flow.none || !status || status.mode === 'hosted' || !status.available || !status.latest) return null
  const latest = status.latest
  const installing = underWay(flow)
  if (!installing && later && bare(later) === bare(latest.version)) return null

  const deferIt = () => {
    try { localStorage.setItem(LATER_KEY, latest.version) } catch { /* hidden for this page only */ }
    setLater(latest.version)
  }
  const releases = status.releases ?? []
  const failed = (status.state === 'error' && status.error) || flow.error

  return (
    <Callout variant="bar" tone="info" live className="update-notice" testId="update-notice"
      title={installing ? `Updating to version ${latest.version}` : `Version ${latest.version} is available`}>
      {!installing && <p className="update-notice-sub">You have version {status.current.version}.</p>}
      {!installing && releases.length > 0 && (
        <div className="update-notice-list">
          <ReleaseList releases={releases} headingLevel={3} />
        </div>
      )}
      {installing ? (
        <div className="update-notice-progress"><UpdateProgress flow={flow} barClassName="update-notice-bar" /></div>
      ) : (
        <>
          {failed && <p className="update-notice-error" role="alert">{failed}</p>}
          {status.mode === 'clone' && status.command && (
            <div className="update-notice-command">
              <p>To update, run these in the Studio's folder, then stop it and start it again with <code>pnpm start</code>:</p>
              <CopyCodeBlock className="mcp-snippet" text={status.command} label="update commands">{status.command}</CopyCodeBlock>
            </div>
          )}
          {!status.canInstall && !status.command && status.reason && <p className="update-notice-reason">{status.reason}</p>}
          <div className="update-notice-actions">
            {status.mode === 'app' && status.canInstall && (
              <Button variant="primary" size="sm" loading={flow.busy === 'install'} disabled={!!flow.busy}
                onClick={() => void updateActions.install()}>
                Update now
              </Button>
            )}
            <Button variant="secondary" size="sm" onClick={deferIt}>Later</Button>
            <Button variant="ghost" size="sm" onClick={openUpdates}>Settings</Button>
            {latest.url && !status.canInstall && !status.command && (
              <a className="update-notice-link" href={latest.url} target="_blank" rel="noopener noreferrer">Release page</a>
            )}
          </div>
        </>
      )}
    </Callout>
  )
}
