import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = IX
fs.writeFileSync(path.join(KB, 'a.md'), 'a')
fs.writeFileSync(path.join(KB, 'b.md'), 'b')
const ov = await import('../dist/tagsOverlay.js')
const FILE = path.join(IX, 'tags-overlay.json')

const rewriteKeysWithForeignDevice = () => {
  // Simulate a rebuilt host: same inodes, same creation times, new fsid.
  const raw = JSON.parse(fs.readFileSync(FILE, 'utf8'))
  const entries = {}
  for (const [k, e] of Object.entries(raw.entries)) entries[`999999.${ov.inodeOf(k)}`] = e
  raw.entries = entries
  fs.writeFileSync(FILE, JSON.stringify(raw))
}

test('labels survive a host rebuild that changes the device id, and are re-keyed', async () => {
  ov.overlaySet('a.md', ['dataset'])
  ov.overlaySet('b.md', ['validated'])
  rewriteKeysWithForeignDevice()
  // The module caches the file; a fresh import sees the rewritten keys.
  const fresh = await import('../dist/tagsOverlay.js?v=1')
  assert.equal(Object.keys(fresh.overlayEntries()).every(k => k.startsWith('999999.')), true, 'fixture keys are foreign')
  const dropped = fresh.overlayPrune(new Set())
  assert.equal(dropped, 0, 'nothing is dropped when the files are all still there')
  assert.deepEqual(fresh.overlayGet('a.md'), ['dataset'])
  assert.deepEqual(fresh.overlayGet('b.md'), ['validated'])
  assert.equal(Object.keys(fresh.overlayEntries()).some(k => k.startsWith('999999.')), false, 'entries were re-keyed to the live device id')
})

test('a file that is really gone is pruned, and the store is backed up first', async () => {
  const fresh = await import('../dist/tagsOverlay.js?v=2')
  fs.unlinkSync(path.join(KB, 'b.md'))
  const liveKeyA = ov.overlayKey(path.join(KB, 'a.md'))
  const dropped = fresh.overlayPrune(new Set([liveKeyA]))
  assert.equal(dropped, 1)
  assert.deepEqual(fresh.overlayGet('a.md'), ['dataset'])
  assert.equal(fs.existsSync(`${FILE}.bak`), true, 'a backup precedes any removal')
})

test('adopting from the index fills an empty overlay without overwriting a present label', async () => {
  const fresh = await import('../dist/tagsOverlay.js?v=3')
  assert.equal(fresh.overlayAdopt('a.md', ['other']), false, 'a labeled file keeps its label')
  fs.writeFileSync(path.join(KB, 'c.md'), 'c')
  assert.equal(fresh.overlayAdopt('c.md', ['research']), true)
  assert.deepEqual(fresh.overlayGet('c.md'), ['research'])
})
