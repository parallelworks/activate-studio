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
