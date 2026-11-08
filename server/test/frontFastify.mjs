// Fastify for the Go front run (test/front.mjs): the same Fastify, whose
// app.inject sends the request over HTTP through the Go server and returns
// what light-my-request would (statusCode, headers, body, payload,
// rawPayload, json()).
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawn } from 'node:child_process'
import realFastify from 'fastify'

const BIN = process.env.STUDIO_TEST_FRONT
// One bridge secret for the Go server and this process's Node app
// (server-go/internal/bridge), as a Go server that spawns Node hands it on.
process.env.STUDIO_BRIDGE_SECRET ||= `test-${process.pid}-${Math.random().toString(36).slice(2)}`
// STUDIO_TEST_FRONT_STRICT=1: a request for a route the Go server serves
// that reaches Node anyway fails, unless the Go server sent it itself for
// a value Node still owns (X-Studio-Internal). The routes come from
// `studio-server routes`, Go patterns written as Fastify's.
const STRICT = process.env.STUDIO_TEST_FRONT_STRICT === '1'
const goRoutes = STRICT
  ? new Set(JSON.parse(execFileSync(BIN, ['routes'], { encoding: 'utf8' })).ported
    .map(r => r.replace(/\{([A-Za-z_]+)\.\.\.\}/g, '*').replace(/\{([A-Za-z_]+)\}/g, ':$1')))
  : null
// Test files that change the environment, the restart hook or the port
// between requests for routes the Go server serves. The Go server reads its
// environment once at start, so these files keep plain inject against Node,
// and their cases run against the Go routes in the Go test named.
const NODE_DIRECT = new Map([
  ['setup.test.mjs', 'server-go/internal/core (TestSetupStatusAndBrowse, TestPrimaryFolderAndRestart, TestPort, TestPersonalDefaultsAndComplete)'],
  ['setupSignIn.test.mjs', 'server-go/internal/core (TestSignInRefusesSetup)'],
  // The chat tests build their own app (no chat bridge) and script the
  // gateway through globalThis.fetch or inject a user; the Go server calls
  // the gateway itself. Their cases run against Go in internal/chat's golden
  // tests (TestGoldenStreamMatchesNode drives these routes of Node and of Go
  // against one fake gateway) and the unit tests named.
  ['aiFeatures.test.mjs', 'server-go/internal/chat (TestAIOffAnswersWithoutAModel)'],
  ['bulkDelete.test.mjs', 'server-go/internal/conversations (TestBulkDeleteOwnAndUnownedOnly)'],
  ['endpointOptions.test.mjs', 'server-go/internal/modelkeys, internal/llm; internal/chat (TestModelKeyWithSignInOff)'],
  ['gufiCommands.test.mjs', 'server-go/internal/chat (TestGoldenStreamMatchesNode)'],
  ['modelsListingCache.test.mjs', 'server-go/internal/chat (TestModelListingIsCached)'],
  ['offline.test.mjs', 'server-go/internal/chat (TestNoCredentialIsASentence), internal/llm'],
  ['standaloneChat.test.mjs', 'server-go/internal/chat (TestGoldenStreamMatchesNode, TestCitationsMatchNode)'],
  ['toolApproval.test.mjs', 'server-go/internal/chat (TestHeldCallsAskThenRunOnce, TestToolCatalogMatchesNode)'],
  ['unansweredTurn.test.mjs', 'server-go/internal/conversations (TestUnansweredWhileAReplyStreams)'],
])
const direct = NODE_DIRECT.has(path.basename(process.argv[1] ?? ''))
// The ports the Go servers take; another test process may hold one.
const PORTS = (process.env.STUDIO_TEST_FRONT_PORTS ?? '5870-5899').split('-').map(Number)
const fronts = new Set()
let sent = 0

process.on('exit', () => {
  for (const f of fronts) f.child.kill('SIGKILL')
  // STUDIO_TEST_FRONT_COUNT names a file that gets a line per test file:
  // how many requests went through the Go server.
  const count = process.env.STUDIO_TEST_FRONT_COUNT
  if (count && sent) fs.appendFileSync(count, `${path.basename(process.argv[1] ?? '')} ${sent}\n`)
})

function request(port, opts) {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, agent: false, ...opts }, res => {
      const chunks = []
      res.on('data', c => chunks.push(c))
      res.on('end', () => resolve({ res, body: Buffer.concat(chunks) }))
      res.on('error', reject)
    })
    req.on('error', reject)
    req.end(opts.payload)
  })
}

const sleep = ms => new Promise(r => setTimeout(r, ms))

/** The Go server in front of a Node app listening on nodePort. */
async function startFront(nodePort) {
  const env = { ...process.env }
  for (const k of Object.keys(env)) if (k.startsWith('AUTH_') || k.startsWith('OIDC_') || k === 'STUDIO_NODE_SPAWN') delete env[k]
  const webDist = fs.mkdtempSync(path.join(os.tmpdir(), 'front-web-'))
  Object.assign(env, {
    STUDIO_NODE_URL: `http://127.0.0.1:${nodePort}`, STUDIO_FRONT_AUTH: '0', HOST: '127.0.0.1',
    // No web app: every page request reaches Node, as inject's do.
    STUDIO_WEB_DIST: path.join(webDist, 'none'), STUDIO_LOG_FORMAT: 'text',
  })
  const deadline = Date.now() + 120_000
  for (let attempt = 0; Date.now() < deadline; attempt++) {
    const port = PORTS[0] + Math.floor(Math.random() * (PORTS[1] - PORTS[0] + 1))
    const child = spawn(BIN, [], { env: { ...env, PORT: String(port) }, stdio: ['ignore', 'ignore', 'pipe'] })
    let log = ''
    // Its own "listening" line, not an answer on the port: the port may be
    // another test process's Go server, and then this one exits.
    const up = await new Promise(resolve => {
      const timer = setTimeout(() => resolve(false), 20_000)
      child.stderr.on('data', d => {
        if (log.length < 8000) log += d
        if (/msg=listening/.test(log)) { clearTimeout(timer); resolve(true) }
      })
      child.on('exit', () => { clearTimeout(timer); resolve(false) })
    })
    if (up) {
      // Neither keeps a test file's process alive; it is killed on exit.
      child.unref()
      child.stderr.unref?.()
      const front = { child, port, webDist }
      fronts.add(front)
      return front
    }
    const exited = child.exitCode !== null || child.signalCode !== null
    if (!exited) child.kill('SIGKILL')
    if (!/address already in use/.test(log) && attempt > 3) throw new Error(`the Go server did not start: ${log}`)
    await sleep(50 + Math.random() * 200)
  }
  throw new Error(`no free port for the Go server in ${PORTS[0]}-${PORTS[1]}`)
}

function stopFront(front) {
  if (!front) return
  fronts.delete(front)
  front.child.kill('SIGTERM')
  fs.rmSync(front.webDist, { recursive: true, force: true })
}

function lowerHeaders(h = {}) {
  const out = {}
  for (const [k, v] of Object.entries(h)) if (v !== undefined) out[k.toLowerCase()] = Array.isArray(v) ? v : String(v)
  return out
}

/** light-my-request's options as an HTTP request. */
function asRequest(opts) {
  if (typeof opts === 'string') opts = { url: opts }
  const method = (opts.method ?? 'GET').toUpperCase()
  let url = opts.url ?? opts.path ?? '/'
  if (typeof url === 'object') url = (url.pathname ?? '/') + (url.query ? `?${new URLSearchParams(url.query)}` : '')
  if (opts.query) {
    const q = typeof opts.query === 'string' ? opts.query : new URLSearchParams(opts.query).toString()
    if (q) url += (url.includes('?') ? '&' : '?') + q
  }
  const headers = { host: 'localhost:80', 'user-agent': 'lightMyRequest', ...lowerHeaders(opts.headers) }
  if (opts.cookies) {
    const c = Object.entries(opts.cookies).map(([k, v]) => `${k}=${v}`).join('; ')
    headers.cookie = headers.cookie ? `${headers.cookie}; ${c}` : c
  }
  let payload = opts.payload ?? opts.body
  if (payload !== undefined && payload !== null && typeof payload === 'object' && !Buffer.isBuffer(payload)) {
    payload = JSON.stringify(payload)
    headers['content-type'] ??= 'application/json'
  }
  if (payload !== undefined && payload !== null) {
    payload = Buffer.isBuffer(payload) ? payload : Buffer.from(String(payload))
    headers['content-length'] = String(payload.length)
  } else payload = undefined
  return { method, path: url, headers, payload }
}

function parseCookies(setCookie) {
  return [].concat(setCookie ?? []).map(line => {
    const [pair, ...attrs] = line.split(';')
    const i = pair.indexOf('=')
    const c = { name: pair.slice(0, i).trim(), value: pair.slice(i + 1).trim() }
    for (const a of attrs) {
      const [k, v] = a.split('=')
      const key = k.trim().toLowerCase()
      const name = { httponly: 'httpOnly', samesite: 'sameSite', 'max-age': 'maxAge' }[key] ?? key
      c[name] = v === undefined ? true : v.trim()
    }
    return c
  })
}

function install(app) {
  if (direct) return app
  if (STRICT) {
    app.addHook('onRequest', async (req, reply) => {
      const key = `${req.method === 'HEAD' ? 'GET' : req.method} ${req.routeOptions?.url ?? ''}`
      if (goRoutes.has(key) && !req.headers['x-studio-internal']) {
        reply.code(599).send({ error: `strict front: ${key} is served by Go but reached Node` })
      }
    })
  }
  let front = null
  let starting = null
  const ensure = async () => {
    if (front) return front
    starting ??= (async () => {
      if (!app.server.listening) {
        await app.listen({ port: 0, host: '127.0.0.1' })
        // inject needs no listener, so a test may never close its app: the
        // listener and the Go server's kept-alive connections to it do not
        // keep the process alive (a request waiting on its answer does).
        app.server.unref()
        app.server.on('connection', socket => socket.unref())
      }
      front = await startFront(app.server.address().port)
      return front
    })()
    return starting
  }
  const inject = async opts => {
    const f = await ensure()
    sent++
    const { res, body } = await request(f.port, asRequest(opts))
    const payload = body.toString()
    return {
      statusCode: res.statusCode, statusMessage: res.statusMessage, headers: res.headers,
      rawPayload: body, payload, body: payload,
      json: () => JSON.parse(payload), cookies: parseCookies(res.headers['set-cookie']),
    }
  }
  app.inject = (opts, cb) => {
    // The chained form (app.inject().get(...)) is light-my-request's own.
    if (opts === undefined) throw new Error('test/front.mjs: the chained inject form is not supported')
    const p = inject(opts)
    if (typeof cb === 'function') { p.then(r => cb(null, r), cb); return undefined }
    return p
  }
  app.addHook('onClose', async () => { stopFront(front); front = null })
  return app
}

export default function Fastify(opts) { return install(realFastify(opts)) }
export { Fastify as fastify }
