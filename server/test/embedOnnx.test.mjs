// Search by meaning with the ONNX embedding model: the query worker's
// protocol (a stand-in worker, so this runs everywhere), and, where GUFI,
// the ONNX packages and the model are present, an index embedded by
// embed.py, its per-directory model marker, a search that embeds the query
// with the index's own model, and the GGUF model kept for an older index.
// STUDIO_TEST_ONNX_DIR names a folder with model.onnx and tokenizer.json
// (indexer/fetch_embed.py --model DIR fetches one); STUDIO_TEST_GGUF the
// older GGUF model. Without them those parts skip.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const hasGufi = fs.existsSync(path.join(gufiBin, 'gufi_dir2index')) && fs.existsSync(path.join(gufiBin, 'gufi_sqlite3'))
const onnxDir = process.env.STUDIO_TEST_ONNX_DIR || ''
const gguf = process.env.STUDIO_TEST_GGUF || ''
const py = process.env.PYTHON_BIN || 'python3'
const onnxWhy = (() => {
  if (!onnxDir) return 'STUDIO_TEST_ONNX_DIR is not set'
  const r = spawnSync(py, ['-c', 'import sys; sys.path.insert(0, sys.argv[1]); import embed_onnx; print(embed_onnx.unavailable(sys.argv[2]) or "")',
    path.join(ROOT, 'indexer'), onnxDir], { encoding: 'utf8' })
  return r.status === 0 ? r.stdout.trim() : (r.stderr || 'python failed').trim()
})()
const skipOnnx = !hasGufi ? 'GUFI not installed' : onnxWhy || false
const skipGguf = !hasGufi ? 'GUFI not installed' : !gguf || !fs.existsSync(gguf) ? 'STUDIO_TEST_GGUF is not set' : false

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'embed-onnx-'))
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
const models = path.join(dir, 'ix', 'models')
fs.mkdirSync(models, { recursive: true })
if (onnxDir) fs.symlinkSync(path.resolve(onnxDir), path.join(models, 'minilm-l6'))
if (gguf) fs.symlinkSync(path.resolve(gguf), path.join(models, 'minilm384.gguf'))

const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const prose = (s) => `${s} `.repeat(12)
write(path.join(dir, 'kb', 'engines', 'turbine.md'), prose('The turbine blade study measured metal fatigue under repeated cyclic loading at high temperature.'))
write(path.join(dir, 'kb', 'weather', 'forecast.md'), prose('The regional weather forecast predicts heavy rainfall, strong winds and a cold front moving east.'))
write(path.join(dir, 'kb', 'kitchen', 'bread.md'), prose('Knead the dough, let it rise for two hours, then bake the bread in a hot oven until golden.'))

const W = await import('../dist/embedWorker.js')
const G = await import('../dist/gufi.js')

/** A stand-in worker: answers every text with the same vector, or misbehaves as told. */
function stub(behaviour) {
  const f = path.join(dir, `stub-${behaviour}.py`)
  fs.writeFileSync(f, `import sys, json, base64, struct
mode = ${JSON.stringify(behaviour)}
if mode == 'cannot-start':
    print(json.dumps({'ready': False, 'error': 'onnxruntime is not installed'})); sys.stdout.flush(); sys.exit(1)
print(json.dumps({'ready': True, 'model': 'minilm-l6-mean-int8'})); sys.stdout.flush()
for line in sys.stdin:
    req = json.loads(line)
    if mode == 'crash':
        sys.exit(3)
    v = base64.b64encode(struct.pack('<384f', *([0.05] * 384))).decode()
    print(json.dumps({'id': req['id'], 'vectors': [v for _ in req['texts']]})); sys.stdout.flush()
`)
  return { cmd: py, args: [f] }
}

test('the query worker answers one vector of 384 float32 values per text, and is started once', async () => {
  W.setQueryWorkerCommand(stub('ok'))
  const [a, b] = await W.embedQueries(['first', 'second'])
  assert.equal(a.length, 384 * 4)
  assert.equal(b.length, 384 * 4)
  assert.ok(Math.abs(a.readFloatLE(0) - 0.05) < 1e-6)
  const again = await Promise.all([W.embedQueries(['x']), W.embedQueries(['y'])])
  assert.equal(again.flat().length, 2, 'requests side by side are told apart by id')
  W.stopQueryWorker()
})

test('a worker that cannot start says why, and is not started again at once', async () => {
  W.setQueryWorkerCommand(stub('cannot-start'))
  await assert.rejects(W.embedQueries(['q']), /onnxruntime is not installed/)
  assert.match(W.queryWorkerDown() ?? '', /onnxruntime is not installed/)
  await assert.rejects(W.embedQueries(['q']), /onnxruntime is not installed/)
  W.setQueryWorkerCommand(stub('ok'))
  assert.equal(W.queryWorkerDown(), null, 'choosing a worker clears the failure')
  assert.equal((await W.embedQueries(['q']))[0].length, 1536)
  W.stopQueryWorker()
})

test('a worker that stops mid-request fails that request, and the next one starts a new worker', async () => {
  W.setQueryWorkerCommand(stub('crash'))
  await assert.rejects(W.embedQueries(['q']), /stopped/)
  W.setQueryWorkerCommand(stub('ok'))
  assert.equal((await W.embedQueries(['q']))[0].length, 1536)
  W.stopQueryWorker()
})

test('embed.py says why it cannot embed when neither model is there', () => {
  const r = spawnSync(py, [path.join(ROOT, 'indexer', 'embed.py'), '--index', path.join(dir, 'nowhere'), '--onnx', path.join(dir, 'no-model')], { encoding: 'utf8' })
  assert.notEqual(r.status, 0)
  assert.match(r.stderr, /no embedding model/)
})

const index = path.join(dir, 'ix', 'gufi', 'kb')
function buildIndex() {
  fs.rmSync(path.join(dir, 'ix', 'gufi'), { recursive: true, force: true })
  fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
  execFileSync(py, [path.join(ROOT, 'indexer', 'enrich.py'), '--kb-root', path.join(dir, 'kb'), '--index', index, '--extract-cache', path.join(dir, 'extract')], { stdio: 'ignore' })
  G.invalidateDbList()
}
function embed(...extra) {
  const out = execFileSync(py, [path.join(ROOT, 'indexer', 'embed.py'), '--index', index, '--gufi-sqlite3', path.join(gufiBin, 'gufi_sqlite3'),
    '--jobs', '2', '--cache', path.join(dir, 'cache.db'), ...extra], { encoding: 'utf8', env: { ...process.env, PYTHONDONTWRITEBYTECODE: '1' } })
  G.invalidateDbList()
  return out
}
const markerOf = (rel) => execFileSync(py, ['-c', 'import sqlite3, sys; print(sqlite3.connect(sys.argv[1]).execute("SELECT model FROM gvecmodel").fetchone()[0])',
  path.join(index, rel, 'db.db')], { encoding: 'utf8' }).trim()

test('an index embedded with the ONNX model names it per directory, and a search embeds the query with it', { skip: skipOnnx }, async () => {
  W.setQueryWorkerCommand(null)
  buildIndex()
  const out = embed('--model', path.join(models, 'minilm384.gguf'))
  assert.match(out, /model: minilm-l6-mean-int8 \(onnx\)/)
  assert.equal(markerOf('engines'), 'minilm-l6-mean-int8')
  assert.deepEqual(await G.vectorModels(index), { 'minilm-l6-mean-int8': 3 })
  const hits = await G.searchVector('how much stress can a jet engine part take before it cracks', 3, index)
  assert.equal(hits[0]?.name, 'turbine.md', JSON.stringify(hits))
  assert.equal((await G.searchVector('recipe for baking a loaf', 3, index))[0]?.name, 'bread.md')
  W.stopQueryWorker()
})

test('an index embedded with the older model is still searched, with that model', { skip: skipGguf }, async () => {
  buildIndex()
  embed('--backend', 'lembed', '--model', path.join(models, 'minilm384.gguf'))
  assert.equal(markerOf('weather'), 'minilm384')
  assert.deepEqual(await G.vectorModels(index), { minilm384: 3 })
  assert.equal((await G.searchVector('will it rain tomorrow', 3, index))[0]?.name, 'forecast.md')
})

test('one folder of an index on the other model is embedded with all of it', { skip: skipOnnx || skipGguf }, async () => {
  buildIndex()
  embed('--backend', 'lembed', '--model', path.join(models, 'minilm384.gguf'))
  const out = embed('--model', path.join(models, 'minilm384.gguf'), '--subdir', 'kitchen')
  assert.match(out, /holds vectors of minilm384: embedding all of it with minilm-l6-mean-int8/)
  assert.deepEqual(await G.vectorModels(index), { 'minilm-l6-mean-int8': 3 })
  assert.equal((await G.searchVector('will it rain tomorrow', 3, index))[0]?.name, 'forecast.md')
  W.stopQueryWorker()
})
