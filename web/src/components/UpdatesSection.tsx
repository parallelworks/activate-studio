import { useEffect, useRef, useState } from 'react'
import { Streamdown } from 'streamdown'
import {
  Button, Callout, ConfirmModal, CopyCodeBlock, Field, Loader, ProgressBar, Select, type SelectItem, Switch,
  formatBytes, formatDateTime,
} from '@parallelworks/ui'
import { onUpdatesFocus, openWhatsNew, updatesApi, type UpdatePolicy } from '../api'
import { reloadPage, setUpdateFlowOptions, underWay as isUnderWay, updateActions, useUpdateFlow, type UpdateFlow } from '../updateFlow'
import { ReleaseList } from './ChangeList'
import { ReopenApp } from './ReopenApp'

export { bare } from '../updateFlow'

const CHANNELS: SelectItem<UpdatePolicy['channel']>[] = [
  { value: 'stable', label: 'Stable' },
  { value: 'prerelease', label: 'Pre-release' },
]

/** How a clone of the repository updates itself, before a restart; the
 *  server's `command` names it. */
export const CLONE_UPDATE_COMMAND = 'git pull\npnpm install\npnpm build'

const errText = (e: unknown) => String((e as Error)?.message ?? e)

/**
 * Settings, General, Updates: the version running, the latest release and
 * what it and the releases before it bring, installing it with progress,
 * the update policy, and a rollback to the version before. Only the macOS
 * app installs; a clone is shown the commands that update it, and a hosted
 * deployment shows nothing. The install itself, and the reload onto the
 * new version, are the page's update flow (updateFlow.ts), which the
 * update notice shares.
 */
export function UpdatesSection({ onPolicySaved, pollMs, giveUpMs, reload }: {
  /** Called with the policy once it is saved, so a form holding the other
   *  settings does not save the old one back. */
  onPolicySaved?: (policy: UpdatePolicy) => void
  /** For tests: the update flow's timings and reload. */
  pollMs?: number
  giveUpMs?: number
  reload?: () => void
}) {
  setUpdateFlowOptions({ pollMs, giveUpMs, reload })
  const flow = useUpdateFlow()
  const { status, waiting, busy } = flow
  const [policyBusy, setPolicyBusy] = useState(false)
  const [policyError, setPolicyError] = useState('')
  const [confirmRollback, setConfirmRollback] = useState(false)
  const section = useRef<HTMLElement>(null)

  // The footer's "update available" lands here.
  const shown = !!status && !flow.none
  useEffect(() => {
    if (!shown) return
    return onUpdatesFocus(() => section.current?.scrollIntoView?.({ block: 'start', behavior: 'smooth' }))
  }, [shown])

  const check = () => updateActions.check()
  const install = () => updateActions.install()
  const rollback = () => updateActions.rollback()

  const savePolicy = async (next: UpdatePolicy) => {
    if (!status) return
    const before = status.policy
    updateActions.showPolicy(next)
    setPolicyBusy(true)
    setPolicyError('')
    try {
      await updatesApi.savePolicy(next)
      onPolicySaved?.(next)
      // Another channel can have another latest version.
      if (next.channel !== before.channel) {
        setPolicyBusy(false)
        await check()
      }
    } catch (e) {
      updateActions.showPolicy(before)
      setPolicyError(errText(e))
    } finally {
      setPolicyBusy(false)
    }
  }

  if (flow.none) return null
  if (!status) return flow.readError ? <Callout tone="error">Could not read the update status: {flow.readError}</Callout> : null

  const { current, latest, policy } = status
  const underWay = isUnderWay(flow)
  const checking = busy === 'check' || status.state === 'checking'
  const error = flow.error || policyError
  const releases = status.releases ?? []

  return (
    <section ref={section} id="settings-updates" className="settings-field" aria-labelledby="settings-updates-head">
      <div className="tool-group" id="settings-updates-head">Updates</div>
      <div className="query-actions" style={{ marginTop: 0, flexWrap: 'wrap' }}>
        <span>
          Version {current.version}
          {current.builtAt && <span className="muted">, built {formatDateTime(current.builtAt, 'date')}</span>}
        </span>
        <Button variant="secondary" size="sm" loading={checking} disabled={!!busy || policyBusy || underWay} onClick={() => void check()}>
          {checking ? 'Checking…' : 'Check now'}
        </Button>
        <span className="muted">
          {status.offline
            ? 'Offline; the Studio checks again later'
            : status.checkedAt ? `Last checked ${formatDateTime(status.checkedAt, 'short')}` : 'Not checked yet'}
        </span>
        <Button variant="ghost" size="sm" onClick={openWhatsNew}>What's new</Button>
      </div>

      {status.reopen && (
        <Callout tone="info" action={<ReopenApp />}>The update also changed the app's window, which updates when the app reopens.</Callout>
      )}

      {latest && status.available ? (
        <Callout className="rag-banner" tone="info"
          title={`Version ${latest.version} is available`}
          action={status.canInstall && !underWay
            ? <Button variant="primary" size="sm" loading={busy === 'install'} disabled={!!busy || policyBusy} onClick={() => void install()}>Update now</Button>
            : undefined}>
          <p className="muted key-note">
            {latest.publishedAt ? `Published ${formatDateTime(latest.publishedAt, 'date')}.` : null}
            {latest.url && <> <a href={latest.url} target="_blank" rel="noopener noreferrer">Release page</a></>}
          </p>
          {releases.length ? (
            <div className="changes-scroll">
              <ReleaseList releases={releases} />
            </div>
          ) : latest.notes?.trim() ? (
            <div className="md-body" style={{ maxHeight: 260, overflowY: 'auto', marginTop: 8, paddingRight: 8 }}>
              <Streamdown>{latest.notes}</Streamdown>
            </div>
          ) : null}
        </Callout>
      ) : status.checkedAt && status.state !== 'error' && !status.offline ? (
        <p className="muted key-note">This is the latest version{policy.channel === 'prerelease' ? ', pre-releases included' : ''}.</p>
      ) : null}

      <UpdateProgress flow={flow} />

      {status.state === 'error' && status.error && <Callout className="rag-banner" tone="error">{status.error}</Callout>}
      {error && <Callout className="rag-banner" tone="error" live>{error}</Callout>}

      {!status.canInstall && status.reason && <p className="muted key-note">{status.reason}</p>}
      {status.mode === 'clone' && status.command && (
        <>
          <p className="muted key-note">To update this copy, run these in its folder, then stop the Studio and start it again with <code>pnpm start</code>:</p>
          <CopyCodeBlock className="mcp-snippet" text={status.command} label="update commands">{status.command}</CopyCodeBlock>
        </>
      )}

      <Field className="settings-field" controlFirst label="Check for updates automatically"
        description="A minute after the Studio starts, then every six hours.">
        <Switch checked={policy.check} disabled={policyBusy}
          onCheckedChange={v => void savePolicy({ ...policy, check: v })} />
      </Field>
      {status.mode === 'app' && (
        <Field className="settings-field" controlFirst label="Install updates automatically when idle"
          description="Installs a new version once no chat reply or indexing has run for ten minutes.">
          <Switch checked={policy.install === 'auto'} disabled={!policy.check || policyBusy}
            onCheckedChange={v => void savePolicy({ ...policy, install: v ? 'auto' : 'manual' })} />
        </Field>
      )}
      <div className="settings-grid">
        <Field label="Channel" description="Pre-release also offers test versions published before a stable release.">
          <Select items={CHANNELS} value={policy.channel} disabled={policyBusy}
            onValueChange={v => { if (v && v !== policy.channel) void savePolicy({ ...policy, channel: v }) }} />
        </Field>
      </div>

      {status.mode === 'app' && status.previous && (
        <div className="query-actions">
          <Button variant="secondary" disabled={!!busy || policyBusy || underWay} onClick={() => setConfirmRollback(true)}>
            Roll back to {status.previous.version}
          </Button>
        </div>
      )}
      {status.previous && (
        <ConfirmModal open={confirmRollback} onClose={() => setConfirmRollback(false)}
          title={`Roll back to ${status.previous.version}?`}
          description={`The Studio goes back to version ${status.previous.version} and restarts. This page reloads when it is running.`}
          confirmLabel="Roll back"
          onConfirm={rollback} />
      )}
    </section>
  )
}

/** An install or rollback under way: the download's progress, the checks,
 *  the restart, and a restart that did not come back in time. */
export function UpdateProgress({ flow, barClassName = 'settings-field' }: { flow: UpdateFlow; barClassName?: string }) {
  const { status, waiting } = flow
  if (!status) return null
  const expected = waiting?.version ?? status.latest?.version ?? ''
  const progress = status.progress
  const percentKnown = !!progress?.total
  return (
    <>
      {status.state === 'downloading' && !waiting && (
        <ProgressBar className={barClassName} label={`Downloading version ${expected}`}
          value={percentKnown ? progress!.bytes : null} max={progress?.total ?? undefined}
          detail={progress ? (percentKnown ? `${formatBytes(progress.bytes)} of ${formatBytes(progress.total!)}` : formatBytes(progress.bytes)) : undefined} />
      )}
      {status.state === 'verifying' && !waiting && <p className="key-note"><Loader inline size={14} label="Checking the download's signature and checksum" /></p>}
      {status.state === 'installing' && !waiting && <p className="key-note"><Loader inline size={14} label={`Installing version ${expected}`} /></p>}
      {waiting && !waiting.timedOut && (
        <div className="key-note">
          <Loader inline size={14} label="Restarting…" />
          <p className="muted key-note">This page reloads when {waiting.version ? `version ${waiting.version}` : 'the new version'} is running.</p>
        </div>
      )}
      {waiting?.timedOut && (
        <Callout className="rag-banner" tone="error" live
          title={`${waiting.version ? `Version ${waiting.version}` : 'The new version'} did not start within three minutes`}
          action={<Button variant="secondary" size="sm" onClick={reloadPage}>Reload</Button>}>
          The app may have gone back to the version it had. Reload the page to see which version is running, or quit and reopen the app.
        </Callout>
      )}
    </>
  )
}
