// A Studio that signs users in never runs setup: the status says so
// without disclosing server paths, and every other setup route refuses.
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
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { setupRoutes } = await import('../dist/setup.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(setupRoutes)
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
