import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { expandSlashCommand } = await import('../dist/chat/tools.js')

test('a built-in tool by name becomes an instruction to call it with the arguments', () => {
  const r = expandSlashCommand('/search_kb federation strategy')
  assert.equal(typeof r, 'string')
  assert.match(r, /Call the search_kb tool now with arguments derived from: federation strategy/)
})
test('meta names are case-insensitive and answer without a model', () => {
  const r = expandSlashCommand('/Help')
  assert.equal(typeof r, 'object'); assert.match(r.listing, /Slash commands/)
})
test('an unknown command with content after it is left for the model; bare it lists', () => {
  assert.equal(expandSlashCommand('/shared/corpus/file.md is where it lives'), null)
  const r = expandSlashCommand('/nosuchthing')
  assert.match(r.listing, /There is no \/nosuchthing command/)
  assert.equal(expandSlashCommand('plain text'), null)
})
