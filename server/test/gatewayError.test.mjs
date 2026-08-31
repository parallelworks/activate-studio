// A provider error has to be reported as what it is. Telling someone their
// key expired when the request carried a parameter the provider rejects
// sends them to renew a credential that was never the problem.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))

const { gatewayChatMessage } = await import('../dist/chat/routes.js')

// The shape observed from the platform gateway on a codex-backed model:
// streaming plus a token cap, which the gateway translates into the
// provider's own spelling before the provider refuses it.
const PARAM_400 = 'gateway chat 400: {"error":{"message":"{\\"detail\\":\\"Unsupported parameter: max_output_tokens\\"}","type":"error"}}'
const PERSONAL = 'Matthew.Shaxted:codex/gpt-5.6-sol'

test('a rejected parameter is not reported as an expired key', () => {
  const m = gatewayChatMessage(PARAM_400, PERSONAL)
  assert.match(m, /parameter this provider does not accept/)
  assert.doesNotMatch(m, /expired/i)
  assert.doesNotMatch(m, /renew/i)
})

test('an auth failure on a personal-key model does point at the key', () => {
  const m = gatewayChatMessage('gateway chat 401: {"error":"unauthorized"}', PERSONAL)
  assert.match(m, /expired key fails exactly this way/)
  assert.match(m, /renew it/)
})

test('a 403 is treated as an auth failure too', () => {
  const m = gatewayChatMessage('gateway chat 403: forbidden', PERSONAL)
  assert.match(m, /renew it/)
})

test('a shared model never gets personal-key advice', () => {
  // A session or org model is not backed by the caller's own provider key,
  // so telling them to renew one is advice they cannot act on.
  for (const model of ['session:someone:studio-rag/studio-agent', 'org:shared/model-a']) {
    const m = gatewayChatMessage('gateway chat 401: nope', model)
    assert.doesNotMatch(m, /renew it/, `${model} should not be told to renew a key`)
    assert.match(m, /Pick another model/)
  }
})

// Verbatim from a locked GenAI provider key, August 2026.
const MASKED_400 = 'gateway chat 400: {"error":{"message":"An error occurred while generating the response","type":"error"}}'

test('the masked generation error names the locked key, without asserting it', () => {
  const m = gatewayChatMessage(MASKED_400, 'vschuetz:genaiprod/gemini-3.7-flash')
  assert.match(m, /locked/, 'a locked provider key is the actionable cause')
  assert.match(m, /re-check under model availability/, 'says where to confirm')
  // The gateway does not distinguish the causes, so the message must not
  // claim to know which one it is.
  assert.match(m, /and a fault at the provider itself/)
})

test('a shared model gets no provider-key advice on the masked error', () => {
  const m = gatewayChatMessage(MASKED_400, 'session:someone:studio-rag/studio-agent')
  assert.doesNotMatch(m, /locked/)
  assert.match(m, /Pick another model/)
})

test('a parameter rejection is still told apart from the masked failure', () => {
  const m = gatewayChatMessage(PARAM_400, PERSONAL)
  assert.doesNotMatch(m, /locked/, 'an explicit parameter complaint is not a credential problem')
})

// The documented GenAI.mil locked-key body, as the gateway relays it.
const LOCKED_401 = 'gateway chat 401: {"error":{"type":"unauthorized","message":"API key locked - visit the unlock URL to re-enable your key","unlock_url":"https://genai.example.mil/unlock/abc-123"}}'

test('a locked GenAI key hands the user its unlock link', () => {
  const m = gatewayChatMessage(LOCKED_401, PERSONAL)
  assert.match(m, /locked this API key/)
  assert.match(m, /https:\/\/genai\.example\.mil\/unlock\/abc-123/)
  assert.doesNotMatch(m, /renew it under the platform/, 'the unlock link outranks the generic auth advice')
})

test('an escaped unlock_url inside a relayed body is still found', () => {
  const escaped = 'gateway chat 401: {"error":{"message":"{\\"unlock_url\\": \\"https://genai.example.mil/unlock/xyz\\"}","type":"error"}}'
  const m = gatewayChatMessage(escaped, PERSONAL)
  assert.match(m, /unlock\/xyz/)
})

test('an unclassified failure says what is known and nothing more', () => {
  const m = gatewayChatMessage('gateway chat 500: upstream exploded', PERSONAL)
  assert.match(m, /Pick another model from the list while it recovers/)
  assert.doesNotMatch(m, /expired/i)
})

test('every message names the model and quotes the provider', () => {
  const m = gatewayChatMessage(PARAM_400, PERSONAL)
  assert.ok(m.startsWith(`${PERSONAL} cannot be called right now`))
  assert.match(m, /Provider said: gateway chat 400/)
  assert.match(m, /Other models are unaffected/)
})
