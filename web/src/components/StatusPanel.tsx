import { useEffect, useState, type ReactNode } from 'react'
import { Button, DisclosurePanel, IconButton, StatusDot, type BadgeVariant } from '@parallelworks/ui'
import { ChevronRightIcon } from '@parallelworks/ui/icons'
import { api, currentLibrary, type IndexStatus, type PublicLibrary, type UserLibrary } from '../api'
import type { AppConfig } from '../config'
import { setPanelOpen, usePanel } from '../panelState'
import { modelLabel, syncIndex, useShared, type AiHealth, type KbStats } from '../statusShare'
import { AI_LABEL } from './StatusFooter'
import { openInSideView } from './RunPane'
import { openSessionWindow, type SessionInfo } from './SessionView'

/**
 * The status panel at the right of every page: what is running, the
 * libraries and the index, the model, and the connections, each a card
 * whose header line says the one thing worth knowing and opens to the
 * detail. It reads only what is cheap: local routes every 10 seconds while
 * it is open and the tab is visible, the platform's session list once a
 * minute, and the footer's own model and index reads, which it shares
 * rather than repeats. Closed, it asks for nothing.
 */

export interface TaskRow { id: string; objective: string; state: string; running?: number; waiting?: number }
export interface FleetAgent { id: string; name: string; state: string; currentTaskId?: string | null }
export interface RunRow { slug: string; workflow: string; owner?: string | null; launchedAt: string; state: string; endedAt: string | null }
export interface McpRow { name: string; tools: number; error: string | null; checkedAt?: string | null }

export interface StatusData {
  tasks: TaskRow[] | null
  fleet: FleetAgent[] | null
  runs: RunRow[] | null
  sessions: SessionInfo[] | null
  idx: IndexStatus | null
  userLibs: UserLibrary[] | null
  mcp: McpRow[] | null
}

const EMPTY: StatusData = { tasks: null, fleet: null, runs: null, sessions: null, idx: null, userLibs: null, mcp: null }
const FAST_MS = 10_000
const SLOW_MS = 60_000

// Kept across a close and reopen, so the cards come back drawn and a
// toggle does not refetch the platform's list.
let lastData: StatusData = EMPTY
let lastSlowAt = 0
// Attached servers are checked once a page load; polls read the last state.
let mcpChecked = false
// A deployment with delegation off refuses the fleet route; ask once.
let fleetRefused = false

/** Tests start from nothing. */
export function resetStatusPanelForTests(): void { lastData = EMPTY; lastSlowAt = 0; mcpChecked = false; fleetRefused = false }

const LIVE_TASK = new Set(['submitted', 'working', 'input-required'])
const TERMINAL_RUN = new Set(['completed', 'error', 'failed', 'canceled', 'cancelled', 'skipped'])
const runEnded = (r: RunRow) => !!r.endedAt || TERMINAL_RUN.has(String(r.state).toLowerCase())

function ago(iso: string | null | undefined): string {
  if (!iso) return ''
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  if (s < 172800) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}
const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`
const go = (hash: string) => { location.hash = hash }

function runTone(state: string): { tone: BadgeVariant; pulse: boolean } {
  const s = String(state).toLowerCase()
  if (s === 'running' || s === 'starting' || s === 'started' || s === 'submitted') return { tone: 'info', pulse: true }
  if (s === 'completed') return { tone: 'success', pulse: false }
  if (s === 'error' || s === 'failed' || s === 'faulted') return { tone: 'error', pulse: false }
  return { tone: 'muted', pulse: false }
}

/** Opens a run the way the page it is clicked on does: beside the chat on
 *  the chat page, in the Workflows page's side view everywhere else. */
export function openRun(slug: string, view: string): void {
  if (view === 'chat') { window.postMessage({ type: 'ade-side-view', run: slug }, window.location.origin); return }
  if (view !== 'workflows') go('#view=workflows')
  openInSideView({ run: slug })
}

/** A session the Studio can relay opens in a side view, as on the chat and
 *  Workflows pages; one served only under the platform's address opens in
 *  a window of its own. */
export function openSession(s: SessionInfo, view: string): void {
  if (!(s.ownHost && s.url)) { openSessionWindow(s); return }
  if (view === 'chat') { window.postMessage({ type: 'ade-side-view', session: s }, window.location.origin); return }
  if (view !== 'workflows') go('#view=workflows')
  openInSideView({ session: s })
}

// ---- what each card's header line says ----

export interface Summary { text: string; tone: BadgeVariant | null; pulse?: boolean }

export function activitySummary(d: Pick<StatusData, 'tasks' | 'fleet' | 'runs' | 'sessions'>): Summary {
  const tasks = (d.tasks ?? []).filter(t => LIVE_TASK.has(t.state))
  const fleet = d.fleet ?? []
  const waiting = tasks.filter(t => t.state === 'input-required').length + fleet.filter(a => a.state === 'input-required').length
  const running = tasks.filter(t => t.state !== 'input-required').length + fleet.filter(a => a.state === 'working').length
    + (d.runs ?? []).filter(r => !runEnded(r)).length
  const sessions = (d.sessions ?? []).length
  // Two parts at most, so the line stays one line: what needs the viewer,
  // then what runs, then the sessions open.
  const parts = [
    waiting ? `${waiting} need${waiting === 1 ? 's' : ''} you` : '',
    running ? `${running} running` : '',
    sessions ? plural(sessions, 'session') : '',
  ].filter(Boolean).slice(0, 2)
  return { text: parts.join(' · ') || 'idle', tone: waiting ? 'warning' : running ? 'info' : null, pulse: !waiting && running > 0 }
}

export interface LibLine { id: string; label: string; kind: string; state: string; tone: BadgeVariant; pulse: boolean; count: string | null; note: string | null }

/** Every library the viewer has: the configured ones, and their own on
 *  their clusters, including any still building or failed. */
export function libraryLines(libs: PublicLibrary[], userLibs: UserLibrary[] | null, stats: KbStats | null): LibLine[] {
  const counted = (id: string): number | undefined => {
    const one = stats?.libraries?.find(l => l.id === id)
    if (one) return one.available ? one.files : undefined
    return !stats?.libraries && currentLibrary() === id ? stats?.files : undefined
  }
  const lines: LibLine[] = []
  for (const l of libs) {
    if (userLibs?.some(u => u.id === l.id)) continue
    const st = stats?.libraries?.find(x => x.id === l.id)
    const n = counted(l.id)
    lines.push({
      id: l.id, label: l.label,
      kind: l.primary ? 'knowledge base' : l.site ? `on ${l.site}` : l.writable ? 'writable' : 'read only',
      state: st && !st.available ? 'unavailable' : l.caps.index ? 'indexed' : 'not indexed',
      tone: st && !st.available ? 'warning' : l.caps.index ? 'success' : 'muted', pulse: false,
      count: n != null ? `${n.toLocaleString()} files` : null,
      note: st && !st.available ? st.error ?? null : null,
    })
  }
  for (const u of userLibs ?? []) {
    const n = u.files ?? counted(u.id)
    lines.push({
      id: u.id, label: u.label, kind: `on ${u.site}`, state: u.state,
      tone: u.state === 'failed' ? 'error' : u.state === 'ready' ? 'success' : 'info',
      pulse: u.state === 'building' || u.state === 'updating',
      count: n != null ? `${n.toLocaleString()} files` : null,
      note: u.error,
    })
  }
  return lines
}

export function librariesSummary(lines: LibLine[], idx: IndexStatus | null): Summary {
  const failed = lines.filter(l => l.tone === 'error').length
  const unavailable = lines.filter(l => l.tone === 'warning').length
  const building = lines.filter(l => l.pulse).length
  if (failed) return { text: `${failed} failed`, tone: 'warning' }
  if (unavailable) return { text: `${unavailable} unavailable`, tone: 'warning' }
  if (idx?.lastError) return { text: 'sync failed', tone: 'warning' }
  if (building) return { text: `${building} building`, tone: 'info', pulse: true }
  if (idx?.running) return { text: 'syncing', tone: 'info', pulse: true }
  if (idx?.lastSweepAt) return { text: `synced ${ago(idx.lastSweepAt)}`, tone: null }
  return { text: plural(lines.length, 'library', 'libraries'), tone: null }
}

export function modelSummary(ai: AiHealth | null, model: string | null): Summary {
  if (!ai) return { text: model ?? '', tone: null }
  if (ai.status === 'ok') return { text: model ?? `${ai.models} models`, tone: 'success' }
  return { text: AI_LABEL[ai.status], tone: ai.status === 'unconfigured' || ai.status === 'off' ? 'muted' : 'warning' }
}

export interface ConnLine { key: string; name: string; meta: string; tone: BadgeVariant; title?: string; onClick?: () => void }

export function connectionLines(o: {
  platform: boolean; authEnabled: boolean | null; ai: AiHealth | null; mcp: McpRow[] | null
  libs: PublicLibrary[]; userLibs: UserLibrary[] | null
}): ConnLine[] {
  const rows: ConnLine[] = []
  if (o.platform) {
    rows.push(o.ai?.platformAccess === false
      ? { key: 'platform', name: 'Platform', meta: 'your key does not reach it', tone: 'warning', onClick: () => go('#view=settings:access') }
      : { key: 'platform', name: 'Platform', meta: 'connected', tone: 'success' })
  } else if (o.authEnabled) {
    rows.push({ key: 'platform', name: 'Platform', meta: 'needs your key', tone: 'warning', onClick: () => go('#view=settings:access') })
  }
  for (const m of o.mcp ?? []) {
    rows.push(m.error
      ? { key: `mcp:${m.name}`, name: m.name, meta: 'not answering', tone: 'warning', title: m.error }
      : m.checkedAt === null
        ? { key: `mcp:${m.name}`, name: m.name, meta: 'not checked yet', tone: 'muted' }
        : { key: `mcp:${m.name}`, name: m.name, meta: `connected, ${plural(m.tools, 'tool')}`, tone: 'success' })
  }
  // The clusters the viewer's libraries live on, as their libraries last
  // found them; the panel does not reach out to check.
  const sites = new Map<string, Map<string, string>>()
  const note = (site: string, id: string, state: string) => sites.set(site, (sites.get(site) ?? new Map()).set(id, state))
  for (const l of o.libs) if (l.site) note(l.site, l.id, 'ready')
  for (const u of o.userLibs ?? []) note(u.site, u.id, u.state)
  for (const [site, byId] of sites) {
    const states = [...byId.values()]
    const failed = states.includes('failed')
    rows.push({
      key: `site:${site}`, name: site,
      meta: failed ? 'a library build failed' : plural(states.length, 'library', 'libraries'),
      tone: failed ? 'warning' : 'success',
    })
  }
  return rows
}

export function connectionsSummary(rows: ConnLine[]): Summary {
  const attention = rows.filter(r => r.tone === 'warning' || r.tone === 'error').length
  if (attention) return { text: `${attention} need${attention === 1 ? 's' : ''} attention`, tone: 'warning' }
  return { text: `${rows.filter(r => r.tone === 'success').length} connected`, tone: null }
}

// ---- polling ----

async function getJson<T>(url: string): Promise<T> {
  const r = await fetch(url)
  if (!r.ok) throw Object.assign(new Error(`${url}: ${r.status}`), { status: r.status })
  return r.json() as Promise<T>
}

function useStatusData(agents: boolean, runs: boolean): [StatusData, (p: Partial<StatusData>) => void] {
  const [data, setData] = useState<StatusData>(lastData)
  const patch = (p: Partial<StatusData>) => setData(d => (lastData = { ...d, ...p }))
  useEffect(() => {
    let live = true
    const put = (p: Partial<StatusData>) => { if (live) patch(p) }
    const fast = () => {
      api.indexStatus().then(idx => put({ idx })).catch(() => {})
      if (agents) {
        getJson<{ tasks: TaskRow[] }>('/api/tasks').then(d => put({ tasks: d.tasks ?? [] })).catch(() => {})
        if (!fleetRefused) {
          getJson<{ agents: FleetAgent[] }>('/api/fleet').then(d => put({ fleet: d.agents ?? [] }))
            .catch(e => { if ((e as { status?: number }).status === 403) { fleetRefused = true; put({ fleet: [] }) } })
        }
      }
      if (runs) getJson<{ runs: RunRow[] }>('/api/runs').then(d => put({ runs: d.runs ?? [] })).catch(() => {})
      const url = mcpChecked ? '/api/settings/mcp-servers?cached=1' : '/api/settings/mcp-servers'
      mcpChecked = true
      getJson<{ servers: McpRow[] }>(url).then(d => put({ mcp: d.servers ?? [] })).catch(() => {})
    }
    // A platform request and a cluster check behind the viewer's own
    // libraries, so once a minute, the footer's pace.
    const slow = () => {
      lastSlowAt = Date.now()
      // A refusal leaves the list as it was, and waits its minute too.
      if (runs) getJson<{ sessions: SessionInfo[] }>('/api/sessions').then(d => put({ sessions: (d.sessions ?? []).filter(s => s.status === 'running') }))
        .catch(() => put({ sessions: lastData.sessions ?? [] }))
      api.userLibraries().then(d => put({ userLibs: d.libraries ?? [] })).catch(() => {})
    }
    const tick = () => {
      if (document.visibilityState === 'hidden') return
      fast()
      // Due once a minute, and at once when sessions become wanted.
      if (Date.now() - lastSlowAt >= SLOW_MS || (runs && lastData.sessions === null)) slow()
    }
    tick()
    const t = window.setInterval(tick, FAST_MS)
    const onVisible = () => { if (document.visibilityState === 'visible') tick() }
    document.addEventListener('visibilitychange', onVisible)
    return () => { live = false; window.clearInterval(t); document.removeEventListener('visibilitychange', onVisible) }
  }, [agents, runs])
  return [data, patch]
}

// ---- the parts ----

const CARDS_KEY = 'ade-status-cards'
const CARD_DEFAULTS: Record<string, boolean> = { activity: true }
function readCards(): Record<string, boolean> {
  try { return JSON.parse(localStorage.getItem(CARDS_KEY) ?? '{}') as Record<string, boolean> } catch { return {} }
}

function Card({ id, title, summary, children }: { id: string; title: string; summary: Summary; children: ReactNode }) {
  const [open, setOpen] = useState(() => readCards()[id] ?? CARD_DEFAULTS[id] ?? false)
  const toggle = () => {
    const next = !open
    setOpen(next)
    try { localStorage.setItem(CARDS_KEY, JSON.stringify({ ...readCards(), [id]: next })) } catch { /* not remembered */ }
  }
  const panelId = `status-card-${id}`
  return (
    <section className="status-card" data-card={id}>
      <button type="button" className="status-card-head" aria-expanded={open} aria-controls={panelId} onClick={toggle}>
        <span className="status-card-title">{title}</span>
        <ChevronRightIcon aria-hidden="true" size={14} className={`status-card-chev${open ? ' open' : ''}`} />
        <span className="status-card-summary">
          {summary.tone && <StatusDot variant={summary.tone} pulse={summary.pulse} size={6} />}
          <span className="status-card-summary-text">{summary.text}</span>
        </span>
      </button>
      <DisclosurePanel open={open} id={panelId}><div className="status-card-inner">{children}</div></DisclosurePanel>
    </section>
  )
}

function Row({ tone, pulse, name, sub, meta, title, onClick }: {
  tone: BadgeVariant; pulse?: boolean; name: ReactNode; sub?: ReactNode; meta?: ReactNode; title?: string; onClick?: () => void
}) {
  const inner = (
    <>
      <StatusDot variant={tone} pulse={pulse} size={7} className="status-row-dot" />
      {sub
        ? <span className="status-row-text"><span className="status-row-name">{name}</span><span className="status-row-sub">{sub}</span></span>
        : <span className="status-row-name">{name}</span>}
      {meta && <span className="status-row-meta">{meta}</span>}
    </>
  )
  return onClick
    ? <button type="button" className="status-row" title={title} onClick={onClick}>{inner}</button>
    : <div className="status-row" title={title}>{inner}</div>
}

const Group = ({ label, children }: { label: string; children: ReactNode }) => (
  <div className="status-group"><div className="status-group-label">{label}</div>{children}</div>
)

const PANEL_ICON = (
  <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.5"><rect x="1.75" y="2.25" width="12.5" height="11.5" rx="2"/><path d="M10 2.5v11"/></svg>
)

/** Shows or hides the panel. Pages put it at their top right; while the
 *  panel shows, its own copy sits at the panel's top instead. */
export function PanelToggle({ inPanel = false, className }: { inPanel?: boolean; className?: string }) {
  const p = usePanel()
  if (!p.offered || (!inPanel && p.visible)) return null
  return (
    <IconButton
      className={`panel-toggle${className ? ` ${className}` : ''}`}
      label={inPanel ? 'Hide the status panel' : 'Show the status panel'}
      pressed={inPanel}
      onClick={() => setPanelOpen(!inPanel)}
      icon={PANEL_ICON}
    />
  )
}

/** The model the chat last chose, from the chat package's own record, for
 *  when the chat has not mounted to say. */
function rememberedModel(): string | null {
  try { const v = JSON.parse(localStorage.getItem('aiChat.model') ?? 'null'); return typeof v === 'string' ? modelLabel({ id: v }) : null } catch { return null }
}

export function StatusPanel({ view, cfg }: { view: string; cfg: AppConfig }) {
  const p = usePanel()
  if (!p.visible) return null
  // The cards wait for the configuration, which says which sections exist.
  return (
    <aside className="status-panel" aria-label="Status">
      <div className="status-panel-head">
        <span className="status-panel-title">Status</span>
        <PanelToggle inPanel />
      </div>
      {cfg.loaded && <PanelCards view={view} cfg={cfg} />}
    </aside>
  )
}

function PanelCards({ view, cfg }: { view: string; cfg: AppConfig }) {
  const shown = (id: string) => !cfg.sections || cfg.sections.includes(id)
  const agentsOn = shown('agents')
  const runsOn = !!cfg.platform && shown('workflows')
  const [data, patch] = useStatusData(agentsOn, runsOn)
  const shared = useShared()
  const [syncing, setSyncing] = useState(false)
  const sync = async () => {
    setSyncing(true)
    await syncIndex()
    try { patch({ idx: await api.indexStatus() }) } catch { /* next poll */ }
    setSyncing(false)
  }

  // Activity: what is running, then the latest runs, then open sessions.
  const tasks = agentsOn ? (data.tasks ?? []).filter(t => LIVE_TASK.has(t.state)) : []
  const fleet = agentsOn ? (data.fleet ?? []).filter(a => a.state !== 'retired') : []
  const me = [cfg.user.username, cfg.user.id]
  const myRuns = runsOn ? (data.runs ?? []).filter(r => !r.owner || me.includes(r.owner)) : []
  const byNewest = (a: RunRow, b: RunRow) => (a.launchedAt < b.launchedAt ? 1 : -1)
  const running = myRuns.filter(r => !runEnded(r)).sort(byNewest)
  const runs = [...running.slice(0, 6), ...myRuns.filter(runEnded).sort(byNewest).slice(0, 3)]
  const sessions = runsOn ? data.sessions ?? [] : []
  const hasActivity = tasks.length + fleet.length + runs.length + sessions.length > 0

  const libs = libraryLines(cfg.libraries ?? [], data.userLibs, shared.stats)
  const model = shared.model ?? rememberedModel()
  const conns = connectionLines({ platform: !!cfg.platform, authEnabled: shared.authEnabled, ai: shared.ai, mcp: data.mcp, libs: cfg.libraries ?? [], userLibs: data.userLibs })
  const idx = data.idx

  return (
    <div className="status-panel-cards">
      {hasActivity && (
        <Card id="activity" title="Activity" summary={activitySummary({ tasks, fleet, runs: myRuns, sessions })}>
          {(tasks.length > 0 || fleet.length > 0) && (
            <Group label="Agents">
              {tasks.map(t => (
                <Row key={t.id} tone={t.state === 'input-required' ? 'warning' : 'info'} pulse={t.state !== 'input-required'}
                  name={t.objective} meta={t.state === 'input-required' ? 'needs you' : t.state === 'submitted' ? 'starting' : 'working'}
                  title={t.objective} onClick={() => go(`#view=agents:${t.id}`)} />
              ))}
              {fleet.map(a => (
                <Row key={a.id} tone={a.state === 'working' ? 'info' : a.state === 'input-required' ? 'warning' : a.state === 'idle' ? 'success' : 'muted'}
                  pulse={a.state === 'working'} name={a.name} meta={a.state === 'input-required' ? 'needs you' : a.state}
                  onClick={() => go('#view=agents:fleet')} />
              ))}
            </Group>
          )}
          {runs.length > 0 && (
            <Group label="Runs">
              {runs.map(r => {
                const { tone, pulse } = runTone(r.state)
                return (
                  <Row key={r.slug} tone={tone} pulse={pulse} name={r.workflow} title={r.slug}
                    meta={runEnded(r) ? `${r.state} ${ago(r.endedAt ?? r.launchedAt)}` : r.state}
                    onClick={() => openRun(r.slug, view)} />
                )
              })}
            </Group>
          )}
          {sessions.length > 0 && (
            <Group label="Sessions">
              {sessions.slice(0, 6).map(s => (
                <Row key={`${s.user}/${s.name}`} tone="success" name={s.name} meta={s.type}
                  title={s.ownHost ? 'Open it in the side view' : 'Open it in a window of its own'} onClick={() => openSession(s, view)} />
              ))}
              {sessions.length > 6 && (
                <button type="button" className="status-more" onClick={() => go('#view=workflows')}>{sessions.length - 6} more on the Workflows page</button>
              )}
            </Group>
          )}
        </Card>
      )}
      {libs.length > 0 && (
        <Card id="libraries" title="Libraries" summary={librariesSummary(libs, idx)}>
          {libs.map(l => (
            <Row key={l.id} tone={l.tone} pulse={l.pulse} name={l.label} sub={l.kind} title={l.note ?? undefined}
              meta={(l.state === 'indexed' || l.state === 'ready') && l.count ? l.count : l.state} />
          ))}
          <div className="status-sync">
            <StatusDot size={7} className="status-row-dot"
              variant={idx?.lastError ? 'warning' : idx?.running || syncing ? 'info' : idx?.lastSweepAt ? 'success' : 'muted'} pulse={!!idx?.running || syncing} />
            <span className="status-row-name" title={idx?.lastError ?? undefined}>
              {idx?.running || syncing ? 'Syncing now' : idx?.lastError ? `Sync failed: ${idx.lastError}` : idx?.lastSweepAt ? `Synced ${ago(idx.lastSweepAt)}` : 'Not synced yet'}
            </span>
            <Button variant="ghost" size="xs" onClick={() => void sync()} disabled={syncing || !!idx?.running}>Sync now</Button>
          </div>
          <button type="button" className="status-link" onClick={() => go('#view=settings:libraries')}>Library settings</button>
        </Card>
      )}
      {(shared.ai || model) && (
        <Card id="model" title="Model" summary={modelSummary(shared.ai, model)}>
          {model && <Row tone={shared.ai?.status === 'ok' ? 'success' : 'muted'} name={model} meta={shared.modelProvider ?? 'selected'} title={model} />}
          {shared.ai && (
            <Row tone={shared.ai.status === 'ok' ? 'success' : shared.ai.status === 'unconfigured' || shared.ai.status === 'off' ? 'muted' : 'warning'}
              name={shared.ai.status === 'ok' ? `${shared.ai.models} models ready` : AI_LABEL[shared.ai.status]}
              sub={shared.ai.status === 'ok' ? undefined : shared.ai.message ?? undefined}
              title={shared.ai.gatewayHost ? `Through ${shared.ai.gatewayHost}` : undefined} />
          )}
          {shared.ai?.lastCallFailure && (
            <Row tone="warning" name={`Last call failed ${ago(shared.ai.lastCallFailure.at)}`} title={shared.ai.lastCallFailure.message} />
          )}
          {shared.authEnabled && <button type="button" className="status-link" onClick={() => go('#view=settings:access')}>Model access</button>}
        </Card>
      )}
      {conns.length > 0 && (
        <Card id="connections" title="Connections" summary={connectionsSummary(conns)}>
          {conns.map(c => <Row key={c.key} tone={c.tone} name={c.name} meta={c.meta} title={c.title} onClick={c.onClick} />)}
        </Card>
      )}
    </div>
  )
}
