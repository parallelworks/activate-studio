// What a library's index holds (words, vectors) is found by a sample of its
// directory databases first, and what the sample misses is counted over the
// whole tree, in the background. Until that count says, search looks there
// anyway: a library whose vectors sit in a few folders the sample does not
// reach must not lose its meaning results. Needs GUFI (GUFI_BIN, else
// /opt/gufi/bin).
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const skip = !fs.existsSync(path.join(gufiBin, 'gufi_dir2index')) && 'GUFI not installed'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'libcaps-'))
Object.assign(process.env, { GUFI_BIN: gufiBin, KB_ROOT: path.join(dir, 'kb'), INDEX_BASE: path.join(dir, 'ix') })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR']) delete process.env[k]
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
write(path.join(dir, 'kb', 'main.md'), '# main\n')

// Thirty folders under the top, each with a folder three deep: the sample
// (twelve databases, breadth first) reaches the top and the first eleven.
// Only the last folder's deepest directory has vectors, and one other its
// words, as a library of code with a few documents would.
const index = (name, tables) => {
  const src = path.join(dir, 'src', name)
  for (let i = 0; i < 30; i++) write(path.join(src, `a${String(i).padStart(2, '0')}`, 'deep', 'x', 'y', 'f.txt'), 'x')
  fs.mkdirSync(path.join(dir, 'indexes'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', src, path.join(dir, 'indexes')], { stdio: 'ignore' })
  const root = path.join(dir, 'indexes', name)
  for (const [rel, sql] of tables) execFileSync(path.join(gufiBin, 'gufi_sqlite3'), [path.join(root, rel, 'db.db'), sql])
  return root
}
const GVEC = 'CREATE TABLE gvec (cid INTEGER PRIMARY KEY, fp384 BLOB); CREATE TABLE gvecmodel (model TEXT);'
const WORDS = 'CREATE TABLE words (tinode TEXT, fname TEXT, wordf TEXT);'
let few, none
if (!skip) {
  fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
  few = index('few', [['a29/deep/x/y', GVEC], ['a28/deep/x', WORDS]])
  none = index('none', [])
}

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const L = await import('../dist/libraries.js')
const { meaningSearched, wordsSearched } = await import('../dist/libraryScope.js')
const { libraryGap } = await import('../dist/omissions.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.ready()
const call = async (method, url, body) => {
  const r = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: r.statusCode, body: r.json() }
}
const lib = id => L.listLibraries('kb').find(l => l.id === id)

test('vectors in a few folders past the sample: searched before the count, and found by it', { skip }, async () => {
  assert.equal((await call('POST', '/api/libraries', { id: 'few', indexRoot: few })).status, 200)
  // The sample missed both; the count has not answered yet, so neither is known missing.
  const before = L.knownCaps(few)
  assert.deepEqual([before.caps.fullText, before.caps.vectors, before.exactAt], [false, false, undefined])
  assert.equal(L.lacks(lib('few'), 'vectors'), false)
  assert.equal(meaningSearched(lib('few')), true, 'not known: searched')
  assert.equal(wordsSearched(lib('few')), true)
  await L.capsCountedForTests()
  const after = L.knownCaps(few)
  assert.deepEqual([after.caps.fullText, after.caps.vectors, after.stale], [true, true, false])
  assert.ok(after.exactAt)
  assert.deepEqual(lib('few').caps, { index: true, fullText: true, vectors: true })
  assert.equal(meaningSearched(lib('few')), true)
  // Kept on disk with the time it was counted, for the next start.
  const saved = JSON.parse(fs.readFileSync(path.join(dir, 'ix', 'library-caps.json'), 'utf8'))[few]
  assert.deepEqual([saved.caps.vectors, saved.exactAt], [true, after.exactAt])
})

test('an index with none is known to have none once counted, and not searched for them', { skip }, async () => {
  assert.equal((await call('POST', '/api/libraries', { id: 'none', indexRoot: none })).status, 200)
  await L.capsCountedForTests()
  const l = lib('none')
  assert.deepEqual(l.caps, { index: true, fullText: false, vectors: false })
  assert.ok(L.knownCaps(none).exactAt)
  assert.deepEqual([L.lacks(l, 'vectors'), L.lacks(l, 'fullText')], [true, true])
  assert.deepEqual([meaningSearched(l), wordsSearched(l)], [false, false])
  // A library the Studio builds at a depth with meaning, seen through the
  // same index: named as not searched by meaning only once that is known.
  const built = { ...l, meaning: 'documents', depth: 'meaning-documents', build: { state: 'ready' } }
  assert.match(libraryGap(built, { words: true, meaning: true }).reason, /not searched by meaning: its index has no vectors yet/)
  // A change marks it stale: not known again, so searched, until counted.
  L.staleProbe(none)
  assert.equal(L.lacks(lib('none'), 'vectors'), false)
  assert.equal(meaningSearched(built), true)
  assert.equal(libraryGap({ ...built, caps: lib('none').caps }, { words: true, meaning: true }), null)
  await L.probeLibrary(none)
  await L.capsCountedForTests()
  assert.equal(L.lacks(lib('none'), 'vectors'), true)
})

test('a count that began before a change is not taken: the tree is counted again', { skip }, async () => {
  // Vectors arrive in a folder past the sample while a count may be running.
  execFileSync(path.join(gufiBin, 'gufi_sqlite3'), [path.join(none, 'a27', 'deep', 'db.db'), GVEC])
  L.staleProbe(none)
  await L.probeLibrary(none)
  await L.capsCountedForTests()
  assert.equal(lib('none').caps.vectors, true)
  assert.equal(L.lacks(lib('none'), 'vectors'), false)
})
