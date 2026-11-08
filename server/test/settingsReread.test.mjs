// settings.json may be written by another process (the Go server in front
// of this one, docs/GO-PORT.md): a change to the file is read on the next
// call, a half-written file leaves the last good read in place, and the
// server's own writes go through a rename.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const FILE = path.join(process.env.INDEX_BASE, 'settings.json')
const { loadSettings, effectiveSettings, updateSettings } = await import('../dist/settings.js')

test('a write by another process is read on the next call', () => {
  assert.deepEqual(loadSettings(), {})
  fs.writeFileSync(FILE, JSON.stringify({ appName: 'First' }))
  assert.equal(effectiveSettings().appName, 'First')
  // Same size, another name: the modification time or inode tells.
  const tmp = `${FILE}.other`
  fs.writeFileSync(tmp, JSON.stringify({ appName: 'Other' }))
  fs.renameSync(tmp, FILE)
  assert.equal(effectiveSettings().appName, 'Other')
})

test('a half-written file keeps the last good read', () => {
  fs.writeFileSync(FILE, JSON.stringify({ appName: 'Good' }))
  assert.equal(loadSettings().appName, 'Good')
  fs.writeFileSync(FILE, '{"appName": "Ba')
  assert.equal(loadSettings().appName, 'Good')
  fs.rmSync(FILE)
  assert.deepEqual(loadSettings(), {})
})

test('updateSettings writes through a rename and leaves no temporary file', async () => {
  await updateSettings({ kbLabel: 'Docs' })
  assert.equal(JSON.parse(fs.readFileSync(FILE, 'utf8')).kbLabel, 'Docs')
  assert.deepEqual(fs.readdirSync(process.env.INDEX_BASE).filter(f => f.startsWith('settings.json')), ['settings.json'])
  assert.equal(effectiveSettings().kbLabel, 'Docs')
})
