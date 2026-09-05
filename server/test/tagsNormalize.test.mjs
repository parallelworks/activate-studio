import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { normalizeTag } = await import('../dist/tags.js')

test('labels are lowercase, hyphenated, stripped of punctuation, and capped', () => {
  assert.equal(normalizeTag('  Agent Persona '), 'agent-persona')
  assert.equal(normalizeTag('User: Matthew!'), 'user-matthew')
  assert.equal(normalizeTag('x'.repeat(60)).length, 40)
  assert.equal(normalizeTag('already-fine_ok'), 'already-fine_ok')
})
