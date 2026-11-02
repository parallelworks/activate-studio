// Which GUFI wrote an index, and what the Studio says of one that GUFI 0.8.0
// reads wrongly. GUFI 0.8.0 reads an index from before its `dev` column, and
// one from before the rollupscore rename, as it was; the one thing it gets
// wrong is a folder rolled up by a GUFI before 0.8.0. The detection, the
// plain words for it (search, Query, Settings), the swap recovery and the
// check of a tree before it is swapped in are tested here. The parts that
// run GUFI skip without it (GUFI_BIN, else /opt/gufi/bin).
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const HOME = tmp('ifmt-home-')
const KB = tmp('ifmt-kb-')
const IX = tmp('ifmt-ix-')
const GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
const REAL = fs.existsSync(path.join(GUFI, 'gufi_query')) && fs.existsSync(path.join(GUFI, 'gufi_dir2index')) && fs.existsSync(path.join(GUFI, 'gufi_sqlite3'))
Object.assign(process.env, { HOME, KB_ROOT: KB, INDEX_BASE: IX, GUFI_BIN: GUFI })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR', 'STUDIO_LIBRARIES']) delete process.env[k]

const F = await import('../dist/indexFormat.js')
const L = await import('../dist/libraries.js')
const O = await import('../dist/omissions.js')
const M = await import('../dist/indexModes.js')
const G = await import('../dist/gufi.js')

const CURRENT = 'name,type,dev,inode,mode,totfiles,totlinks,depth,rectype,pinode,isroot,canrollup,isrolledup'
const EARLIER = 'name,type,inode,mode,totfiles,totlinks,depth,rectype,pinode,isroot,canrollup,isrolledup'
const OLDER = 'name,type,inode,mode,totfiles,totlinks,depth,rectype,pinode,isroot,rollupscore'

test('the summary table\'s columns say which GUFI wrote the index', () => {
  assert.equal(F.formatOfColumns(CURRENT), 'current')
  assert.equal(F.formatOfColumns(EARLIER), 'earlier')
  assert.equal(F.formatOfColumns(OLDER), 'older')
  assert.equal(F.formatOfColumns(CURRENT.split(',')), 'current', 'a list as well as a joined value')
  assert.equal(F.formatOfColumns(' NAME,Totfiles,IsRoot,IsRolledUp,Dev\n'), 'current', 'case and spacing do not matter')
  assert.equal(F.formatOfColumns(''), null, 'no summary table')
  assert.equal(F.formatOfColumns('a,b,c'), null, 'not a GUFI summary')
  assert.equal(F.formatOfColumns('name,totfiles,isroot'), null, 'neither rolled-up column')
})

test('only an index GUFI 0.8.0 reads wrongly is named, in search, Query and Settings', () => {
  const rolled = { format: 'older', rolledUp: true }
  for (const f of [null, undefined, { format: 'current' }, { format: 'earlier' }, { format: 'older' }, { format: 'older', rolledUp: false }]) {
    assert.equal(F.formatGap(f), null, JSON.stringify(f))
    assert.equal(F.formatNote(f), null, JSON.stringify(f))
  }
  assert.match(F.formatGap(rolled), /^written by a GUFI before 0\.8\.0 and rolled up, so the folders it rolled up can list the wrong files; rebuild it with GUFI 0\.8\.0 or later$/)
  assert.match(F.formatNote(rolled), /^Written by a GUFI before 0\.8\.0 and rolled up.*0\.8\.0 or later\.$/, 'the same words, as a sentence')
  const lib = over => ({ id: 'x', label: 'Shelf', indexRoot: '/nowhere/x', sourceRoot: null, writable: false, primary: false, pinned: false, caps: { index: true, fullText: false, vectors: false }, meaning: null, depth: null, ...over })
  const gap = O.libraryGap(lib({ format: rolled }), { words: true, meaning: true })
  assert.deepEqual([gap.id, gap.partial], ['x', true], 'searched in part, not left out')
  assert.match(gap.reason, /rolled up, so the folders it rolled up can list the wrong files/)
  assert.equal(O.libraryGap(lib({ format: { format: 'earlier' } }), { words: true, meaning: true }), null, 'an index from before dev reads as it did')
  assert.equal(O.libraryGap(lib({ format: { format: 'older', rolledUp: false } }), { words: true, meaning: true }), null)
})

test('a query that asks an older index for a column GUFI 0.8.0 added says why in plain words', () => {
  const dev = 'Error: no such column: dev'
  assert.match(F.formatFailure(dev, { format: 'earlier' }), /^this index was written by a GUFI before 0\.8\.0 and has no dev column; rebuild it with GUFI 0\.8\.0 or later to add one$/)
  assert.match(F.formatFailure(dev, { format: 'older' }), /no dev column/)
  assert.equal(F.formatFailure(dev, { format: 'current' }), null, 'on a current index it is the person\'s own mistake')
  assert.equal(F.formatFailure(dev, null), null)
  assert.equal(F.formatFailure('Error: no such column: size2', { format: 'earlier' }), null, 'another column is not about the format')
  assert.match(F.formatFailure('Could not get isrolledup: no such column: canrollup', { format: 'older' }), /written by a GUFI before 0\.8\.0/)
  const l = { id: 'x', label: 'Shelf', indexRoot: '/nowhere/x', sourceRoot: null, caps: { index: true, fullText: false, vectors: false }, format: { format: 'earlier' } }
  assert.match(O.failureReason(l, new Error(dev)), /has no dev column/)
  assert.equal(O.failureReason({ ...l, format: null }, new Error(dev)), `search failed: ${dev}`)
})

test('the start-up line names an index from before 0.8.0', () => {
  const l = { id: 'x', label: 'Shelf', indexRoot: '/nowhere/x', sourceRoot: null, writable: false, primary: false, pinned: false, caps: { index: true, fullText: false, vectors: false }, meaning: null, depth: null }
  assert.doesNotMatch(O.libraryStateLine({ ...l, format: { format: 'current' } }), /written by/)
  assert.match(O.libraryStateLine({ ...l, format: { format: 'earlier' } }), /written by a GUFI before 0\.8\.0$/)
  assert.match(O.libraryStateLine({ ...l, format: { format: 'older', rolledUp: true } }), /\(0\.7\.0 or earlier, rolled up\)/)
})

// ---- a swap a stop cut off ----

test('a swap cut off between its renames is undone: the old tree comes back, and nothing is lost', () => {
  const dir = tmp('ifmt-swap-')
  const tree = path.join(dir, 'tree'), old = path.join(dir, '.old')
  const make = (p, text) => { fs.mkdirSync(p, { recursive: true }); fs.writeFileSync(path.join(p, 'db.db'), text) }
  // Whole: nothing to do, and the old one is left alone.
  make(tree, 'new'); make(old, 'old')
  assert.equal(M.recoverSwap(tree, [old]), null)
  assert.equal(fs.readFileSync(path.join(old, 'db.db'), 'utf8'), 'old')
  // Cut off: no tree, the old one aside.
  fs.rmSync(tree, { recursive: true })
  assert.equal(M.recoverSwap(tree, [old]), old)
  assert.equal(fs.readFileSync(path.join(tree, 'db.db'), 'utf8'), 'old')
  assert.ok(!fs.existsSync(old))
  // A tree with no db.db (the new one half moved in) gives way to the old one.
  const half = path.join(dir, 'half'), older = path.join(dir, 'half.old-1-2')
  fs.mkdirSync(half); fs.writeFileSync(path.join(half, 'partial'), 'x'); make(older, 'kept')
  assert.equal(M.recoverSwap(half, M.swapLeftovers(half)), older)
  assert.equal(fs.readFileSync(path.join(half, 'db.db'), 'utf8'), 'kept')
  // The newest of several is the one restored; one without a db.db is not.
  const t3 = path.join(dir, 't3')
  make(`${t3}.old-1-1`, 'first'); make(`${t3}.old-9-9`, 'second'); fs.mkdirSync(`${t3}.old-5-5`)
  const past = new Date(Date.now() - 60_000)
  fs.utimesSync(`${t3}.old-1-1`, past, past)
  assert.equal(M.recoverSwap(t3, M.swapLeftovers(t3)), `${t3}.old-9-9`)
  assert.equal(M.recoverSwap(path.join(dir, 'none'), []), null, 'nothing to restore')
})

// ---- with GUFI ----

const sqlite = script => execFileSync(path.join(GUFI, 'gufi_sqlite3'), [], { input: script, encoding: 'utf8', env: { ...process.env, LD_LIBRARY_PATH: [path.join(path.dirname(GUFI), 'lib'), process.env.LD_LIBRARY_PATH].filter(Boolean).join(':') } })
const indexOf = (name, files) => {
  const src = tmp(`ifmt-${name}-src-`), idx = tmp(`ifmt-${name}-idx-`)
  for (const [rel, text] of Object.entries(files)) { fs.mkdirSync(path.dirname(path.join(src, rel)), { recursive: true }); fs.writeFileSync(path.join(src, rel), text) }
  execFileSync(path.join(GUFI, 'gufi_dir2index'), ['-x', '-n', '2', src, idx], { env: { ...process.env, LD_LIBRARY_PATH: [path.join(path.dirname(GUFI), 'lib'), process.env.LD_LIBRARY_PATH].filter(Boolean).join(':') }, stdio: 'ignore' })
  return path.join(idx, path.basename(src))
}
/** The summary of every directory made to look like one written by an older
 *  GUFI: the views that name its columns dropped, rollupscore for isrolledup
 *  and canrollup, the top directory marked rolled up if asked. */
const asOlder = (tree, rolledUp) => execFileSync('python3', ['-c', `
import os, sqlite3, sys
tree, rolled = sys.argv[1], sys.argv[2] == '1'
top = True
for d, _, names in sorted(os.walk(tree)):
    if 'db.db' not in names: continue
    c = sqlite3.connect(os.path.join(d, 'db.db'))
    views = c.execute("SELECT name, sql FROM sqlite_master WHERE type = 'view'").fetchall()
    for v, _ in views: c.execute('DROP VIEW ' + v)
    c.execute('ALTER TABLE summary RENAME COLUMN isrolledup TO rollupscore')
    c.execute('ALTER TABLE summary DROP COLUMN canrollup')
    for t in ('entries', 'summary', 'pentries_rollup'):
        if 'dev' in [r[1] for r in c.execute('PRAGMA table_info(' + t + ')')]: c.execute('ALTER TABLE ' + t + ' DROP COLUMN dev')
    for v, sql in views:
        try: c.execute(sql.replace('isrolledup', 'rollupscore'))
        except sqlite3.OperationalError: pass
    if rolled and top: c.execute('UPDATE summary SET rollupscore = 1 WHERE isroot = 1')
    top = False
    c.commit(); c.close()
`, tree, rolledUp ? '1' : '0'])

test('detection reads the top summary table of a tree GUFI wrote, and of one made to look like an older GUFI\'s', { skip: !REAL && 'GUFI not installed', timeout: 120_000 }, async () => {
  const tree = indexOf('cur', { 'a/one.txt': 'one', 'b/two.txt': 'two', 'top.txt': 'top' })
  const cur = await L.detectFormat(tree)
  assert.ok(cur?.format === 'current' || cur?.format === 'earlier', `this host's GUFI wrote ${JSON.stringify(cur)}`)
  const older = indexOf('old', { 'a/one.txt': 'one', 'b/two.txt': 'two' })
  asOlder(older, false)
  assert.deepEqual(await L.detectFormat(older), { format: 'older', rolledUp: false })
  const rolled = indexOf('rolled', { 'a/one.txt': 'one', 'b/two.txt': 'two' })
  asOlder(rolled, true)
  assert.deepEqual(await L.detectFormat(rolled), { format: 'older', rolledUp: true })
  assert.equal(await L.detectFormat(tmp('ifmt-nodb-')).catch(() => null), null, 'a folder with no index says nothing')
})

test('a library\'s probe records the format, keeps it across a restart, and names a rolled-up older index in search and Query', { skip: !REAL && 'GUFI not installed', timeout: 120_000 }, async () => {
  const rolled = indexOf('probe-rolled', { 'a/one.txt': 'wombatword one', 'b/two.txt': 'wombatword two' })
  asOlder(rolled, true)
  const plain = indexOf('probe-plain', { 'a/one.txt': 'wombatword one' })
  asOlder(plain, false)
  const fs2 = await import('node:fs/promises')
  await fs2.mkdir(IX, { recursive: true })
  await L.addLibrary({ id: 'rolled', label: 'Rolled', indexRoot: rolled })
  await L.addLibrary({ id: 'plain', label: 'Plain', indexRoot: plain })
  await L.probeLibrary(rolled); await L.probeLibrary(plain)
  const rolledLib = L.getLibrary('rolled'), plainLib = L.getLibrary('plain')
  assert.deepEqual(rolledLib.format, { format: 'older', rolledUp: true })
  assert.deepEqual(plainLib.format, { format: 'older', rolledUp: false })
  assert.match(L.publicLibrary(rolledLib).formatNote, /rolled up, so the folders it rolled up can list the wrong files/)
  assert.equal(L.publicLibrary(plainLib).formatNote, null, 'an older index that reads as it did has nothing to say')
  // What was found is on disk: a restart knows it before any probe.
  L.resetLibrariesForTests({ keepKnownCaps: true })
  assert.deepEqual(L.getLibrary('rolled').format, { format: 'older', rolledUp: true })
  // Search and Query name it.
  const { default: Fastify } = await import('fastify')
  const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
  const { queryRoutes } = await import('../dist/query.js')
  const app = Fastify()
  app.setErrorHandler(sanitizedErrorHandler(app))
  await app.register(kbRoutes); await app.register(queryRoutes); await app.ready()
  const search = await app.inject({ method: 'GET', url: '/api/search?q=one.txt&library=rolled,plain' })
  const sj = search.json()
  const om = (sj.omitted ?? []).find(o => o.id === 'rolled')
  assert.ok(om && om.partial === true && /rolled up/.test(om.reason), JSON.stringify(sj.omitted))
  assert.equal((sj.omitted ?? []).find(o => o.id === 'plain'), undefined, 'the other one is searched in full')
  assert.ok(sj.hits.some(h => h.library.id === 'plain'))
  const q = await app.inject({ method: 'POST', url: '/api/query', payload: { sql: 'SELECT count(*) FROM vrpentries;', library: 'rolled' } })
  assert.equal(q.statusCode, 200)
  assert.match(q.json().omitted?.[0]?.reason ?? '', /rolled up/)
  const dev = await app.inject({ method: 'POST', url: '/api/query', payload: { sql: 'SELECT dev FROM vrpentries;', library: 'plain' } })
  assert.equal(dev.statusCode, 400)
  assert.match(dev.json().error, /^Plain: this index was written by a GUFI before 0\.8\.0 and has no dev column/)
  await app.close()
})

test('a tree GUFI wrote reads before it is swapped in, and one it cannot read is refused', { skip: !REAL && 'GUFI not installed', timeout: 60_000 }, async () => {
  const tree = indexOf('verify', { 'a/one.txt': 'one', 'b/two.txt': 'two' })
  assert.ok(await G.verifyTree(tree) >= 3)
  const empty = tmp('ifmt-empty-')
  await assert.rejects(G.verifyTree(empty), /could not read the index it wrote/)
  const junk = tmp('ifmt-junk-')
  fs.writeFileSync(path.join(junk, 'db.db'), 'not a database')
  await assert.rejects(G.verifyTree(junk), /could not read the index it wrote/)
})
