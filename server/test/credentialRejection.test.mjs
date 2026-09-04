import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { credentialRejectionMessage } = await import('../dist/chat/routes.js')

test('an expired token names the time and the non-expiring alternative', () => {
  const m = credentialRejectionMessage('personal', 'token', '2026-09-02T13:56:02.000Z')
  assert.match(m, /expired at Sep 2, 2026, 1:56 PM UTC/)
  assert.match(m, /API key from Account, API keys, which does not expire/)
})
test('a rejected credential with no readable expiry still explains the likely cause', () => {
  const m = credentialRejectionMessage('personal', 'api-key', null)
  assert.match(m, /rejected your stored credential \(401\)/)
  assert.match(m, /tokens last 24 hours/)
})
test('a rejected deployment credential points at the operator and the personal fallback', () => {
  const m = credentialRejectionMessage('deployment', null, null)
  assert.match(m, /deployment credential/)
  assert.match(m, /Settings, Model access/)
})
