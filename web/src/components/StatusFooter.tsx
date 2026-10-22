import { useEffect, useState, type ReactElement, type ReactNode } from 'react'
import {
  Button, DescriptionList, DescriptionListItem, Popover, PopoverClose, Skeleton, StatusDot, type BadgeVariant,
} from '@parallelworks/ui'
import { api, IndexStatus } from '../api'
import { onLibraryChange } from '../api'
import type { LibraryStats } from '../api'
import { openUpdates, openWhatsNew } from '../api'
import { useUpdateFlow } from '../updateFlow'
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
function FooterPanel({ trigger, title, children }: { trigger: ReactElement; title: ReactNode; children: ReactNode }) {
  return (
    <Popover trigger={trigger} title={title} side="top" align="start" initialFocus="popup" className="identity-panel">
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
  // A row marked in its last field is a library's, indented under the total.
  const rows: [string, string, Dot | null, boolean?][] = [
    ['GUFI engine', gufi === null ? 'checking…' : gufi ? 'available' : 'unavailable (search falls back to grep)', gufi === null ? 'off' : gufi ? 'ok' : 'warn'],
    ['Indexed', stats?.files ? `${stats.files.toLocaleString()} files in ${stats.dirs?.toLocaleString()} directories, ${gb}${stats.libraries ? `, across ${stats.libraries.length} libraries` : ''}` : 'loading…', null],
    ...(stats?.libraries ?? []).map(l => [`${l.label}${l.site ? ` (on ${l.site})` : ''}`, l.available ? `${(l.files ?? 0).toLocaleString()} files in ${(l.dirs ?? 0).toLocaleString()} directories` : (l.error ?? 'not available'), l.available ? 'ok' : 'warn', true] as [string, string, Dot | null, boolean]),
    ['Sync loop', idx ? (idx.running ? 'sweep running now' : `every ${idx.sweepIntervalSec}s, last ${ago(idx.lastSweepAt ?? null)}${idx.lastSweepMs != null ? ` (${idx.lastSweepMs} ms)` : ''}`) : 'loading…', idx?.running ? 'ok' : null],
    ['Last result', idx?.lastError ? idx.lastError : idx?.lastSweepAt ? 'clean' : 'no sweep yet this server start', idx?.lastError ? 'warn' : idx?.lastSweepAt ? 'ok' : 'off'],
  ]
  const busy = syncing || !!idx?.running
  return (
    <FooterPanel trigger={trigger} title="Index health">
      <DescriptionList variant="divided" className="identity-list">
        {rows.map(([k, v, dot, sub]) => (
          <DescriptionListItem key={k} label={k} className={sub ? 'identity-sub' : undefined}>
            {dot && <StatusDot variant={DOT_TONE[dot]} size={7} className="identity-dot" />}{v}
          </DescriptionListItem>
        ))}
      </DescriptionList>
      {(idx?.lastChanges?.length ?? 0) > 0 && (
        <>
          <div className="scope-menu-head">Last sweep re-indexed</div>
          <pre className="identity-claims">{idx!.lastChanges.slice(0, 10).join('\n')}</pre>
        </>
      )}
      <div className="scope-menu-foot">
        <span className="muted identity-src">GET /api/index/status</span>
        <span className="identity-actions">
          <Button variant="secondary" size="sm" onClick={onSync} loading={busy}>{busy ? 'syncing…' : 'Sync now'}</Button>
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

function BuildPanel({ trigger, build }: { trigger: ReactElement; build: Build }) {
  const rows: [string, string][] = [
    ['Version', build.version],
    ['Commit', build.commit ?? 'unknown'],
    ['Built', build.builtAt ? new Date(build.builtAt).toLocaleString() : 'unknown'],
    ['Server started', new Date(build.startedAt).toLocaleString()],
    ['Source', BUILD_SOURCE[build.source]],
  ]
  return (
    <FooterPanel trigger={trigger} title="Build">
      <DetailRows rows={rows} />
      <p className="identity-note">
        Compare this against the release you deployed. A redeploy that did not take leaves
        the old commit and build time here, which is otherwise indistinguishable from one
        that worked.
      </p>
      <div className="scope-menu-foot">
        <span className="muted identity-src">GET /api/version</span>
        <span className="identity-actions">
          <PopoverClose render={<Button variant="secondary" size="sm" />} onClick={openWhatsNew}>What's new</PopoverClose>
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
  const [idx, setIdx] = useState<IndexStatus | null>(null)
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
    api.indexStatus().then(setIdx),
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

  // A newer release the Studio can tell about. The version line says so and
  // opens Settings, General at the Updates section. The page's update flow
  // reads the server's answer now and then (the server checks every few
  // hours) and at once after a check from Settings; a hosted deployment has
  // no updates.
  const flow = useUpdateFlow()
  const update = !flow.none && flow.status?.mode !== 'hosted' && flow.status?.available && flow.status.latest
    ? flow.status.latest.version : null

  const sync = async () => {
    setSyncing(true)
    await syncIndex()
    refresh()
    setSyncing(false)
  }

  const busy = syncing || !!idx?.running
  if (collapsed) {
    return (
      <div className="sidenav-footer">
        <div className="status-line" title={stats?.files ? `${stats.files.toLocaleString()} files indexed, sync ${ago(idx?.lastSweepAt ?? null)}` : 'index loading'}>
          <StatusDot variant={idx?.lastError ? 'warning' : 'success'} size={7} />
        </div>
      </div>
    )
  }
  // Hold the space so the sidebar does not shift when the rows arrive.
  if (!ready) return <div className="sidenav-footer footer-pending"><Skeleton count={3} width="70%" height={10} className="footer-skeleton" /></div>
  return (
    <div className="sidenav-footer">
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
            trigger={
              <button className="identity-btn">
                <StatusDot variant={build.version === 'dev' ? 'muted' : 'success'} size={7} />
                <span className="identity-text">{build.version}</span>
              </button>
            }
          />
          {update && (
            <Button variant="ghost" size="xs" className="footer-sync" title={`Version ${update} is available. Opens Settings, General, Updates.`}
              onClick={openUpdates}>
              update available
            </Button>
          )}
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
            <button className="identity-btn">
              <StatusDot variant={idx?.lastError ? 'warning' : 'success'} size={7} />
              {stats?.files ? `${stats.files.toLocaleString()} files indexed` : 'index loading'}
            </button>
          }
        />
      </div>
      <div className="status-line muted2">
        sync {ago(idx?.lastSweepAt ?? null)}
        <Button variant="ghost" size="xs" className="footer-sync" onClick={sync} loading={busy}>
          {busy ? 'syncing…' : 'sync now'}
        </Button>
      </div>
    </div>
  )
}
