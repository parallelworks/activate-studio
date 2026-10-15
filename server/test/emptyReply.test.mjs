// What the user is told when a turn ends with no answer text. A provider
// that declines a request can return an empty completion (no tokens, finish
// reason "stop"); that turn did no reasoning and must not be described as
// if it had, even when the server pre-searched the knowledge base for it.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
const { emptyReplyNote } = await import('../dist/chat/routes.js')

test('an empty completion is reported as empty, with its finish reason and token count', () => {
  const note = emptyReplyNote({ modelReasoned: false, toolCalls: 0, finishReason: 'stop', completionTokens: 0 })
  assert.match(note, /^The model returned an empty reply \(finish reason: stop, no tokens generated\)/)
  assert.doesNotMatch(note, /reasoning/)
  assert.match(emptyReplyNote({ modelReasoned: false, toolCalls: 0, finishReason: null, completionTokens: null }), /\(finish reason: stop\)/)
})

test('reasoning, tool calls, and a content filter each get their own explanation', () => {
  assert.match(emptyReplyNote({ modelReasoned: true, toolCalls: 2, finishReason: 'length', completionTokens: 4000 }), /spent this turn reasoning/)
  assert.match(emptyReplyNote({ modelReasoned: false, toolCalls: 1, finishReason: 'stop', completionTokens: 0 }), /^The model ran 1 tool call and then returned no answer/)
  assert.match(emptyReplyNote({ modelReasoned: false, toolCalls: 0, finishReason: 'content_filter', completionTokens: 0 }), /content filter blocked this reply/)
})

test('a reply that could not be given links the other models, and the same one when retrying it may help', async () => {
  const { retryLinks, isRateLimit } = await import('../dist/chat/routes.js')
  const links = retryLinks([{ id: 'u:prov/model-b', name: 'u:prov/model-b' }], 'u:prov/model-a', 'msg-1')
  assert.equal(links, '\n\nRetry with: [model-b](#retry-with=u%3Aprov%2Fmodel-b&reply-to=msg-1) · [try model-a again](#retry-with=u%3Aprov%2Fmodel-a&reply-to=msg-1)')
  assert.equal(retryLinks([], null, null), '')
  assert.ok(isRateLimit('gateway chat 429: {"error":{"code":"rate_limit_exceeded"}}'))
  assert.ok(isRateLimit('The AI provider is rate limiting requests'))
  assert.ok(!isRateLimit('gateway chat 400: error occurred while generating the response'))
})

test('each model turn opens a new paragraph, so headings and tables render', async () => {
  const { turnSeparator } = await import('../dist/chat/routes.js')
  assert.equal(turnSeparator(''), '')
  assert.equal(turnSeparator('Let me check the workflows.'), '\n\n')
  assert.equal(turnSeparator('A list:\n'), '\n')
  assert.equal(turnSeparator('Done.\n\n'), '')
})

test('earlier turns reach the model as text: their tool calls and results are left out of the history', async () => {
  const { isEarlierToolTraffic } = await import('../dist/chat/routes.js')
  assert.equal(isEarlierToolTraffic({ role: 'tool', content: 'result', tool_call_id: 'c1' }), true)
  assert.equal(isEarlierToolTraffic({ role: 'tool', content: 'result' }), true)
  assert.equal(isEarlierToolTraffic({ role: 'assistant', content: null, tool_calls: [{ id: 'c1' }] }), true)
  assert.equal(isEarlierToolTraffic({ role: 'assistant', content: 'Here is what I found.', tool_calls: [{ id: 'c1' }] }), false)
  assert.equal(isEarlierToolTraffic({ role: 'assistant', content: 'Plain answer.' }), false)
  assert.equal(isEarlierToolTraffic({ role: 'user', content: 'And the margins?' }), false)
})

test('inputs written by plain name go where the workflow declares them, when exactly one input has that name', async () => {
  const { placeFlatInputs } = await import('../dist/chat/tools.js')
  const declared = [{ path: 'resource' }, { path: 'optimizer.batch_size' }, { path: 'optimizer.seed' }, { path: 'cluster.seed' }]
  const inputs = { batch_size: 4, seed: 7, resource: 'pw://u/x' }
  const moved = placeFlatInputs(inputs, declared)
  assert.deepEqual(inputs, { resource: 'pw://u/x', seed: 7, optimizer: { batch_size: 4 } }, 'an ambiguous name stays where it was')
  assert.deepEqual(moved, ['batch_size → optimizer.batch_size'])
})
