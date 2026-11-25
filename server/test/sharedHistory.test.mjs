// With sign-in, people see each other's chats only where it was turned on.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'sh-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'sh-kb-'))
process.env.INDEX_BASE = IX
process.env.AUTH_JWKS_URL = 'https://keys.example.invalid/jwks'
delete process.env.CHAT_SHARED_HISTORY
const file = path.join(IX, 'settings.json')
const form = { ragAdvertiseRagModel: false, ragEndpointAutoStart: false, requirePersonalKey: false, chatSharedHistory: true }
let n = 0
async function shared(saved, env) {
  if (saved) fs.writeFileSync(file, JSON.stringify(saved))
  else fs.rmSync(file, { force: true })
  if (env === undefined) delete process.env.CHAT_SHARED_HISTORY
  else process.env.CHAT_SHARED_HISTORY = env
  const { effectiveSettings } = await import(`../dist/settings.js?${n++}`)
  return effectiveSettings().chatSharedHistory
}

test('shared history with sign-in is off unless it was chosen', async () => {
  assert.equal(await shared(null), false)
  assert.equal(await shared(form), false)
  assert.equal(await shared({ ...form, chatSharedHistoryChosen: true }), true)
  assert.equal(await shared({ chatSharedHistory: true }), true)
  assert.equal(await shared({ chatSharedHistory: false }, '1'), false)
  assert.equal(await shared(null, '1'), true)
  assert.equal(await shared(form, '0'), true)
})
