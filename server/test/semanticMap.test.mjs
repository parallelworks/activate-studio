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
  try { execFileSync(python, ['-c', `import sys; sys.path.insert(0, ${JSON.stringify(path.join(root, 'indexer', '.deps'))}); import numpy`]); return true } catch { return false }
})()

// Three folders, each about one topic: its files' vectors point one way,
// with noise. One passage sits in a slot marked invalid, as a deleted row
// is, and must not count.
execFileSync(python, ['-c', `
import os, random, sqlite3, struct
random.seed(1)
D = 384
def unit(axis):
    v = [random.gauss(0, 0.05) for _ in range(D)]
    v[axis] += 1.0
    return v
topics = {'turbines': 3, 'bread': 100, 'tax': 200}
root = ${JSON.stringify(INDEX)}
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
    for f in range(20):
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
  // Each cluster holds files of one topic: its folder names it.
  for (const c of overview.clusters) {
    const members = JSON.parse(fs.readFileSync(path.join(config.SEMANTIC_MAP_DIR, `cluster-${c.c}.json`), 'utf8')).points
    assert.equal(members.length, c.count)
    const folders = new Set(members.map(m => m.p.split('/')[0]))
    assert.equal(folders.size, 1, `cluster ${c.c} mixes ${[...folders]}`)
    assert.equal(c.label, [...folders][0])
  }
  // The sample spreads across the clusters, a few from each at least.
  const sampled = new Set(overview.points.map(p => p.c))
  assert.equal(sampled.size, overview.clusters.length)
  assert.ok(overview.points.length <= 100)
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
