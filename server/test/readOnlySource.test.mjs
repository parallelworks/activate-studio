// A folder its owner may not write, built into an index: a library and
// the main folder whose top folder and folders inside are mode 0555, with
// files 0444, as a read-only share or snapshot is. gufi_dir2index gives each
// index folder its source folder's mode before the folders inside it make
// theirs, so without help everything inside a 0555 folder was left out and
// the build failed as it moved the tree into place (indexModes.ts). The
// build, a sync after a change, a change of depth, a removal and deleting
// the library are run for real; GUFI's record of the source keeps the
// source's modes. The parts that run GUFI skip without it, and as root,
// which every folder lets in.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const HOME = tmp('ro-home-'), IX = tmp('ro-ix-')
const KB = path.join(tmp('ro-kb-'), 'shelf')
const SRC = path.join(tmp('ro-src-'), 'archive')
const GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
const AS_ROOT = typeof process.getuid === 'function' && process.getuid() === 0
const skip = (!fs.existsSync(path.join(GUFI, 'gufi_dir2index')) && 'GUFI not installed') || (AS_ROOT && 'root writes in any folder')

Object.assign(process.env, { HOME, KB_ROOT: KB, INDEX_BASE: IX, GUFI_BIN: GUFI, PYTHON_BIN: 'python3', SWEEP_INTERVAL_SEC: '0' })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR', 'STUDIO_LIBRARIES']) delete process.env[k]

const write = (root, rel, text) => { fs.mkdirSync(path.dirname(path.join(root, rel)), { recursive: true }); fs.writeFileSync(path.join(root, rel), text) }
/** Every folder under root 0555 and every file 0444, deepest first. */
const readOnly = root => {
  for (const e of fs.readdirSync(root, { withFileTypes: true })) {
    const p = path.join(root, e.name)
    if (e.isDirectory()) readOnly(p); else fs.chmodSync(p, 0o444)
  }
  fs.chmodSync(root, 0o555)
}
/** A change inside a read-only folder: opened for it, then read only again,
 *  its time moved on so the sync cannot miss it. */
const change = (dir, fn) => {
  fs.chmodSync(dir, 0o755)
  try { fn() } finally {
    const later = new Date(Date.now() + 5000)
    fs.utimesSync(dir, later, later)
    fs.chmodSync(dir, 0o555)
  }
}
const openAll = root => { try { execFileSync('chmod', ['-R', 'u+rwx', root]) } catch { /* gone */ } }

for (const root of [KB, SRC]) {
  write(root, 'top.md', 'the top file about wombatword\n')
  write(root, 'reports/q1.md', 'first quarter wombatword\n')
  write(root, 'reports/deep/inner/q2.md', 'second quarter numbatword\n')
  write(root, 'scans/page.md', 'a scanned page\n')
  readOnly(root)
}
test.after(() => { for (const root of [KB, SRC]) openAll(root) })

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const B = await import('../dist/libraryBuild.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
const M = await import('../dist/indexModes.js')
const O = await import('../dist/omissions.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(B.libraryBuildRoutes)
await app.ready()
test.after(() => app.close())
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  let json = null
  try { json = res.json() } catch { /* text */ }
  return { status: res.statusCode, body: json }
}
const sleep = ms => new Promise(r => setTimeout(r, ms))
async function until(fn, ms = 60_000) {
  const end = Date.now() + ms
  for (;;) {
    const v = await fn()
    if (v) return v
    if (Date.now() > end) throw new Error('timed out')
    await sleep(50)
  }
}
const finished = id => until(async () => {
  const r = await j('GET', `/api/index/job/${id}`)
  return r.body.state !== 'queued' && r.body.state !== 'running' ? r.body : null
})
const names = async (q, root) => (await G.searchNames(q, 10, root)).map(h => h.path).sort()
const words = async (q, root) => (await G.searchFts(q, 10, root).catch(() => [])).map(h => h.path).sort()
/** What GUFI recorded of each folder and file: name and mode, from the index. */
const recorded = (root, table) => execFileSync(path.join(GUFI, 'gufi_query'), ['-d', '|', '-E', `SELECT name, mode FROM ${table};`, root], { encoding: 'utf8' })
  .trim().split('\n').filter(Boolean).map(l => l.split('|')).map(([n, m]) => [path.basename(n), Number(m) & 0o7777])

test('the index folders made ahead and opened after: only where a folder locks, and parents first', () => {
  const at = tmp('modes-')
  for (const d of ['a/b', 'a/.hidden', 'a/node_modules', 'a/shut', 'c']) fs.mkdirSync(path.join(at, d), { recursive: true })
  fs.chmodSync(path.join(at, 'a', 'shut'), 0o000)
  try {
    const entries = fs.readdirSync(path.join(at, 'a'), { withFileTypes: true })
    const skipped = new Set(['node_modules'])
    assert.equal(M.lockedEntry(path.join(at, 'a'), 'a', 0o40755, entries, skipped), null, 'its owner writes it')
    const l = M.lockedEntry(path.join(at, 'a'), 'a', 0o40555, entries, skipped)
    if (AS_ROOT) { assert.equal(l, null, 'root writes in any folder'); return }
    // A folder this process cannot open gets no index folder from gufi_dir2index either.
    assert.deepEqual([l.rel, l.dirs.sort()], ['a', ['.hidden', 'b']])
    assert.ok(M.locks(0o40500) && M.locks(0o40055) && !M.locks(0o40700))
    assert.deepEqual(M.lockedUnder([{ rel: '', dirs: ['a'] }, { rel: 'a', dirs: ['b'] }, { rel: 'a/b', dirs: [] }, { rel: 'ab', dirs: [] }], 'a'),
      [{ rel: '', dirs: ['b'] }, { rel: 'b', dirs: [] }])
  } finally { fs.chmodSync(path.join(at, 'a', 'shut'), 0o755) }
})

test('a tree an earlier build left with folders of mode 0555 is removed whole, and what gufi_dir2index could not make is named by its source folder', async () => {
  const at = tmp('rm-')
  const tree = path.join(at, 'old')
  write(tree, 'x/y/db.db', '')
  write(tree, 'x/db.db', '')
  fs.chmodSync(path.join(tree, 'x', 'y'), 0o555)
  fs.chmodSync(path.join(tree, 'x'), 0o555)
  await M.rmTree(tree)
  assert.equal(fs.existsSync(tree), false)
  write(tree, 'x/db.db', '')
  fs.chmodSync(path.join(tree, 'x'), 0o555)
  M.rmTreeSync(tree)
  assert.equal(fs.existsSync(tree), false)
  await M.rmTree(path.join(at, 'never-there'))

  const err = 'Creating GUFI tree\nmkdir /ix/.build/archive/reports/deep/db.db failure: 13 Permission denied\nmkdir /elsewhere/db.db failure: 13 Permission denied\n'
  assert.deepEqual(M.unmadeIndexDirs(err), ['/ix/.build/archive/reports/deep', '/elsewhere'])
  assert.equal(M.sourceOf('/ix/.build/archive/reports/deep', '/ix/.build/archive', '/src/archive'), '/src/archive/reports/deep')
  assert.equal(M.sourceOf('/ix/.build/archive', '/ix/.build/archive', '/src/archive'), '/src/archive')
  assert.equal(M.sourceOf('/elsewhere', '/ix/.build/archive', '/src/archive'), null)
  // Not taken for a source folder the build could not read.
  assert.deepEqual(O.deniedPaths(err), [])
})

const ID = 'archive'
test('a library of a read-only folder builds whole, keeps the source\'s modes in its record, and opens its index folders', { skip, timeout: 120_000 }, async () => {
  const r = await j('POST', '/api/libraries/build', { sourceRoot: SRC, label: 'Archive', id: ID, depth: 'names' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const job = await finished(r.body.job.id)
  assert.equal(job.state, 'done', job.error ?? '')
  const lib = L.getLibrary(ID)
  assert.equal(lib.build.state, 'ready', lib.build.error ?? '')
  assert.equal(lib.build.files, 4)
  assert.equal(lib.build.dirs, 5)
  assert.deepEqual(await names('q2', lib.indexRoot), ['reports/deep/inner/q2.md'], 'a folder three levels inside a 0555 one')
  assert.deepEqual(await names('page', lib.indexRoot), ['scans/page.md'])
  // GUFI's record of the source is the source's: folders 0555, files 0444.
  const dirs = recorded(lib.indexRoot, 'vrsummary')
  assert.equal(dirs.length, 5)
  for (const [n, mode] of dirs) assert.equal(mode, 0o555, n)
  for (const [n, mode] of recorded(lib.indexRoot, 'vrpentries')) assert.equal(mode, 0o444, n)
  // The index folders: the source's group and other bits, and this process's to write.
  for (const rel of ['', 'reports', 'reports/deep', 'reports/deep/inner', 'scans']) {
    assert.equal(fs.statSync(path.join(lib.indexRoot, rel)).mode & 0o777, 0o755, rel || '(top)')
  }
  assert.equal(O.readDenied(ID), null, 'nothing left out')
})

test('a change of depth writes the text into the index of the read-only folder', { skip, timeout: 120_000 }, async () => {
  const lib = L.getLibrary(ID)
  assert.deepEqual(await words('wombatword', lib.indexRoot), [])
  assert.equal((await j('PUT', `/api/libraries/${ID}/depth`, { depth: 'text' })).status, 200)
  const done = await B.syncLibrary(ID)
  assert.ok(done.includes('(index depth: text)'), JSON.stringify(done))
  assert.equal(L.getLibrary(ID).build.error, null)
  assert.deepEqual(await words('wombatword', lib.indexRoot), ['reports/q1.md', 'top.md'])
  assert.deepEqual(await words('numbatword', lib.indexRoot), ['reports/deep/inner/q2.md'])
})

test('a sync after a change indexes the folder that changed, the top folder\'s files, and drops a folder that is gone', { skip, timeout: 120_000 }, async () => {
  const lib = L.getLibrary(ID)
  change(path.join(SRC, 'reports', 'deep', 'inner'), () => write(SRC, 'reports/deep/inner/q3.md', 'third quarter quollword\n'))
  let done = await B.syncLibrary(ID)
  assert.deepEqual(done, ['reports/deep/inner'])
  assert.equal(L.getLibrary(ID).build.error, null)
  assert.deepEqual(await words('quollword', lib.indexRoot), ['reports/deep/inner/q3.md'])
  assert.equal(fs.statSync(path.join(lib.indexRoot, 'reports', 'deep', 'inner')).mode & 0o777, 0o755)

  change(SRC, () => {
    write(SRC, 'later.md', 'a later top file about dunnartword\n')
    fs.chmodSync(path.join(SRC, 'scans'), 0o755)
    fs.rmSync(path.join(SRC, 'scans'), { recursive: true })
  })
  done = await B.syncLibrary(ID)
  assert.deepEqual(done.sort(), ['(top folder)', 'scans (removed)'])
  assert.equal(L.getLibrary(ID).build.error, null)
  assert.deepEqual(await words('dunnartword', lib.indexRoot), ['later.md'])
  assert.equal(fs.existsSync(path.join(lib.indexRoot, 'scans')), false)
  assert.deepEqual(await B.syncLibrary(ID), [], 'nothing new')
})

test('an index an earlier version built, with folders left out and 0555 index folders, is filled in by the next sync', { skip, timeout: 120_000 }, async () => {
  const lib = L.getLibrary(ID)
  // As before the fix: the index folder of reports/deep had the source's
  // 0555, and nothing inside it was indexed.
  fs.rmSync(path.join(lib.indexRoot, 'reports', 'deep', 'inner'), { recursive: true })
  fs.chmodSync(path.join(lib.indexRoot, 'reports', 'deep'), 0o555)
  fs.chmodSync(path.join(lib.indexRoot, 'reports'), 0o555)
  const done = await B.syncLibrary(ID)
  assert.deepEqual(done, ['reports/deep/inner'])
  assert.deepEqual(await names('q3', lib.indexRoot), ['reports/deep/inner/q3.md'])
  assert.equal(fs.statSync(path.join(lib.indexRoot, 'reports')).mode & 0o777, 0o755)
  // And a rebuild replaces such a tree.
  fs.chmodSync(path.join(lib.indexRoot, 'reports', 'deep'), 0o555)
  const r = await j('POST', `/api/libraries/${ID}/rebuild`, {})
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const job = await finished(r.body.job.id)
  assert.equal(job.state, 'done', job.error ?? '')
  assert.equal(L.getLibrary(ID).build.files, 5)
  assert.deepEqual(await words('numbatword', L.getLibrary(ID).indexRoot), ['reports/deep/inner/q2.md'])
})

test('deleting the library removes its index of the read-only folder and leaves the folder', { skip }, async () => {
  const own = L.builtLibraryDir(ID)
  assert.ok(fs.existsSync(own))
  assert.equal((await j('DELETE', `/api/libraries/${ID}`)).status, 200)
  assert.equal(fs.existsSync(own), false)
  assert.ok(fs.existsSync(path.join(SRC, 'reports', 'deep', 'inner', 'q2.md')))
  assert.equal(fs.statSync(SRC).mode & 0o777, 0o555, 'the folder itself is never touched')
})

test('the main folder, read only, builds whole and syncs a change', { skip, timeout: 120_000 }, async () => {
  assert.equal((await j('PUT', '/api/index/settings', { depth: 'text' })).status, 200)
  const job = await finished((await j('POST', '/api/index/build')).body.id)
  assert.equal(job.state, 'done', job.error ?? '')
  const root = path.join(IX, 'gufi', path.basename(KB))
  assert.deepEqual(await names('q2', root), ['reports/deep/inner/q2.md'])
  assert.deepEqual(await words('wombatword', root), ['reports/q1.md', 'top.md'])
  for (const [n, mode] of recorded(root, 'vrsummary')) assert.equal(mode, 0o555, n)
  assert.equal(fs.statSync(path.join(root, 'reports', 'deep')).mode & 0o777, 0o755)

  change(path.join(KB, 'reports', 'deep'), () => write(KB, 'reports/deep/q4.md', 'fourth quarter bilbyword\n'))
  const changed = (await j('POST', '/api/index/sweep')).body.changed
  assert.ok(changed.some(c => c.startsWith('reports')), JSON.stringify(changed))
  assert.deepEqual(await words('bilbyword', root), ['reports/deep/q4.md'])
  assert.deepEqual(await names('q2', root), ['reports/deep/inner/q2.md'], 'the folder below it is still there')
  change(KB, () => write(KB, 'later.md', 'a later file about dibblerword\n'))
  await j('POST', '/api/index/sweep')
  assert.deepEqual(await words('dibblerword', root), ['later.md'])
})
