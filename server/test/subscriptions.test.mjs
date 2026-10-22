// Subscriptions connected through the platform: a ChatGPT plan with the
// device sign-in the platform runs, and GitHub Copilot as a platform
// provider. The Studio only relays; the platform API is a stand-in here.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'sub-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'sub-ix-'))
process.env.PW_GATEWAY_URL = 'https://platform.example.com/api/openai/v1'
process.env.PW_API_KEY = 'alex-platform-key'
process.env.PW_CREDENTIALS_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'sub-pw-'))

const calls = []
let pollsUntilConnected = 2
let copilotOffered = true
globalThis.fetch = async (url, init = {}) => {
  const u = new URL(String(url))
  const method = init.method ?? 'GET'
  calls.push({ method, path: u.pathname, auth: init.headers?.Authorization ?? null, body: init.body ? JSON.parse(init.body) : null })
  const json = (d, status = 200) => new Response(JSON.stringify(d), { status, headers: { 'content-type': 'application/json' } })
  if (u.pathname === '/api/auth/whoami') return new Response('user:alex', { status: 200, headers: { 'content-type': 'text/plain' } })
  if (u.pathname === '/api/auth/whoami/organization') return json('org-a')
  if (u.pathname === '/api/organizations/org-a/ai-integrations') return json({ integrations: [
    { id: 'codex-subscription', kind: 'codex-subscription', displayName: 'Codex subscription', lifecycle: 'active' },
    ...(copilotOffered ? [{ id: 'copilot-entry', kind: 'github-copilot', displayName: 'GitHub Copilot', lifecycle: 'active' }] : []),
  ] })
  if (u.pathname === '/api/organizations/org-a/users/alex/ai-providers' && method === 'GET') return json([
    { name: 'chatgpt', displayName: 'ChatGPT', catalogEntryId: 'codex-subscription', status: 'provisioned', accountEmail: 'alex@example.com' },
  ])
  if (u.pathname === '/api/organizations/org-a/users/alex/ai-providers' && method === 'POST') return json({ name: 'copilot', displayName: 'GitHub Copilot', catalogEntryId: 'copilot-entry', accountLogin: 'alex-gh' })
  if (u.pathname === '/api/integrations/github/oauth/status') return json({ appConfigured: true, tokensAllowed: false, username: 'alex-gh' })
  if (u.pathname === '/api/organizations/org-a/users/alex/codex-device-authorizations') return json({ id: 'auth-1', providerName: 'chatgpt', verificationUrl: 'https://auth.example.com/codex/device', userCode: 'WXYZ-1234', expiresAt: '2026-10-22T12:00:00Z', pollIntervalSeconds: 5 })
  if (u.pathname === '/api/organizations/org-a/users/alex/codex-device-authorizations/auth-1') {
    return json(--pollsUntilConnected > 0 ? { status: 'pending' } : { status: 'connected', provider: { name: 'chatgpt', displayName: 'ChatGPT', accountEmail: 'alex@example.com' } })
  }
  return json({ message: 'not found' }, 404)
}

const { accessRoutes } = await import('../dist/accessRoutes.js')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(accessRoutes)
await app.ready()
const req = async (method, url, payload) => { const r = await app.inject({ method, url, payload }); return { status: r.statusCode, body: r.json() } }

test('the subscriptions the platform offers, and those already connected, are listed for the account', async () => {
  const r = await req('GET', '/api/access/subscriptions')
  assert.equal(r.status, 200)
  assert.deepEqual([r.body.available, r.body.user, r.body.org], [true, 'alex', 'org-a'])
  assert.equal(r.body.codex.offered, true)
  assert.deepEqual(r.body.codex.providers, [{ name: 'chatgpt', displayName: 'ChatGPT', status: 'provisioned', account: 'alex@example.com' }])
  assert.equal(r.body.copilot.offered, true)
  assert.deepEqual(r.body.copilot.github, { linked: true, username: 'alex-gh', appConfigured: true, tokensAllowed: false })
  assert.ok(calls.every(c => c.auth === 'Bearer alex-platform-key'), 'every call carries the platform credential')
})

test('a ChatGPT plan connects through the platform device sign-in: address and code, then pending until connected', async () => {
  calls.length = 0
  const start = await req('POST', '/api/access/codex', { name: 'ChatGPT Plus!' })
  assert.equal(start.status, 200)
  assert.deepEqual(start.body, { id: 'auth-1', url: 'https://auth.example.com/codex/device', code: 'WXYZ-1234', expiresAt: '2026-10-22T12:00:00Z', intervalSec: 5, provider: 'chatgpt' })
  const startCall = calls.find(c => c.path.endsWith('/codex-device-authorizations'))
  assert.deepEqual(startCall.body, { name: 'chatgptplus' }, 'the provider name is reduced to what the platform accepts')
  assert.equal((await req('POST', '/api/access/codex/auth-1')).body.status, 'pending')
  const done = await req('POST', '/api/access/codex/auth-1')
  assert.deepEqual(done.body, { status: 'connected', provider: { name: 'chatgpt', displayName: 'ChatGPT', account: 'alex@example.com' } })
})

test('Copilot is added as a platform provider from the catalog entry the platform names', async () => {
  calls.length = 0
  const r = await req('POST', '/api/access/copilot', {})
  assert.equal(r.status, 200)
  assert.deepEqual(r.body.provider, { name: 'copilot', displayName: 'GitHub Copilot', account: 'alex-gh' })
  const made = calls.find(c => c.method === 'POST' && c.path === '/api/organizations/org-a/users/alex/ai-providers')
  assert.deepEqual(made.body, { name: 'copilot', catalogEntryId: 'copilot-entry', displayName: 'GitHub Copilot' })
  copilotOffered = false
  const refused = await req('POST', '/api/access/copilot', {})
  assert.equal(refused.status, 409)
  assert.match(refused.body.error, /does not offer GitHub Copilot/)
})

test('a platform that cannot be reached is said plainly', async () => {
  const prev = globalThis.fetch
  globalThis.fetch = async () => { throw Object.assign(new TypeError('fetch failed'), { cause: { code: 'ENOTFOUND' } }) }
  try {
    const { resetAccessRoutesForTests } = await import('../dist/accessRoutes.js')
    resetAccessRoutesForTests()
    const r = await req('POST', '/api/access/codex', {})
    assert.equal(r.status, 503)
    assert.match(r.body.error, /^Could not reach platform\.example\.com \(ENOTFOUND\)/)
  } finally { globalThis.fetch = prev }
})

test('offline, the subscriptions are not reported as "not offered"', async () => {
  const prev = globalThis.fetch
  // The account is known from before; the platform has since gone out of reach.
  await req('GET', '/api/access/subscriptions')
  globalThis.fetch = async () => { throw Object.assign(new TypeError('fetch failed'), { cause: { code: 'ECONNRESET' } }) }
  try {
    const r = await req('GET', '/api/access/subscriptions')
    assert.equal(r.status, 503)
    assert.match(r.body.error, /^Could not reach platform\.example\.com \(ECONNRESET\)/)
  } finally { globalThis.fetch = prev }
})
