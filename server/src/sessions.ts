import type { FastifyInstance } from 'fastify'
import { GATEWAY_BASE } from './config.js'
import { gatewayKey } from './chat/gateway.js'
import { resolveUserCred } from './credentials.js'
import { KbError } from './kb.js'

/**
 * Platform sessions: the interactive apps workflows open (a design
 * explorer, a notebook, a remote desktop) and the endpoints users run.
 * They are read from the platform API with the viewer's own key, not
 * through the CLI: `pw sessions ls -o json` is built from the CLI's own
 * types, and older releases drop fields the platform sends, as v7.105's
 * `workflows ls` drops imageFallbackUrl. The field that matters here is
 * workflowRun, which ties a session to the run that opened it.
 *
 * The Studio opens a session in a window of its own, not in a frame:
 * each session host signs the browser in through the platform's login
 * page, which refuses to load in a frame.
 */

export interface SessionRun { slug: string; workflow: string; number: number | null; status: string | null }
export interface SessionRow {
  name: string
  user: string
  type: string
  status: string
  healthy: boolean | null
  /** The address a browser opens, absolute. */
  url: string | null
  /** Served from its own host under the sessions domain, which is what a frame needs. */
  ownHost: boolean
  /** The session's page on the platform, which shows it embedded. */
  platformUrl: string | null
  /** The workflow run that opened it, when one did. */
  run: SessionRun | null
  resource: string | null
  createdAt: string | null
}

/** The platform's origin, from the gateway address the Studio already uses. */
export function platformOrigin(): string | null {
  try { return new URL(GATEWAY_BASE).origin } catch { return null }
}

/** One platform session record as the Studio shows it, or null when it has no name. */
export function sessionRow(raw: any, platform: string): SessionRow | null {
  const name = typeof raw?.name === 'string' ? raw.name : ''
  if (!name) return null
  const href = typeof raw.externalHref === 'string' ? raw.externalHref : ''
  let url: string | null = null
  if (/^https:\/\//.test(href)) url = href
  else if (href.startsWith('/')) url = platform + href
  let ownHost = false
  try { ownHost = !!url && !!raw.domainName && new URL(url).hostname === String(raw.domainName) } catch { /* not a URL */ }
  const wr = raw.workflowRun && typeof raw.workflowRun === 'object' ? raw.workflowRun : null
  return {
    name,
    user: String(raw.user ?? ''),
    type: String(raw.type ?? ''),
    status: String(raw.status ?? ''),
    healthy: typeof raw.healthy === 'boolean' ? raw.healthy : null,
    url,
    ownHost,
    platformUrl: typeof raw.internalHref === 'string' && /^\/sessions\/[^?#]+$/.test(raw.internalHref) ? platform + raw.internalHref : null,
    run: wr?.slug ? {
      slug: String(wr.slug),
      workflow: String(wr.displayName || wr.name || ''),
      number: typeof wr.number === 'number' ? wr.number : null,
      status: wr.status ? String(wr.status) : null,
    } : null,
    resource: raw.targetName ? String(raw.targetName) : null,
    createdAt: raw.createdAt ? String(raw.createdAt) : null,
  }
}

/** Whether a session belongs to a run: the run the platform recorded for
 *  it, or, for a session with none recorded, a name that is the run's id or
 *  ends with it. A job that registers its own endpoint (a study viewer, a
 *  model server) leaves the record empty on some platforms, and those
 *  workflows name the endpoint after the run, <prefix>-<run id>. */
export function sessionOfRun(s: SessionRow, slug: string): boolean {
  if (!slug) return false
  if (s.run) return s.run.slug === slug
  return s.name === slug || s.name.endsWith(`-${slug}`)
}

/** The platform key a user's requests run with: their own, else the deployment's. */
export function sessionKeyFor(userId: string | null | undefined): string | null {
  const cred = resolveUserCred(userId)
  if (cred && !cred.baseUrl && cred.key) return cred.key
  return gatewayKey() || null
}

const cache = new Map<string, { at: number; rows: SessionRow[] }>()
const TTL_MS = 10_000

/** Every session the key's account can see: its own and those shared with it. */
export async function listSessions(key: string): Promise<SessionRow[]> {
  const k = key.slice(-12)
  const hit = cache.get(k)
  if (hit && Date.now() - hit.at < TTL_MS) return hit.rows
  const platform = platformOrigin()
  if (!platform) throw new KbError(502, 'No platform address is configured.')
  const res = await fetch(`${platform}/api/sessions`, { headers: { Authorization: `Bearer ${key}` }, signal: AbortSignal.timeout(20_000) })
  if (!res.ok) throw new KbError(res.status === 401 || res.status === 403 ? 409 : 502, `The platform refused the session list (${res.status}).`)
  const body = await res.json() as unknown
  const raw = Array.isArray(body) ? body : ((body as { sessions?: unknown[] })?.sessions ?? [])
  const rows = raw.map(r => sessionRow(r, platform)).filter((r): r is SessionRow => !!r)
  cache.set(k, { at: Date.now(), rows })
  return rows
}

/** The markdown that shows a session in a chat reply: a card with its
 *  state and buttons that open it in a window of its own or a new tab. */
export function sessionEmbed(s: SessionRow): string {
  const q = new URLSearchParams({ embed: 'session', user: s.user, name: s.name })
  return `![${s.name}](/?${q})`
}

export async function sessionRoutes(app: FastifyInstance): Promise<void> {
  const keyOf = (req: { user?: { id?: string } }) => {
    const key = sessionKeyFor(req.user?.id)
    if (!key) throw new KbError(409, 'No platform credential: add your ACTIVATE API key under Settings, Model access.')
    return key
  }

  // The viewer's sessions, newest first; ?run=<slug> keeps those one run opened.
  app.get('/api/sessions', async req => {
    const { run } = req.query as { run?: string }
    const rows = (await listSessions(keyOf(req)))
      .filter(s => !run || sessionOfRun(s, run))
      .sort((a, b) => String(b.createdAt).localeCompare(String(a.createdAt)))
    return { sessions: rows }
  })

  // One session by name, by the address it is served at (?host= or ?url=),
  // or by the run that opened it (?run=), which are what a model that saw
  // the address or the run tends to pass. A run's running session wins.
  app.get('/api/sessions/item', async req => {
    const { user = '', name = '', host = '', url = '', run = '' } = req.query as { user?: string; name?: string; host?: string; url?: string; run?: string }
    let want = String(host).toLowerCase()
    if (!want && url) { try { want = new URL(String(url)).host.toLowerCase() } catch { /* not an address */ } }
    const hostOf = (s: SessionRow) => { try { return s.url ? new URL(s.url).host.toLowerCase() : '' } catch { return '' } }
    const rows = await listSessions(keyOf(req))
    const ofRun = run ? rows.filter(s => sessionOfRun(s, String(run))).sort((a, b) => Number(b.status === 'running') - Number(a.status === 'running')) : []
    const row = ofRun[0] ?? rows.find(s =>
      (name && s.name === name && (!user || s.user === user)) || (want && hostOf(s) === want))
    if (!row) throw new KbError(404, run && !name && !want ? `Run ${run} has no session yet.` : `No session ${name || want} is visible to you.`)
    return row
  })
}

/** Tests reset the cache between cases. */
export function resetSessionsForTests(): void { cache.clear() }
