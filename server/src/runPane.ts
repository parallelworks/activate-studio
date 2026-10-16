import type { FastifyInstance, FastifyReply, FastifyRequest } from 'fastify'
import { Readable } from 'node:stream'
import { KbError } from './kb.js'
import { execFile } from 'node:child_process'
import { PW_CLI } from './config.js'
import { platformFetch, platformJson } from './platformRecords.js'
import { isWorkspaceDown } from './workspace.js'
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

/**
 * Injected at the top of every relayed page. A same-origin frame's
 * scrollIntoView and focus scroll every container around the frame as
 * well, so a study viewer keeping its newest row in view scrolled the
 * Studio's own panes, including ones with overflow hidden that the user
 * cannot scroll back, and the shell slid up and sideways out of reach.
 * Inside a frame, both now scroll only the page's own containers.
 */
export const FRAME_SCROLL_GUARD = `<script>/* ACTIVATE Studio: keep this page's scrolling inside its frame */(function () {
  if (window.top === window) return;
  var E = Element.prototype, H = HTMLElement.prototype, focus = H.focus;
  function scrollers(el) {
    var out = [], p = el.parentElement;
    while (p) { var s = getComputedStyle(p); if (/(auto|scroll)/.test(s.overflowY + ' ' + s.overflowX)) out.push(p); p = p.parentElement; }
    out.push(document.scrollingElement || document.documentElement);
    return out;
  }
  function delta(a0, a1, b0, b1, mode) {
    if (mode === 'start') return a0 - b0;
    if (mode === 'end') return a1 - b1;
    if (mode === 'center') return (a0 + a1) / 2 - (b0 + b1) / 2;
    if (a0 < b0) return a0 - b0;
    if (a1 > b1) return Math.min(a1 - b1, a0 - b0);
    return 0;
  }
  E.scrollIntoView = function (arg) {
    var o = arg && typeof arg === 'object' ? arg : { block: arg === false ? 'end' : 'start' };
    var list = scrollers(this);
    for (var i = 0; i < list.length; i++) {
      var p = list[i], r = this.getBoundingClientRect();
      var top = p === list[list.length - 1] ? { top: 0, bottom: innerHeight, left: 0, right: innerWidth } : p.getBoundingClientRect();
      p.scrollTop += delta(r.top, r.bottom, top.top, top.bottom, o.block || 'start');
      p.scrollLeft += delta(r.left, r.right, top.left, top.right, o.inline || 'nearest');
    }
  };
  if (E.scrollIntoViewIfNeeded) E.scrollIntoViewIfNeeded = function () { E.scrollIntoView.call(this, { block: 'nearest' }); };
  H.focus = function (o) { return focus.call(this, Object.assign({}, o, { preventScroll: true })); };
})();</script>`

/** A relayed HTML page with the frame scroll guard ahead of its own scripts. */
export function withScrollGuard(html: string): string {
  const head = /<head[^>]*>/i.exec(html)
  if (head) return html.slice(0, head.index + head[0].length) + FRAME_SCROLL_GUARD + html.slice(head.index + head[0].length)
  const doctype = /^\s*<!doctype[^>]*>/i.exec(html)
  return doctype ? doctype[0] + FRAME_SCROLL_GUARD + html.slice(doctype[0].length) : FRAME_SCROLL_GUARD + html
}

type StartWorkspace = (key: string) => Promise<unknown>
let startWorkspace: StartWorkspace = key => new Promise(resolve =>
  execFile(PW_CLI, ['workspace', 'start'], { timeout: 120_000, env: { ...process.env, PW_API_KEY: key } }, () => resolve(null)))
/** Tests replace the workspace start. */
export function setRunPaneWorkspaceStart(fn: StartWorkspace): void { startWorkspace = fn }
// One start request per credential while a start is likely under way; the
// step dialog polls every two seconds and must not ask on every poll.
const startedAt = new Map<string, number>()
const START_EVERY_MS = 3 * 60_000

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

  // A file in a run's directory (a step's log, its script, its summary), for
  // the step dialogs of the run view. The platform returns it as a JSON
  // string; a path outside the run directory is refused here first. The
  // platform reads run files from the user's workspace, which scales down
  // when idle: then one start is requested, and the dialog's next polls
  // find the file once the workspace is up.
  app.get('/api/runs/file', async req => {
    const { slug = '', path = '' } = req.query as { slug?: string; path?: string }
    if (!SLUG.test(slug)) throw new KbError(400, 'slug required')
    if (!path || path.length > 512 || path.startsWith('/') || path.split('/').includes('..')) throw new KbError(400, 'a path inside the run directory is required')
    const key = keyOf(req)
    const res = await platformFetch(`/api/workflow-runs/${encodeURIComponent(slug)}/files?file=${encodeURIComponent(path)}`, key)
    if (!res) throw new KbError(502, 'The platform did not answer.')
    if (res.ok && typeof res.body === 'string') return { text: res.body }
    const message = String(res.body?.message ?? res.body?.error ?? '')
    if (isWorkspaceDown(message)) {
      if (Date.now() - (startedAt.get(key) ?? 0) > START_EVERY_MS) {
        startedAt.set(key, Date.now())
        void startWorkspace(key)
      }
      throw new KbError(503, 'Your platform workspace was not running, so a start was requested; run files appear once it is up, usually within a minute or two.')
    }
    if (res.status === 404) throw new KbError(404, `No file ${path} in run ${slug}.`)
    throw new KbError(502, message ? `The platform could not read ${path}: ${message}` : `The platform could not read ${path} (HTTP ${res.status}).`)
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
    if (req.method === 'HEAD') return reply.send('')
    if (/^text\/html\b/i.test(res.headers.get('content-type') ?? '')) return reply.send(withScrollGuard(Buffer.from(await res.arrayBuffer()).toString('utf8')))
    // Everything else streams through as it arrives: a study viewer's gallery
    // asks for dozens of full-size images through the session tunnel, and
    // holding each one until its last byte came in made the gallery slow.
    if (!res.body) return reply.send('')
    return reply.send(Readable.fromWeb(res.body as unknown as import('node:stream/web').ReadableStream))
  }
  // Relative addresses resolve against the page's own path, so the page is
  // served at the folder address with its trailing slash.
  app.get('/api/session-proxy/:host', async (req, reply) => reply.redirect(`/api/session-proxy/${(req.params as { host: string }).host}/`))
  app.route({ method: ['GET', 'HEAD'], url: '/api/session-proxy/:host/*', handler: relay })
}
