// A Studio that signs users in never runs setup: the status says so
// without disclosing server paths, and every other setup route refuses,
// the icon upload included. The single-user model key is not offered
// either: a request without a verified user has no key of its own.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'setup-si-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'setup-si-ix-'))
// Verification is on; nothing is fetched until a request carries a token.
process.env.AUTH_JWKS_URL = 'http://127.0.0.1:9/api/platform/keys'
process.env.STUDIO_CONFIG_FILE = path.join(process.env.INDEX_BASE, 'studio.env')

const { default: Fastify } = await import('fastify')
const { default: multipart } = await import('@fastify/multipart')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { setupRoutes } = await import('../dist/setup.js')
const { chatRoutes } = await import('../dist/chat/routes.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(multipart)
await app.register(setupRoutes)
await app.register(chatRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}

test('status is not a first run and carries no paths', async () => {
  const r = await j('GET', '/api/setup/status')
  assert.equal(r.status, 200)
  assert.deepEqual(r.body, { firstRun: false, standalone: false, canRestart: false })
})

test('browse, primary folder, index and complete refuse', async () => {
  assert.equal((await j('GET', '/api/setup/browse?path=/')).status, 403)
  assert.equal((await j('POST', '/api/setup/primary-folder', { path: os.tmpdir() })).status, 403)
  assert.equal((await j('POST', '/api/setup/index')).status, 403)
  assert.equal((await j('POST', '/api/setup/complete')).status, 403)
  assert.equal(fs.existsSync(process.env.STUDIO_CONFIG_FILE), false)
})

test('the icon upload and removal refuse, and nothing is stored', async () => {
  const boundary = '----studio-test'
  const payload = Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="mark.png"\r\nContent-Type: image/png\r\n\r\n\x89PNG\r\n\x1a\n\r\n--${boundary}--\r\n`, 'latin1')
  const up = await app.inject({ method: 'POST', url: '/api/setup/icon', payload, headers: { 'content-type': `multipart/form-data; boundary=${boundary}` } })
  assert.equal(up.statusCode, 403)
  assert.equal((await j('DELETE', '/api/setup/icon?variant=dark')).status, 403)
  assert.equal(fs.existsSync(path.join(process.env.INDEX_BASE, 'branding')), false)
})

test('the model key stays with verified users', async () => {
  const me = await j('GET', '/api/me/model-key')
  assert.deepEqual([me.body.authEnabled, me.body.verified, me.body.single], [true, false, undefined])
  assert.equal((await j('POST', '/api/me/model-key', { key: 'sk-x', baseUrl: 'https://models.example/v1' })).status, 403)
  assert.equal((await j('POST', '/api/me/model-key/test', { key: 'sk-x', baseUrl: 'https://models.example/v1' })).status, 403)
  assert.equal(fs.existsSync(path.join(process.env.INDEX_BASE, 'user-credentials.json')), false)
})
