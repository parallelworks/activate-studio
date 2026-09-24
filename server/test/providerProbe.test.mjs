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
  assert.equal('max_tokens' in calls[0].body, false, 'no token cap: some families reject every spelling of one')
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

test('a provider that rejects the token-cap parameter is up, not unavailable', async () => {
  // The gpt-5.6 family answers any max_tokens with a parameter rejection,
  // explicitly or behind the gateway's masked 400. A probe that carried
  // one marked every model of the family [unavailable] while all of them
  // answered; the probe now sends no cap, and a parameter complaint is
  // read as a reachable provider either way.
  reset()
  responses.push({ status: 400, text: '{"error":{"message":"{\\"detail\\":\\"Unsupported parameter: max_output_tokens\\"}","type":"error"}}' })
  const v = await probeProvider('me:vega', 'me:vega/model-a', 'k7')
  assert.deepEqual([v.ok, v.kind], [true, null])
  assert.equal(calls.length, 1, 'no second ping needed for a parameter complaint')
})

test('a healthy stream is read only to its first frame', async () => {
  reset()
  let pulls = 0
  const frames = ['data: {"choices":[{"delta":{"content":"p"}}]}\n\n', 'data: {"choices":[{"delta":{"content":"ong"}}]}\n\n', 'data: [DONE]\n']
  const enc = new TextEncoder()
  // highWaterMark 0: chunks are produced only when read, so the count is the probe's reads.
  const body = new ReadableStream({ pull(c) { if (pulls < frames.length) c.enqueue(enc.encode(frames[pulls++])); else c.close() } }, { highWaterMark: 0 })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), body: init?.body ? JSON.parse(init.body) : null })
    return { ok: true, status: 200, body, text: async () => frames.join('') }
  }
  const v = await probeProvider('me:vega', 'me:vega/model-b', 'k8')
  assert.equal(v.ok, true)
  assert.ok(pulls <= 1, `read ${pulls} chunks; one frame is proof enough`)
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), body: init?.body ? JSON.parse(init.body) : null })
    const r = responses.shift() ?? { status: 200, text: 'data: {"choices":[{"delta":{"content":"pong"}}]}\n\ndata: [DONE]\n' }
    return { ok: r.status < 400, status: r.status, text: async () => r.text }
  }
})

test('the provider reason is its own sentence, unwrapped from nested error bodies', async () => {
  const { providerReason } = await import('../dist/chat/gateway.js')
  const body = '{"error":{"message":"received error while streaming: {\\"message\\": \\"Requested model is not available and no compliant same-model variant was found.\\", \\"type\\": \\"invalid_request_error\\"}","type":"error"}}'
  assert.equal(providerReason(body), 'Requested model is not available and no compliant same-model variant was found.')
  assert.equal(providerReason('plain text'), 'plain text')
})

test('a marked model carries the reason to the client', async () => {
  const { markImpaired } = await import('../dist/chat/routes.js')
  const verdicts = new Map([['me:vega', { ok: false, kind: 'unavailable', unlockUrl: null, message: '{"error":{"message":"model not served","type":"error"}}' }]])
  const r = markImpaired([{ id: 'me:vega/model-a' }], verdicts)
  assert.equal(r.impaired[0].reason, 'model not served')
  assert.equal(r.impaired[0].locked, false)
})
