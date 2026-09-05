import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
process.env.PW_GATEWAY_URL = 'https://gw.test/api/openai/v1'
const { probeProvider, invalidateProviderProbes, aiHealth, extractUnlockUrl } = await import('../dist/chat/gateway.js')

const responses = []
const calls = []
globalThis.fetch = async (url, init) => {
  calls.push({ url: String(url), body: init?.body ? JSON.parse(init.body) : null })
  const r = responses.shift() ?? { status: 200, text: 'data: {"choices":[{"delta":{"content":"pong"}}]}\n\ndata: [DONE]\n' }
  return { ok: r.status < 400, status: r.status, text: async () => r.text }
}
const reset = () => { responses.length = 0; calls.length = 0; invalidateProviderProbes() }

test('the probe streams, and a locked key read through the streaming path is a locked verdict', async () => {
  reset()
  responses.push({ status: 400, text: '{"error":{"message":"API key locked - visit the unlock URL to re-enable your key","type":"error"}}' })
  const v = await probeProvider('me:genaimil', 'me:genaimil/gemini', 'k1')
  assert.equal(calls[0].body.stream, true, 'stream:true is the path the gateway does not mask')
  assert.equal(calls[0].body.max_tokens, 1)
  assert.deepEqual([v.ok, v.kind], [false, 'locked'])
})
test('an unlock url in the body is carried on the verdict', async () => {
  reset()
  responses.push({ status: 401, text: '{"error":{"type":"unauthorized","message":"API key locked","unlock_url":"https://genai.test/unlock/abc"}}' })
  const v = await probeProvider('me:genaimil', 'me:genaimil/gemini', 'k2')
  assert.equal(v.unlockUrl, 'https://genai.test/unlock/abc')
})
test('a masked generic failure counts only when it happens twice, then reads as unavailable', async () => {
  reset()
  const masked = '{"error":{"message":"An error occurred while generating the response","type":"error"}}'
  responses.push({ status: 400, text: masked }, { status: 400, text: masked })
  const v = await probeProvider('me:genaimil', 'me:genaimil/gemini', 'k3')
  assert.equal(calls.length, 2)
  assert.deepEqual([v.ok, v.kind], [false, 'unavailable'])
  reset()
  responses.push({ status: 400, text: masked })
  const v2 = await probeProvider('me:genaimil', 'me:genaimil/gemini', 'k4')
  assert.equal(v2.ok, true, 'one masked failure followed by success is not a verdict')
})
test('a healthy provider is ok, cached, and re-probed only after invalidation', async () => {
  reset()
  const a = await probeProvider('me:genaimil', 'me:genaimil/gemini', 'k5')
  const b = await probeProvider('me:genaimil', 'me:genaimil/gemini', 'k5')
  assert.equal(a.ok, true); assert.equal(b.ok, true)
  assert.equal(calls.length, 1, 'the second call is served from the cache')
  invalidateProviderProbes()
  await probeProvider('me:genaimil', 'me:genaimil/gemini', 'k5')
  assert.equal(calls.length, 2)
})
test('aiHealth explains a 401 and carries the unlock url when the provider gives one', async () => {
  reset()
  responses.push({ status: 401, text: '{"error":{"message":"API key locked","unlock_url":"https://genai.test/unlock/xyz"}}' })
  const locked = await aiHealth('k6', 'https://genai.test/v1')
  assert.equal(locked.status, 'auth'); assert.equal(locked.unlockUrl, 'https://genai.test/unlock/xyz'); assert.match(locked.message, /locked/)
  responses.push({ status: 401, text: 'Unauthorized' })
  const plain = await aiHealth('k7')
  assert.equal(plain.status, 'auth'); assert.match(plain.message, /24 hours/); assert.match(plain.message, /does not expire/)
  responses.push({ status: 200, text: '{"data":[{"id":"a"},{"id":"b"}]}' })
  const ok = await aiHealth('k8')
  assert.equal(ok.status, 'ok'); assert.equal(ok.models, 2)
  assert.equal(extractUnlockUrl('none'), null)
})
