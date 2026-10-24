import { useEffect, useState } from 'react'
import { Button, Callout, CopyCodeBlock, Modal } from '@parallelworks/ui'
import { openUpdates } from '../api'
import { bare, setUpdateFlowOptions, underWay, updateActions, useUpdateFlow } from '../updateFlow'
import { ReleaseList } from './ChangeList'
import { UpdateProgress } from './UpdatesSection'

/** The version Later was pressed for, in this browser, and until when. */
const LATER_KEY = 'ade-update-later'
/** How long Later hides the notice for that version. */
export const LATER_MS = 24 * 3600_000

interface Later { version: string; until: number }

/** A Later still in force. A bare version, stored before Later expired,
 *  hides nothing: it kept a version's notice away for good. */
function readLater(raw: string | null = null): Later | null {
  try {
    const v = JSON.parse(raw ?? localStorage.getItem(LATER_KEY) ?? 'null') as Partial<Later> | null
    return v && typeof v.version === 'string' && typeof v.until === 'number' && v.until > Date.now() ? { version: v.version, until: v.until } : null
  } catch { return null }
}

/**
 * A one-line notice at the top of every page while a newer version is
 * available, in the macOS app and a clone (a hosted deployment is updated by
 * its operator and never shows it). It stays one line so the page under it,
 * the chat above all, keeps its room: What's new opens a dialog with the
 * changes in every newer version, newest first, and for a clone the commands
 * that update it. Update now installs (the app, when it can), and Later hides
 * the notice for a day, or until a newer version appears. While an install
 * runs the line shows its progress; the footer's "update available" and
 * Settings, Updates stay either way.
 */
export function UpdateNotice({ pollMs, giveUpMs, reload }: {
  /** For tests: the update flow's timings and reload. */
  pollMs?: number
  giveUpMs?: number
  reload?: () => void
} = {}) {
  setUpdateFlowOptions({ pollMs, giveUpMs, reload })
  const flow = useUpdateFlow()
  const [later, setLater] = useState<Later | null>(() => readLater())
  const [details, setDetails] = useState(false)

  // Later in another tab of the same Studio applies here too.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => { if (e.key === LATER_KEY) setLater(readLater(e.newValue ?? 'null')) }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  const { status } = flow
  if (flow.none || !status || status.mode === 'hosted' || !status.available || !status.latest) return null
  const latest = status.latest
  const installing = underWay(flow)
  if (!installing && later && later.until > Date.now() && bare(later.version) === bare(latest.version)) return null

  const deferIt = () => {
    const v = { version: latest.version, until: Date.now() + LATER_MS }
    try { localStorage.setItem(LATER_KEY, JSON.stringify(v)) } catch { /* hidden for this page only */ }
    setLater(v)
    setDetails(false)
  }
  const releases = status.releases ?? []
  const changes = releases.reduce((n, r) => n + r.entries.length, 0)
  const failed = (status.state === 'error' && status.error) || flow.error
  const canInstall = status.mode === 'app' && status.canInstall
  const install = () => { setDetails(false); void updateActions.install() }

  if (installing) {
    return (
      <Callout variant="bar" tone="info" live className="update-notice" testId="update-notice">
        <div className="update-notice-row">
          <span className="update-notice-text"><b>Updating to version {latest.version}</b></span>
          <div className="update-notice-progress"><UpdateProgress flow={flow} barClassName="update-notice-bar" /></div>
        </div>
      </Callout>
    )
  }

  return (
    <>
      <Callout variant="bar" tone="info" live className="update-notice" testId="update-notice">
        <div className="update-notice-row">
          <span className="update-notice-text">
            <b>Version {latest.version} is available</b>
            <span className="update-notice-sub"> · you have {status.current.version}</span>
          </span>
          <div className="update-notice-actions">
            <Button variant="ghost" size="sm" onClick={() => setDetails(true)}>
              What's new{changes ? ` (${changes})` : ''}
            </Button>
            {canInstall && (
              <Button variant="primary" size="sm" loading={flow.busy === 'install'} disabled={!!flow.busy} onClick={install}>
                Update now
              </Button>
            )}
            <Button variant="secondary" size="sm" onClick={deferIt}>Later</Button>
          </div>
        </div>
        {failed && <p className="update-notice-error" role="alert">{failed}</p>}
      </Callout>
      {details && (
        <Modal open onClose={() => setDetails(false)} size="lg" className="update-details"
          title={`Version ${latest.version}`}
          description={`You have version ${status.current.version}. These are the changes since.`}
          footer={
            <>
              <Button variant="ghost" onClick={() => { setDetails(false); openUpdates() }}>Update settings</Button>
              <Button variant="secondary" onClick={deferIt}>Later</Button>
              {canInstall && <Button variant="primary" disabled={!!flow.busy} onClick={install}>Update now</Button>}
            </>
          }>
          {releases.length > 0
            ? <div className="update-details-list"><ReleaseList releases={releases} headingLevel={3} /></div>
            : <p className="muted">This release has no notes.</p>}
          {status.mode === 'clone' && status.command && (
            <div className="update-notice-command">
              <p>To update, run these in the Studio's folder, then stop it and start it again with <code>pnpm start</code>:</p>
              <CopyCodeBlock className="mcp-snippet" text={status.command} label="update commands">{status.command}</CopyCodeBlock>
            </div>
          )}
          {!status.canInstall && !status.command && status.reason && <p className="update-notice-reason">{status.reason}</p>}
          {latest.url && !status.canInstall && !status.command && (
            <p><a className="update-notice-link" href={latest.url} target="_blank" rel="noopener noreferrer">Release page</a></p>
          )}
        </Modal>
      )}
    </>
  )
}
