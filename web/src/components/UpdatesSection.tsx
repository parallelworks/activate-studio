import { useEffect, useRef, useState } from 'react'
import { Streamdown } from 'streamdown'
import {
  Button, Callout, ConfirmModal, CopyCodeBlock, Field, Loader, ProgressBar, Select, type SelectItem, Switch,
  formatBytes, formatDateTime,
} from '@parallelworks/ui'
import { UPDATES_CHANGED, onUpdatesFocus, updatesApi, type UpdatePolicy, type UpdateState, type UpdatesStatus } from '../api'

const CHANNELS: SelectItem<UpdatePolicy['channel']>[] = [
  { value: 'stable', label: 'Stable' },
  { value: 'prerelease', label: 'Pre-release' },
]

/** How a clone of the repository updates itself, before a restart. */
export const CLONE_UPDATE_COMMAND = 'git pull\npnpm install\npnpm build'

/** The server's work on an install, which the section follows. */
const UNDER_WAY = new Set<UpdateState>(['downloading', 'verifying', 'installing'])

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))
const errText = (e: unknown) => String((e as Error)?.message ?? e)
// A release as both sides report it: no leading v, and no git describe
// suffix (v1.140-1-gabc1234 is the v1.140 release built a commit later).
export const bare = (v: string) => v.trim().replace(/^v/i, '').replace(/-\d+-g[0-9a-f]+$/i, '')

/**
 * Settings, General, Updates: the version running, the latest release and
 * its notes, installing it with progress, the update policy, and a rollback
 * to the version before. Only the macOS app installs; a clone is shown the
 * commands that update it, and a hosted deployment shows nothing.
 *
 * An install ends with the server restarting on the new version. The page
 * then waits until /api/version reports that version and reloads, since its
 * own code is the old build.
 */
export function UpdatesSection({ onPolicySaved, pollMs = 1500, giveUpMs = 180_000, reload = () => location.reload() }: {
  /** Called with the policy once it is saved, so a form holding the other
   *  settings does not save the old one back. */
  onPolicySaved?: (policy: UpdatePolicy) => void
  pollMs?: number
  giveUpMs?: number
  reload?: () => void
}) {
  const [status, setStatus] = useState<UpdatesStatus | null>(null)
  const [hidden, setHidden] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState<'check' | 'install' | 'rollback' | 'policy' | null>(null)
  const [confirmRollback, setConfirmRollback] = useState(false)
  // The version the page waits for after an install or a rollback, and
  // whether it stopped waiting. `from` stands in when no version is known:
  // any other version then counts.
  const [waiting, setWaiting] = useState<{ version: string | null; from: string; timedOut: boolean } | null>(null)
  const target = useRef<string | null>(null)
  const lastState = useRef<UpdateState>('idle')
  const section = useRef<HTMLElement>(null)

  const waitFor = (version: string | null, from: string) =>
    setWaiting(w => w ?? { version, from, timedOut: false })

  const apply = (s: UpdatesStatus) => {
    lastState.current = s.state
    setStatus(s)
    window.dispatchEvent(new CustomEvent(UPDATES_CHANGED, { detail: s }))
    // A restart quick enough to fall between two reads: the server already
    // runs the version being installed.
    if (target.current && s.state === 'idle' && bare(s.current.version) === bare(target.current)) { reload(); return }
    // An install the server started on its own is followed the same way.
    if (!target.current && s.latest && (UNDER_WAY.has(s.state) || s.state === 'restarting')) target.current = s.latest.version
    if (s.state === 'restarting') waitFor(target.current, s.current.version)
  }

  useEffect(() => {
    updatesApi.status()
      .then(s => { if (s && s.mode !== 'hosted') apply(s); else setHidden(true) })
      .catch(e => setError(errText(e)))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The footer's "update available" lands here.
  const shown = !!status
  useEffect(() => {
    if (!shown) return
    return onUpdatesFocus(() => section.current?.scrollIntoView?.({ block: 'start', behavior: 'smooth' }))
  }, [shown])

  // Follow an install while the server works on it. Once the download is
  // in hand, the server not answering means it is restarting; during the
  // download it is taken for a passing failure and read again.
  const following = !!status && UNDER_WAY.has(status.state) && !waiting
  useEffect(() => {
    if (!following) return
    let stop = false
    const from = status!.current.version
    const t = window.setInterval(() => {
      updatesApi.status()
        .then(s => { if (!stop && s) apply(s) })
        .catch(() => { if (!stop && lastState.current !== 'downloading') waitFor(target.current, from) })
    }, pollMs)
    return () => { stop = true; window.clearInterval(t) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [following, pollMs])

  // Wait for the new version to answer, then reload onto it.
  useEffect(() => {
    if (!waiting || waiting.timedOut) return
    let stop = false
    const until = Date.now() + giveUpMs
    const arrived = (v: string) => (waiting.version ? bare(v) === bare(waiting.version) : bare(v) !== bare(waiting.from))
    void (async () => {
      while (!stop && Date.now() < until) {
        await sleep(pollMs)
        if (stop) return
        try {
          const res = await fetch('/api/version')
          const b = res.ok ? await res.json() as { version?: string } : null
          if (!stop && b?.version && arrived(b.version)) { reload(); return }
        } catch { /* still restarting */ }
      }
      if (!stop) setWaiting(w => w && { ...w, timedOut: true })
    })()
    return () => { stop = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [waiting])

  const run = async (kind: 'check' | 'install' | 'rollback', call: () => Promise<UpdatesStatus>): Promise<boolean> => {
    setBusy(kind)
    setError('')
    try {
      apply(await call())
      return true
    } catch (e) {
      if (kind !== 'check') target.current = null
      setError(errText(e))
      return false
    } finally {
      setBusy(null)
    }
  }
  const check = () => run('check', updatesApi.check)
  const install = () => {
    target.current = status?.latest?.version ?? null
    return run('install', updatesApi.install)
  }
  // A rollback restarts at once, whatever state the answer reports.
  const rollback = async () => {
    const from = status?.current.version ?? ''
    const to = status?.previous?.version ?? null
    target.current = to
    if (await run('rollback', updatesApi.rollback)) waitFor(to, from)
  }

  const savePolicy = async (next: UpdatePolicy) => {
    if (!status) return
    const before = status.policy
    setStatus({ ...status, policy: next })
    setBusy('policy')
    setError('')
    try {
      await updatesApi.savePolicy(next)
      onPolicySaved?.(next)
      // Another channel can have another latest version.
      if (next.channel !== before.channel) {
        setBusy(null)
        await check()
      }
    } catch (e) {
      setStatus(s => s && { ...s, policy: before })
      setError(errText(e))
    } finally {
      setBusy(b => (b === 'policy' ? null : b))
    }
  }

  if (hidden) return null
  if (!status) return error ? <Callout tone="error">Could not read the update status: {error}</Callout> : null

  const { current, latest, policy } = status
  const underWay = UNDER_WAY.has(status.state) || status.state === 'restarting' || !!waiting
  const checking = busy === 'check' || status.state === 'checking'
  const expected = waiting?.version ?? target.current ?? latest?.version ?? ''
  const progress = status.progress
  const percentKnown = !!progress?.total

  return (
    <section ref={section} id="settings-updates" className="settings-field" aria-labelledby="settings-updates-head">
      <div className="tool-group" id="settings-updates-head">Updates</div>
      <div className="query-actions" style={{ marginTop: 0, flexWrap: 'wrap' }}>
        <span>
          Version {current.version}
          {current.builtAt && <span className="muted">, built {formatDateTime(current.builtAt, 'date')}</span>}
        </span>
        <Button variant="secondary" size="sm" loading={checking} disabled={!!busy || underWay} onClick={() => void check()}>
          {checking ? 'Checking…' : 'Check now'}
        </Button>
        <span className="muted">
          {status.checkedAt ? `Last checked ${formatDateTime(status.checkedAt, 'short')}` : 'Not checked yet'}
        </span>
      </div>

      {status.reopen && (
        <Callout tone="info">The update also changed the app's window. Quit and reopen ACTIVATE Studio to finish.</Callout>
      )}

      {latest && status.available ? (
        <Callout className="rag-banner" tone="info"
          title={`Version ${latest.version} is available`}
          action={status.canInstall && !underWay
            ? <Button variant="primary" size="sm" loading={busy === 'install'} disabled={!!busy} onClick={() => void install()}>Update now</Button>
            : undefined}>
          <p className="muted key-note">
            {latest.publishedAt ? `Published ${formatDateTime(latest.publishedAt, 'date')}.` : null}
            {latest.url && <> <a href={latest.url} target="_blank" rel="noopener noreferrer">Release page</a></>}
          </p>
          {latest.notes?.trim() && (
            <div className="md-body" style={{ maxHeight: 260, overflowY: 'auto', marginTop: 8, paddingRight: 8 }}>
              <Streamdown>{latest.notes}</Streamdown>
            </div>
          )}
        </Callout>
      ) : status.checkedAt && status.state !== 'error' ? (
        <p className="muted key-note">This is the latest version{policy.channel === 'prerelease' ? ', pre-releases included' : ''}.</p>
      ) : null}

      {status.state === 'downloading' && !waiting && (
        <ProgressBar className="settings-field" label={`Downloading version ${expected}`}
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
          action={<Button variant="secondary" size="sm" onClick={reload}>Reload</Button>}>
          The app may have gone back to the version it had. Reload the page to see which version is running, or quit and reopen the app.
        </Callout>
      )}

      {status.state === 'error' && status.error && <Callout className="rag-banner" tone="error">{status.error}</Callout>}
      {error && <Callout className="rag-banner" tone="error" live>{error}</Callout>}

      {!status.canInstall && status.reason && <p className="muted key-note">{status.reason}</p>}
      {status.mode === 'clone' && (
        <>
          <p className="muted key-note">To update this copy, run these in its folder, then stop the Studio and start it again with <code>pnpm start</code>:</p>
          <CopyCodeBlock className="mcp-snippet" text={CLONE_UPDATE_COMMAND} label="update commands">{CLONE_UPDATE_COMMAND}</CopyCodeBlock>
        </>
      )}

      <Field className="settings-field" controlFirst label="Check for updates automatically"
        description="A minute after the Studio starts, then every six hours.">
        <Switch checked={policy.check} disabled={busy === 'policy'}
          onCheckedChange={v => void savePolicy({ ...policy, check: v })} />
      </Field>
      {status.mode === 'app' && (
        <Field className="settings-field" controlFirst label="Install updates automatically when idle"
          description="Installs a new version once no chat reply or indexing has run for ten minutes.">
          <Switch checked={policy.install === 'auto'} disabled={!policy.check || busy === 'policy'}
            onCheckedChange={v => void savePolicy({ ...policy, install: v ? 'auto' : 'manual' })} />
        </Field>
      )}
      <div className="settings-grid">
        <Field label="Channel" description="Pre-release also offers test versions published before a stable release.">
          <Select items={CHANNELS} value={policy.channel} disabled={busy === 'policy'}
            onValueChange={v => { if (v && v !== policy.channel) void savePolicy({ ...policy, channel: v }) }} />
        </Field>
      </div>

      {status.mode === 'app' && status.previous && (
        <div className="query-actions">
          <Button variant="secondary" disabled={!!busy || underWay} onClick={() => setConfirmRollback(true)}>
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
