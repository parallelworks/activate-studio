// A change of index depth applies at the next sync (Sync now, or the
// background sync, which a change asks for soon), since it touches no file
// the sync's look for changed folders would find: only the phases the new
// depth adds run, over files that did not change, on the index in place.
// The record of the depth is written only when that succeeded; an index
// from before the record has none and is not indexed again; a depth that
// went down runs nothing; a PDF converted without OCR is converted again
// with it. The same for a library the Studio builds. GUFI's real tools
// where they are installed (search is checked then), the stand-in from
// testdata otherwise.
import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const ROOT = path.resolve(import.meta.dirname, '..', '..')
const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const HOME = tmp('dsync-home-')
const KB = tmp('dsync-kb-')
const IX = tmp('dsync-ix-')
const REAL_GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
const GUFI = fs.existsSync(path.join(REAL_GUFI, 'gufi_dir2index')) ? REAL_GUFI : path.join(ROOT, 'testdata', 'fake-gufi')
const REAL = GUFI === REAL_GUFI && fs.existsSync(path.join(GUFI, 'gufi_query'))
const TESSERACT = spawnSync('tesseract', ['--version'], { stdio: 'ignore' }).status === 0

// Python runs through this wrapper, which notes each indexer script it
// starts, and fails enrich.py while the FAIL file exists.
const CALLS = path.join(HOME, 'calls')
const FAIL = path.join(HOME, 'fail')
const WRAPPER = path.join(HOME, 'python')
fs.writeFileSync(WRAPPER, `#!/bin/sh
for a in "$@"; do
  case "$a" in *.py)
    n=$(basename "$a"); echo "$n" >> '${CALLS}'
    if [ "$n" = enrich.py ] && [ -f '${FAIL}' ]; then echo 'enrich.py failed on purpose' >&2; exit 1; fi
    break;;
  esac
done
exec python3 "$@"
`, { mode: 0o755 })
const calls = () => { try { return fs.readFileSync(CALLS, 'utf8').split('\n').filter(Boolean) } catch { return [] } }

Object.assign(process.env, { HOME, KB_ROOT: KB, INDEX_BASE: IX, GUFI_BIN: GUFI, PYTHON_BIN: WRAPPER, SWEEP_INTERVAL_SEC: '300' })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR']) delete process.env[k]

const put = (rel, from) => { fs.mkdirSync(path.dirname(path.join(KB, rel)), { recursive: true }); fs.copyFileSync(from, path.join(KB, rel)) }
const write = (rel, text) => { fs.mkdirSync(path.dirname(path.join(KB, rel)), { recursive: true }); fs.writeFileSync(path.join(KB, rel), text) }
write('README.md', '# top\n')
write('notes/a.md', 'notes about a quokkafish\n')
put('pics/sign.png', path.join(ROOT, 'deploy', 'mac', 'testdata', 'ocr-sample.png'))
put('scans/form-mixed.pdf', path.join(ROOT, 'testdata', 'corpus', 'scans', 'form-mixed.pdf'))

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const I = await import('../dist/indexing.js')
const P = await import('../dist/preextract.js')
const B = await import('../dist/libraryBuild.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
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
})
const RECORD = path.join(IX, 'index-depth.json')
const record = () => JSON.parse(fs.readFileSync(RECORD, 'utf8'))
const status = async () => (await j('GET', '/api/index/status')).body
const phases = job => Object.fromEntries(job.phases.map(p => [p.id, [p.state, p.note]]))
const hits = async q => (await j('GET', `/api/search?q=${q}`)).body.hits.map(h => h.path)
/** Sync now, as the button asks: waits for what it started. */
const syncNow = async () => (await j('POST', '/api/index/sweep')).body.changed
const setDepth = async depth => assert.equal((await j('PUT', '/api/index/settings', { depth })).status, 200)

test.afterEach(() => { fs.rmSync(FAIL, { force: true }) })

test('a build records the depth it read at; an index from before the record is not indexed again', { timeout: 120_000 }, async () => {
  await setDepth('text')
  const job = await finished((await j('POST', '/api/index/build')).body.id)
  assert.equal(job.state, 'done', job.error ?? '')
  assert.deepEqual(record(), { depth: 'text', ocr: false, meaningSpeed: 'standard' })
  if (REAL) assert.deepEqual(await hits('heliotrope'), [], 'no OCR at the Text depth')

  // An index from before the record: the sync records the depth set now and runs nothing.
  fs.rmSync(RECORD)
  const last = (await status()).last.id
  fs.writeFileSync(CALLS, '')
  assert.deepEqual(await syncNow(), [])
  assert.equal((await status()).last.id, last, 'no job')
  assert.deepEqual(calls(), [])
  assert.equal(record().depth, 'text')
})

test('Text to Text + OCR: Sync now reads images and scanned pages again, with no build and no embedding', { skip: !TESSERACT && 'tesseract is not installed', timeout: 120_000 }, async () => {
  await setDepth('ocr')
  fs.writeFileSync(CALLS, '')
  await syncNow()
  const job = (await status()).last
  assert.equal(job.state, 'done', job.error ?? '')
  assert.equal(job.label, 'Applying the new index depth: OCR')
  const p = phases(job)
  assert.deepEqual(p.names, ['skipped', 'unchanged'], 'the names are not read again')
  assert.equal(p.documents[0], 'done')
  assert.equal(p.text[0], 'done')
  assert.deepEqual(p.meaning, ['skipped', 'unchanged'])
  assert.deepEqual(calls(), ['enrich.py'])
  assert.deepEqual(record(), { depth: 'ocr', ocr: true, meaningSpeed: 'standard' })
  if (REAL) {
    assert.deepEqual(await hits('heliotrope'), ['pics/sign.png'], 'the picture is found by its text')
    assert.ok((await hits('7742')).includes('scans/form-mixed.pdf'), 'and the scanned body of the PDF')
  }
  // Applied once: the next sync has nothing to do.
  fs.writeFileSync(CALLS, '')
  assert.deepEqual(await syncNow(), [])
  assert.deepEqual(calls(), [])
})

test('a depth that went down runs nothing and is recorded', { timeout: 60_000 }, async () => {
  await setDepth('names')
  const last = (await status()).last.id
  fs.writeFileSync(CALLS, '')
  assert.deepEqual(await syncNow(), [])
  assert.equal((await status()).last.id, last, 'no job')
  assert.deepEqual(calls(), [])
  assert.equal(record().depth, 'names')
})

test('a failed run keeps the old record, and the next sync tries again', { timeout: 120_000 }, async () => {
  // Names to Text: the text phase runs over the whole folder.
  await setDepth('text')
  fs.writeFileSync(FAIL, '')
  await syncNow()
  const failed = (await status()).last
  assert.equal(failed.state, 'error')
  assert.match(failed.error, /failed on purpose/)
  assert.equal(record().depth, 'names', 'not recorded')
  fs.rmSync(FAIL)
  fs.writeFileSync(CALLS, '')
  await syncNow()
  const job = (await status()).last
  assert.equal(job.state, 'done', job.error ?? '')
  assert.equal(job.label, 'Applying the new index depth: text')
  assert.deepEqual(calls(), ['enrich.py'])
  assert.equal(record().depth, 'text')
  if (REAL) assert.deepEqual(await hits('quokkafish'), ['notes/a.md'])
})

test('Text to search by meaning runs the meaning phase alone', { timeout: 120_000 }, async () => {
  // With OCR on already, so that only the meaning is new.
  if (TESSERACT) { await setDepth('ocr'); await syncNow() }
  await setDepth('meaning-documents')
  fs.writeFileSync(CALLS, '')
  await syncNow()
  const job = (await status()).last
  assert.equal(job.state, 'done', job.error ?? '')
  const p = phases(job)
  assert.deepEqual([p.names, p.documents, p.text], [['skipped', 'unchanged'], ['skipped', 'unchanged'], ['skipped', 'unchanged']])
  assert.notEqual(p.meaning[1], 'unchanged', 'the meaning phase ran (or said why it could not, here without the model)')
  assert.ok(!calls().includes('enrich.py'))
  assert.equal(record().depth, 'meaning-documents')
})

test('a PDF converted without OCR is converted again by a pass with OCR, and only then', { skip: !(TESSERACT && P.downmarkNative()) && 'needs tesseract and the native downmark', timeout: 120_000 }, async () => {
  const kb = tmp('dsync-pdf-kb-')
  const cache = tmp('dsync-pdf-cache-')
  fs.copyFileSync(path.join(ROOT, 'testdata', 'corpus', 'scans', 'form-mixed.pdf'), path.join(kb, 'form.pdf'))
  const entry = () => fs.readFileSync(path.join(cache, 'form.pdf.txt'), 'utf8')
  let s = await P.preExtract({ kbRoot: kb, cacheRoot: cache, ocr: false })
  assert.equal(s.converted, 1)
  assert.match(entry(), /^<!-- extracted by downmark [^ ]+ \(no OCR\) -->/)
  assert.doesNotMatch(entry(), /7742/)
  s = await P.preExtract({ kbRoot: kb, cacheRoot: cache, ocr: false })
  assert.equal(s.reused, 1, 'still without OCR: reused')
  s = await P.preExtract({ kbRoot: kb, cacheRoot: cache })
  assert.equal(s.converted, 1, 'with OCR: converted again')
  assert.match(entry(), /SCANNED BODY 7742/)
  assert.doesNotMatch(entry(), /no OCR/)
  assert.equal((await P.preExtract({ kbRoot: kb, cacheRoot: cache })).reused, 1)
  assert.equal((await P.preExtract({ kbRoot: kb, cacheRoot: cache, ocr: false })).reused, 1, 'text read with OCR is kept at a lower depth')
})

test('saving a depth asks for a sync soon, once for several changes in a row', { timeout: 120_000 }, async () => {
  I.startSweepTimer(() => {})
  await setDepth('names')
  await syncNow()
  await until(async () => record().depth === 'names')
  await sleep(2500) // the request that saving Names made
  const after = (await status()).last.id
  // A slider dragged across stops saves each one.
  await setDepth('text')
  await setDepth('ocr')
  await setDepth('meaning-documents')
  await until(async () => record().depth === 'meaning-documents', 60_000)
  await until(async () => !(await status()).running)
  const applied = (await status()).runs.filter(r => r.id > after)
  assert.deepEqual(applied.map(r => r.label), [`Applying the new index depth: ${TESSERACT ? 'text and OCR' : 'text'} and search by meaning`])
  assert.equal(applied[0].state, 'done', applied[0].error ?? '')
})

// ---- a library the Studio builds ----

const papers = tmp('dsync-papers-')
fs.writeFileSync(path.join(papers, 'intro.md'), '# Introduction\nThe turbine blade study.\n')
fs.copyFileSync(path.join(ROOT, 'deploy', 'mac', 'testdata', 'ocr-sample.png'), path.join(papers, 'sign.png'))

test('a library the Studio builds applies a new depth at its sync, and saving the depth asks for one', { skip: !REAL && 'GUFI not installed', timeout: 180_000 }, async () => {
  const L = await import('../dist/libraries.js')
  const G = await import('../dist/gufi.js')
  const lib = async () => (await j('GET', '/api/libraries')).body.libraries.find(l => l.id === 'papers')
  const r = await j('POST', '/api/libraries/build', { sourceRoot: papers, label: 'Papers', depth: 'text' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  await until(async () => { const l = await lib(); return l?.build?.state === 'ready' && !l.build.progress })
  const rec = () => JSON.parse(fs.readFileSync(path.join(IX, 'libraries', 'papers', 'depth-state.json'), 'utf8'))
  assert.equal(rec().depth, 'text')
  const indexRoot = L.getLibrary('papers').indexRoot
  const fts = q => G.searchFts(q, 5, indexRoot).then(h => h.map(x => x.path)).catch(() => [])
  assert.deepEqual(await fts('turbine'), ['intro.md'])
  assert.deepEqual(await fts('heliotrope'), [])

  // Down to Names: recorded, nothing run.
  assert.equal((await j('PUT', '/api/libraries/papers/depth', { depth: 'names' })).status, 200)
  fs.writeFileSync(CALLS, '')
  assert.deepEqual(await B.syncLibrary('papers'), [])
  assert.deepEqual(calls(), [])
  assert.equal(rec().depth, 'names')

  // Back up, past Text to Text + OCR: the sync reads the text again, with OCR, and embeds nothing.
  assert.equal((await j('PUT', '/api/libraries/papers/depth', { depth: TESSERACT ? 'ocr' : 'text' })).status, 200)
  fs.writeFileSync(CALLS, '')
  const changed = await B.syncLibrary('papers')
  assert.deepEqual(changed, [`(index depth: ${TESSERACT ? 'ocr' : 'text'})`])
  assert.deepEqual(calls(), ['enrich.py'])
  assert.equal(rec().depth, TESSERACT ? 'ocr' : 'text')
  assert.equal((await lib()).build.state, 'ready')
  if (TESSERACT) assert.deepEqual(await fts('heliotrope'), ['sign.png'])
  assert.deepEqual(await B.syncLibrary('papers'), [], 'applied once')

  // Saved through the route, the sync follows by itself.
  B.startLibrarySync(() => {})
  assert.equal((await j('PUT', '/api/libraries/papers/depth', { depth: 'names' })).status, 200)
  await until(() => rec().depth === 'names', 30_000)
})
