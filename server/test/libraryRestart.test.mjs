// A library the Studio builds of a folder here stays in search and Query
// with no visit to Settings: right after its build, after a restart, and
// while a build a restart interrupted runs again. Before, each build or
// sync forgot what the library's index holds, so search left its words out
// until Settings, Libraries probed it again; and a rebuild swapped in an
// index with names only, so its words were missing until the text was read
// again. Needs GUFI (GUFI_BIN, else /opt/gufi/bin) and skips without it.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const HOME = tmp('lrestart-home-')
const KB = tmp('lrestart-kb-')
const IX = tmp('lrestart-ix-')
const SRC = tmp('lrestart-shelf-')
const GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
const REAL = fs.existsSync(path.join(GUFI, 'gufi_query')) && fs.existsSync(path.join(GUFI, 'gufi_dir2index'))

// Python runs through this wrapper, which notes each indexer script it
// starts and holds enrich.py while the HOLD file exists.
const CALLS = path.join(HOME, 'calls')
const HOLD = path.join(HOME, 'hold')
const WRAPPER = path.join(HOME, 'python')
fs.writeFileSync(WRAPPER, `#!/bin/sh
for a in "$@"; do
  case "$a" in *.py)
    n=$(basename "$a"); echo "$n" >> '${CALLS}'
    if [ "$n" = enrich.py ]; then while [ -f '${HOLD}' ]; do sleep 0.1; done; fi
    break;;
  esac
done
exec python3 "$@"
`, { mode: 0o755 })
const calls = () => { try { return fs.readFileSync(CALLS, 'utf8').split('\n').filter(Boolean) } catch { return [] } }

Object.assign(process.env, { HOME, KB_ROOT: KB, INDEX_BASE: IX, GUFI_BIN: GUFI, PYTHON_BIN: WRAPPER, SWEEP_INTERVAL_SEC: '300' })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR', 'STUDIO_LIBRARIES']) delete process.env[k]

const write = (root, rel, text) => { fs.mkdirSync(path.dirname(path.join(root, rel)), { recursive: true }); fs.writeFileSync(path.join(root, rel), text) }
write(KB, 'notes/main.md', 'a note in the main folder\n')
for (let i = 0; i < 12; i++) write(SRC, `shelf${i % 3}/doc${i}.md`, `document ${i} about the wombatword survey\n`)

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { queryRoutes } = await import('../dist/query.js')
const B = await import('../dist/libraryBuild.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
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

// What a viewer sees, without opening Settings (whose library list probes
// every library again): search and Query across every library.
const searched = async () => {
  const r = await j('GET', '/api/search?q=wombatword&library=all')
  assert.equal(r.status, 200, JSON.stringify(r.body))
  return r.body.hits.filter(h => h.library.id === 'shelf').length
}
// The Query page across every library, by size (canned), by name and by
// words (the builder), as rows from the library: each must list it.
const builder = filters => ({ builder: { source: 'files', filters, groupBy: 'none', sort: 'size', dir: 'desc', limit: 100 }, library: 'all' })
const QUERIES = {
  size: { canned: 'largest', n: 100, library: 'all' },
  name: builder([{ field: 'name', op: 'contains', value: 'doc' }]),
  words: builder([{ field: 'text', op: 'match', value: 'wombatword' }]),
}
const queriedBy = async () => {
  const out = {}
  for (const [k, body] of Object.entries(QUERIES)) {
    const r = await j('POST', '/api/query', body)
    assert.equal(r.status, 200, JSON.stringify(r.body))
    out[k] = { rows: r.body.rowLibraries.filter(id => id === 'shelf').length, failed: (r.body.failed ?? []).map(f => f.library) }
  }
  return out
}
const ALL_ROWS = { size: { rows: 12, failed: [] }, name: { rows: 12, failed: [] }, words: { rows: 12, failed: [] } }
const queried = async () => {
  const q = await queriedBy()
  assert.deepEqual(q, ALL_ROWS)
  return q.size.rows
}
// What the server does when it starts again: everything held in memory is
// gone, and main.ts probes every library before it answers.
const restart = async () => {
  L.resetLibrariesForTests()
  G.invalidateDbList()
  await L.probeAll()
}

test('a library the Studio builds is searched by its words and queried after its build, after a restart, and while an interrupted build runs again', { skip: !REAL && 'GUFI not installed', timeout: 300_000 }, async () => {
  const main = await finished((await j('POST', '/api/index/build')).body.id)
  assert.equal(main.state, 'done', main.error ?? '')

  const r = await j('POST', '/api/libraries/build', { sourceRoot: SRC, label: 'Shelf', id: 'shelf', depth: 'text' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const built = await finished(r.body.job.id)
  assert.equal(built.state, 'done', built.error ?? '')

  // Right after the build.
  assert.equal(L.getLibrary('shelf').caps.fullText, true, 'its words are known once it is built')
  assert.equal(await searched(), 12, 'found by its words after the build')
  assert.equal(await queried(), 12)

  // After a restart.
  await restart()
  assert.equal(await searched(), 12, 'found by its words after a restart')
  assert.equal(await queried(), 12, 'queried after a restart')

  // A build a restart interrupted runs again at start (startLibrarySync);
  // while it reads the text, the index it replaces keeps answering.
  L.updateBuild('shelf', { state: 'updating' })
  await restart()
  fs.writeFileSync(CALLS, '')
  fs.writeFileSync(HOLD, '')
  try {
    B.startLibrarySync(() => {})
    await until(() => calls().includes('enrich.py'), 60_000)
    assert.equal(await searched(), 12, 'its words stay searchable while the rebuild reads the text')
    assert.equal(await queried(), 12, 'and it stays in Query')
  } finally {
    fs.rmSync(HOLD, { force: true })
  }
  await until(() => L.addedDef('shelf')?.build?.state === 'ready', 120_000)
  assert.equal(await searched(), 12, 'and after the rebuild, with no visit to Settings')
  assert.equal(await queried(), 12)
  assert.equal(fs.existsSync(path.join(L.addedDef('shelf').build.indexDir, '.build')), false, 'the staging folder is gone')
})

test('an existing GUFI index connected as it is stays in Query and search by name after a restart', { skip: !REAL && 'GUFI not installed', timeout: 120_000 }, async () => {
  const { execFileSync } = await import('node:child_process')
  const src = tmp('lrestart-archive-')
  for (let i = 0; i < 5; i++) write(src, `box/numbat${i}.dat`, 'x'.repeat(100 + i))
  const out = tmp('lrestart-archive-ix-')
  execFileSync(path.join(GUFI, 'gufi_dir2index'), ['-n', '2', src, out])
  const root = path.join(out, path.basename(src))
  const r = await j('POST', '/api/libraries', { id: 'archive', label: 'Archive', indexRoot: root })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const names = async () => (await j('GET', '/api/search?q=numbat&library=all')).body.hits.filter(h => h.library.id === 'archive').length
  const rows = async () => (await j('POST', '/api/query', { canned: 'largest', n: 100, library: 'all' })).body.rowLibraries.filter(id => id === 'archive').length
  assert.equal(await names(), 5)
  assert.equal(await rows(), 5)
  await restart()
  assert.equal(await names(), 5, 'found by name after a restart')
  assert.equal(await rows(), 5, 'queried after a restart')
})
