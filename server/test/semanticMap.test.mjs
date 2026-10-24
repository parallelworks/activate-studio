// The semantic map: indexer/semantic_map.py over a small index whose
// folders hold vectors the way sqlite-vec stores them (a passage's chunk and
// offset, the float32 chunks, a validity bit per slot), and the routes,
// which only read what it wrote, so a page load never makes a map.
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'semmap-'))
process.env.KB_ROOT = path.join(tmp, 'kb')
process.env.INDEX_BASE = path.join(tmp, 'ix')
fs.mkdirSync(process.env.KB_ROOT, { recursive: true })
const INDEX = path.join(process.env.INDEX_BASE, 'gufi', 'kb')
const python = process.env.PYTHON_BIN || 'python3'
const hasNumpy = (() => {
  try { execFileSync(python, ['-c', `import sys; sys.path.insert(0, ${JSON.stringify(path.join(root, 'indexer', '.deps'))}); import numpy`], { stdio: 'ignore' }); return true } catch { return false }
})()

// Folders each about one topic: its files' vectors point one way, with
// noise. One passage sits in a slot marked invalid, as a deleted row is,
// and must not count.
const writeIndex = (root, topics, files) => execFileSync(python, ['-c', `
import os, random, sqlite3, struct
random.seed(1)
D = 384
def unit(axis):
    v = [random.gauss(0, 0.05) for _ in range(D)]
    v[axis] += 1.0
    return v
topics = ${JSON.stringify(topics)}
root = ${JSON.stringify(root)}
for folder, axis in [('', None)] + list(topics.items()):
    d = os.path.join(root, folder) if folder else root
    os.makedirs(d, exist_ok=True)
    c = sqlite3.connect(os.path.join(d, 'db.db'))
    c.execute('CREATE TABLE entries (name TEXT)')
    if axis is None:
        c.commit(); continue
    c.execute('CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT)')
    c.execute('CREATE TABLE gvec_rowids (rowid INTEGER PRIMARY KEY AUTOINCREMENT, id, chunk_id INTEGER, chunk_offset INTEGER)')
    c.execute('CREATE TABLE gvec_chunks (chunk_id INTEGER PRIMARY KEY AUTOINCREMENT, size INTEGER NOT NULL, validity BLOB NOT NULL, rowids BLOB NOT NULL)')
    c.execute('CREATE TABLE gvec_vector_chunks00 (rowid PRIMARY KEY, vectors BLOB NOT NULL)')
    c.execute('CREATE TABLE gvecmodel (model TEXT)')
    c.execute("INSERT INTO gvecmodel VALUES ('minilm-l6-mean-int8')")
    vecs, valid = [], bytearray(128)
    cid = 0
    for f in range(${files}):
        for seq in range(2):
            cid += 1
            c.execute('INSERT INTO gchunks VALUES (?, ?, ?, ?, ?, ?)', (cid, '1', f'{folder}-{f}.md', seq, 'text', f'h{cid}'))
            c.execute('INSERT INTO gvec_rowids (rowid, chunk_id, chunk_offset) VALUES (?, 1, ?)', (cid, cid - 1))
            vecs.append(unit(axis))
            valid[(cid - 1) >> 3] |= 1 << ((cid - 1) & 7)
    # A passage in a slot that is no longer valid, for a file that has no other.
    cid += 1
    c.execute('INSERT INTO gchunks VALUES (?, ?, ?, ?, ?, ?)', (cid, '1', 'deleted.md', 0, 'gone', 'hx'))
    c.execute('INSERT INTO gvec_rowids (rowid, chunk_id, chunk_offset) VALUES (?, 1, ?)', (cid, cid - 1))
    vecs.append(unit(300))
    blob = b''.join(struct.pack('<384f', *v) for v in vecs) + bytes(4 * D * (1024 - len(vecs)))
    c.execute('INSERT INTO gvec_chunks VALUES (1, 1024, ?, ?)', (bytes(valid), b''))
    c.execute('INSERT INTO gvec_vector_chunks00 VALUES (1, ?)', (blob,))
    c.commit()
`])
writeIndex(INDEX, { turbines: 3, bread: 100, tax: 200 }, 20)

const config = await import('../dist/config.js')
const M = await import('../dist/semanticMap.js')
const { saveIndexSettings } = await import('../dist/indexSettings.js')
const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(M.semanticMapRoutes)
await app.ready()
const get = async url => { const r = await app.inject({ method: 'GET', url }); return { status: r.statusCode, body: r.json() } }
const runScript = (...extra) => JSON.parse(execFileSync(python, [path.join(root, 'indexer', 'semantic_map.py'), '--index', INDEX, '--out', config.SEMANTIC_MAP_DIR, ...extra], { encoding: 'utf8' }).trim())

test('the map is made from the stored vectors: every file with embeddings, in clusters named for their folders', { skip: !hasNumpy && 'numpy is not installed' }, () => {
  const summary = runScript('--sample', '100')
  assert.equal(summary.available, true)
  assert.equal(summary.files, 60, 'a passage in an invalid slot is not a file of the map')
  assert.equal(summary.model, 'minilm-l6-mean-int8')
  const overview = JSON.parse(fs.readFileSync(path.join(config.SEMANTIC_MAP_DIR, 'overview.json'), 'utf8'))
  assert.equal(overview.files, 60)
  assert.ok(overview.clusters.length >= 4 && overview.clusters.length <= 14)
  assert.equal(overview.clusters.reduce((s, c) => s + c.count, 0), 60)
  assert.deepEqual(overview.clusters.map(c => c.count), [...overview.clusters.map(c => c.count)].sort((a, b) => b - a), 'largest cluster first')
  for (const p of overview.points) for (const k of ['x', 'y', 'z']) assert.ok(p[k] >= 0 && p[k] <= 1, `${k} in [0, 1]`)
  // Each cluster holds files of one topic. Every passage reads "text", which
  // sets no cluster apart, so the folder names it; a folder split into two
  // clusters gives the second one a number, so no two names are alike.
  for (const c of overview.clusters) {
    const members = JSON.parse(fs.readFileSync(path.join(config.SEMANTIC_MAP_DIR, `cluster-${c.c}.json`), 'utf8')).points
    assert.equal(members.length, c.count)
    const folders = new Set(members.map(m => m.p.split('/')[0]))
    assert.equal(folders.size, 1, `cluster ${c.c} mixes ${[...folders]}`)
    assert.equal(c.folder, [...folders][0])
    assert.ok(c.label === c.folder || c.label.startsWith(`${c.folder} (`), c.label)
    assert.deepEqual(c.terms, [])
  }
  assert.equal(new Set(overview.clusters.map(c => c.label)).size, overview.clusters.length, 'names are unique')
  // Links: each cluster to its two nearest, each pair once, nearest first.
  const k = overview.clusters.length
  assert.ok(overview.links.length >= k && overview.links.length <= 2 * k, `${overview.links.length} links for ${k} clusters`)
  const pairs = overview.links.map(l => `${l.a}-${l.b}`)
  assert.equal(new Set(pairs).size, pairs.length, 'each pair once')
  for (const l of overview.links) assert.ok(l.a < l.b && l.sim <= 1 && l.sim >= -1)
  for (let c = 0; c < k; c++) assert.ok(overview.links.some(l => l.a === c || l.b === c), `cluster ${c} is linked`)
  assert.deepEqual(overview.links.map(l => l.sim), [...overview.links.map(l => l.sim)].sort((a, b) => b - a))
  // Five clusters from three topic folders: a folder split in two gives the
  // nearest pair, while different topics (orthogonal here) are far apart.
  const folderOf = c => overview.clusters.find(x => x.c === c).folder
  assert.equal(folderOf(overview.links[0].a), folderOf(overview.links[0].b), 'the strongest link joins two halves of one topic')
  for (const l of overview.links) if (folderOf(l.a) !== folderOf(l.b)) assert.ok(l.sim < overview.links[0].sim - 0.2, `${l.a}-${l.b} ${l.sim}`)
  // The sample spreads across the clusters, a few from each at least.
  const sampled = new Set(overview.points.map(p => p.c))
  assert.equal(sampled.size, overview.clusters.length)
  assert.ok(overview.points.length <= 100)
})

test('a library of a few files maps too: fewer files than the dimensions clusters are found in', { skip: !hasNumpy && 'numpy is not installed' }, () => {
  const small = path.join(tmp, 'small')
  writeIndex(small, { notes: 7 }, 5)
  const out = path.join(tmp, 'small-map')
  const summary = JSON.parse(execFileSync(python, [path.join(root, 'indexer', 'semantic_map.py'), '--index', small, '--out', out], { encoding: 'utf8' }).trim())
  assert.deepEqual([summary.available, summary.files], [true, 5])
  const overview = JSON.parse(fs.readFileSync(path.join(out, 'overview.json'), 'utf8'))
  assert.equal(overview.clusters.reduce((n, c) => n + c.count, 0), 5)
  assert.equal(overview.points.length, 5)
})

// The naming functions on their own, through the script's module.
const py = code => JSON.parse(execFileSync(python, ['-c', `
import json, sys
sys.path.insert(0, ${JSON.stringify(path.join(root, 'indexer'))})
import semantic_map as sm
${code}`], { encoding: 'utf8' }).trim())

test('clusters are named by their distinctive words: TF-IDF against the whole map, unique, short', { skip: !hasNumpy && 'numpy is not installed' }, () => {
  // Words, stop words, short words and digit runs.
  assert.deepEqual(py(`print(json.dumps(sm.file_terms('The turbine blades of the turbine; v2.1.0 build 20261023 and xy ok turbine Rotor rotor')))`),
    [['turbine', 3], ['rotor', 2], ['blades', 1], ['build', 1]])
  // Three folders whose files share the word "docs" (so it names nothing) and
  // each have words of their own; the folder "docs" twice under two parents.
  const tmpIdx = path.join(tmp, 'named')
  execFileSync(python, ['-c', `
import os, random, sqlite3, struct
random.seed(2)
D = 384
words = {'pw/docs': 'kubernetes helm ingress cluster', 'gs/docs': 'turbine blade rotor wind', 'kitchen': 'sourdough starter flour oven'}
root = ${JSON.stringify(tmpIdx)}
for i, (folder, text) in enumerate(words.items()):
    d = os.path.join(root, folder)
    os.makedirs(d, exist_ok=True)
    c = sqlite3.connect(os.path.join(d, 'db.db'))
    c.execute('CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT)')
    c.execute('CREATE TABLE gvec_rowids (rowid INTEGER PRIMARY KEY AUTOINCREMENT, id, chunk_id INTEGER, chunk_offset INTEGER)')
    c.execute('CREATE TABLE gvec_chunks (chunk_id INTEGER PRIMARY KEY AUTOINCREMENT, size INTEGER NOT NULL, validity BLOB NOT NULL, rowids BLOB NOT NULL)')
    c.execute('CREATE TABLE gvec_vector_chunks00 (rowid PRIMARY KEY, vectors BLOB NOT NULL)')
    vecs, valid = [], bytearray(128)
    for f in range(12):
        cid = f + 1
        c.execute('INSERT INTO gchunks VALUES (?, ?, ?, ?, ?, ?)', (cid, '1', f'n{f}.md', 0, f'docs docs {text} {text} note{f}', f'h{cid}'))
        c.execute('INSERT INTO gvec_rowids (rowid, chunk_id, chunk_offset) VALUES (?, 1, ?)', (cid, cid - 1))
        v = [random.gauss(0, 0.05) for _ in range(D)]; v[i * 40] += 1.0
        vecs.append(v)
        valid[(cid - 1) >> 3] |= 1 << ((cid - 1) & 7)
    blob = b''.join(struct.pack('<384f', *v) for v in vecs) + bytes(4 * D * (1024 - len(vecs)))
    c.execute('INSERT INTO gvec_chunks VALUES (1, 1024, ?, ?)', (bytes(valid), b''))
    c.execute('INSERT INTO gvec_vector_chunks00 VALUES (1, ?)', (blob,))
    c.commit()
`])
  const out = path.join(tmp, 'named-map')
  execFileSync(python, [path.join(root, 'indexer', 'semantic_map.py'), '--index', tmpIdx, '--out', out])
  const overview = JSON.parse(fs.readFileSync(path.join(out, 'overview.json'), 'utf8'))
  assert.equal(overview.version, 3)
  const labels = overview.clusters.map(c => c.label)
  assert.equal(new Set(labels).size, labels.length, `unique: ${labels}`)
  for (const c of overview.clusters) {
    assert.ok(c.terms.length >= 2 && c.terms.length <= 3, `${c.label}: ${c.terms}`)
    assert.equal(c.label, c.terms.join(', '))
    assert.ok(c.label.length <= 26 || c.terms.length === 2, c.label)
    assert.ok(!c.terms.includes('docs'), `a word every file has names nothing: ${c.label}`)
    assert.ok(c.folder, 'the folder name stays, for hover')
  }
  const topic = { pw: 'kubernetes helm ingress cluster', gs: 'turbine blade rotor wind', kitchen: 'sourdough starter flour oven' }
  for (const c of overview.clusters) {
    const words = topic[c.folder.split(' + ')[0].split('/')[0]]
    assert.ok(words, c.folder)
    for (const t of c.terms) assert.ok(words.split(' ').includes(t), `${c.label} is named from its own folder's words (${c.folder})`)
  }

  // Unique names: two clusters with the same best words take the next ones;
  // a term another name has goes behind fresh ones.
  const [names, chosen] = py(`print(json.dumps(sm.name_clusters([['alpha', 'beta', 'gamma'], ['alpha', 'beta', 'delta'], ['alpha', 'epsilon', 'zeta'], [], ['lonely']], ['a', 'b', 'c', 'pw/docs + gs/docs', 'admin + hr'])))`)
  assert.equal(new Set(names).size, 5, names.join(' | '))
  assert.equal(names[0], 'alpha, beta, gamma')
  assert.ok(!names[1].startsWith('alpha, beta'), names[1])
  assert.equal(names[3], 'pw/docs + gs/docs', 'no words: the folder name')
  assert.equal(names[4], 'admin + hr', 'one word says less than the folder name')
  assert.deepEqual([chosen[3], chosen[4]], [[], []])

  // The folder fallback keeps a parent where two names would read the same:
  // both clusters read "pw + gs" at the top, so they go a level deeper, and
  // the first one's second folder is also called docs.
  assert.deepEqual(py(`
import numpy as np
paths = ['pw/docs/a.md'] * 5 + ['gs/docs/b.md'] * 4 + ['pw/src/c.py'] * 5 + ['gs/lib/d.py'] * 4
assign = np.array([0] * 9 + [1] * 9, dtype=np.int32)
print(json.dumps(sm.label_clusters(paths, assign, 2)))`), ['pw/docs + gs/docs', 'pw/src + lib'])
})

test('the routes read the map and never make one while it is there', { skip: !hasNumpy && 'numpy is not installed' }, async () => {
  let builds = 0
  M.resetSemanticMapForTests()
  M.setSemanticMapBuilderForTests(async () => { builds++ })
  const a = await get('/api/kb/semantic-map')
  assert.equal(a.status, 200)
  assert.equal(a.body.available, true)
  assert.equal(a.body.building, false)
  await get('/api/kb/semantic-map')
  await new Promise(r => setTimeout(r, 20))
  assert.equal(builds, 0, 'reading the map does not rebuild it')

  const c0 = a.body.clusters[0]
  const page = await get(`/api/kb/semantic-map/cluster/${c0.c}?offset=0&limit=5`)
  assert.equal(page.body.count, c0.count)
  assert.equal(page.body.points.length, Math.min(5, c0.count))
  const rest = await get(`/api/kb/semantic-map/cluster/${c0.c}?offset=5&limit=5000`)
  assert.equal(rest.body.points.length, c0.count - 5)
  assert.deepEqual(new Set([...page.body.points, ...rest.body.points].map(p => p.p)).size, c0.count, 'pages do not overlap')
  assert.equal((await get('/api/kb/semantic-map/cluster/99')).status, 404)
  assert.equal((await get('/api/kb/semantic-map/cluster/x')).status, 400)
})

// The nearest files by meaning, from real sqlite-vec tables made with GUFI's
// own sqlite3, which carries the extension.
const sqlite3Bin = path.join(config.GUFI_BIN, 'gufi_sqlite3')
const hasVec = (() => {
  try { return execFileSync(sqlite3Bin, [], { input: 'SELECT vec_version();', encoding: 'utf8' }).trim().length > 0 } catch { return false }
})()
// Unit vectors mostly along `axis`, leaning toward `toward` by `lean`.
const vecHex = (axis, toward = axis, lean = 0) => {
  const v = new Float32Array(384)
  v[axis] = 1 - lean; v[toward] += lean
  const n = Math.hypot(...v)
  const buf = Buffer.alloc(384 * 4)
  v.forEach((x, i) => buf.writeFloatLE(x / n, i * 4))
  return `X'${buf.toString('hex')}'`
}
const vecFolder = (folder, model, files) => {
  const d = path.join(INDEX, folder)
  fs.mkdirSync(d, { recursive: true })
  const sql = [
    'CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT);',
    'CREATE VIRTUAL TABLE gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);',
    'CREATE TABLE gvecmodel (model TEXT);',
    `INSERT INTO gvecmodel VALUES ('${model}');`,
  ]
  let cid = 0
  for (const [fname, ...passages] of files) for (const hex of passages) {
    cid++
    sql.push(`INSERT INTO gchunks VALUES (${cid}, '1', '${fname}', ${cid}, 'text of ${fname}', 'h${cid}');`, `INSERT INTO gvec (cid, fp384) VALUES (${cid}, ${hex});`)
  }
  execFileSync(sqlite3Bin, [path.join(d, 'db.db')], { input: sql.join('\n') })
}

test('a file\'s nearest files by meaning: the mean of its passages, across folders, one model, with their places', { skip: !(hasVec && hasNumpy) && 'needs GUFI\'s sqlite3 with sqlite-vec, and numpy' }, async () => {
  vecFolder('near', 'minilm-l6-mean-int8', [
    ['a.md', vecHex(10), vecHex(10, 11, 0.2)],   // two passages, both along 10
    ['b.md', vecHex(10, 11, 0.3)],
    ['far.md', vecHex(200)],
  ])
  vecFolder('near/deeper', 'minilm-l6-mean-int8', [['c.md', vecHex(10, 12, 0.4)], ['d.md', vecHex(10, 13, 0.6)]])
  // The same direction from another model is not comparable: never a neighbor.
  vecFolder('other-model', 'minilm384', [['twin.md', vecHex(10)]])
  runScript('--sample', '200')
  M.resetSemanticMapForTests()
  M.resetNeighborsForTests()

  const r = await get(`/api/kb/semantic-map/neighbors?file=${encodeURIComponent('near/a.md')}&k=3`)
  assert.equal(r.status, 200)
  assert.equal(r.body.file, 'near/a.md')
  assert.deepEqual(r.body.neighbors.map(n => n.p), ['near/b.md', 'near/deeper/c.md', 'near/deeper/d.md'])
  const sims = r.body.neighbors.map(n => n.sim)
  assert.deepEqual(sims, [...sims].sort((a, b) => b - a), 'nearest first')
  assert.ok(sims[0] > 0.9 && sims[0] <= 1, `${sims[0]}`)
  for (const n of r.body.neighbors) for (const key of ['x', 'y', 'z', 'c']) assert.equal(typeof n[key], 'number', `${n.p} has its place on the map`)
  assert.ok(!r.body.neighbors.some(n => n.p === 'near/a.md' || n.p.startsWith('other-model/')))

  // The answer is kept; a file or folder without vectors has none, and says so.
  const again = await get(`/api/kb/semantic-map/neighbors?file=${encodeURIComponent('near/a.md')}&k=3`)
  assert.deepEqual(again.body, r.body)
  const none = await get('/api/kb/semantic-map/neighbors?file=turbines/nothing.md')
  assert.deepEqual([none.status, none.body.neighbors, none.body.reason], [200, [], 'none'])
  assert.equal((await get('/api/kb/semantic-map/neighbors?file=..%2Fsecret')).status, 400)
  assert.equal((await get('/api/kb/semantic-map/neighbors')).status, 400)
})

test('rebuilds asked for together run once; a missing map is made once, in the background', async () => {
  let builds = 0
  M.resetSemanticMapForTests()
  M.setSemanticMapBuilderForTests(async () => { builds++ })
  M.scheduleSemanticMapRebuild(30)
  M.scheduleSemanticMapRebuild(10)
  M.scheduleSemanticMapRebuild(50)
  await new Promise(r => setTimeout(r, 120))
  assert.equal(builds, 1)

  fs.rmSync(config.SEMANTIC_MAP_DIR, { recursive: true, force: true })
  M.resetSemanticMapForTests()
  M.setSemanticMapBuilderForTests(async () => { builds++ })
  builds = 0
  const r = await get('/api/kb/semantic-map')
  assert.equal(r.body.available, false)
  assert.equal(r.body.reason, 'none')
  assert.equal(r.body.building, true)
  await get('/api/kb/semantic-map')
  await new Promise(r2 => setTimeout(r2, 30))
  assert.equal(builds, 1, 'one build for a missing map, however many reads')
})

test('with search by meaning off for the main folder there is no map, and it says why', async () => {
  await saveIndexSettings('kb', { meaning: 'off' })
  const r = await get('/api/kb/semantic-map')
  assert.deepEqual([r.body.available, r.body.reason], [false, 'off'])
  await saveIndexSettings('kb', { meaning: 'documents' })
})
