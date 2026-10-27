// The maps on the Stats page for any library, not only the main folder.
// The semantic map: made after a library's meaning phase, kept in the
// library's own folder and removed with it, read at the library's address,
// and, where there is none, the reason (search by meaning off, an index
// used as it is with no vectors the Studio reads, a library on another
// system). The corpus topology: one aggregate gufi_query per folder asked
// for, here or over ssh on a host that runs only gufi_query, cut to a few
// levels, kept for a while and asked for again on refresh. The ssh host is
// local: the transport's spawn seam runs each command through a fake login
// that runs gufi_query only. Needs GUFI (GUFI_BIN, else /opt/gufi/bin).
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(gufiBin, 'gufi_query')) && fs.existsSync(path.join(gufiBin, 'gufi_dir2index'))
const skip = !haveGufi && 'GUFI not installed'
const sqlite3Bin = path.join(gufiBin, 'gufi_sqlite3')
const hasVec = (() => {
  try { return execFileSync(sqlite3Bin, [], { input: 'SELECT vec_version();', encoding: 'utf8' }).trim().length > 0 } catch { return false }
})()

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'libmaps-'))
Object.assign(process.env, { GUFI_BIN: gufiBin, KB_ROOT: path.join(dir, 'kb'), INDEX_BASE: path.join(dir, 'ix'), XDG_RUNTIME_DIR: fs.mkdtempSync('/tmp/libmapsrun-') })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR']) delete process.env[k]
test.after(() => fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }))
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }

write(path.join(dir, 'kb', 'main.md'), '# main\n')
write(path.join(dir, 'kb', 'notes', 'a.md'), '# a\n')
const papers = path.join(dir, 'papers')
write(path.join(papers, 'intro.md'), '# Introduction\nThe turbine blade study.\n')
write(path.join(papers, 'data', 'runs.csv'), 'a,b\n1,2\n')
write(path.join(papers, 'data', 'deep', 'er', 'big.bin'), Buffer.alloc(4096, 1))
const desk = path.join(dir, 'desk')
write(path.join(desk, 'todo.txt'), 'buy bread\n')

// A large tree on the remote host: 22 folders of 10 of 10, ten files in
// each of those, 22,000 files of 3 bytes; and a chain nine folders deep.
const home = path.join(dir, 'home')
const big = path.join(home, 'big')
if (haveGufi) {
  for (let a = 0; a < 22; a++) for (let b = 0; b < 10; b++) for (let c = 0; c < 10; c++) {
    const d = path.join(big, `a${a}`, `b${b}`, `c${c}`)
    fs.mkdirSync(d, { recursive: true })
    for (let f = 0; f < 10; f++) fs.writeFileSync(path.join(d, `f${f}.txt`), 'abc')
  }
  write(path.join(big, 'deep', '1', '2', '3', '4', '5', '6', '7', '8', 'end.txt'), '12345')
  fs.mkdirSync(path.join(home, 'idx'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', '-n', '8', big, path.join(home, 'idx')], { stdio: 'ignore' })
  fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
}

// The fake login: runs gufi_query and nothing else, and logs each command.
// With the mark file there, the connection drops on a walk that fetches
// vectors, as ssh's does (exit 255).
const log = path.join(dir, 'commands.log')
const QUERY_ONLY = `printf '%s\\n' "$CMD" >> ${log}
if [ -f ${path.join(dir, 'fail-vectors')} ]; then case "$CMD" in *vec_add*) echo "Connection closed by 127.0.0.1 port 22" >&2; exit 255 ;; esac; fi
case "$CMD" in
  gufi_query|"gufi_query "*) eval "$CMD" ;;
  *) echo "This account may run only gufi_query." >&2; exit 1 ;;
esac`
const R = await import('../dist/remoteExec.js')
R.setRemoteSpawn((site, key, command) => {
  const script = R.parseSshSite(site)?.destination === 'me@query-only' ? QUERY_ONLY : 'exit 255'
  return spawn('/bin/sh', ['-c', script], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: home, PATH: `${gufiBin}:/usr/bin:/bin`, CMD: command } })
})
const commands = () => (fs.existsSync(log) ? fs.readFileSync(log, 'utf8').split('\n').filter(Boolean) : [])

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const B = await import('../dist/libraryBuild.js')
const L = await import('../dist/libraries.js')
const M = await import('../dist/semanticMap.js')
const T = await import('../dist/topology.js')
const U = await import('../dist/userLibraries.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(B.libraryBuildRoutes)
await app.register(T.topologyRoutes)
await app.register(U.userLibraryRoutes)
await app.ready()
test.after(async () => { R.closeRemoteChannels(); await app.close() })
const call = async (method, url, body) => {
  const r = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: r.statusCode, body: r.json() }
}
const get = url => call('GET', url)
async function until(fn, ms = 60_000) {
  const end = Date.now() + ms
  for (;;) {
    const v = await fn()
    if (v) return v
    if (Date.now() > end) throw new Error('timed out')
    await new Promise(r => setTimeout(r, 50))
  }
}
const waitReady = id => until(async () => {
  const l = (await get('/api/libraries')).body.libraries.find(x => x.id === id)
  return l?.build && (l.build.state === 'ready' || l.build.state === 'failed') && !l.build.progress ? l : null
})

// The map builder is replaced: it notes each library it was asked for and
// writes a map of two clusters into the folder it was given.
const built = []
let deskId
const fakeMap = async t => {
  built.push(t)
  fs.mkdirSync(t.out, { recursive: true })
  const points = [{ p: 'intro.md', x: 0.1, y: 0.2, z: 0.3 }, { p: 'data/runs.csv', x: 0.8, y: 0.7, z: 0.6 }]
  fs.writeFileSync(path.join(t.out, 'overview.json'), JSON.stringify({
    version: 3, available: true, files: 2, shown: 2,
    clusters: [{ c: 0, label: 'intro', count: 1, x: 0.1, y: 0.2, z: 0.3 }, { c: 1, label: 'data', count: 1, x: 0.8, y: 0.7, z: 0.6 }],
    points: points.map((p, i) => ({ ...p, c: i })),
  }))
  points.forEach((p, i) => fs.writeFileSync(path.join(t.out, `cluster-${i}.json`), JSON.stringify({ c: i, label: i ? 'data' : 'intro', count: 1, points: [p] })))
}
M.setSemanticMapBuilderForTests(fakeMap, { delayScale: 0 })

test('a library the Studio builds gets a map of its own after its meaning phase, in its own folder', { skip }, async () => {
  const r = await call('POST', '/api/libraries/build', { sourceRoot: papers, label: 'Papers', create: true, depth: 'meaning-documents' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const lib = await waitReady('papers')
  assert.equal(lib.build.state, 'ready', lib.build.error ?? '')
  const t = await until(() => built.find(b => b.id === 'papers'))
  assert.equal(t.index, L.getLibrary('papers').indexRoot)
  assert.equal(t.out, path.join(L.builtLibraryDir('papers'), 'semantic-map'))
  assert.equal(t.out, M.semanticMapDir('papers'))
  assert.ok(t.out.startsWith(path.join(dir, 'ix')), 'under the Studio\'s own folder, not in the index')
  const o = await get('/api/libraries/papers/semantic-map')
  assert.equal(o.status, 200)
  assert.equal(o.body.available, true)
  assert.deepEqual(o.body.library, { id: 'papers', label: 'Papers', kind: 'built', depth: 'meaning-documents', vectors: false })
  assert.deepEqual(o.body.clusters.map(c => c.label), ['intro', 'data'])
  const c1 = await get('/api/libraries/papers/semantic-map/cluster/1?offset=0&limit=10')
  assert.deepEqual(c1.body.points.map(p => p.p), ['data/runs.csv'])
  assert.equal((await get('/api/libraries/papers/semantic-map/cluster/7')).status, 404)
  assert.equal((await get('/api/libraries/nothing-here/semantic-map')).status, 404)
  assert.equal((await get('/api/libraries/nothing-here/semantic-map/cluster/0')).status, 404)
  // The main folder's map is its own, at the address it has always had.
  assert.ok(!built.some(b => b.id === 'kb' && b.out === t.out))
})

test('a library at a depth without meaning has no map and says so; a new depth with meaning makes one', { skip }, async () => {
  const r = await call('POST', '/api/libraries/build', { sourceRoot: desk, label: 'Desktop', create: true, depth: 'ocr' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  deskId = r.body.library.id
  await waitReady(deskId)
  const o = await get(`/api/libraries/${deskId}/semantic-map`)
  assert.deepEqual([o.body.available, o.body.reason, o.body.library.kind, o.body.library.depth], [false, 'off', 'built', 'ocr'])
  const near = await get(`/api/libraries/${deskId}/semantic-map/neighbors?file=todo.txt`)
  assert.deepEqual([near.body.neighbors, near.body.reason], [[], 'off'])
  assert.ok(!built.some(b => b.id === deskId), 'nothing made')
  assert.equal((await call('PUT', `/api/libraries/${deskId}/depth`, { depth: 'meaning-documents' })).status, 200)
  // The sync the new depth asks for runs its phases; the map follows it.
  assert.ok((await B.syncLibrary(deskId)).includes('(index depth: meaning-documents)'))
  await until(() => built.find(b => b.id === deskId))
  const after = await get(`/api/libraries/${deskId}/semantic-map`)
  assert.equal(after.body.available, true)
})

test('every library\'s map at a glance, for the picker and its default', { skip }, async () => {
  const { maps } = (await get('/api/semantic-maps')).body
  const byId = Object.fromEntries(maps.map(m => [m.id, m]))
  assert.deepEqual([byId.papers.available, byId.papers.files, byId.papers.label], [true, 2, 'Papers'])
  assert.equal(byId[deskId].available, true)
  assert.ok(byId.kb, 'the main folder is listed')
})

test('an index used as it is, with no vectors the Studio reads, says so and takes no depth', { skip }, async () => {
  // Without vectors (or with them only in another schema's tables).
  const plain = path.join(dir, 'connected', 'plain')
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', desk, path.dirname(plain)], { stdio: 'ignore' })
  fs.renameSync(path.join(path.dirname(plain), 'desk'), plain)
  assert.equal((await call('POST', '/api/libraries', { id: 'lab', label: 'Lab index', indexRoot: plain })).status, 200)
  // Known to have none once the whole tree is counted.
  await L.capsCountedForTests()
  const o = await get('/api/libraries/lab/semantic-map')
  assert.deepEqual([o.body.available, o.body.reason, o.body.library.kind, o.body.library.depth], [false, 'connected', 'connected', null])
  assert.ok(!built.some(b => b.id === 'lab'))
  // depth refuses it, so the page offers no depth control there.
  assert.equal((await call('PUT', '/api/libraries/lab/depth', { depth: 'meaning-documents' })).status, 409)
})

// Unit vectors mostly along `axis`, leaning toward `toward` by `lean`.
const vecHex = (axis, toward = axis, lean = 0) => {
  const v = new Float32Array(384)
  v[axis] = 1 - lean; v[toward] += lean
  const n = Math.hypot(...v)
  const buf = Buffer.alloc(384 * 4)
  v.forEach((x, i) => buf.writeFloatLE(x / n, i * 4))
  return `X'${buf.toString('hex')}'`
}
const vecFolder = (root, folder, files) => {
  const sql = [
    'CREATE TABLE IF NOT EXISTS gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT);',
    'CREATE VIRTUAL TABLE IF NOT EXISTS gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);',
    'CREATE TABLE IF NOT EXISTS gvecmodel (model TEXT);',
    "INSERT INTO gvecmodel VALUES ('minilm-l6-mean-int8');",
  ]
  let cid = 0
  for (const [fname, hex] of files) {
    cid++
    sql.push(`INSERT INTO gchunks VALUES (${cid}, '1', '${fname}', ${cid}, 'text of ${fname}', 'h${cid}');`, `INSERT INTO gvec (cid, fp384) VALUES (${cid}, ${hex});`)
  }
  execFileSync(sqlite3Bin, [path.join(root, folder, 'db.db')], { input: sql.join('\n') })
}

test('a file\'s nearest files by meaning come from its own library', { skip: (skip || !hasVec) && 'needs GUFI\'s sqlite3 with sqlite-vec' }, async () => {
  const vec = path.join(dir, 'connected', 'vec')
  fs.mkdirSync(path.dirname(vec), { recursive: true })
  write(path.join(dir, 'vsrc', 'vec', 'x', 'keep.txt'), 'x')
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'vsrc', 'vec'), path.dirname(vec)], { stdio: 'ignore' })
  vecFolder(vec, '', [['a.md', vecHex(10)], ['b.md', vecHex(10, 11, 0.3)], ['far.md', vecHex(200)]])
  vecFolder(vec, 'x', [['c.md', vecHex(10, 12, 0.5)]])
  // The same file name in the main folder, along another axis: not asked.
  vecFolder(path.join(dir, 'ix', 'gufi', 'kb'), '', [['a.md', vecHex(300)], ['b.md', vecHex(300)]])
  assert.equal((await call('POST', '/api/libraries', { id: 'vecs', label: 'Vectors', indexRoot: vec })).status, 200)
  await L.capsCountedForTests()
  const near = await get('/api/libraries/vecs/semantic-map/neighbors?file=a.md&k=2')
  assert.equal(near.status, 200)
  assert.deepEqual(near.body.neighbors.map(n => n.p), ['b.md', 'x/c.md'])
  // A connected index with the Studio's vectors gets a map, in the Studio's folder for it.
  const o = await get('/api/libraries/vecs/semantic-map')
  assert.equal(o.body.library.vectors, true)
  const t = await until(() => built.find(b => b.id === 'vecs'))
  assert.deepEqual([t.index, t.out], [vec, M.semanticMapDir('vecs')])
  const main = await get('/api/kb/semantic-map/neighbors?file=a.md&k=2')
  assert.deepEqual(main.body.neighbors.map(n => n.p), ['b.md'])
})

test('a library removed takes its map with it', { skip }, async () => {
  const out = M.semanticMapDir('papers')
  assert.ok(fs.existsSync(path.join(out, 'overview.json')))
  assert.equal((await call('DELETE', '/api/libraries/papers')).status, 200)
  assert.equal(fs.existsSync(out), false)
  assert.equal((await get('/api/libraries/papers/semantic-map')).status, 404)
  assert.ok(!(await get('/api/semantic-maps')).body.maps.some(m => m.id === 'papers'))
})

test('the topology of the main folder and of a library on this computer, a few levels at a time', { skip }, async () => {
  const main = (await get('/api/kb/topology')).body
  assert.equal(main.available, true)
  assert.deepEqual(main.dirs.map(d => [d.dir, d.files]).sort(), [['', 1], ['notes', 1]])
  assert.equal((await get('/api/libraries/kb/topology')).body.dirs.length, 2)
  const desk1 = (await get(`/api/libraries/${deskId}/topology`)).body
  assert.deepEqual(desk1.dirs.map(d => [d.dir, d.files, d.bytes]), [['', 1, 10]])
  assert.equal((await get(`/api/libraries/${deskId}/topology?dir=../x`)).status, 400)
  assert.equal((await get('/api/libraries/nothing-here/topology')).status, 404)
})

let remoteId
test('the topology of a 22,000-file index on a host that runs only gufi_query: one aggregate query, cut to six levels, in seconds', { skip, timeout: 120_000 }, async () => {
  const added = await call('POST', '/api/user-libraries/ssh', { destination: 'me@query-only', label: 'Big', indexRoot: path.join(home, 'idx', 'big') })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  remoteId = added.body.id
  fs.rmSync(log, { force: true })
  const t0 = Date.now()
  const r = await get(`/api/libraries/${remoteId}/topology`)
  const ms = Date.now() - t0
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.available, true)
  assert.ok(ms < 5000, `${ms} ms`)
  console.log(`remote topology of 22,001 files: ${ms} ms (${r.body.ms} ms in gufi_query), ${r.body.dirs.length} rows`)
  assert.equal(r.body.levels, 6)
  assert.equal(r.body.dirs.reduce((s, d) => s + d.files, 0), 22_001)
  assert.equal(r.body.dirs.reduce((s, d) => s + d.bytes, 0), 22_000 * 3 + 5)
  assert.equal(r.body.dirs.find(d => d.dir === 'a7/b3/c9').files, 10)
  // Below six levels, a folder holds the totals of everything under it.
  assert.deepEqual(r.body.dirs.filter(d => d.dir.startsWith('deep')).map(d => [d.dir, d.files]).sort(),
    [['deep', 0], ['deep/1', 0], ['deep/1/2', 0], ['deep/1/2/3', 0], ['deep/1/2/3/4', 0], ['deep/1/2/3/4/5', 1]])
  const sent = commands()
  assert.equal(sent.length, 1, 'one command')
  assert.match(sent[0], /^gufi_query /)
  // Kept: asked again, nothing is sent; refresh sends one.
  await get(`/api/libraries/${remoteId}/topology`)
  assert.equal(commands().length, 1)
  await get(`/api/libraries/${remoteId}/topology?refresh=1`)
  assert.equal(commands().length, 2)
  // A folder at the last level, asked for, shows what is below it.
  const below = (await get(`/api/libraries/${remoteId}/topology?dir=${encodeURIComponent('deep/1/2/3/4/5')}`)).body
  assert.equal(below.root, 'deep/1/2/3/4/5')
  assert.deepEqual(below.dirs.filter(d => d.files).map(d => [d.dir, d.files]), [['deep/1/2/3/4/5/6/7/8', 1]])
})

// An index with vectors on the remote host: folders about two topics, the
// files of one along one axis and of the other along another, with inodes
// of their own (a sample is taken by inode), and a folder whose vectors come
// from another model, which the map leaves out.
const far = path.join(home, 'vidx', 'vsrc')
const failMark = path.join(dir, 'fail-vectors')
let farId, farHome
const farFiles = []
const unit384 = (axis, toward, lean) => {
  const v = new Float32Array(384)
  v[axis] = 1 - lean; v[toward] += lean
  const n = Math.hypot(...v)
  return v.map(x => x / n)
}
const hexOf = v => { const b = Buffer.alloc(1536); v.forEach((x, i) => b.writeFloatLE(x, i * 4)); return b.toString('hex') }
const farFolder = (folder, model, files) => {
  const sql = [
    'CREATE TABLE IF NOT EXISTS gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT);',
    'CREATE VIRTUAL TABLE IF NOT EXISTS gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);',
    'CREATE TABLE IF NOT EXISTS gvecmodel (model TEXT);',
    'DELETE FROM gvecmodel;',
    `INSERT INTO gvecmodel VALUES ('${model}');`,
  ]
  for (const f of files) {
    // Two passages a file: the map takes their mean.
    f.passages.forEach((v, seq) => sql.push(
      `INSERT INTO gchunks (tinode, fname, seq, ctext, h) VALUES ('${f.inode}', '${f.name}', ${seq}, '${f.text}', 'h${f.inode}${seq}');`,
      `INSERT INTO gvec (cid, fp384) VALUES ((SELECT max(cid) FROM gchunks), X'${hexOf(v)}');`))
  }
  execFileSync(sqlite3Bin, [path.join(far, folder, 'db.db')], { input: sql.join('\n') })
}
const farFile = (folder, i, axis, text) => {
  const f = { p: `${folder}/${folder[0]}${i}.md`, name: `${folder[0]}${i}.md`, inode: 5000 + farFiles.length, text,
    passages: [unit384(axis, axis + 1, 0.05 * (i % 5)), unit384(axis, axis + 2, 0.05 * (i % 3))] }
  farFiles.push(f)
  return f
}
const vecSkip = (skip || !hasVec) && 'needs GUFI\'s sqlite3 with sqlite-vec'
if (haveGufi && hasVec) {
  for (const folder of ['turbines', 'kitchen', 'other']) write(path.join(home, 'vsrc', folder, 'keep.txt'), 'x')
  fs.mkdirSync(path.join(home, 'vidx'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, 'vsrc'), path.join(home, 'vidx')], { stdio: 'ignore' })
  farFolder('turbines', 'minilm-l6-mean-int8', Array.from({ length: 30 }, (_, i) => farFile('turbines', i, 10, 'turbine blade rotor wind turbine blade')))
  farFolder('kitchen', 'minilm-l6-mean-int8', Array.from({ length: 30 }, (_, i) => farFile('kitchen', i, 200, 'sourdough starter flour oven sourdough bread')))
  farFolder('other', 'another-model', Array.from({ length: 4 }, (_, i) => farFile('other', i, 10, 'turbine elsewhere')))
}
const mapped = farFiles.filter(f => !f.p.startsWith('other/'))
const fetched = () => fs.readFileSync(path.join(farHome, 'vectors.jsonl'), 'utf8').trim().split('\n').map(l => JSON.parse(l))
const overviewOf = async id => (await get(`/api/libraries/${id}/semantic-map`)).body
const settled = async id => until(async () => { const o = await overviewOf(id); return o.building ? null : o })

test('a library on another system with vectors: a sample fetched with gufi_query alone, the map made here in its owner\'s folder', { skip: vecSkip, timeout: 120_000 }, async () => {
  const added = await call('POST', '/api/user-libraries/ssh', { destination: 'me@query-only', label: 'Far', indexRoot: far })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  farId = added.body.id
  farHome = U.remoteMapHome(U.remoteRootOf('single', farId))
  // The map is made by the script (or, without numpy, by the server).
  M.setSemanticMapBuilderForTests(null, { delayScale: 0 })
  fs.rmSync(log, { force: true })
  const first = await overviewOf(farId)
  assert.deepEqual([first.available, first.building, first.library.kind, first.library.host], [false, true, 'remote', 'me@query-only'])
  const o = await until(async () => { const v = await overviewOf(farId); return v.available && !v.building ? v : null })
  assert.deepEqual([o.files, o.of, o.model], [60, 60, 'minilm-l6-mean-int8'])
  assert.equal(o.remote.host, 'me@query-only')
  // The models its vectors there were made with: the map is of the one most files use.
  assert.deepEqual(o.remote.models, [{ model: 'minilm-l6-mean-int8', files: 60 }, { model: 'another-model', files: 4 }])
  assert.ok(o.remote.fetchedAt && o.remote.bytes > 60 * 768, JSON.stringify(o.remote))
  assert.ok(o.clusters.length >= 2)
  // One walk to count, one to fetch: gufi_query alone, on a host that runs nothing else.
  const sent = commands()
  assert.equal(sent.length, 2, sent.join('\n'))
  assert.ok(sent.every(c => c.startsWith('gufi_query ')))
  assert.match(sent[0], /gvec_rowids/)
  assert.match(sent[1], /vec_add/)
  // Kept in the owner's folder, not the shared one the library ids name.
  assert.ok(farHome.startsWith(path.join(dir, 'ix', 'users')), farHome)
  assert.ok(fs.existsSync(path.join(farHome, 'semantic-map', 'overview.json')))
  assert.ok(!fs.existsSync(M.semanticMapDir(farId)))
  // Each file once, below the library's top, as the mean of its passages:
  // the other model's files are left out.
  const rows = fetched()
  assert.deepEqual(rows.map(r => r.p).sort(), mapped.map(f => f.p).sort())
  assert.ok(rows.every(r => /^[0-9A-F]{768}$/.test(r.v) && r.t.startsWith(r.p.startsWith('turbines') ? 'turbine' : 'sourdough')))
  const R2 = await import('../dist/remoteMap.js')
  for (const r of rows.slice(0, 5)) {
    const f = farFiles.find(x => x.p === r.p)
    const mean = f.passages[0].map((x, i) => (x + f.passages[1][i]) / 2)
    const got = R2.dequantize(r.v, r.r)
    const dot = mean.reduce((s, x, i) => s + x * got[i], 0)
    assert.ok(dot / Math.hypot(...mean) / Math.hypot(...got) > 0.995, `${r.p}: ${dot}`)
    assert.ok(Math.abs(Math.hypot(...got) - Math.hypot(...mean)) < 1e-3, 'its length too')
  }
  // Its clusters at the library's address, and in the picker's list.
  assert.ok((await get(`/api/libraries/${farId}/semantic-map/cluster/0?limit=100`)).body.points.length > 0)
  const listed = (await get('/api/semantic-maps')).body.maps.find(m => m.id === farId)
  assert.deepEqual([listed.available, listed.files], [true, 60])
})

test('a file\'s nearest files on another system come from a search there, by gufi_query, kept until the map is made again', { skip: vecSkip }, async () => {
  fs.rmSync(log, { force: true })
  const near = await get(`/api/libraries/${farId}/semantic-map/neighbors?file=turbines/t0.md&k=4`)
  assert.equal(near.status, 200, JSON.stringify(near.body))
  assert.equal(near.body.neighbors.length, 4)
  assert.ok(near.body.neighbors.every(n => n.p.startsWith('turbines/') && n.p !== 'turbines/t0.md' && n.sim > 0.9), JSON.stringify(near.body.neighbors))
  assert.ok(near.body.neighbors.every(n => typeof n.x === 'number'), 'with their places on the map')
  const sent = commands()
  assert.equal(sent.length, 1)
  assert.match(sent[0], /^gufi_query .*MATCH/)
  await get(`/api/libraries/${farId}/semantic-map/neighbors?file=turbines/t0.md&k=4`)
  assert.equal(commands().length, 1, 'answered from what was kept')
  const none = await get(`/api/libraries/${farId}/semantic-map/neighbors?file=no/such.md`)
  assert.deepEqual([none.body.neighbors, none.body.reason], [[], 'none'])
})

test('past the cap, the sample is the files whose inodes hash lowest: the same files every time', { skip: vecSkip, timeout: 120_000 }, async () => {
  process.env.STUDIO_REMOTE_MAP_FILES = '5'
  try {
    const R2 = await import('../dist/remoteMap.js')
    const want = mapped.map(f => ({ p: f.p, h: R2.fileHash(f.inode) })).sort((a, b) => a.h - b.h).slice(0, 5).map(f => f.p).sort()
    for (let round = 0; round < 2; round++) {
      fs.rmSync(log, { force: true })
      const r = await call('POST', `/api/libraries/${farId}/semantic-map/refresh`)
      assert.equal(r.status, 200, JSON.stringify(r.body))
      assert.equal(r.body.building, true)
      const o = await settled(farId)
      assert.deepEqual([o.files, o.of], [5, 60])
      assert.deepEqual(fetched().map(x => x.p).sort(), want)
      // The walk asks for a share of the files by hash, not all of them.
      assert.match(commands()[1], /% 2147483648 < \d+/)
      assert.equal(commands().length, 2)
    }
  } finally {
    delete process.env.STUDIO_REMOTE_MAP_FILES
  }
})

test('looked at, a map is checked at most hourly: fetched again only when its vectors changed there', { skip: vecSkip, timeout: 120_000 }, async () => {
  // Each look is due for a check while remoteCheckMs is 0, so it is set
  // back before the map is read again.
  const look = async () => {
    M.setSemanticMapBuilderForTests(null, { delayScale: 0, remoteCheckMs: 0 })
    try { await overviewOf(farId) } finally { M.setSemanticMapBuilderForTests(null, { delayScale: 0 }) }
  }
  {
    // Unchanged there: one count, nothing fetched.
    fs.rmSync(log, { force: true })
    await look()
    await settled(farId)
    assert.deepEqual(commands().map(c => /vec_add/.test(c)), [false])
    // A file more there: the count differs, so the files are fetched again.
    farFolder('kitchen', 'minilm-l6-mean-int8', [farFile('kitchen', 30, 200, 'sourdough rye')])
    fs.rmSync(log, { force: true })
    await look()
  }
  const o = await settled(farId)
  assert.deepEqual([o.files, o.of], [61, 61])
  assert.deepEqual(commands().map(c => /vec_add/.test(c)), [false, true])
  // Not due: viewing it sends nothing.
  fs.rmSync(log, { force: true })
  await overviewOf(farId)
  assert.equal(commands().length, 0)
})

test('vectors that could not be fetched: the map there stays, with the error; with none yet, a failed state to retry', { skip: vecSkip, timeout: 120_000 }, async () => {
  fs.writeFileSync(failMark, '')
  try {
    await call('POST', `/api/libraries/${farId}/semantic-map/refresh`)
    const o = await settled(farId)
    assert.equal(o.available, true)
    assert.match(o.error, /me@query-only: Connection closed/)
    // Another library, of a copy of that index, with no map yet.
    const copy = path.join(home, 'vidx2', 'vsrc')
    fs.cpSync(far, copy, { recursive: true })
    const added = await call('POST', '/api/user-libraries/ssh', { destination: 'me@query-only', label: 'Far again', indexRoot: copy })
    assert.equal(added.status, 200, JSON.stringify(added.body))
    const againId = added.body.id
    await overviewOf(againId)
    const failed = await settled(againId)
    assert.deepEqual([failed.available, failed.reason], [false, 'failed'])
    assert.match(failed.error, /Connection closed/)
    fs.rmSync(failMark)
    const retry = await call('POST', `/api/libraries/${againId}/semantic-map/refresh`)
    assert.deepEqual([retry.body.building, retry.body.error ?? null], [true, null])
    const ok = await settled(againId)
    assert.equal(ok.available, true)
    // Removed, its folder goes with it.
    const home2 = U.remoteMapHome(U.remoteRootOf('single', againId))
    assert.ok(fs.existsSync(home2))
    assert.equal((await call('DELETE', `/api/user-libraries/${againId}`)).status, 200)
    assert.equal(fs.existsSync(home2), false)
  } finally {
    fs.rmSync(failMark, { force: true })
  }
})

test('a library on another system with no vectors says so, after one count there', { skip, timeout: 120_000 }, async () => {
  fs.rmSync(log, { force: true })
  await overviewOf(remoteId)
  const o = await settled(remoteId)
  assert.deepEqual([o.available, o.reason, o.library.kind], [false, 'remote-none', 'remote'])
  assert.equal(commands().length, 1)
  assert.match(commands()[0], /^gufi_query /)
  const near = await get(`/api/libraries/${remoteId}/semantic-map/neighbors?file=a0/x.txt`)
  assert.deepEqual([near.body.neighbors, near.body.reason], [[], 'none'])
})

test('a library on another system with too few files with vectors for a map says how many', { skip: vecSkip, timeout: 120_000 }, async () => {
  write(path.join(home, 'fsrc', 'x', 'keep.txt'), 'x')
  fs.mkdirSync(path.join(home, 'fidx'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, 'fsrc'), path.join(home, 'fidx')], { stdio: 'ignore' })
  const few = path.join(home, 'fidx', 'fsrc')
  const sql = [
    'CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT);',
    'CREATE VIRTUAL TABLE gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);',
    'CREATE TABLE gvecmodel (model TEXT);', "INSERT INTO gvecmodel VALUES ('minilm384');",
    `INSERT INTO gchunks VALUES (1, '7001', 'one.md', 0, 'one', 'h1'); INSERT INTO gvec (cid, fp384) VALUES (1, X'${hexOf(unit384(3, 4, 0.1))}');`,
    `INSERT INTO gchunks VALUES (2, '7002', 'two.md', 0, 'two', 'h2'); INSERT INTO gvec (cid, fp384) VALUES (2, X'${hexOf(unit384(5, 6, 0.1))}');`,
  ]
  execFileSync(sqlite3Bin, [path.join(few, 'x', 'db.db')], { input: sql.join('\n') })
  const added = await call('POST', '/api/user-libraries/ssh', { destination: 'me@query-only', label: 'Few', indexRoot: few })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  await overviewOf(added.body.id)
  const o = await settled(added.body.id)
  assert.deepEqual([o.available, o.reason, o.of, o.remote.models], [false, 'remote-none', 2, [{ model: 'minilm384', files: 2 }]])
})
