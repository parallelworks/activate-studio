// An OpenAI-compatible endpoint whose portal differs from the OpenAI shape:
// the key in an api-key header, a header of its own, a query string on every
// request, and, at another path, no model list at all. The options are kept
// with the key and protected like it, every request the Studio makes to the
// endpoint carries them, and Test says whether the model calls tools. The
// portal is a stand-in on the loopback interface.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'

const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'ep-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'ep-kb-'))
process.env.INDEX_BASE = IX
process.env.HOME = fs.mkdtempSync(path.join(os.tmpdir(), 'ep-home-'))
process.env.PW_GATEWAY_URL = 'https://gw.test/api/openai/v1'
for (const k of ['AUTH_JWKS_URL', 'PW_API_KEY', 'OPENAI_API_KEY', 'DISABLE_PERSONAL_KEYS', 'PW_ALLOCATION']) delete process.env[k]
process.env.AI_ENABLED = '1'

const KEY = 'sk-portal-virtual-0001'
const TENANT = 'tenant-secret-value-77'
const QUERY = 'api-version=2026-01-01'

// Every request the portal sees, with the headers it was sent.
const seen = []
const sse = (res, frames) => {
  res.writeHead(200, { 'Content-Type': 'text/event-stream' })
  res.end(frames.map(f => `data: ${JSON.stringify(f)}\n\n`).join('') + 'data: [DONE]\n\n')
}
const portal = http.createServer((req, res) => {
  const json = (status, body) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(body)) }
  let raw = ''
  req.on('data', d => { raw += d })
  req.on('end', () => {
    const body = raw ? JSON.parse(raw) : null
    seen.push({ method: req.method, url: req.url, headers: req.headers, body })
    const [pathname, query] = req.url.split('?')
    const authorized = req.headers['api-key'] === KEY && req.headers['x-tenant'] === TENANT && query === QUERY
    // A 404 for every path it does not serve, as a portal does.
    if (!['/portal/v1/models', '/portal/v1/chat/completions', '/nolist/v1/chat/completions'].includes(pathname)) return json(404, { error: { message: 'Not Found' } })
    if (!authorized) return json(401, { error: { message: 'Authentication Error, invalid api key' } })
    if (req.method === 'GET') return json(200, { object: 'list', data: [{ id: 'tool-model' }, { id: 'plain-model' }, { id: 'refusing-model' }, { id: 'text-embedding-small' }] })
    const offersTools = Array.isArray(body.tools) && body.tools.length > 0
    if (body.model === 'refusing-model' && offersTools) {
      return json(400, { error: { message: '"auto" tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set' } })
    }
    if (body.model === 'tool-model' && offersTools) {
      return sse(res, [
        { choices: [{ delta: { tool_calls: [{ index: 0, id: 'c1', type: 'function', function: { name: body.tools[0].function.name, arguments: '' } }] } }] },
        { choices: [{ delta: { tool_calls: [{ index: 0, function: { arguments: '{}' } }] } }] },
        { choices: [{ delta: {}, finish_reason: 'tool_calls' }] },
      ])
    }
    sse(res, [{ choices: [{ delta: { content: 'words from the portal' } }] }, { choices: [{ delta: {}, finish_reason: 'stop' }] }])
  })
})
await new Promise(r => portal.listen(0, '127.0.0.1', r))
const origin = `http://127.0.0.1:${portal.address().port}`
test.after(() => portal.close())

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { chatRoutes, toolCheckMessage, toolCheckModel } = await import('../dist/chat/routes.js')
const { settingsRoutes } = await import('../dist/settings.js')
const { ragProxyRoutes } = await import('../dist/ragProxy.js')
const { notFoundMessage, modelHeaders, withQuery } = await import('../dist/chat/gateway.js')
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
  return { status: r.statusCode, body: r.json() }
}

const portalOptions = { auth: 'api-key', headers: [{ name: 'X-Tenant', value: TENANT }], query: QUERY }

test('the options are checked, and a header sent without a value keeps the stored one', () => {
  const stored = C.normalizeEndpointOptions({ auth: 'x-api-key', headers: [{ name: 'X-Org', value: 'org-1' }], models: 'a, b\nb\n', query: '?v=1' })
  assert.deepEqual(stored, { auth: 'x-api-key', headers: [{ name: 'X-Org', value: 'org-1' }], models: ['a', 'b'], query: 'v=1' })
  assert.deepEqual(C.normalizeEndpointOptions({ auth: 'x-api-key', headers: [{ name: 'x-org' }] }, stored), { auth: 'x-api-key', headers: [{ name: 'x-org', value: 'org-1' }] })
  assert.equal(C.normalizeEndpointOptions({ auth: 'bearer', headers: [], models: [], query: '' }), null, 'all defaults is nothing to store')
  assert.equal(C.normalizeEndpointOptions({ headers: [{ name: '', value: '' }] }), null, 'an empty row is skipped')
  for (const [bad, why] of [
    [{ headers: [{ name: 'X-New' }] }, /X-New needs a value/],
    [{ headers: [{ name: 'Authorization', value: 'Basic x' }] }, /already sent as Authorization; choose None/],
    [{ auth: 'none', headers: [{ name: 'Content-Type', value: 'text/plain' }] }, /sets Content-Type itself/],
    [{ headers: [{ name: 'Bad Name', value: 'v' }] }, /not a valid header name/],
    [{ headers: [{ name: 'X-A', value: 'one\r\nX-B: two' }] }, /not a valid header value/],
    [{ headers: [{ name: 'X-A', value: '1' }, { name: 'x-a', value: '2' }] }, /listed twice/],
    [{ auth: 'basic' }, /unknown key header/],
    [{ models: ['two words'] }, /one word/],
    [{ query: 'a=1#frag' }, /query string/],
  ]) assert.throws(() => C.normalizeEndpointOptions(bad), why)
  // With None, an Authorization header of one's own is the way to send it.
  assert.deepEqual(C.normalizeEndpointOptions({ auth: 'none', headers: [{ name: 'Authorization', value: 'Basic abc' }] }), { auth: 'none', headers: [{ name: 'Authorization', value: 'Basic abc' }] })
})

test('requests carry the key in the chosen header, the extra headers, and the query', () => {
  assert.deepEqual(modelHeaders('k'), { Authorization: 'Bearer k' })
  assert.deepEqual(modelHeaders('k', { auth: 'api-key', headers: [{ name: 'X-T', value: 't' }] }), { 'X-T': 't', 'api-key': 'k' })
  assert.deepEqual(modelHeaders('k', { auth: 'x-api-key' }), { 'x-api-key': 'k' })
  assert.deepEqual(modelHeaders('k', { auth: 'none' }), {})
  assert.equal(withQuery('https://p.example.org/v1/models', { query: 'api-version=1' }), 'https://p.example.org/v1/models?api-version=1')
  assert.equal(withQuery('https://p.example.org/catalog?x=1', { query: 'y=2' }), 'https://p.example.org/catalog?x=1&y=2')
  assert.equal(withQuery('https://p.example.org/v1/models', null), 'https://p.example.org/v1/models')
})

test('a 404 names the URL tried and what to do, and never says to add /v1 to a URL that has it', () => {
  const at = 'https://portal.example.org'
  assert.equal(notFoundMessage(`${at}/api/openai/v1/models`, `${at}/api/openai/v1`),
    `${at}/api/openai/v1/models does not exist (404), so portal.example.org is not an ACTIVATE platform. For a site AI portal or another OpenAI-compatible service, use OpenAI-compatible endpoint with the service's base URL.`)
  const v1 = notFoundMessage(`${at}/v1/models`, `${at}/v1`)
  assert.match(v1, /^https:\/\/portal\.example\.org\/v1\/models does not exist \(404\)\. If https:\/\/portal\.example\.org\/v1 is the right address, the endpoint does not list its models: type their names under Advanced/)
  assert.doesNotMatch(v1, /ends in/)
  assert.equal(notFoundMessage(`${at}/v1/chat/completions/models`, `${at}/v1/chat/completions`),
    `${at}/v1/chat/completions/models does not exist (404). The base URL ends before /chat/completions: try ${at}/v1.`)
  assert.equal(notFoundMessage(`${at}/models`, at), `${at}/models does not exist (404). An OpenAI-compatible base URL usually ends in a version, such as ${at}/v1.`)
})

test('the tool check model: the one asked for, else the first chat model', () => {
  const ids = ['text-embedding-small', 'org:x/big', 'chat-a', 'chat-b']
  assert.equal(toolCheckModel(ids, 'chat-b'), 'chat-b')
  assert.equal(toolCheckModel(ids, 'not-listed'), 'chat-a')
  assert.equal(toolCheckModel(['text-embedding-small']), null)
  assert.match(toolCheckMessage('m', 'emulated'), /in the prompt.*CHAT_SYSTEM_IN_USER/)
  assert.equal(toolCheckMessage('m', { kind: 'failed', detail: 'no answer within 15 seconds' }), 'Tool calling with m was not checked: no answer within 15 seconds.')
})

test('Test reaches a portal that wants an api-key header, a header of its own and a query, and checks tool calling', async () => {
  seen.length = 0
  const plain = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/portal/v1` })
  assert.equal(plain.body.status, 'auth', 'without the options the portal refuses the key')
  assert.match(plain.body.message, /under Advanced the header the endpoint expects it in/)

  seen.length = 0
  const ok = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/portal/v1`, endpoint: portalOptions, model: 'tool-model' })
  assert.deepEqual([ok.body.status, ok.body.models, ok.body.checked], ['ok', 4, 'models'])
  assert.deepEqual(ok.body.toolCheck, { model: 'tool-model', result: 'calls', message: 'tool-model calls tools, so chat can search the libraries with it.' })
  assert.deepEqual(seen.map(r => `${r.method} ${r.url}`), [`GET /portal/v1/models?${QUERY}`, `POST /portal/v1/chat/completions?${QUERY}`])
  for (const r of seen) {
    assert.equal(r.headers['api-key'], KEY)
    assert.equal(r.headers['x-tenant'], TENANT)
    assert.equal(r.headers.authorization, undefined, 'the key goes in api-key only')
  }
  assert.equal(seen[1].body.max_tokens, 256, 'the check is capped')

  const noCall = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/portal/v1`, endpoint: portalOptions, model: 'plain-model' })
  assert.equal(noCall.body.toolCheck.result, 'no-call')
  assert.match(noCall.body.toolCheck.message, /^plain-model answered without calling the test tool, so chat cannot search the libraries with this model\./)
  const refused = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/portal/v1`, endpoint: portalOptions, model: 'refusing-model' })
  assert.equal(refused.body.status, 'ok', 'the check is information, not a failed test')
  assert.equal(refused.body.toolCheck.result, 'refused')
  assert.match(refused.body.toolCheck.message, /refused a request that offers refusing-model a tool: "auto" tool choice requires --enable-auto-tool-choice/)
  // With no model asked for, the first chat model: not the embedding model.
  const first = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/portal/v1`, endpoint: portalOptions })
  assert.equal(first.body.toolCheck.model, 'tool-model')
  // tools:false skips it.
  const skipped = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/portal/v1`, endpoint: portalOptions, tools: false })
  assert.equal(skipped.body.toolCheck, undefined)
})

test('saved, the options are encrypted with the key, the browser sees header names only, and every request carries them', async () => {
  const saved = await j('POST', '/api/me/model-key', { key: KEY, baseUrl: `${origin}/portal/v1`, endpoint: portalOptions, persist: true })
  assert.equal(saved.status, 200)
  assert.deepEqual(saved.body.endpoint, { auth: 'api-key', headers: ['X-Tenant'], models: [], query: QUERY })
  const vault = fs.readFileSync(path.join(IX, 'user-credentials.json'), 'utf8')
  assert.doesNotMatch(vault, new RegExp(TENANT), 'the header value is encrypted')
  assert.doesNotMatch(vault, /X-Tenant/, 'and so is the rest of the options')
  for (const url of ['/api/me/model-key', '/api/access']) {
    assert.doesNotMatch(JSON.stringify((await j('GET', url)).body), new RegExp(`${TENANT}|${KEY}`), `${url} sends no secret to the browser`)
  }
  assert.deepEqual(C.resolveUserCred(undefined), { key: KEY, baseUrl: `${origin}/portal/v1`, endpoint: { auth: 'api-key', headers: [{ name: 'X-Tenant', value: TENANT }], query: QUERY } })

  seen.length = 0
  const health = (await j('GET', '/api/ai/health')).body
  assert.deepEqual([health.status, health.models], ['ok', 4])
  const models = (await j('GET', '/api/chat/models?refresh=1')).body
  assert.deepEqual(models.models.map(m => m.id).sort(), ['plain-model', 'refusing-model', 'text-embedding-small', 'tool-model'])
  const chat = await app.inject({ method: 'POST', url: '/api/chat/stream', payload: { model: 'plain-model', messages: [{ role: 'user', content: 'Say hello.' }] } })
  assert.match(chat.body, /words from the portal/)
  // The OpenAI-compatible endpoint the Studio serves forwards to the
  // person's endpoint, with its options, and never to the gateway.
  const v1models = await j('GET', '/v1/models?all=1')
  assert.ok(v1models.body.data.some(m => m.id === 'tool-model' && m.owned_by === 'endpoint'), JSON.stringify(v1models.body))
  const rag = await app.inject({ method: 'POST', url: '/v1/chat/completions', payload: { model: 'studio-rag/plain-model', messages: [{ role: 'user', content: 'hi' }], stream: true } })
  assert.match(rag.body, /words from the portal/)
  const agent = await app.inject({ method: 'POST', url: '/v1/chat/completions', payload: { model: 'studio-agent/plain-model', messages: [{ role: 'user', content: 'hi' }] } })
  assert.match(agent.json().choices[0].message.content, /words from the portal/)

  const kinds = seen.map(r => `${r.method} ${r.url.split('?')[0]}`)
  assert.ok(kinds.includes('GET /portal/v1/models') && kinds.filter(k => k === 'POST /portal/v1/chat/completions').length >= 3, kinds.join(', '))
  for (const r of seen) {
    assert.equal(r.url.split('?')[1], QUERY, `${r.method} ${r.url} carries the query`)
    assert.equal(r.headers['api-key'], KEY, `${r.method} ${r.url} carries the key`)
    assert.equal(r.headers['x-tenant'], TENANT, `${r.method} ${r.url} carries the extra header`)
    assert.equal(r.headers.authorization, undefined)
  }
})

test('saving the same endpoint without the key keeps the key and the stored header values', async () => {
  const kept = await j('POST', '/api/me/model-key', { key: '', baseUrl: `${origin}/portal/v1`, endpoint: { ...portalOptions, headers: [{ name: 'X-Tenant' }], models: ['tool-model'] } })
  assert.equal(kept.status, 200)
  assert.deepEqual(kept.body.endpoint.models, ['tool-model'])
  assert.deepEqual(C.resolveUserCred(undefined).endpoint.headers, [{ name: 'X-Tenant', value: TENANT }])
  assert.equal(C.resolveUserCred(undefined).key, KEY)
  // Left out, the options stay as they are.
  await j('POST', '/api/me/model-key', { key: '', baseUrl: `${origin}/portal/v1` })
  assert.deepEqual(C.resolveUserCred(undefined).endpoint.models, ['tool-model'])
  // Another address gets neither the stored key nor the stored header values.
  const other = await j('POST', '/api/me/model-key', { key: '', baseUrl: `${origin}/elsewhere/v1` })
  assert.deepEqual([other.status, other.body.error], [400, 'key required'])
  const otherHeader = await j('POST', '/api/me/model-key', { key: 'sk-other', baseUrl: `${origin}/elsewhere/v1`, endpoint: { headers: [{ name: 'X-Tenant' }] } })
  assert.deepEqual([otherHeader.status, otherHeader.body.error], [400, 'header X-Tenant needs a value'])
  seen.length = 0
  const otherTest = await j('POST', '/api/me/model-key/test', { key: '', baseUrl: `${origin}/elsewhere/v1` })
  assert.equal(otherTest.status, 400)
  assert.match(otherTest.body.error, /^enter the key for/)
  assert.equal(seen.length, 0, 'the stored key was not sent to the other address')
})

test('with model names typed, nothing asks for a model list: the names are the list and Test tries a chat completion', async () => {
  const listless = { ...portalOptions, models: ['plain-model', 'tool-model'] }
  seen.length = 0
  const t = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/nolist/v1`, endpoint: listless })
  assert.deepEqual([t.body.status, t.body.models, t.body.ids, t.body.checked], ['ok', 2, ['plain-model', 'tool-model'], 'completion'])
  assert.equal(t.body.toolCheck.model, 'plain-model')
  assert.deepEqual(seen.map(r => `${r.method} ${r.url.split('?')[0]} ${r.body?.model}`), ['POST /nolist/v1/chat/completions plain-model', 'POST /nolist/v1/chat/completions plain-model'])
  assert.equal(seen[0].body.tools, undefined, 'the health check is a plain completion')

  // Without the names, the same endpoint has no list to give.
  const bare = await j('POST', '/api/me/model-key/test', { key: KEY, baseUrl: `${origin}/nolist/v1`, endpoint: portalOptions })
  assert.equal(bare.body.status, 'unreachable')
  assert.match(bare.body.message, /nolist\/v1\/models\?api-version=2026-01-01 does not exist \(404\)\. If .* is the right address, the endpoint does not list its models/)

  await j('POST', '/api/me/model-key', { key: KEY, baseUrl: `${origin}/nolist/v1`, endpoint: listless })
  seen.length = 0
  const models = (await j('GET', '/api/chat/models?refresh=1')).body
  assert.deepEqual(models.models.map(m => m.id), ['plain-model', 'tool-model'])
  assert.equal(seen.length, 0, 'the listing came from the typed names')
  const health = (await j('GET', '/api/ai/health')).body
  assert.deepEqual([health.status, health.models, health.checked], ['ok', 2, 'completion'])
  const again = (await j('GET', '/api/ai/health')).body
  assert.equal(again.status, 'ok')
  assert.equal(seen.length, 1, 'the footer\'s minute-by-minute check reuses a recent completion')
  await j('POST', '/api/me/model-key/clear', {})
})

test('a key sealed into the browser cookie keeps its endpoint options', () => {
  const sub = 'person-with-portal'
  C.setUserKey(sub, KEY, false, `${origin}/portal/v1`, portalOptions)
  const sealed = C.sealKeyCookie(sub)
  assert.ok(sealed?.length)
  assert.doesNotMatch(sealed.join(''), new RegExp(TENANT))
  C.clearUserKey(sub)
  C.hydrateFromCookie(sub, sealed.map((c, i) => `ade_mk${i}=${c}`).join('; '))
  assert.deepEqual(C.resolveUserCred(sub).endpoint, { auth: 'api-key', headers: [{ name: 'X-Tenant', value: TENANT }], query: QUERY })
  assert.deepEqual(C.getUserKeyStatus(sub).endpoint, { auth: 'api-key', headers: ['X-Tenant'], models: [], query: QUERY })
  // A platform key carries no endpoint options.
  C.setUserKey(sub, KEY, false, null, portalOptions)
  assert.equal(C.resolveUserCred(sub).endpoint, undefined)
  C.clearUserKey(sub)
})
