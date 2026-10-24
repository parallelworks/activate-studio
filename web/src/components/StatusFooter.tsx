import { useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react'
import {
  Button, CopyCodeBlock, DescriptionList, DescriptionListItem, Popover, PopoverClose, Skeleton, StatusDot, type BadgeVariant,
} from '@parallelworks/ui'
import { api, IndexStatus } from '../api'
import { onLibraryChange } from '../api'
import { elapsed, fmtDuration, indexSummary, kickIndexStatus, useIndexStatus } from '../indexActivity'
import { IndexJobView, IndexLogButton, IndexLogHost, jobOutcome } from './IndexActivity'
import type { LibraryStats } from '../api'
import { openAbout, openUpdates, openWhatsNew } from '../api'
import { underWay, updateActions, useUpdateFlow, type UpdateFlow } from '../updateFlow'
import { UpdateProgress } from './UpdatesSection'
import { share, syncIndex, type AiHealth } from '../statusShare'
import { useAppConfig } from '../config'

function ago(iso: string | null): string {
  // Null just means no sweep has completed since the server started.
  if (!iso) return 'pending'
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  return `${Math.round(s / 3600)}h ago`
}

type Dot = 'ok' | 'warn' | 'off'
const DOT_TONE: Record<Dot, BadgeVariant> = { ok: 'success', warn: 'warning', off: 'muted' }

/** A footer line's details, opening upward from it. Focus moves to the panel
 *  itself, which never scrolls the page the way focusing a control in it can. */
function FooterPanel({ trigger, title, children, open, onOpenChange }: {
  trigger: ReactElement; title: ReactNode; children: ReactNode; open?: boolean; onOpenChange?: (open: boolean) => void
}) {
  return (
    <Popover trigger={trigger} title={title} side="top" align="start" initialFocus="popup" className="identity-panel"
      {...(open === undefined ? {} : { open, onOpenChange })}>
      {children}
    </Popover>
  )
}

function CloseButton() {
  return <PopoverClose render={<Button variant="secondary" size="sm" />}>Close</PopoverClose>
}

function IndexPanel({ trigger, idx, gufi, stats, onSync, syncing }: {
  trigger: ReactElement
  idx: IndexStatus | null
  gufi: boolean | null
  stats: { files?: number; dirs?: number; totalBytes?: number; libraries?: LibraryStats[] } | null
  onSync: () => void
  syncing: boolean
}) {
  const gb = stats?.totalBytes ? `${(stats.totalBytes / 1e9).toFixed(2)} GB` : ''
  const job = idx?.job ?? null
  const summary = indexSummary(idx, stats?.files)
  const [stopping, setStopping] = useState(false)
  const stop = async () => {
    setStopping(true)
    try { await api.stopIndexing(); kickIndexStatus() } catch { /* the status says */ } finally { setStopping(false) }
  }
  const last = idx?.lastBuild?.kind === 'build' ? idx.lastBuild : null
  // A row marked in its last field is a library's, indented under the total.
  const rows: [string, string, Dot | null, boolean?][] = [
    ['GUFI engine', gufi === null ? 'checking…' : gufi ? 'available' : 'unavailable (search falls back to grep)', gufi === null ? 'off' : gufi ? 'ok' : 'warn'],
    ['Indexed', stats?.files ? `${stats.files.toLocaleString()} files in ${stats.dirs?.toLocaleString()} directories, ${gb}${stats.libraries ? `, across ${stats.libraries.length} libraries` : ''}` : idx?.indexed === false ? 'not indexed yet' : 'loading…', null],
    ...(stats?.libraries ?? []).map(l => [`${l.label}${l.site ? ` (on ${l.site})` : ''}`, l.available ? `${(l.files ?? 0).toLocaleString()} files in ${(l.dirs ?? 0).toLocaleString()} directories` : (l.error ?? 'not available'), l.available ? 'ok' : 'warn', true] as [string, string, Dot | null, boolean]),
    ...(last ? [['Last build', `${jobOutcome(last)}${last.finishedAt ? ` ${ago(last.finishedAt)}` : ''}`, last.state === 'done' ? 'ok' : 'warn'] as [string, string, Dot | null]] : []),
    ['Sync loop', idx ? (idx.sweepIntervalSec > 0 ? `every ${idx.sweepIntervalSec}s, last ${ago(idx.lastSweepAt ?? null)}${idx.lastSweepMs != null ? ` (${fmtDuration(idx.lastSweepMs)})` : ''}` : 'paused') : 'loading…', null],
    ['Last result', summary.tone === 'warning' && summary.detail ? summary.detail : idx?.lastSweepAt || last ? 'clean' : 'no sync yet this server start', summary.tone === 'warning' ? 'warn' : idx?.lastSweepAt || last ? 'ok' : 'off'],
  ]
  const busy = syncing || !!idx?.running
  return (
    <FooterPanel trigger={trigger} title="Index health">
      {job && <IndexJobView job={job} onStop={() => void stop()} stopping={stopping} />}
      <DescriptionList variant="divided" className="identity-list">
        {rows.map(([k, v, dot, sub]) => (
          <DescriptionListItem key={k} label={k} className={sub ? 'identity-sub' : undefined}>
            {dot && <StatusDot variant={DOT_TONE[dot]} size={7} className="identity-dot" />}{v}
          </DescriptionListItem>
        ))}
      </DescriptionList>
      {(idx?.lastChanges?.length ?? 0) > 0 && (
        <>
          <div className="scope-menu-head">Last sync re-indexed</div>
          <pre className="identity-claims">{idx!.lastChanges.slice(0, 10).join('\n')}</pre>
        </>
      )}
      <div className="scope-menu-foot">
        <span className="muted identity-src">GET /api/index/status</span>
        <span className="identity-actions">
          {!job && <IndexLogButton variant="secondary" />}
          <Button variant="secondary" size="sm" onClick={onSync} loading={syncing} disabled={busy}>{busy ? (job ? 'indexing…' : 'syncing…') : 'Sync now'}</Button>
          <CloseButton />
        </span>
      </div>
    </FooterPanel>
  )
}

/** Plain rows of a panel's details. */
function DetailRows({ rows }: { rows: [string, string][] }) {
  return (
    <DescriptionList variant="divided" className="identity-list">
      {rows.map(([k, v]) => <DescriptionListItem key={k} label={k}>{v}</DescriptionListItem>)}
    </DescriptionList>
  )
}

export const AI_LABEL: Record<AiHealth['status'], string> = {
  ok: 'models ready',
  auth: 'model key rejected',
  unreachable: 'models unreachable',
  offline: 'offline',
  unconfigured: 'models not configured',
  'key-required': 'add your model key',
  off: 'AI features off',
}

function AiPanel({ trigger, ai, onRecheck }: { trigger: ReactElement; ai: AiHealth; onRecheck: () => void }) {
  const rows: [string, string][] = [
    ['Status', AI_LABEL[ai.status] + (ai.status === 'ok' ? ` (${ai.models} models)` : '')],
    ['Gateway', ai.gatewayHost],
    ['Checked', new Date(ai.checkedAt).toLocaleTimeString()],
  ]
  if (ai.message) rows.push(['Detail', ai.message])
  if (ai.lastCallFailure) rows.push(['Last call failure', `${new Date(ai.lastCallFailure.at).toLocaleTimeString()}: ${ai.lastCallFailure.message.slice(0, 160)}`])
  return (
    <FooterPanel trigger={trigger} title="Model availability">
      <DetailRows rows={rows} />
      {ai.unlockUrl ? (
        <p className="identity-note">
          The provider locked this key, which it does on a schedule.{' '}
          <a href={ai.unlockUrl} target="_blank" rel="noopener noreferrer">Unlock your key</a>, then re-check.
        </p>
      ) : ai.status === 'auth' ? (
        <p className="identity-note">The gateway rejected the credential. Some providers lock keys on a schedule; unlock the key at your provider, then re-check.</p>
      ) : null}
      <p className="identity-note">Checked via the model listing; a key can also fail at call time, which shows under last call failure.</p>
      <div className="scope-menu-foot">
        <span className="muted identity-src">GET /api/ai/health</span>
        <span className="identity-actions">
          <Button variant="secondary" size="sm" onClick={onRecheck}>Re-check</Button>
          <CloseButton />
        </span>
      </div>
    </FooterPanel>
  )
}

interface Build {
  version: string
  commit: string | null
  builtAt: string | null
  startedAt: string
  source: 'bundle' | 'git' | 'package'
}

const BUILD_SOURCE: Record<Build['source'], string> = {
  bundle: 'stamped when the deployment bundle was built',
  git: 'read from the checkout this server runs from',
  package: 'no tag or stamp found; package version only',
}

/** The Build panel's updates: up to date or a newer version, with Check now,
 *  and Update now (the app) or the commands (a clone). Not for a hosted
 *  deployment, which its operator updates. */
function BuildUpdates({ flow }: { flow: UpdateFlow }) {
  const { status } = flow
  if (flow.none || !status) return null
  const checking = flow.busy === 'check' || status.state === 'checking'
  const installing = underWay(flow)
  const latest = status.available ? status.latest : null
  const error = flow.error || (status.state === 'error' ? status.error : null)
  const check = (
    <Button variant="secondary" size="sm" loading={checking} disabled={!!flow.busy || installing} onClick={() => void updateActions.check()}>
      {checking ? 'Checking…' : 'Check now'}
    </Button>
  )
  return (
    <div className="build-updates" data-testid="build-updates">
      {installing ? (
        <UpdateProgress flow={flow} barClassName="build-updates-bar" />
      ) : latest ? (
        <>
          <p className="build-updates-head"><b>Version {latest.version} is available</b></p>
          <div className="build-updates-row">
            {status.mode === 'app' && status.canInstall && (
              <Button variant="primary" size="sm" disabled={!!flow.busy} loading={flow.busy === 'install'} onClick={() => void updateActions.install()}>Update now</Button>
            )}
            <PopoverClose render={<Button variant="secondary" size="sm" />} onClick={openUpdates}>Changes</PopoverClose>
            {check}
          </div>
          {status.mode === 'clone' && status.command && (
            <CopyCodeBlock className="mcp-snippet" text={status.command} label="update commands">{status.command}</CopyCodeBlock>
          )}
          {!status.canInstall && !status.command && status.reason && <p className="identity-note">{status.reason}</p>}
        </>
      ) : (
        <>
          <p className="build-updates-head">
            {checking ? 'Checking for updates…' : status.offline ? 'Offline; checks again later' : error ? 'The last check failed' : 'Up to date'}
          </p>
          <div className="build-updates-row">
            <span className="muted build-updates-when">{status.checkedAt ? `Checked ${ago(status.checkedAt)}` : 'Not checked yet'}</span>
            {check}
          </div>
        </>
      )}
      {error && !installing && <p className="identity-note build-updates-error" role="alert">{error}</p>}
    </div>
  )
}

function BuildPanel({ trigger, build, flow, open, onOpenChange }: {
  trigger: ReactElement; build: Build; flow: UpdateFlow; open?: boolean; onOpenChange?: (open: boolean) => void
}) {
  // A hosted deployment has no updates (flow.none); the app and a clone do,
  // and their build comes with the app or the checkout, not a deployment.
  const hosted = flow.none
  const app = flow.status?.mode === 'app'
  const rows: [string, string][] = [
    ['Version', build.version],
    ['Commit', build.commit ?? 'unknown'],
    ['Built', build.builtAt ? new Date(build.builtAt).toLocaleString() : 'unknown'],
    ['Server started', new Date(build.startedAt).toLocaleString()],
    ['Source', app && build.source === 'bundle' ? 'stamped when the app was built' : BUILD_SOURCE[build.source]],
  ]
  return (
    <FooterPanel trigger={trigger} title="Build" open={open} onOpenChange={onOpenChange}>
      <DetailRows rows={rows} />
      <BuildUpdates flow={flow} />
      {hosted && (
        <p className="identity-note">
          Compare this against the release you deployed. A redeploy that did not take leaves
          the old commit and build time here, which is otherwise indistinguishable from one
          that worked.
        </p>
      )}
      <div className="scope-menu-foot">
        {hosted ? <span className="muted identity-src">GET /api/version</span> : <span />}
        <span className="identity-actions">
          <PopoverClose render={<Button variant="secondary" size="sm" />} onClick={openWhatsNew}>What's new</PopoverClose>
          <PopoverClose render={<Button variant="secondary" size="sm" />} onClick={openAbout}>About</PopoverClose>
          <CloseButton />
        </span>
      </div>
    </FooterPanel>
  )
}

interface WhoAmI {
  authEnabled: boolean
  header: string
  verified: boolean
  user: { id: string; username: string; name?: string } | null
  token: { issuer: string | null; audience: unknown; issuedAt: string | null; expiresAt: string | null } | null
  claims: Record<string, unknown> | null
}

function IdentityPanel({ trigger, who }: { trigger: ReactElement; who: WhoAmI }) {
  const rows: [string, string][] = who.verified
    ? [
        ['Username', who.user?.username ?? ''],
        ['Name', who.user?.name ?? '(not set)'],
        ['Subject', String(who.claims?.sub ?? '')],
        ['Issuer', who.token?.issuer ?? ''],
        ['Audience', Array.isArray(who.token?.audience) ? (who.token?.audience as string[]).join('\n') : String(who.token?.audience ?? '')],
        ['Issued', who.token?.issuedAt ? new Date(who.token.issuedAt).toLocaleTimeString() : ''],
        ['Expires', who.token?.expiresAt ? `${new Date(who.token.expiresAt).toLocaleTimeString()} (${Math.max(0, Math.round((Date.parse(who.token.expiresAt) - Date.now()) / 1000))}s)` : ''],
      ]
    : []
  return (
    <FooterPanel trigger={trigger} title={who.verified ? 'Platform-verified identity' : 'Identity not verified'}>
      {who.verified ? (
        <>
          <DetailRows rows={rows} />
          <div className="scope-menu-head">Raw token claims</div>
          <pre className="identity-claims">{JSON.stringify(who.claims, null, 2)}</pre>
        </>
      ) : (
        <p className="identity-note">
          This connection carried no valid platform token. Verification is enabled and the
          server expects a short-lived JWT in the <code>{who.header}</code> header, which the
          platform session proxy adds when the app is reached through it. Direct access
          (localhost, the bare subdomain outside the proxy) has no such header. If this shows
          while using the platform, check the server log for a token verification warning.
        </p>
      )}
      <div className="scope-menu-foot">
        <span className="muted identity-src">GET /api/whoami</span>
        <CloseButton />
      </div>
    </FooterPanel>
  )
}

export function StatusFooter({ collapsed = false }: { collapsed?: boolean }) {
  // With AI features off there is no model line.
  const cfg = useAppConfig()
  const [stats, setStats] = useState<{ files?: number; dirs?: number; totalBytes?: number; libraries?: LibraryStats[] } | null>(null)
  // The same index status the rest of the app shows (indexActivity.ts).
  const idx = useIndexStatus()
  const [syncing, setSyncing] = useState(false)
  const [who, setWho] = useState<WhoAmI | null>(null)
  const [gufi, setGufi] = useState<boolean | null>(null)
  const [ai, setAi] = useState<AiHealth | null>(null)
  const [build, setBuild] = useState<Build | null>(null)

  // Each line waited on its own request, so the footer assembled itself a
  // row at a time while the page settled. The first pass is awaited as a
  // whole and the footer appears once, complete; later refreshes update in
  // place, where a line changing is meaningful rather than noise.
  const [ready, setReady] = useState(false)
  // What it reads is shared with the status panel, which shows the same
  // health and counts without asking the server again.
  const refresh = () => Promise.allSettled([
    api.stats().then(s => { setStats(s as typeof stats); share({ stats: s }) }),
    fetch('/api/whoami').then(r => r.json()).then((w: WhoAmI) => { setWho(w); share({ authEnabled: !!w.authEnabled }) }),
    api.health().then(h => setGufi(h.gufi)).catch(() => setGufi(null)),
    fetch('/api/ai/health').then(r => r.json()).then((h: AiHealth) => { setAi(h); share({ ai: h }) }).catch(() => { setAi(null); share({ ai: null }) }),
  ])
  useEffect(() => {
    void refresh().then(() => setReady(true))
    const t = setInterval(() => { void refresh() }, 60_000)
    // The count follows the library selected and any change to the index.
    const now = () => { void refresh() }
    const off = onLibraryChange(now)
    window.addEventListener('ade-kb-changed', now)
    // A change in Settings, Model access changes the model line.
    window.addEventListener('ade:model-access-changed', now)
    return () => { clearInterval(t); off(); window.removeEventListener('ade-kb-changed', now); window.removeEventListener('ade:model-access-changed', now) }
  }, [])
  // Polled: a change means a new version was rolled out under this page.
  // The page keeps working against the new server, but its own code is
  // the old build, so it says so and offers a reload; open conversations
  // are saved server-side and survive it.
  const [updated, setUpdated] = useState<Build | null>(null)
  useEffect(() => {
    let first: string | null = null
    const check = () => fetch('/api/version').then(r => r.json()).then((b: Build) => {
      if (first === null) { first = b.commit; setBuild(b); return }
      if (b.commit !== first) setUpdated(b)
    }).catch(() => { if (first === null) setBuild(null) })
    check()
    const t = setInterval(check, 60_000)
    const onVis = () => { if (document.visibilityState === 'visible') check() }
    document.addEventListener('visibilitychange', onVis)
    return () => { clearInterval(t); document.removeEventListener('visibilitychange', onVis) }
  }, [])

  // A newer release the Studio can tell about. The version line says so,
  // and the Build panel it opens has Update now; with none known, the line
  // offers a check. The page's update flow reads the server's answer every
  // two minutes and when the window is shown (the server checks every 15
  // minutes); a hosted deployment has no updates and shows neither.
  const flow = useUpdateFlow()
  const updates = !flow.none && !!flow.status && flow.status.mode !== 'hosted'
  const update = updates && flow.status?.available && flow.status.latest ? flow.status.latest.version : null
  const [buildOpen, setBuildOpen] = useState(false)
  // What a check from the line found, shown for a few seconds.
  const [checked, setChecked] = useState<'up to date' | 'check failed' | null>(null)
  const checkedTimer = useRef<number | null>(null)
  useEffect(() => () => { if (checkedTimer.current !== null) window.clearTimeout(checkedTimer.current) }, [])
  const checking = flow.busy === 'check' || flow.status?.state === 'checking'
  const checkNow = async () => {
    setChecked(null)
    const ok = await updateActions.check()
    if (checkedTimer.current !== null) window.clearTimeout(checkedTimer.current)
    setChecked(ok ? 'up to date' : 'check failed')
    checkedTimer.current = window.setTimeout(() => setChecked(null), ok ? 4000 : 8000)
  }

  const sync = async () => {
    setSyncing(true)
    kickIndexStatus()
    await syncIndex()
    refresh()
    kickIndexStatus()
    setSyncing(false)
  }

  const busy = syncing || !!idx?.running
  const summary = indexSummary(idx, stats?.files)
  if (collapsed) {
    return (
      <div className="sidenav-footer">
        {update && (
          <button className="status-line footer-rail-update" title={`Version ${update} is available. Opens Settings, Updates.`}
            aria-label={`Version ${update} is available`} onClick={openUpdates}>
            <StatusDot variant="info" size={7} />
          </button>
        )}
        <div className="status-line" title={`${summary.text}${summary.detail ? `: ${summary.detail}` : ''}, sync ${ago(idx?.lastSweepAt ?? null)}`}>
          <StatusDot variant={summary.tone} pulse={summary.pulse} size={7} />
        </div>
      </div>
    )
  }
  // Hold the space so the sidebar does not shift when the rows arrive.
  if (!ready) return <div className="sidenav-footer footer-pending"><Skeleton count={3} width="70%" height={10} className="footer-skeleton" /></div>
  return (
    <div className="sidenav-footer">
      <IndexLogHost />
      {who?.authEnabled && (
        <div className="status-line" title="Click for identity details">
          <IdentityPanel
            who={who}
            trigger={
              <button className="identity-btn">
                <StatusDot variant={who.verified ? 'success' : 'muted'} size={7} />
                {who.verified ? who.user?.username : 'identity not verified'}
              </button>
            }
          />
        </div>
      )}
      {ai && cfg.aiEnabled !== false && (
        <div className="status-line" title="Click for model availability details">
          <AiPanel
            ai={ai}
            onRecheck={() => { fetch('/api/ai/health?fresh=1').then(r => r.json()).then((h: AiHealth) => { setAi(h); share({ ai: h }) }).catch(() => {}) }}
            trigger={
              <button className="identity-btn">
                <StatusDot variant={ai.status === 'ok' ? 'success' : ai.status === 'unconfigured' ? 'muted' : 'warning'} size={7} />
                {ai.status === 'ok' ? `${ai.models} models ready` : AI_LABEL[ai.status]}
              </button>
            }
          />
        </div>
      )}
      {updated && (
        <div className="status-line" title="A new version was deployed while this page was open">
          <StatusDot variant="warning" size={7} />
          Updated to {updated.version}
          <Button variant="secondary" size="xs" onClick={() => location.reload()}>Reload</Button>
        </div>
      )}
      {build && (
        <div className="status-line" title="Click for build details">
          <BuildPanel
            build={build}
            flow={flow}
            open={buildOpen}
            onOpenChange={setBuildOpen}
            trigger={
              <button className="identity-btn">
                <StatusDot variant={build.version === 'dev' ? 'muted' : 'success'} size={7} />
                <span className="identity-text">{build.version}</span>
              </button>
            }
          />
          {update ? (
            <Button variant="ghost" size="xs" className="footer-sync footer-update" title={`Version ${update} is available. Opens the Build panel, with Update now.`}
              onClick={() => setBuildOpen(true)}>
              update available
            </Button>
          ) : updates ? (
            <Button variant="ghost" size="xs" className="footer-sync" title={checked === 'check failed' && flow.error ? flow.error : 'Check for a newer version now'}
              loading={checking} disabled={checking || !!flow.busy} onClick={() => void checkNow()}>
              {checking ? 'checking…' : checked ?? 'check for updates'}
            </Button>
          ) : null}
        </div>
      )}
      <div className="status-line" title="Click for index health details">
        <IndexPanel
          idx={idx}
          gufi={gufi}
          stats={stats}
          onSync={() => { void sync() }}
          syncing={syncing}
          trigger={
            <button className="identity-btn" title={summary.detail ?? undefined}>
              <StatusDot variant={summary.tone} pulse={summary.pulse} size={7} />
              {summary.text}
            </button>
          }
        />
      </div>
      <div className="status-line muted2">
        {idx?.job
          ? <>for {fmtDuration(elapsed(idx.job))}</>
          : <>sync {ago(idx?.lastSweepAt ?? null)}</>}
        <Button variant="ghost" size="xs" className="footer-sync" onClick={sync} loading={syncing} disabled={busy}>
          {idx?.job ? 'indexing…' : busy ? 'syncing…' : 'sync now'}
        </Button>
      </div>
    </div>
  )
}
