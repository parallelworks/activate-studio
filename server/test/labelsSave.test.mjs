// Labels that are applied must be kept and shown. Covers: a main library
// whose folder was copied or restored after it was indexed (same names and
// mtimes, new inodes), where the label row has to join to the index's
// inode; the main library's labels in the tree with All libraries
// selected; a label on a path that is not there, which must be refused
// rather than reported as saved; and the assistant's apply_labels, which
// applies in the library the paths are in.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'lsv-kb-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'lsv-ix-'))
const SRC = fs.mkdtempSync(path.join(os.tmpdir(), 'lsv-src-'))
const IDX = fs.mkdtempSync(path.join(os.tmpdir(), 'lsv-idx-'))
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'lsv-bin-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = IX
process.env.STUDIO_SECTIONS = 'library,search,overview'
fs.writeFileSync(path.join(bin, 'gufi_query'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
process.env.GUFI_BIN = bin
process.env.PYTHON_BIN ??= 'python3'

fs.mkdirSync(path.join(KB, 'images'))
fs.writeFileSync(path.join(KB, 'images', 'a.md'), '# a\n')
fs.writeFileSync(path.join(SRC, 'r.md'), '# r\n')
fs.writeFileSync(path.join(IDX, 'db.db'), '')

// The index of the main library as it was before the folder was copied:
// the same names, the inodes the files had then.
const OLD_DIR_INODE = '990001'
const OLD_FILE_INODE = '990002'
const GI = path.join(IX, 'gufi', path.basename(KB), 'images')
fs.mkdirSync(GI, { recursive: true })
const py = (script, ...args) => execFileSync(process.env.PYTHON_BIN, ['-c', script, ...args], { encoding: 'utf8' })
py(`
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute('CREATE TABLE summary (name TEXT, inode TEXT, isroot INT64)')
db.execute('CREATE TABLE entries (name TEXT, inode TEXT)')
db.execute('CREATE TABLE xattrs_pwd (inode TEXT, name BLOB, value BLOB)')
db.execute("INSERT INTO summary VALUES ('images', ?, 1)", (sys.argv[2],))
db.execute("INSERT INTO entries VALUES ('a.md', ?)", (sys.argv[3],))
db.commit()
`, path.join(GI, 'db.db'), OLD_DIR_INODE, OLD_FILE_INODE)
const rows = () => JSON.parse(py(`
import json, sqlite3, sys
db = sqlite3.connect(sys.argv[1])
print(json.dumps(sorted([r[0], r[1]] for r in db.execute("SELECT CAST(inode AS TEXT), CAST(value AS TEXT) FROM xattrs_pwd WHERE CAST(name AS TEXT) = 'user.studio.tags'"))))
`, path.join(GI, 'db.db')))

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { tagRoutes } = await import('../dist/tags.js')
const { executeTool } = await import('../dist/chat/tools.js')
const LL = await import('../dist/libraryLabels.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(tagRoutes)
await app.ready()
test.after(async () => {
  await app.close()
  for (const d of [KB, IX, SRC, IDX, bin]) fs.rmSync(d, { recursive: true, force: true })
})
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}

test('a second library, so All libraries lists more than one', async () => {
  const r = await j('POST', '/api/libraries', { id: 'site', label: 'Site index', indexRoot: IDX, sourceRoot: SRC })
  assert.equal(r.status, 200, JSON.stringify(r.body))
})

test('a label on a copied folder is written against the inode the index has for it', async () => {
  assert.notEqual(String(fs.statSync(path.join(KB, 'images')).ino), OLD_DIR_INODE)
  const r = await j('POST', '/api/kb/tags', { paths: ['images', 'images/a.md'], add: ['validated'] })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  // Keyed by the disk's inode the rows would join to nothing in the index,
  // and the label would read back as never applied.
  assert.deepEqual(rows(), [[OLD_DIR_INODE, 'validated'], [OLD_FILE_INODE, 'validated']])
  await j('POST', '/api/kb/tags', { paths: ['images/a.md'], remove: ['validated'] })
  assert.deepEqual(rows(), [[OLD_DIR_INODE, 'validated']])
})

test('with All libraries selected, the tree shows the main library\'s labels', async () => {
  const top = await j('GET', '/api/kb/tree?library=all&path=kb')
  assert.equal(top.status, 200)
  const images = top.body.entries.find(e => e.path === 'kb/images')
  assert.ok(images, JSON.stringify(top.body))
  assert.deepEqual(images.tags, ['validated'])
  await j('POST', '/api/kb/tags?library=all', { paths: ['kb/images/a.md'], add: ['draft'] })
  const inner = await j('GET', '/api/kb/tree?library=all&path=kb/images')
  assert.deepEqual(inner.body.entries.find(e => e.path === 'kb/images/a.md')?.tags, ['draft'])
})

test('a label on a path that is not there is refused, not reported as saved', async () => {
  const r = await j('POST', '/api/kb/tags', { paths: ['nowhere/ghost.md'], add: ['x'] })
  assert.equal(r.status, 404, JSON.stringify(r.body))
  assert.match(r.body.error, /no such file: nowhere\/ghost\.md/)
})

test('the assistant applies labels in the library the paths are in', async () => {
  const site = await executeTool('apply_labels', JSON.stringify({ paths: 'r.md', add: 'reviewed', library: 'site' }))
  assert.match(site.result, /kept by the Studio/)
  assert.deepEqual(LL.libraryLabelsOf('site', 'r.md'), ['reviewed'])
  // The conversation's library is the default.
  await executeTool('apply_labels', JSON.stringify({ paths: 'r.md', remove: 'reviewed' }), { library: 'site' })
  assert.deepEqual(LL.libraryLabelsOf('site', 'r.md'), [])
  // With All libraries selected and none named, the main library.
  const main = await executeTool('apply_labels', JSON.stringify({ paths: 'images', add: 'imagery' }), { library: 'all' })
  assert.match(main.result, /on the files/)
  assert.deepEqual((await j('GET', '/api/kb/tags?path=images')).body.stored, 'files')
  // A path of another library is not labeled in the main one.
  const wrong = await executeTool('apply_labels', JSON.stringify({ paths: 'r.md', add: 'x' }))
  assert.match(wrong.result, /no such file: r\.md/)
})
