// The corpus timeline: levels are built from the census, diffs tell moves
// from churn by inode, and snapshot metadata carries its own on-disk cost.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import zlib from 'node:zlib'

const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-hist-'))
process.env.INDEX_BASE = idx
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
const hist = await import('../dist/history.js')

function snap(id, entries) {
  fs.mkdirSync(path.join(idx, 'history'), { recursive: true })
  fs.writeFileSync(path.join(idx, 'history', `${id}.json.gz`), zlib.gzipSync(JSON.stringify({
    id: String(id), takenAt: Number(id), files: entries.length,
    bytes: entries.reduce((n, e) => n + e.size, 0), entries,
  })))
}

snap(1000, [
  { path: 'a.md', size: 10, mtime: 1, inode: 1 },
  { path: 'docs/b.md', size: 20, mtime: 1, inode: 2 },
  { path: 'docs/deep/c.md', size: 30, mtime: 1, inode: 3 },
])
snap(2000, [
  { path: 'a.md', size: 11, mtime: 2, inode: 1 },        // modified
  { path: 'moved/b.md', size: 20, mtime: 1, inode: 2 },  // renamed (same inode)
  { path: 'new.md', size: 5, mtime: 2, inode: 9 },       // added
])                                                        // c.md removed

test('snapshots list oldest first with their disk cost', () => {
  const all = hist.listSnapshots()
  assert.deepEqual(all.map(s => s.id), ['1000', '2000'])
  assert.ok(all.every(s => s.disk > 0))
})

test('a level rolls files into direct children', () => {
  const l = hist.level('1000', '')
  assert.deepEqual(l.dirs.map(d => d.name), ['docs'])
  assert.equal(l.dirs[0].files, 2)
  assert.equal(l.dirs[0].bytes, 50)
  assert.deepEqual(l.files.map(f => f.name), ['a.md'])
  assert.equal(l.totals.files, 3)
})

test('a diff tells a move from an add-plus-remove by inode', () => {
  const d = hist.diff('1000', '2000')
  assert.deepEqual(d.counts, { added: 1, removed: 1, modified: 1, renamed: 1 })
  assert.equal(d.renamed[0].from, 'docs/b.md')
  assert.equal(d.renamed[0].to, 'moved/b.md')
  assert.equal(d.removed[0].path, 'docs/deep/c.md')
  assert.equal(d.modified[0].path, 'a.md')
})
