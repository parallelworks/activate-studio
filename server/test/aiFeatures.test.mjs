// AI features on a single-user install. With sign-in off the one user
// keeps a model key for an OpenAI-compatible endpoint: tested against it,
// stored on this computer, and used by the rest of the Studio. With
// aiEnabled off nothing is sent to a model: the chat answers a plain
// message, the model routes say so, and search and the rest keep working.
// The endpoint is a stand-in on the loopback interface.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'

const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'ai-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'ai-kb-'))
process.env.INDEX_BASE = IX
// No deployment credential, and no pw CLI sign-in read from this host.
process.env.HOME = fs.mkdtempSync(path.join(os.tmpdir(), 'ai-home-'))
process.env.PW_GATEWAY_URL = 'https://gw.test/api/openai/v1'
for (const k of ['AUTH_JWKS_URL', 'PW_API_KEY', 'OPENAI_API_KEY', 'DISABLE_PERSONAL_KEYS']) delete process.env[k]
// As a Studio whose AI features were turned on (a new one starts with them off).
process.env.AI_ENABLED = '1'

// The endpoint: /v1 lists two models for the right key, /empty/v1 none,
// /html answers a web page; chat completions stream one line.
const completions = []
const endpoint = http.createServer((req, res) => {
  const ok = req.headers.authorization === 'Bearer sk-good'
  const json = (status, body) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(body)) }
  if (req.method === 'GET' && req.url === '/v1/models') return ok ? json(200, { object: 'list', data: [{ id: 'small-model' }, { id: 'large-model' }] }) : json(401, { error: 'invalid key' })
  if (req.method === 'GET' && req.url === '/empty/v1/models') return json(200, { object: 'list', data: [] })
  if (req.method === 'GET' && req.url === '/html/models') { res.writeHead(200, { 'Content-Type': 'text/html' }); return res.end('<html>a web page</html>') }
  if (req.method === 'POST' && req.url === '/v1/chat/completions') {
    let body = ''
    req.on('data', d => { body += d })
    req.on('end', () => {
      completions.push({ auth: req.headers.authorization, body: JSON.parse(body) })
      res.writeHead(200, { 'Content-Type': 'text/event-stream' })
      res.end('data: {"choices":[{"delta":{"content":"hello from the endpoint"}}]}\n\ndata: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
    })
    return
  }
  json(404, { error: 'not found' })
})
await new Promise(r => endpoint.listen(0, '127.0.0.1', r))
const base = `http://127.0.0.1:${endpoint.address().port}`
test.after(() => endpoint.close())

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { chatRoutes, AI_OFF } = await import('../dist/chat/routes.js')
const { settingsRoutes, effectiveSettings, updateSettings } = await import('../dist/settings.js')
const { ragProxyRoutes } = await import('../dist/ragProxy.js')
const C = await import('../dist/credentials.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(settingsRoutes)
await app.register(chatRoutes)
await app.register(ragProxyRoutes)
await app.ready()
const j = async (method, url, payload) => {
  const r = await app.inject({ method, url, ...(payload ? { payload } : {}) })
  return { status: r.statusCode, body: r.json(), headers: r.headers }
}
const chat = () => app.inject({ method: 'POST', url: '/api/chat/stream', payload: { model: 'small-model', messages: [{ role: 'user', content: 'Say hello.' }] } })

test('the one user tests an endpoint: models listed, or what failed said plainly', async () => {
  const me = await j('GET', '/api/me/model-key')
  assert.deepEqual([me.body.authEnabled, me.body.verified, me.body.single, me.body.mode], [false, true, true, 'none'])

  const ok = await j('POST', '/api/me/model-key/test', { key: 'sk-good', baseUrl: `${base}/v1` })
  assert.equal(ok.status, 200)
  assert.deepEqual([ok.body.status, ok.body.models, ok.body.ids, ok.body.gatewayHost], ['ok', 2, ['small-model', 'large-model'], `127.0.0.1:${endpoint.address().port}`])
  // The endpoint answered, so one small request offering a tool asked the
  // first model whether it calls tools. This stand-in only ever writes text.
  assert.equal(completions.length, 1)
  assert.equal(completions[0].body.model, 'small-model')
  assert.equal(completions[0].body.tools[0].function.name, 'get_utc_time')
  assert.deepEqual([ok.body.toolCheck.model, ok.body.toolCheck.result], ['small-model', 'no-call'])
  assert.match(ok.body.toolCheck.message, /^small-model answered without calling the test tool, so chat cannot search the libraries with this model\./)
  completions.length = 0
  const refused = await j('POST', '/api/me/model-key/test', { key: 'sk-bad', baseUrl: `${base}/v1` })
  assert.equal(refused.body.status, 'auth')
  const none = await j('POST', '/api/me/model-key/test', { key: 'sk-good', baseUrl: `${base}/empty/v1` })
  assert.deepEqual([none.body.status, none.body.models, none.body.ids], ['ok', 0, []])
  const noV1 = await j('POST', '/api/me/model-key/test', { key: 'sk-good', baseUrl: base })
  assert.equal(noV1.body.status, 'unreachable')
  assert.equal(noV1.body.message, `${base}/models does not exist (404). An OpenAI-compatible base URL usually ends in a version, such as ${base}/v1.`)
  assert.equal(noV1.body.notFound, `${base}/models`)
  const html = await j('POST', '/api/me/model-key/test', { key: 'sk-good', baseUrl: `${base}/html` })
  assert.equal(html.body.status, 'unreachable')
  assert.match(html.body.message, /JSON/)
  const closed = await j('POST', '/api/me/model-key/test', { key: 'sk-good', baseUrl: 'http://127.0.0.1:9/v1' })
  assert.equal(closed.body.status, 'unreachable')
  assert.equal((await j('POST', '/api/me/model-key/test', { key: 'sk-good', baseUrl: 'ftp://host/v1' })).status, 400)
})

test('saved, the key is stored on this computer, encrypted, and the rest of the Studio uses it', async () => {
  const saved = await j('POST', '/api/me/model-key', { key: 'sk-good', baseUrl: `${base}/v1`, persist: true })
  assert.equal(saved.status, 200)
  assert.deepEqual([saved.body.single, saved.body.mode, saved.body.last4, saved.body.baseUrl], [true, 'stored', 'good', `${base}/v1`])
  assert.equal(saved.headers['set-cookie'], undefined, 'no browser cookie: there is no sign-in to tie it to')
  const vault = fs.readFileSync(path.join(IX, 'user-credentials.json'), 'utf8')
  assert.ok(JSON.parse(vault).single, 'kept in the vault')
  assert.doesNotMatch(vault, /sk-good/, 'encrypted')
  // It survives the startup scrub of personal entries, and a request
  // without a user is the one user's.
  assert.equal(C.scrubPersonalVaultEntries(), 0)
  assert.deepEqual(C.resolveUserCred(undefined), { key: 'sk-good', baseUrl: `${base}/v1` })

  const health = (await j('GET', '/api/ai/health')).body
  assert.deepEqual([health.status, health.models], ['ok', 2])
  const models = (await j('GET', '/api/chat/models?refresh=1')).body
  assert.ok(models.models.some(m => m.id === 'small-model'), JSON.stringify(models))

  const res = await chat()
  assert.match(res.body, /hello from the endpoint/)
  assert.equal(completions.length, 1)
  assert.equal(completions[0].auth, 'Bearer sk-good')
  assert.equal(completions[0].body.model, 'small-model')
})

test('with AI features off nothing is sent to a model, and search and the library keep working', async () => {
  const sections = ['library', 'search', 'query', 'overview', 'history', 'workflows', 'settings', 'help']
  const put = await j('PUT', '/api/settings', { aiEnabled: false, sections })
  assert.equal(put.status, 200)
  assert.equal(put.body.effective.aiEnabled, false)
  const cfg = (await j('GET', '/api/config')).body
  assert.equal(cfg.aiEnabled, false)
  assert.deepEqual(cfg.sections, sections)

  const res = await chat()
  assert.equal(res.statusCode, 200)
  assert.match(res.body, new RegExp(`event: content\\ndata: ${JSON.stringify({ text: AI_OFF }).replace(/[.]/g, '\\.')}`))
  assert.match(res.body, /event: done/)
  assert.equal(AI_OFF, 'AI features are off in this Studio.')
  assert.equal(completions.length, 1, 'the endpoint was not called')

  assert.deepEqual((await j('GET', '/api/chat/models')).body, { models: [], unreachableSessions: [], error: AI_OFF })
  const health = (await j('GET', '/api/ai/health')).body
  assert.deepEqual([health.ok, health.status, health.message], [false, 'off', AI_OFF])
  const v1 = await j('GET', '/v1/models')
  assert.deepEqual([v1.status, v1.body.error], [403, AI_OFF])
  const labels = await j('POST', '/api/kb/label-suggestions', { dir: '' })
  assert.equal(labels.status, 409)
  assert.match(labels.body.error, /^AI features are off/)
  assert.equal((await j('GET', '/api/search?q=notes')).status, 200)
  assert.equal((await j('GET', '/api/kb/tree')).status, 200)
  assert.equal((await j('GET', '/api/kb/stats')).status, 200)

  await j('PUT', '/api/settings', { aiEnabled: true })
  assert.equal((await j('GET', '/api/config')).body.aiEnabled, true)
})

test('AI_ENABLED sets the default; a saved setting wins; a new computer of its own starts with it off', async () => {
  const setupDone = effectiveSettings().setupDone
  await updateSettings({ aiEnabled: undefined })
  process.env.AI_ENABLED = '0'
  assert.equal(effectiveSettings().aiEnabled, false)
  await updateSettings({ aiEnabled: true })
  assert.equal(effectiveSettings().aiEnabled, true)
  delete process.env.AI_ENABLED
  await updateSettings({ aiEnabled: undefined, setupDone: false })
  assert.equal(effectiveSettings().aiEnabled, false, 'off until setup or Settings turns it on')
  await updateSettings({ setupDone: true })
  assert.equal(effectiveSettings().aiEnabled, true, 'an install that finished setup before this default keeps AI on')
  process.env.AI_ENABLED = '1'
  await updateSettings({ setupDone })
})

test('the one user can remove the key', async () => {
  const cleared = await j('POST', '/api/me/model-key/clear', {})
  assert.deepEqual([cleared.body.single, cleared.body.mode], [true, 'none'])
  assert.equal(C.resolveUserCred(undefined), null)
})
