// Working offline. Every call to a non-loopback host fails the way Node's
// fetch fails with no network (ENOTFOUND, ECONNRESET, a timeout), while a
// model server on this computer keeps answering. The Studio has to keep
// working from local files and the local model, say plainly what it cannot
// reach, and never make a page wait out a long timeout.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'offline-'))
process.env.KB_ROOT = path.join(tmp, 'kb')
process.env.INDEX_BASE = path.join(tmp, 'ix')
fs.mkdirSync(process.env.KB_ROOT)
fs.mkdirSync(process.env.INDEX_BASE)
process.env.PW_GATEWAY_URL = 'https://gw.example.com/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'
process.env.PW_CREDENTIALS_DIR = path.join(tmp, 'no-cli')
process.env.STUDIO_WORKFLOWS = 'batch-job'
delete process.env.STUDIO_APP_BUNDLE
delete process.env.PW_PLATFORM_HOST

// What Node's fetch throws for each way the network can be missing.
const failures = {
  ENOTFOUND: () => Object.assign(new TypeError('fetch failed'), { cause: Object.assign(new Error('getaddrinfo ENOTFOUND gw.example.com'), { code: 'ENOTFOUND' }) }),
  ECONNRESET: () => Object.assign(new TypeError('fetch failed'), { cause: Object.assign(new Error('read ECONNRESET'), { code: 'ECONNRESET' }) }),
  timeout: () => new DOMException('The operation was aborted due to timeout', 'TimeoutError'),
}
let failWith = 'ENOTFOUND'
let remoteCalls = 0
const guarded = globalThis.fetch // setup.mjs: loopback passes, anything else is refused
globalThis.fetch = async (input, init) => {
  const href = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  const host = new URL(href).hostname
  if (host === '127.0.0.1' || host === 'localhost') return guarded(input, init)
  remoteCalls++
  throw failures[failWith]()
}

// A model server on this computer, OpenAI-compatible, as Ollama or vLLM serve.
const local = http.createServer((req, res) => {
  if (req.url === '/v1/models') { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ object: 'list', data: [{ id: 'llama3.2', object: 'model', owned_by: 'ollama' }] })); return }
  if (req.url === '/v1/chat/completions') {
    res.setHeader('Content-Type', 'text/event-stream')
    res.end('data: {"model":"llama3.2","choices":[{"delta":{"content":"hello from here"}}]}\n\ndata: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
    return
  }
  res.statusCode = 404; res.end('{}')
})
await new Promise(r => local.listen(0, '127.0.0.1', r))
const LOCAL = `http://127.0.0.1:${local.address().port}/v1`

const { networkFailure, unreachableMessage, cliNetworkFailure } = await import('../dist/net.js')
const { aiHealth, listModels, streamTurn, GatewayUnreachable } = await import('../dist/chat/gateway.js')
const { setUserKey, clearUserKey, SINGLE_OWNER } = await import('../dist/credentials.js')
const { updateSettings } = await import('../dist/settings.js')
const { remoteStatus, invalidateRemote } = await import('../dist/mcpClient.js')
const { workflowsTabRoutes, setWorkflowsCli } = await import('../dist/workflowsTab.js')
const { chatRoutes } = await import('../dist/chat/routes.js')
const { updateRoutes, resetUpdatesForTests } = await import('../dist/updates.js')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { default: Fastify } = await import('fastify')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(chatRoutes)
await app.register(workflowsTabRoutes)
await app.register(updateRoutes)
await app.ready()
const timed = async fn => { const t0 = Date.now(); const v = await fn(); return [v, Date.now() - t0] }

test.after(() => local.close())

test('network failures are told from other failures, in every shape fetch and the CLI report them', () => {
  assert.deepEqual(networkFailure(failures.ENOTFOUND()), { code: 'ENOTFOUND', noDns: true })
  assert.deepEqual(networkFailure(failures.ECONNRESET()), { code: 'ECONNRESET', noDns: false })
  assert.deepEqual(networkFailure(failures.timeout()), { code: 'timeout', noDns: false })
  assert.equal(networkFailure(new Error('read ECONNRESET'))?.code, 'ECONNRESET', 'a code only in the text')
  assert.equal(networkFailure(Object.assign(new Error('x'), { code: 'UND_ERR_CONNECT_TIMEOUT' }))?.code, 'UND_ERR_CONNECT_TIMEOUT')
  assert.equal(networkFailure(new DOMException('stopped', 'AbortError')), null, 'a stop the person asked for is not the network')
  assert.equal(networkFailure(new Error('HTTP 500')), null)
  assert.equal(cliNetworkFailure('Get "https://h/api": dial tcp: lookup h: no such host'), true)
  assert.equal(cliNetworkFailure('workflow not found'), false)
  assert.equal(unreachableMessage('127.0.0.1:11434', { code: 'ECONNREFUSED', noDns: false }),
    'Nothing answers at 127.0.0.1:11434 (ECONNREFUSED). Start the model server on this computer, or check its address.')
  assert.equal(unreachableMessage('gw.example.com', { code: 'timeout', noDns: false }),
    'Could not reach gw.example.com (no answer in time). This computer may be offline, or the server is down. Local libraries and a local model keep working.')
})

for (const kind of Object.keys(failures)) {
  test(`model health reports offline, not an error, for ${kind}`, async () => {
    failWith = kind
    const [h, ms] = await timed(() => aiHealth())
    assert.equal(h.status, 'offline')
    assert.equal(h.ok, false)
    assert.match(h.message, /^Could not reach gw\.example\.com/)
    assert.ok(ms < 1000, `answered in ${ms} ms`)
    await assert.rejects(listModels(), e => e instanceof GatewayUnreachable && /This computer may be offline/.test(e.message))
  })
}

test('the chat model list answers with a sentence, not a server error, when the gateway is out of reach', async () => {
  failWith = 'ECONNRESET'
  const [r, ms] = await timed(() => app.inject({ method: 'GET', url: '/api/chat/models' }))
  assert.equal(r.statusCode, 200)
  const body = r.json()
  assert.deepEqual(body.models, [])
  assert.equal(body.offline, true)
  assert.match(body.error, /Could not reach gw\.example\.com \(ECONNRESET\)/)
  assert.ok(ms < 2000, `answered in ${ms} ms`)
})

test('a model server on this computer keeps working offline: listing, health, and a reply', async () => {
  failWith = 'ENOTFOUND'
  setUserKey(SINGLE_OWNER, 'none', true, LOCAL)
  try {
    const list = (await app.inject({ method: 'GET', url: '/api/chat/models?refresh=1' })).json()
    assert.deepEqual(list.models.map(m => m.id), ['llama3.2'])
    assert.equal(list.error, undefined)
    const h = (await app.inject({ method: 'GET', url: '/api/ai/health' })).json()
    assert.equal(h.status, 'ok')
    assert.equal(h.platformAccess, false, 'an endpoint gives models only, and nobody waits on the platform to learn it')
    let said = ''
    const turn = await streamTurn({ model: 'llama3.2', messages: [{ role: 'user', content: 'hi' }] }, null, { onContent: t => { said += t } }, undefined, 'none', LOCAL)
    assert.equal(said, 'hello from here')
    assert.equal(turn.finishReason, 'stop')
  } finally {
    clearUserKey(SINGLE_OWNER)
  }
})

test('a model server on this computer that is not running is said to be not running, not offline', async () => {
  // A port that was free a moment ago, so nothing listens on it.
  const closed = await new Promise(r => { const sv = http.createServer().listen(0, '127.0.0.1', () => { const p = sv.address().port; sv.close(() => r(p)) }) })
  const h = await aiHealth('none', `http://127.0.0.1:${closed}/v1`)
  assert.equal(h.status, 'unreachable')
  assert.match(h.message, new RegExp(`^Nothing answers at 127\\.0\\.0\\.1:${closed} \\(ECONNREFUSED\\)\\. Start the model server`))
})

test('a reply through an unreachable gateway fails with the offline sentence', async () => {
  failWith = 'ENOTFOUND'
  await assert.rejects(
    streamTurn({ model: 'm', messages: [{ role: 'user', content: 'hi' }] }, null, { onContent: () => {} }),
    e => e instanceof GatewayUnreachable && /^Could not reach gw\.example\.com \(ENOTFOUND\)/.test(e.message),
  )
})

test('the update check offline is a state, checked again later, and clears once online', async () => {
  resetUpdatesForTests()
  failWith = 'ECONNRESET'
  const r = (await app.inject({ method: 'POST', url: '/api/updates/check' })).json()
  assert.equal(r.offline, true)
  assert.equal(r.state, 'idle', 'not an error')
  assert.equal(r.error, null)
  failWith = 'timeout'
  const again = (await app.inject({ method: 'POST', url: '/api/updates/check' })).json()
  assert.deepEqual([again.offline, again.state, again.error], [true, 'idle', null])
  // Online again: GitHub answers.
  const prev = globalThis.fetch
  globalThis.fetch = async () => new Response('[]', { status: 200, headers: { 'content-type': 'application/json' } })
  try {
    const online = (await app.inject({ method: 'POST', url: '/api/updates/check' })).json()
    assert.equal(online.offline, false)
    assert.ok(online.checkedAt)
  } finally { globalThis.fetch = prev }
})

test('an attached MCP server out of reach costs no wait and reads plainly', async () => {
  failWith = 'ENOTFOUND'
  await updateSettings({ mcpServers: [{ name: 'tickets', url: 'https://mcp.example.com/mcp' }] })
  invalidateRemote()
  const [st, ms] = await timed(() => remoteStatus())
  assert.equal(st[0].tools, 0)
  assert.equal(st[0].error, 'Could not reach mcp.example.com (ENOTFOUND).')
  assert.ok(ms < 1000)
  await updateSettings({ mcpServers: [] })
})

test('the Workflows page fails at once, in plain words, when the platform is out of reach', async () => {
  let cliCalls = 0
  setWorkflowsCli(async () => { cliCalls++; return '[]' })
  failWith = 'ENOTFOUND'
  const [r, ms] = await timed(() => app.inject({ method: 'GET', url: '/api/workflows/collection' }))
  assert.equal(r.statusCode, 503)
  assert.match(r.json().error, /^Could not reach gw\.example\.com \(ENOTFOUND\)\. This computer may be offline/)
  assert.equal(cliCalls, 0, 'the CLI is not run to wait out its own timeout')
  assert.ok(ms < 1000)
  const before = remoteCalls
  await app.inject({ method: 'GET', url: '/api/workflows/collection' })
  assert.equal(remoteCalls, before, 'the verdict is kept for a few seconds rather than asked again')
})
