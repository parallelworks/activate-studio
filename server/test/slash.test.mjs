// Slash commands resolve before the model sees the message, so the set of
// names people guess at matters as much as the ones that were designed.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))

const { expandSlashCommand } = await import('../dist/chat/tools.js')

test('the names people guess at all reach the listing', () => {
  for (const name of ['help', 'commands', 'skills', 'tools', 'agents', 'personas', 'list']) {
    const r = expandSlashCommand(`/${name}`)
    assert.ok(r && typeof r === 'object' && r.listing, `/${name} should list the commands`)
  }
})

test('a built-in tool can be called by name with arguments', () => {
  const r = expandSlashCommand('/search_kb weather model')
  assert.equal(typeof r, 'string')
  assert.match(r, /Call the search_kb tool now/)
  assert.match(r, /weather model/)
})

test('an unknown bare command says so and shows what does exist', () => {
  const r = expandSlashCommand('/nosuchthing')
  assert.ok(r && typeof r === 'object')
  assert.match(r.listing, /There is no \/nosuchthing command/)
  assert.match(r.listing, /Slash commands resolve before the model sees the message/)
})

test('a pasted path is a path, not a command', () => {
  // The leading segment parses as a command name, so anything carrying more
  // path after it has to reach the model unchanged.
  assert.equal(expandSlashCommand('/shared/corpus/notes.md'), null)
  assert.equal(expandSlashCommand('/etc/hosts is the file'), null)
})

test('ordinary messages are untouched', () => {
  assert.equal(expandSlashCommand('what is in this knowledge base?'), null)
  assert.equal(expandSlashCommand('use 3/4 of the nodes'), null)
})
