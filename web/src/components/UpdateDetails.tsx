import { Button, CopyCodeBlock, Modal } from '@parallelworks/ui'
import { openUpdates, type UpdatesStatus } from '../api'
import type { UpdateFlow } from '../updateFlow'
import { ReleaseList } from './ChangeList'

/** How many changes the newer versions bring. */
export const changeCount = (status: UpdatesStatus): number =>
  (status.releases ?? []).reduce((n, r) => n + r.entries.length, 0)

/**
 * What a newer version brings, opened from the footer's Build panel: the
 * changes in every newer version, newest first, each opening to its
 * description; for a clone or a packaged install, the commands that update
 * it; and Update now, where the Studio installs (the app, when it can).
 */
export function UpdateDetails({ flow, onClose, onInstall }: { flow: UpdateFlow; onClose: () => void; onInstall: () => void }) {
  const { status } = flow
  if (!status?.available || !status.latest) return null
  const latest = status.latest
  const releases = status.releases ?? []
  const canInstall = status.mode === 'app' && status.canInstall
  return (
    <Modal open onClose={onClose} size="lg" className="update-details"
      title={`Version ${latest.version}`}
      description={`You have version ${status.current.version}. These are the changes since.`}
      footer={
        <>
          <Button variant="ghost" onClick={() => { onClose(); openUpdates() }}>Update settings</Button>
          {canInstall && <Button variant="primary" disabled={!!flow.busy} onClick={onInstall}>Update now</Button>}
        </>
      }>
      {releases.length > 0
        ? <div className="update-details-list"><ReleaseList releases={releases} headingLevel={3} /></div>
        : <p className="muted">This release has no notes.</p>}
      {status.command && (
        <div className="update-details-command">
          {status.mode === 'package'
            ? <p>{status.commandNote ?? 'To update, run this in a terminal, then quit and reopen ACTIVATE Studio:'}</p>
            : <p>To update, run these in the Studio's folder, then stop it and start it again with <code>pnpm start</code>:</p>}
          <CopyCodeBlock className="mcp-snippet" text={status.command} label="update commands">{status.command}</CopyCodeBlock>
        </div>
      )}
      {!status.canInstall && !status.command && status.reason && <p className="update-details-reason">{status.reason}</p>}
      {latest.url && !status.canInstall && !status.command && (
        <p><a className="update-details-link" href={latest.url} target="_blank" rel="noopener noreferrer">Release page</a></p>
      )}
    </Modal>
  )
}
