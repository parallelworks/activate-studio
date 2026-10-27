// A library never drops out of search or Query silently: every response
// names the libraries it left out, or searched only in part, and why; what
// the Studio knows a library's index holds survives a build, a failed probe
// and a restart; a build cut off by a stop is taken up again at the next
// start; and a folder the Studio may not read (macOS privacy settings,
// standing in here as chmod 000) is reported, not counted as indexed, and
// kept as last indexed. The parts that run GUFI skip without it
// (GUFI_BIN, else /opt/gufi/bin).
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const HOME = tmp('omit-home-')
const KB = tmp('omit-kb-')
const IX = tmp('omit-ix-')
const GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
const REAL = fs.existsSync(path.join(GUFI, 'gufi_query')) && fs.existsSync(path.join(GUFI, 'gufi_dir2index'))
// chmod 000 keeps nobody but root out.
const AS_ROOT = typeof process.getuid === 'function' && process.getuid() === 0

// Python runs through this wrapper, which holds enrich.py while HOLD exists.
const HOLD = path.join(HOME, 'hold')
const WRAPPER = path.join(HOME, 'python')
// While held it also starts a worker that keeps the output open, as
// enrich.py's process pool does; a stop has to end it too.
fs.writeFileSync(WRAPPER, `#!/bin/sh
for a in "$@"; do
  case "$a" in *enrich.py)
    if [ -f '${HOLD}' ]; then python3 -c 'import os, time
while os.path.exists("${HOLD}"): time.sleep(0.1)' "$@" & fi
    while [ -f '${HOLD}' ]; do sleep 0.1; done; break;;
  esac
done
exec python3 "$@"
`, { mode: 0o755 })

Object.assign(process.env, { HOME, KB_ROOT: KB, INDEX_BASE: IX, GUFI_BIN: GUFI, PYTHON_BIN: WRAPPER, SWEEP_INTERVAL_SEC: '0' })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR', 'STUDIO_LIBRARIES']) delete process.env[k]

const write = (root, rel, text) => { fs.mkdirSync(path.dirname(path.join(root, rel)), { recursive: true }); fs.writeFileSync(path.join(root, rel), text) }
write(KB, 'notes/main.md', 'a note in the main folder about quokkaword\n')

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { queryRoutes } = await import('../dist/query.js')
const B = await import('../dist/libraryBuild.js')
const L = await import('../dist/libraries.js')
const O = await import('../dist/omissions.js')
const S = await import('../dist/libraryScope.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(queryRoutes)
await app.register(B.libraryBuildRoutes)
await app.ready()
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
}, 120_000)
const search = async q => {
  const r = await j('GET', `/api/search?q=${encodeURIComponent(q)}&library=all`)
  assert.equal(r.status, 200, JSON.stringify(r.body))
  return r.body
}
const query = async body => {
  const r = await j('POST', '/api/query', { library: 'all', ...body })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  return r.body
}
const textQuery = word => ({ builder: { source: 'files', filters: [{ field: 'text', op: 'match', value: word }], groupBy: 'none', sort: 'size', dir: 'desc', limit: 100 } })
const omissionOf = (body, id) => (body.omitted ?? []).find(o => o.id === id)
// What the server does when it starts again: memory is gone, the record on disk stays.
const restart = async () => {
  L.resetLibrariesForTests({ keepKnownCaps: true })
  const { invalidateDbList } = await import('../dist/gufi.js')
  invalidateDbList()
}

// ---- the reasons, without GUFI ----

const lib = over => ({ id: 'x', label: 'Shelf', indexRoot: '/nowhere/x', sourceRoot: null, writable: false, primary: false, pinned: false, caps: { index: true, fullText: true, vectors: false }, meaning: null, depth: null, ...over })
const build = over => ({ indexDir: '/nowhere', cacheRoot: '/nowhere/c', managed: true, state: 'ready', error: null, builtAt: null, files: null, dirs: null, ...over })

test('each state that keeps a library out of search, or searches it in part, has its reason', () => {
  const want = { words: true, meaning: true }
  // Words and vectors are missing only once the whole tree was counted;
  // before, search looks there and names nothing.
  L.setKnownCapsForTests('/nowhere/x', { index: true, fullText: false, vectors: false }, false)
  assert.equal(O.libraryGap(lib({ depth: 'meaning-documents', meaning: 'documents', caps: { index: true, fullText: false, vectors: false }, build: build() }), want), null)
  L.setKnownCapsForTests('/nowhere/x', { index: true, fullText: false, vectors: false })
  assert.deepEqual(O.libraryGap(lib({ caps: { index: false, fullText: false, vectors: false }, build: build({ state: 'building' }) }), want),
    { id: 'x', label: 'Shelf', reason: 'being built; searchable once its first pass is done', partial: false })
  assert.match(O.libraryGap(lib({ caps: { index: false, fullText: false, vectors: false }, build: build({ state: 'failed', error: 'gufi_dir2index: no space left on device' }) }), want).reason,
    /^its build failed: gufi_dir2index: no space left on device$/)
  assert.equal(O.libraryGap(lib({ caps: { index: false, fullText: false, vectors: false } }), want).reason, 'index not found')
  const noWords = O.libraryGap(lib({ depth: 'text', caps: { index: true, fullText: false, vectors: false }, build: build() }), want)
  assert.deepEqual([noWords.reason, noWords.partial], ['words not searched: its index has no text yet', true])
  assert.equal(O.libraryGap(lib({ depth: 'ocr', caps: { index: true, fullText: false, vectors: false }, build: build({ state: 'updating' }) }), want).reason,
    'words not searched yet: its text is being read')
  assert.equal(O.libraryGap(lib({ depth: 'meaning-documents', meaning: 'documents', caps: { index: true, fullText: true, vectors: false }, build: build() }), want).reason,
    'not searched by meaning: its index has no vectors yet')
  assert.equal(O.libraryGap(lib({ depth: 'meaning-documents', meaning: 'documents', caps: { index: true, fullText: true, vectors: false }, build: build() }), { words: true, meaning: false }), null,
    'meaning is not missed by a search that does not ask for it')
  // An index connected as it is has no words by its nature: not a gap.
  assert.equal(O.libraryGap(lib({ caps: { index: true, fullText: false, vectors: false } }), want), null)
  assert.equal(O.libraryGap(lib({ depth: 'names', caps: { index: true, fullText: false, vectors: false }, build: build() }), want), null, 'names only: words are not expected')
})

test('a folder macOS will not let the Studio read is named with where to allow it', { skip: AS_ROOT && 'root reads everything' }, () => {
  assert.equal(O.deniedReason('/Users/x/Desktop/Shelf', 'darwin'),
    'macOS has not allowed ACTIVATE Studio to read /Users/x/Desktop/Shelf; allow it in System Settings, Privacy & Security, Files and Folders (or Full Disk Access)')
  assert.equal(O.deniedReason('/data/shelf', 'linux'), 'permission denied reading /data/shelf')
  const src = tmp('omit-locked-')
  fs.chmodSync(src, 0o000)
  try {
    const gap = O.libraryGap(lib({ sourceRoot: src, depth: 'text', build: build() }), { words: true, meaning: false })
    assert.equal(gap.partial, true, 'its index still answers')
    assert.match(gap.reason, new RegExp(`^${O.deniedReason(src).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}; results are from its last pass$`))
    const none = O.libraryGap(lib({ sourceRoot: src, caps: { index: false, fullText: false, vectors: false } }), { words: true, meaning: false })
    assert.deepEqual([none.reason, none.partial], [O.deniedReason(src), false])
  } finally { fs.chmodSync(src, 0o755) }
})

test('permission errors in a tool\'s output name the paths', () => {
  const err = [
    'Could not open directory "/src/Desktop/private": Operation not permitted (1)',
    '  cannot list /src/shelf/locked: [Errno 1] Operation not permitted: \'/src/shelf/locked\'',
    '  extract failed /src/shelf/a b.pdf: [Errno 13] Permission denied: \'/src/shelf/a b.pdf\'',
    'some other warning about /src/shelf/c.txt',
  ].join('\n')
  assert.deepEqual(O.deniedPaths(err), ['/src/Desktop/private', '/src/shelf/locked', '/src/shelf/a b.pdf'])
})

test('a failure while searching says why in plain words', () => {
  const remote = lib({ site: 'cluster-a' })
  assert.match(O.failureReason(remote, new Error('ssh: connect to host cluster-a port 22: Connection refused')), /^not reachable over ssh: /)
  assert.match(O.failureReason(lib({ sourceRoot: '/src/shelf' }), Object.assign(new Error('EPERM: operation not permitted, scandir'), { code: 'EPERM' })), /^permission denied reading \/src\/shelf$|^macOS has not allowed/)
  assert.equal(O.failureReason(lib(), new Error('ENOENT: no such file or directory, open db.db')), 'index not found')
  assert.equal(O.failureReason(lib(), new Error('gufi_query: database disk image is malformed')), 'search failed: gufi_query: database disk image is malformed')
  const list = []
  O.addOmission(list, { id: 'x', label: 'Shelf', reason: 'words not searched yet: its text is being read', partial: true })
  O.addOmission(list, { id: 'x', label: 'Shelf', reason: 'did not answer within 20 s', partial: false })
  assert.deepEqual(list, [{ id: 'x', label: 'Shelf', reason: 'words not searched yet: its text is being read; did not answer within 20 s', partial: false }])
})

// ---- with GUFI: search, Query, capabilities, resume, permissions ----

const SRC = tmp('omit-shelf-')
for (let i = 0; i < 9; i++) write(SRC, `shelf${i % 3}/doc${i}.md`, `document ${i} about the wombatword survey\n`)

test('a main folder never indexed is named "not indexed yet" by search and Query alike', { skip: !REAL && 'GUFI not installed' }, async () => {
  // Its index folder can exist, empty, before the first build: that is not an index.
  fs.mkdirSync(path.join(IX, 'gufi', path.basename(KB)), { recursive: true })
  const s = await search('quokkaword')
  const o = omissionOf(s, 'kb')
  assert.ok(o, `named in search: ${JSON.stringify(s.omitted)}`)
  assert.deepEqual([o.reason, o.partial], ['not indexed yet', false])
  // The one walk is the default; the gufi_vt setting takes its own route,
  // through the same check before anything runs.
  process.env.STUDIO_SEARCH_ENGINE = 'gufi_vt'
  try {
    assert.equal(omissionOf(await search('quokkaword'), 'kb')?.reason, 'not indexed yet', 'with search on gufi_vt too')
  } finally { delete process.env.STUDIO_SEARCH_ENGINE }
  const q = await j('POST', '/api/query', { library: 'all', canned: 'largest', n: 5 })
  assert.equal(q.status, 409, JSON.stringify(q.body))
  assert.match(JSON.stringify(q.body), /cannot be queried: not indexed yet/)
  assert.equal(L.getLibrary('kb').caps.index, false)
})

test('the main folder', { skip: !REAL && 'GUFI not installed', timeout: 120_000 }, async () => {
  const main = await finished((await j('POST', '/api/index/build')).body.id)
  assert.equal(main.state, 'done', main.error ?? '')
})

test('a main folder built once and then without its index is "index not found" in search and Query alike', { skip: !REAL && 'GUFI not installed' }, async () => {
  const db = path.join(IX, 'gufi', path.basename(KB), 'db.db')
  assert.ok(fs.existsSync(db), 'built')
  fs.renameSync(db, `${db}.away`)
  try {
    assert.deepEqual(omissionOf(await search('quokkaword'), 'kb')?.reason, 'index not found')
    const q = await j('POST', '/api/query', { library: 'all', canned: 'largest', n: 5 })
    assert.match(JSON.stringify(q.body), /cannot be queried: index not found/)
  } finally { fs.renameSync(`${db}.away`, db) }
  assert.equal(omissionOf(await search('quokkaword'), 'kb'), undefined, 'named no more once it is back')
})

test('a library being built is named in search and Query as left out, and is not asked', { skip: !REAL && 'GUFI not installed', timeout: 120_000 }, async () => {
  fs.writeFileSync(HOLD, '')
  let job
  try {
    // Names only would finish at once; Text holds in the text pass.
    const r = await j('POST', '/api/libraries/build', { sourceRoot: SRC, label: 'Shelf', id: 'shelf', depth: 'text' })
    assert.equal(r.status, 200, JSON.stringify(r.body))
    job = r.body.job
    // Its first build swaps in the names, then reads the text.
    await until(() => fs.existsSync(path.join(L.addedDef('shelf').indexRoot, 'db.db')))
    await sleep(300)
    const s = await search('wombatword')
    const o = omissionOf(s, 'shelf')
    assert.ok(o, `named: ${JSON.stringify(s.omitted)}`)
    assert.equal(o.partial, true, 'its names answer already')
    assert.equal(o.reason, 'words not searched yet: its text is being read')
    const q = await query(textQuery('wombatword'))
    assert.equal(omissionOf(q, 'shelf')?.reason, 'its text is being read, so text conditions match nothing there yet')
    const names = await query({ canned: 'largest', n: 100 })
    assert.equal(omissionOf(names, 'shelf'), undefined, 'a query that reads no text is answered in full')
  } finally { fs.rmSync(HOLD, { force: true }) }
  assert.equal((await finished(job.id)).state, 'done')
  const after = await search('wombatword')
  assert.equal(after.omitted, undefined, `nothing left out once built: ${JSON.stringify(after.omitted)}`)
  assert.equal(after.hits.filter(h => h.library.id === 'shelf').length, 9)
})

test('what a library holds survives a build, a stale probe and a restart, with no probe in between', { skip: !REAL && 'GUFI not installed', timeout: 60_000 }, async () => {
  const root = L.addedDef('shelf').indexRoot
  assert.equal(L.getLibrary('shelf').caps.fullText, true)
  assert.equal(L.knownCaps(root).caps.fullText, true, 'kept on disk')
  // After a change the probe is stale; the last known capabilities stand.
  L.staleProbe(root)
  assert.equal(L.getLibrary('shelf').caps.fullText, true, 'not reset to none while stale')
  // A restart reads them from disk before any probe.
  await restart()
  assert.equal(L.getLibrary('shelf').caps.fullText, true, 'read from disk after a restart')
  const s = await search('wombatword')
  assert.equal(s.hits.filter(h => h.library.id === 'shelf').length, 9, 'its words are searched before any probe')
  assert.equal(s.omitted, undefined)
})

test('a probe that cannot read the index keeps what was known, marked stale', { skip: (!REAL && 'GUFI not installed') || (AS_ROOT && 'root reads everything'), timeout: 60_000 }, async () => {
  const root = L.addedDef('shelf').indexRoot
  const dbs = []
  const walk = d => { for (const e of fs.readdirSync(d, { withFileTypes: true })) { if (e.isDirectory()) walk(path.join(d, e.name)); else if (e.name === 'db.db') dbs.push(path.join(d, e.name)) } }
  walk(root)
  await restart()
  // Not one database can be opened, as while a disk is busy or a tree is being swapped.
  for (const f of dbs) fs.chmodSync(f, 0o000)
  try {
    const caps = await L.probeLibrary(root)
    assert.equal(caps.fullText, true, 'the last known capabilities stand')
    assert.equal(L.knownCaps(root).stale, true, 'marked stale')
    assert.ok(L.knownCaps(root).error, 'with what the probe met')
    assert.equal(L.getLibrary('shelf').caps.fullText, true, 'and a listing shows them')
  } finally { for (const f of dbs) fs.chmodSync(f, 0o644) }
  await restart()
  await L.probeLibrary(root)
  assert.equal(L.knownCaps(root).stale, false, 'a probe that reads it clears the mark')
})

test('a change of depth not applied yet is named, and applied by a sync', { skip: !REAL && 'GUFI not installed', timeout: 120_000 }, async () => {
  const src = tmp('omit-names-')
  for (let i = 0; i < 4; i++) write(src, `n${i}.md`, `note ${i} about the bilbyword trail\n`)
  const r = await j('POST', '/api/libraries/build', { sourceRoot: src, label: 'Names', id: 'names', depth: 'names' })
  assert.equal((await finished(r.body.job.id)).state, 'done')
  assert.equal(omissionOf(await search('bilbyword'), 'names'), undefined, 'names only: its words are not expected')
  // Raised to Text with background sync paused: nothing has read the text yet.
  assert.equal((await j('PUT', '/api/libraries/names/depth', { depth: 'text' })).status, 200)
  const s = await search('bilbyword')
  assert.equal(omissionOf(s, 'names')?.reason, 'words not searched: its index has no text yet')
  await B.syncLibrary('names')
  assert.equal(omissionOf(await search('bilbyword'), 'names'), undefined, 'applied by the sync')
})

test('a library that does not answer in time is named with the wait', { skip: !REAL && 'GUFI not installed', timeout: 60_000 }, async () => {
  const omitted = []
  const slow = []
  await S.searchScope(S.scopeLibraries('all', null), 'wombatword', 10, { semantic: false, deadlineMs: 1, slow, omitted })
  const late = omitted.filter(o => o.reason === 'did not answer within 1 ms')
  assert.ok(late.length >= 1, JSON.stringify(omitted))
  assert.ok(late.every(o => o.partial === false))
  assert.deepEqual(late.map(o => o.label).sort(), [...slow].sort(), 'the slow list names the same')
})

test('a library whose index is gone is named in search and Query, and Query alone refuses it plainly', { skip: !REAL && 'GUFI not installed', timeout: 60_000 }, async () => {
  const src = tmp('omit-gone-')
  write(src, 'a.md', 'echidnaword\n')
  const r = await j('POST', '/api/libraries/build', { sourceRoot: src, label: 'Gone', id: 'gone', depth: 'names' })
  assert.equal((await finished(r.body.job.id)).state, 'done')
  const root = L.addedDef('gone').indexRoot
  fs.rmSync(root, { recursive: true, force: true })
  // Not even a restart: what is on disk says it had an index, and it is looked for again.
  const s = await search('echidnaword')
  assert.deepEqual(omissionOf(s, 'gone'), { id: 'gone', label: 'Gone', reason: 'index not found', partial: false })
  const q = await query({ canned: 'largest', n: 10 })
  assert.equal(omissionOf(q, 'gone')?.reason, 'index not found')
  assert.ok((q.failed ?? []).some(f => f.library === 'Gone'), 'and among those that could not answer')
  const one = await j('POST', '/api/query', { canned: 'largest', n: 10, library: 'gone' })
  assert.equal(one.status, 409)
  assert.match(one.body.error, /Gone cannot be queried: index not found/)
  assert.equal((await j('DELETE', '/api/libraries/gone')).status, 200)
})

test('a folder the Studio may not read is reported, not counted as indexed, and kept as last indexed by a sync', { skip: (!REAL && 'GUFI not installed') || (AS_ROOT && 'root reads everything'), timeout: 120_000 }, async () => {
  const src = tmp('omit-perm-')
  write(src, 'open/a.md', 'platypusword in the open folder\n')
  write(src, 'shut/b.md', 'platypusword in the shut folder\n')
  const r = await j('POST', '/api/libraries/build', { sourceRoot: src, label: 'Perm', id: 'perm', depth: 'text' })
  assert.equal((await finished(r.body.job.id)).state, 'done')
  const before = (await search('platypusword')).hits.filter(h => h.library.id === 'perm').map(h => h.path).sort()
  assert.deepEqual(before, ['open/a.md', 'shut/b.md'])
  // Access is taken away, as macOS does until it is allowed.
  fs.chmodSync(path.join(src, 'shut'), 0o000)
  try {
    write(src, 'open/c.md', 'platypusword, a new file\n')
    await B.syncLibrary('perm')
    const s = await search('platypusword')
    const o = omissionOf(s, 'perm')
    assert.ok(o, JSON.stringify(s.omitted))
    assert.equal(o.partial, true)
    assert.equal(o.reason, `a folder could not be read (${O.deniedReason(path.join(src, 'shut'))})`)
    const found = s.hits.filter(h => h.library.id === 'perm').map(h => h.path).sort()
    assert.deepEqual(found, ['open/a.md', 'open/c.md', 'shut/b.md'], 'the folder stays as last indexed, not taken for removed')
    // A full build of a tree with an unreadable folder: its files are not indexed, and it is named.
    const rb = await j('POST', '/api/libraries/perm/rebuild')
    assert.equal((await finished(rb.body.job.id)).state, 'done')
    const s2 = await search('platypusword')
    assert.equal(omissionOf(s2, 'perm')?.reason, `a folder could not be read (${O.deniedReason(path.join(src, 'shut'))})`)
    assert.deepEqual(s2.hits.filter(h => h.library.id === 'perm').map(h => h.path).sort(), ['open/a.md', 'open/c.md'])
  } finally { fs.chmodSync(path.join(src, 'shut'), 0o755) }
  await B.syncLibrary('perm')
  assert.equal(omissionOf(await search('platypusword'), 'perm'), undefined, 'readable again: nothing to say')
})

test('the main folder: an unreadable folder is named, kept as indexed by the sync, and a build stands without it', { skip: (!REAL && 'GUFI not installed') || (AS_ROOT && 'root reads everything'), timeout: 120_000 }, async () => {
  write(KB, 'private/diary.md', 'a dingoword entry\n')
  const sweep = async () => { await j('POST', '/api/index/sweep'); await until(async () => !(await j('GET', '/api/index/status')).body.running) }
  await sweep()
  const found = async () => (await search('dingoword')).hits.filter(h => h.library.id === 'kb').map(h => h.path)
  assert.deepEqual(await found(), ['private/diary.md'])
  fs.chmodSync(path.join(KB, 'private'), 0o000)
  try {
    write(KB, 'notes/later.md', 'a later note\n')
    await sweep()
    const s = await search('dingoword')
    assert.equal(omissionOf(s, 'kb')?.reason, `a folder could not be read (${O.deniedReason(path.join(KB, 'private'))})`)
    assert.deepEqual(await found(), ['private/diary.md'], 'kept as last indexed, not removed')
    const b = await finished((await j('POST', '/api/index/build')).body.id)
    assert.equal(b.state, 'done', `a build stands without the folder: ${b.error ?? ''}`)
    assert.equal(omissionOf(await search('dingoword'), 'kb')?.partial, true)
  } finally { fs.chmodSync(path.join(KB, 'private'), 0o755) }
  await sweep()
  assert.equal(omissionOf(await search('dingoword'), 'kb'), undefined, 'readable again')
})

test('a stop leaves a build to resume, and a cut-off change of depth is finished by a sync, not rebuilt', { skip: !REAL && 'GUFI not installed', timeout: 180_000 }, async () => {
  // A rebuild held in its text pass, then stopped as SIGTERM stops it.
  fs.writeFileSync(HOLD, '')
  let rb
  try {
    rb = await j('POST', '/api/libraries/shelf/rebuild')
    assert.equal(rb.status, 200)
    await until(() => L.addedDef('shelf').build.state === 'updating')
    await sleep(500)
    assert.ok(B.stopLibraryJobs() >= 1, 'a program was stopped')
    // Nothing the build started is left running, the held worker included:
    // its process group was stopped, while it would otherwise run on.
    const { execFileSync } = await import('node:child_process')
    const cache = L.addedDef('shelf').build.cacheRoot
    await until(() => !execFileSync('ps', ['-eo', 'args'], { encoding: 'utf8' }).split('\n').some(l => l.includes(cache)), 10_000)
    await finished(rb.body.job.id)
  } finally { fs.rmSync(HOLD, { force: true }) }
  assert.equal(L.addedDef('shelf').build.state, 'updating', 'left for the next start, not failed')
  // Its words still answer from the index it was replacing.
  assert.equal((await search('wombatword')).hits.filter(h => h.library.id === 'shelf').length, 9)
  const log = []
  await restart()
  B.startLibrarySync(m => log.push(m))
  assert.ok(log.some(m => m.includes('library shelf: the build was interrupted')), log.join('\n'))
  await until(() => L.addedDef('shelf').build.state === 'ready', 120_000)

  // A change of depth cut off part way: a sync finishes it.
  L.updateBuild('shelf', { state: 'updating', pending: 'depth' })
  const log2 = []
  await restart()
  B.startLibrarySync(m => log2.push(m))
  assert.ok(log2.some(m => m.includes('library shelf: applying its index depth was interrupted; finishing it')), log2.join('\n'))
  assert.ok(!log2.some(m => m.includes('library shelf: the build was interrupted')), 'not rebuilt')
  assert.equal(L.addedDef('shelf').build.state, 'ready')
  assert.equal(L.addedDef('shelf').build.pending, null)
})

test('the start-up line says each library\'s state', { skip: !REAL && 'GUFI not installed' }, () => {
  const line = O.libraryStateLine(L.getLibrary('shelf'))
  assert.match(line, /^library shelf \(Shelf\): index yes, words yes, vectors no, depth text, build ready \(built .+\)$/)
})
