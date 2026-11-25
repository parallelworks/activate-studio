import { useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react'
import {
  Button, CopyCodeBlock, DescriptionList, DescriptionListItem, Popover, PopoverClose, Skeleton, StatusDot, type BadgeVariant,
} from '@parallelworks/ui'
import { api, IndexStatus } from '../api'
import { currentLibrary, onLibraryChange, withLibrary } from '../api'
import { readNow, useWatch } from '../live'
import { elapsed, fmtDuration, indexSummary, kickIndexStatus, useIndexStatus } from '../indexActivity'
import { IndexJobView, IndexLogButton, IndexLogHost, jobOutcome } from './IndexActivity'
import type { LibraryStats } from '../api'
import { openAbout, openUpdates, openWhatsNew } from '../api'
import { underWay, updateActions, useUpdateFlow, type UpdateFlow } from '../updateFlow'
import { useAfterUpdate } from '../afterUpdate'
import { ReopenApp } from './ReopenApp'
import { changeCount, UpdateDetails } from './UpdateDetails'
import { UpdateProgress } from './UpdatesSection'
import { share, syncIndex, type AiHealth, type KbStats } from '../statusShare'
import { aiOn, mainWritable, useAppConfig } from '../config'
import { reloadOntoNewest, useShellUpdate } from '../shellWorker'
import { plural } from '../plural'
import { steadyCount } from '../steady'
import { embedded, signInHref } from '../signIn'
import { libraryMachine, MachineLabel } from './MachineLabel'

/** The sync row's words: when the background sync last finished. */
export function syncedLine(iso: string | null): string {
  return iso ? `Synced ${ago(iso)}` : 'Sync pending'
}

/** A footer row's words start with a capital, as the rest of Studio's do. */
const sentence = (s: string | undefined) => (s ? s.charAt(0).toUpperCase() + s.slice(1) : '')

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

function IndexPanel({ trigger, idx, gufi, stats, onSync, syncing, canSync }: {
  trigger: ReactElement
  idx: IndexStatus | null
  gufi: boolean | null
  stats: { files?: number; dirs?: number; totalBytes?: number; libraries?: LibraryStats[]; lib?: string } | null
  onSync: () => void
  syncing: boolean
  /** The viewer may sync the main library, and stop its jobs. */
  canSync: boolean
}) {
  const gb = stats?.totalBytes ? `${(stats.totalBytes / 1e9).toFixed(2)} GB` : ''
  const job = idx?.job ?? null
  const libs = useAppConfig().libraries ?? []
  const summary = indexSummary(idx, stats?.files, countedIn(libs, stats?.lib))
  const [stopping, setStopping] = useState(false)
  const stop = async () => {
    setStopping(true)
    try { await api.stopIndexing(); kickIndexStatus() } catch { /* the status says */ } finally { setStopping(false) }
  }
  const last = idx?.lastBuild?.kind === 'build' ? idx.lastBuild : null
  // A row marked in its last field is a library's, indented under the total.
  const rows: [string, string, Dot | null, boolean?, ReactNode?][] = [
    ['GUFI engine', gufi === null ? 'checking…' : gufi ? 'available' : 'unavailable (search falls back to grep)', gufi === null ? 'off' : gufi ? 'ok' : 'warn'],
    ['Indexed', stats?.files ? `${plural(stats.files, 'file')} in ${plural(stats.dirs ?? 0, 'directory', 'directories')}, ${gb}${stats.libraries ? `, across ${stats.libraries.length} libraries` : ''}` : idx?.indexed === false ? 'not indexed yet' : 'loading…', null],
    ...(stats?.libraries ?? []).map(l => [`${l.label}${l.site ? ` (on ${l.site})` : ''}`, l.available ? `${plural(l.files ?? 0, 'file')} in ${plural(l.dirs ?? 0, 'directory', 'directories')}` : (l.error ?? 'not available'), l.available ? 'ok' : 'warn', true,
      l.site ? <>{l.label} (on <MachineLabel machine={libraryMachine(libs.find(x => x.id === l.id)) ?? { name: l.site }} />)</> : undefined] as [string, string, Dot | null, boolean, ReactNode]),
    ...(last ? [['Last build', `${jobOutcome(last)}${last.finishedAt ? ` ${ago(last.finishedAt)}` : ''}`, last.state === 'done' ? 'ok' : 'warn'] as [string, string, Dot | null]] : []),
    ['Sync loop', idx ? (idx.sweepIntervalSec > 0 ? `every ${idx.sweepIntervalSec}s, last ${ago(idx.lastSweepAt ?? null)}${idx.lastSweepMs != null ? ` (${fmtDuration(idx.lastSweepMs)})` : ''}` : 'paused') : 'loading…', null],
    ['Last result', summary.tone === 'warning' && summary.detail ? summary.detail : idx?.lastSweepAt || last ? 'clean' : 'no sync yet this server start', summary.tone === 'warning' ? 'warn' : idx?.lastSweepAt || last ? 'ok' : 'off'],
  ]
  const busy = syncing || !!idx?.running
  return (
    <FooterPanel trigger={trigger} title="Index health">
      {job && <IndexJobView job={job} onStop={canSync ? () => void stop() : undefined} stopping={stopping} />}
      <DescriptionList variant="divided" className="identity-list">
        {rows.map(([k, v, dot, sub, label]) => (
          <DescriptionListItem key={k} label={label ?? k} className={sub ? 'identity-sub' : undefined}>
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
          {canSync && <Button variant="secondary" size="sm" onClick={onSync} loading={syncing} disabled={busy}>{busy ? (job ? 'indexing…' : 'syncing…') : 'Sync now'}</Button>}
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

// The models count as shown, and a lower read not yet confirmed
// (steady.ts): a model served from a session drops out of one read when
// that session is slow to answer.
let shownModels: number | null = null
const modelsLow: { last: number | null; at?: number } = { last: null }
/** The health to show: a lower models count once two reads in a row say
 *  so, or at once when asked (`fresh`, the Re-check button). */
export function steadyModels(h: AiHealth, fresh = false, now = Date.now()): AiHealth {
  if (h.status !== 'ok' || fresh) { shownModels = h.status === 'ok' ? h.models : null; modelsLow.last = null; modelsLow.at = undefined; return h }
  const models = steadyCount(shownModels, h.models, modelsLow, now)
  shownModels = models
  return models === h.models ? h : { ...h, models }
}

/** "1 model ready", "3 models ready". */
export const modelsReady = (n: number) => `${n} ${n === 1 ? 'model' : 'models'} ready`

export const AI_LABEL: Record<AiHealth['status'], string> = {
  ok: 'models ready',
  auth: 'model key rejected',
  unreachable: 'models unreachable',
  offline: 'models offline',
  unconfigured: 'models not configured',
  'key-required': 'sign in with ACTIVATE',
  off: 'AI features off',
}

function AiPanel({ trigger, ai, onRecheck }: { trigger: ReactElement; ai: AiHealth; onRecheck: () => void }) {
  const rows: [string, string][] = [
    ['Status', AI_LABEL[ai.status] + (ai.status === 'ok' ? ` (${ai.models} ${ai.models === 1 ? 'model' : 'models'})` : '')],
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
 *  What's new, and Update now (the app) or the commands (a clone); an
 *  install under way shows its progress. Not for a hosted deployment, which
 *  its operator updates. */
function BuildUpdates({ flow, onDetails }: { flow: UpdateFlow; onDetails: () => void }) {
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
            <PopoverClose render={<Button variant="secondary" size="sm" />} onClick={onDetails}>
              What's new{changeCount(status) ? ` (${changeCount(status)})` : ''}
            </PopoverClose>
            <PopoverClose render={<Button variant="secondary" size="sm" />} onClick={openUpdates}>Changes</PopoverClose>
            {check}
          </div>
          {status.command && (
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
  // What the newer versions bring, in a dialog over the page. Update now in
  // it opens the panel again, which shows the install's progress.
  const [details, setDetails] = useState(false)
  const install = () => { setDetails(false); onOpenChange?.(true); void updateActions.install() }
  return (
    <>
    {details && <UpdateDetails flow={flow} onClose={() => setDetails(false)} onInstall={install} />}
    <FooterPanel trigger={trigger} title="Build" open={open} onOpenChange={onOpenChange}>
      <DetailRows rows={rows} />
      <BuildUpdates flow={flow} onDetails={() => setDetails(true)} />
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
    </>
  )
}

interface SignIn {
  proxy: boolean
  oidc: boolean
  required: boolean
  source: 'proxy' | 'oidc' | null
  loginUrl: string | null
  logoutUrl: string | null
}

interface WhoAmI {
  authEnabled: boolean
  header: string
  verified: boolean
  signIn?: SignIn
  user: { id: string; username: string; name?: string; account?: string | null; uid?: number | null; gid?: number | null; groups?: string[] | null } | null
  token: { issuer: string | null; audience: unknown; issuedAt: string | null; expiresAt: string | null } | null
  claims: Record<string, unknown> | null
}

function SignInLink({ loginUrl }: { loginUrl: string }) {
  return (
    <a className="identity-btn" href={signInHref(loginUrl)} {...(embedded() ? { target: '_blank', rel: 'noopener' } : {})}>
      <StatusDot variant="muted" size={7} />
      Sign in
    </a>
  )
}

function IdentityPanel({ trigger, who }: { trigger: ReactElement; who: WhoAmI }) {
  const oidc = who.signIn?.source === 'oidc'
  const u = who.user
  const rows: [string, string][] = who.verified
    ? [
        ['Username', u?.username ?? ''],
        ['Name', u?.name ?? '(not set)'],
        ...(u?.account ? [['Account', u.account] as [string, string]] : []),
        ...(u?.uid != null ? [['uid', String(u.uid)] as [string, string]] : []),
        ...(u?.gid != null ? [['gid', String(u.gid)] as [string, string]] : []),
        ...(u?.groups?.length ? [['Groups', u.groups.join(', ')] as [string, string]] : []),
        ['Subject', String(who.claims?.sub ?? '')],
        ['Issuer', who.token?.issuer ?? ''],
        ['Audience', Array.isArray(who.token?.audience) ? (who.token?.audience as string[]).join('\n') : String(who.token?.audience ?? '')],
        ['Issued', who.token?.issuedAt ? new Date(who.token.issuedAt).toLocaleTimeString() : ''],
        ['Expires', who.token?.expiresAt ? `${new Date(who.token.expiresAt).toLocaleTimeString()} (${Math.max(0, Math.round((Date.parse(who.token.expiresAt) - Date.now()) / 1000))}s)` : ''],
      ]
    : []
  return (
    <FooterPanel trigger={trigger} title={!who.verified ? 'Identity not verified' : oidc ? 'Signed in' : 'Platform-verified identity'}>
      {who.verified ? (
        <>
          <DetailRows rows={rows} />
          <div className="scope-menu-head">Raw token claims</div>
          <pre className="identity-claims">{JSON.stringify(who.claims, null, 2)}</pre>
        </>
      ) : who.signIn?.oidc && !who.signIn.proxy ? (
        <p className="identity-note">
          You are not signed in. Sign in with your identity provider to use your own conversations, keys and libraries.
        </p>
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
        <span className="identity-actions">
          {who.signIn?.logoutUrl && (
            <form method="post" action={who.signIn.logoutUrl}>
              <Button type="submit" variant="secondary" size="sm">Sign out</Button>
            </form>
          )}
          <CloseButton />
        </span>
      </div>
    </FooterPanel>
  )
}

/** Which library the footer's file count is of, when it is not the main
 *  library's: the count follows the library selected. */
export function countedIn(libs: { id: string; label: string; primary: boolean }[], lib: string = currentLibrary()): string | undefined {
  if (lib === 'all') return 'all libraries'
  const l = libs.find(x => x.id === lib)
  return l && !l.primary ? l.label : undefined
}

export function StatusFooter({ collapsed = false }: { collapsed?: boolean }) {
  // With AI features off there is no model line.
  const cfg = useAppConfig()
  // The counts and the library they are of: a count is never shown beside
  // another library's name.
  const [stats, setStats] = useState<{ files?: number; dirs?: number; totalBytes?: number; libraries?: LibraryStats[]; lib?: string } | null>(null)
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
  const settled = useRef(new Set<string>())
  const done = (what: string) => {
    settled.current.add(what)
    if (settled.current.size === 4) setReady(true)
  }
  // What it reads is shared with the status panel, which shows the same
  // health and counts without asking the server again.
  const [lib, setLib] = useState(currentLibrary)
  const statsPath = withLibrary('/api/kb/stats')
  useWatch<KbStats>(statsPath, 60_000, true, s => {
    // An answer for a library no longer selected is dropped.
    if (lib === currentLibrary()) { setStats({ ...s, lib }); share({ stats: s }) }
    done('stats')
  }, () => done('stats'))
  const takeWho = (w: WhoAmI) => {
    setWho(w); share({ authEnabled: !!w.authEnabled })
    // A sign-in that ended while the page was open, on a Studio that
    // requires one: go through sign-in again and come back here.
    if (w.signIn?.required && w.signIn.loginUrl && !w.verified && !embedded()) location.assign(signInHref(w.signIn.loginUrl))
    done('whoami')
  }
  useWatch<WhoAmI>('/api/whoami', 60_000, true, takeWho, e => { if (e.body) takeWho(e.body as WhoAmI); else done('whoami') })
  useWatch<{ gufi: boolean }>('/healthz', 60_000, true, h => { setGufi(h.gufi); done('health') }, () => { setGufi(null); done('health') })
  useWatch<AiHealth>('/api/ai/health', 60_000, true, h => { h = steadyModels(h); setAi(h); share({ ai: h }); done('ai') }, () => { setAi(null); share({ ai: null }); done('ai') })
  const reread = useRef(() => {})
  reread.current = () => { for (const p of [statsPath, '/api/whoami', '/healthz', '/api/ai/health']) readNow(p).catch(() => {}) }
  const refresh = () => reread.current()
  useEffect(() => {
    // The count follows the library selected and any change to the index.
    // Another library selected: its count is not known until its stats come
    // back, so the last library's is not shown under the new name meanwhile.
    const off = onLibraryChange(() => { setStats(null); setLib(currentLibrary()) })
    window.addEventListener('ade-kb-changed', refresh)
    // A change in Settings, Model access changes the model line.
    window.addEventListener('ade:model-access-changed', refresh)
    return () => { off(); window.removeEventListener('ade-kb-changed', refresh); window.removeEventListener('ade:model-access-changed', refresh) }
  }, [])
  // Switching AI features on or off changes the model line at once: the
  // configuration is read again after the switch is saved, and a change of
  // it here asks the server for the model health again, instead of waiting
  // for the next poll.
  const aiFlag = cfg.loaded ? aiOn(cfg) : null
  const seenAi = useRef<boolean | null>(null)
  useEffect(() => {
    if (aiFlag === null) return
    if (seenAi.current !== null && seenAi.current !== aiFlag) refresh()
    seenAi.current = aiFlag
  }, [aiFlag]) // eslint-disable-line react-hooks/exhaustive-deps
  // Polled: a change means a new version was rolled out under this page.
  // The page keeps working against the new server, but its own code is
  // the old build, so it says so and offers a reload; open conversations
  // are saved server-side and survive it.
  const [updated, setUpdated] = useState<Build | null>(null)
  // A newer build of the page waits on this device: the page opened from
  // the copy kept there (web/shell/sw.js) after the server moved on.
  const shellNewer = useShellUpdate()
  const first = useRef<string | null>(null)
  useWatch<Build>('/api/version', 60_000, true, b => {
    if (first.current === null) { first.current = b.commit; setBuild(b); return }
    if (b.commit !== first.current) setUpdated(b)
  }, () => { if (first.current === null) setBuild(null) })

  // A newer release the Studio can tell about. The version line says so,
  // and the Build panel it opens has Update now; with none known, the line
  // offers a check. An install under way, and one that failed, show in the
  // same place, and the panel has the progress or the error. The page's
  // update flow reads the server's answer every two minutes and when the
  // window is shown (the server checks every 15 minutes); a hosted
  // deployment has no updates and shows none of this.
  const flow = useUpdateFlow()
  const updates = !flow.none && !!flow.status && flow.status.mode !== 'hosted'
  const update = updates && flow.status?.available && flow.status.latest ? flow.status.latest.version : null
  const timedOut = !!flow.waiting?.timedOut
  const failed = updates && (timedOut || (flow.failed && !!(flow.error || flow.status?.state === 'error')))
  // Waiting for the app to reopen is the reopen line's to say.
  const installing = updates && !timedOut && underWay(flow) && flow.status?.state !== 'relaunch'
  // After an update: What's new beside the version for this page load, where
  // the line has nothing about a newer version to say, and a line while the
  // app's window waits to reopen.
  const after = useAfterUpdate(flow)
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
  // Where people sign in the main library is synced on its schedule and by
  // those who may change it; the server refuses the others.
  const canSync = cfg.loaded && mainWritable(cfg)
  const summary = indexSummary(idx, stats?.files, countedIn(cfg.libraries ?? [], stats?.lib))
  // The row itself is short enough for the narrowest sidebar; which
  // libraries the count covers is in its tooltip.
  const row = indexSummary(idx, stats?.files)
  const railUpdate = failed ? 'The update failed' : installing ? 'Updating' : `Version ${update} is available`
  if (collapsed) {
    return (
      <div className="sidenav-footer">
        {after.reopen && (
          <button className="status-line footer-rail-update" title="Reopen the app to finish updating. Opens Settings, Updates."
            aria-label="Reopen to update" onClick={openUpdates}>
            <StatusDot variant="warning" size={7} />
          </button>
        )}
        {(update || installing || failed) && (
          <button className="status-line footer-rail-update" title={`${railUpdate}. Opens Settings, Updates.`}
            aria-label={railUpdate} onClick={openUpdates}>
            <StatusDot variant={failed ? 'warning' : 'info'} pulse={installing} size={7} />
          </button>
        )}
        <div className="status-line" title={`${summary.text}${summary.detail ? `: ${summary.detail}` : ''}, ${syncedLine(idx?.lastSweepAt ?? null).toLowerCase()}`}>
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
      {who?.authEnabled && !who.verified && who.signIn?.loginUrl && (
        <div className="status-line" title={embedded() ? 'Sign in, in a new tab' : 'Sign in'}>
          <SignInLink loginUrl={who.signIn.loginUrl} />
        </div>
      )}
      {who?.authEnabled && (who.verified || !who.signIn?.loginUrl) && (
        <div className="status-line" title="Click for identity details">
          <IdentityPanel
            who={who}
            trigger={
              <button className="identity-btn">
                <StatusDot variant={who.verified ? 'success' : 'muted'} size={7} />
                <span className="identity-text">{who.verified ? who.user?.username : 'Identity not verified'}</span>
              </button>
            }
          />
        </div>
      )}
      {/* An 'off' answer read before AI features were switched on is stale
          until the read the switch starts returns, so it is not shown. */}
      {ai && cfg.aiEnabled !== false && ai.status !== 'off' && (
        <div className="status-line" title="Click for model availability details">
          <AiPanel
            ai={ai}
            onRecheck={() => { fetch('/api/ai/health?fresh=1').then(r => r.json()).then((h: AiHealth) => { h = steadyModels(h, true); setAi(h); share({ ai: h }) }).catch(() => {}) }}
            trigger={
              <button className="identity-btn">
                <StatusDot variant={ai.status === 'ok' ? 'success' : ai.status === 'unconfigured' ? 'muted' : 'warning'} size={7} />
                <span className="identity-text">{sentence(ai.status === 'ok' ? modelsReady(ai.models) : AI_LABEL[ai.status])}</span>
              </button>
            }
          />
        </div>
      )}
      {(updated || (shellNewer && build)) && (
        <div className="status-line" data-testid="footer-reload" title={`Version ${(updated ?? build)?.version} was deployed while this page was open, or since this device last loaded the page`}>
          <StatusDot variant="warning" size={7} />
          <span className="identity-text">Updated<span className="footer-long"> to {(updated ?? build)?.version}</span></span>
          <Button variant="ghost" size="xs" className="footer-sync" onClick={() => { void reloadOntoNewest() }}>Reload</Button>
        </div>
      )}
      {after.reopen && (
        <div className="status-line" data-testid="footer-reopen" title="The update is installed and changed the app's window, which updates when the app reopens. Quit and reopen the app to finish.">
          <StatusDot variant="warning" size={7} />
          <ReopenApp compact />
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
          {installing ? (
            <Button variant="ghost" size="xs" className="footer-sync footer-update" title="An update is being installed. Opens the Build panel, with its progress."
              onClick={() => setBuildOpen(true)}>
              Updating…
            </Button>
          ) : failed ? (
            <Button variant="ghost" size="xs" className="footer-sync footer-update" title="The update was not installed. Opens the Build panel, which says why."
              onClick={() => setBuildOpen(true)}>
              Update failed
            </Button>
          ) : update ? (
            <Button variant="ghost" size="xs" className="footer-sync footer-update footer-update-available" title={`Version ${update} is available. Opens the Build panel, with Update now.`}
              aria-label="Update available" onClick={() => setBuildOpen(true)}>
              <span>Update<span className="footer-long"> available</span></span>
            </Button>
          ) : after.updated ? (
            <Button variant="ghost" size="xs" className="footer-sync" title="Updated since you last opened Studio. Shows what changed."
              onClick={() => { after.clear(); openWhatsNew() }}>
              What&apos;s new
            </Button>
          ) : updates ? (
            <Button variant="ghost" size="xs" className="footer-sync" title={checked === 'check failed' && flow.error ? flow.error : 'Check for a newer version now'}
              aria-label={checking || checked ? undefined : 'Check for updates'}
              loading={checking} disabled={checking || !!flow.busy} onClick={() => void checkNow()}>
              {checking ? 'Checking…' : checked ? sentence(checked) : <span>Check<span className="footer-long"> for updates</span></span>}
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
          canSync={canSync}
          trigger={
            <button className="identity-btn" title={summary.detail ?? summary.text}>
              <StatusDot variant={row.tone} pulse={row.pulse} size={7} />
              <span className="identity-text">{row.text}</span>
            </button>
          }
        />
      </div>
      <div className="status-line muted2">
        {idx?.job
          ? <span className="identity-text">Running for {fmtDuration(elapsed(idx.job))}</span>
          : <span className="identity-text">{syncedLine(idx?.lastSweepAt ?? null)}</span>}
        {canSync && <Button variant="ghost" size="xs" className="footer-sync" onClick={sync} loading={syncing} disabled={busy}>
          {idx?.job ? 'Indexing…' : busy ? 'Syncing…' : 'Sync now'}
        </Button>}
      </div>
    </div>
  )
}
