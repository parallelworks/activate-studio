import type { FastifyInstance, FastifyReply, FastifyRequest } from 'fastify'
import { KbError } from './kb.js'
import { platformJson } from './platformRecords.js'
import { listSessions, sessionKeyFor, type SessionRow } from './sessions.js'

/**
 * What the chat's run pane reads: a workflow run as the platform's run page
 * draws it, and the web session the run opened, relayed onto the Studio's
 * own origin so it can sit in a frame beside the conversation.
 *
 * A session's own host refuses to be framed by another site and signs its
 * visitors in with a cookie the Studio's page cannot carry there, but it
 * accepts the platform key as a bearer token from a server. So the Studio
 * fetches the session's pages with the viewer's key and serves them under
 * /api/session-proxy/<host>/, where a page that loads its data by relative
 * address (the study viewers do) works unchanged. Only hosts of sessions
 * the viewer's own key can see are relayed, and the address fetched comes
 * from the platform's session record, never from the request.
 */

const SLUG = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,120}$/
const TERMINAL = new Set(['completed', 'error', 'failed', 'cancelled', 'canceled'])

/** The run fields the pane draws; the resolved document is left out. */
export function paneRun(raw: any): Record<string, unknown> | null {
  if (!raw || typeof raw !== 'object' || !raw.slug) return null
  const keep = ['slug', 'number', 'status', 'statusReason', 'createdAt', 'completedAt', 'workflowName', 'workflowSlug', 'inputs', 'executedJobs', 'links', 'user']
  const out: Record<string, unknown> = {}
  for (const k of keep) if (raw[k] !== undefined) out[k] = raw[k]
  out.terminal = TERMINAL.has(String(raw.status ?? '').toLowerCase())
  return out
}

/** The origin to relay for a requested host: that of a session the viewer
 *  can see, served on its own host. Null for anything else. */
export function proxyOrigin(host: string, rows: SessionRow[]): string | null {
  const want = String(host ?? '').toLowerCase()
  if (!want || !/^[a-z0-9.-]+(:\d{1,5})?$/.test(want)) return null
  for (const s of rows) {
    if (!s.ownHost || !s.url) continue
    try {
      const u = new URL(s.url)
      if (u.host.toLowerCase() === want) return u.origin
    } catch { /* not a URL */ }
  }
  return null
}

// Response headers passed back. Framing rules, cookies, and transfer
// framing stay with the session's host; the Studio's origin sets its own.
const PASS = ['content-type', 'cache-control', 'etag', 'last-modified', 'content-disposition', 'content-language', 'expires']
export function passHeaders(headers: Headers): Record<string, string> {
  const out: Record<string, string> = {}
  for (const name of PASS) {
    const v = headers.get(name)
    if (v) out[name] = v
  }
  return out
}

export async function runPaneRoutes(app: FastifyInstance): Promise<void> {
  const keyOf = (req: FastifyRequest) => {
    const key = sessionKeyFor(req.user?.id)
    if (!key) throw new KbError(409, 'No platform credential: add your ACTIVATE API key under Settings, Model access.')
    return key
  }

  app.get('/api/runs/item', async req => {
    const slug = String((req.query as { slug?: string }).slug ?? '')
    if (!SLUG.test(slug)) throw new KbError(400, 'slug required')
    const run = paneRun(await platformJson(`/api/workflow-runs/${encodeURIComponent(slug)}`, keyOf(req)))
    if (!run) throw new KbError(404, `No run ${slug} is visible to you.`)
    return run
  })

  const relay = async (req: FastifyRequest, reply: FastifyReply) => {
    const { host } = req.params as { host: string }
    const key = keyOf(req)
    const origin = proxyOrigin(host, await listSessions(key))
    if (!origin) throw new KbError(404, 'That session is not one of yours, or it is not served on its own host.')
    const prefix = `/api/session-proxy/${host}`
    const rest = req.url.slice(req.url.indexOf(prefix) + prefix.length) || '/'
    const res = await fetch(origin + rest, {
      method: req.method,
      headers: {
        Authorization: `Bearer ${key}`,
        ...(req.headers.accept ? { Accept: String(req.headers.accept) } : {}),
        ...(req.headers.range ? { Range: String(req.headers.range) } : {}),
      },
      redirect: 'manual',
      signal: AbortSignal.timeout(60_000),
    }).catch((e: Error) => { throw new KbError(502, `The session did not answer: ${e.message}`) })
    // A redirect from the session's host is its sign-in page: the key was
    // not accepted, and following it would land a sign-in form in the pane.
    if (res.status >= 300 && res.status < 400) throw new KbError(502, 'The session asked for a sign-in the Studio cannot complete; open it in a new tab.')
    reply.code(res.status).headers(passHeaders(res.headers))
    return reply.send(req.method === 'HEAD' ? '' : Buffer.from(await res.arrayBuffer()))
  }
  // Relative addresses resolve against the page's own path, so the page is
  // served at the folder address with its trailing slash.
  app.get('/api/session-proxy/:host', async (req, reply) => reply.redirect(`/api/session-proxy/${(req.params as { host: string }).host}/`))
  app.route({ method: ['GET', 'HEAD'], url: '/api/session-proxy/:host/*', handler: relay })
}
