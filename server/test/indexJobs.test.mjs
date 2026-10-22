// Index jobs: a build's phases and counts, one status for every view,
// stopping a running job (its programs too), dropping a queued one,
// deleting the index only when confirmed and never the files, the log, and
// a folder pointed at again updating its earlier index rather than
// starting over. GUFI's real gufi_dir2index where it is installed, the
// stand-in from testdata otherwise (it writes the same tree of databases).
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { promisify } from 'node:util'

const run = promisify(execFile)
const ROOT = path.resolve(import.meta.dirname, '..', '..')
const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const HOME = tmp('ixjobs-home-')
const KB = tmp('ixjobs-kb-')
const IX = tmp('ixjobs-ix-')
const REAL_GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
const GUFI = fs.existsSync(path.join(REAL_GUFI, 'gufi_dir2index')) ? REAL_GUFI : path.join(ROOT, 'testdata', 'fake-gufi')
const REAL = GUFI === REAL_GUFI && fs.existsSync(path.join(GUFI, 'gufi_query'))

// The text phase runs Python through this wrapper, which waits while the
// flag file exists: a phase that is slow on purpose, to stop.
const SLOW = path.join(IX, 'slow')
const PIDS = path.join(IX, 'pids')
const WRAPPER = path.join(HOME, 'slowpython')
fs.writeFileSync(WRAPPER, `#!/bin/sh\necho $$ >> '${PIDS}'\nwhile [ -f '${SLOW}' ]; do sleep 0.1; done\nexec python3 "$@"\n`, { mode: 0o755 })

Object.assign(process.env, { HOME, KB_ROOT: KB, INDEX_BASE: IX, GUFI_BIN: GUFI, PYTHON_BIN: WRAPPER })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR']) delete process.env[k]

const write = (rel, text) => { fs.mkdirSync(path.dirname(path.join(KB, rel)), { recursive: true }); fs.writeFileSync(path.join(KB, rel), text) }
write('README.md', '# top\n')
for (const d of ['alpha', 'beta', 'beta/inner', 'gamma']) write(`${d}/note.md`, `notes in ${d} about a quokkafish\n`)
fs.copyFileSync(path.join(ROOT, 'testdata', 'corpus', 'docs', 'handbook.docx'), path.join(KB, 'alpha', 'handbook.docx'))

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  let json = null
  try { json = res.json() } catch { /* text */ }
  return { status: res.statusCode, body: json, text: res.body }
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
const alive = pid => { try { process.kill(pid, 0); return true } catch { return false } }

// A failed assertion must not leave the next test waiting on a held phase.
test.afterEach(() => { fs.rmSync(SLOW, { force: true }) })

test('a build counts each phase, and the one status agrees with the job while it runs and after', { timeout: 120_000 }, async () => {
  fs.writeFileSync(SLOW, '')
  const start = await j('POST', '/api/index/build')
  assert.equal(start.status, 200)
  assert.equal(start.body.kind, 'build')
  // A second request while it runs gets the same job.
  assert.equal((await j('POST', '/api/index/build')).body.id, start.body.id)

  // Held in the text phase: names are counted, done and searchable.
  const during = await until(async () => {
    const s = (await j('GET', '/api/index/status')).body
    return s.job?.phases.find(p => p.id === 'text').state === 'running' ? s : null
  })
  assert.equal(during.running, true)
  assert.equal(during.job.id, start.body.id)
  const names = during.job.phases.find(p => p.id === 'names')
  assert.equal(names.state, 'done')
  assert.equal(names.total, 5) // the top, alpha, beta, beta/inner and gamma
  assert.equal(names.done, names.total)
  assert.ok(during.job.searchableAt, 'a first build puts the names in place before the text')
  assert.deepEqual((await j('GET', `/api/index/job/${start.body.id}`)).body.phases, during.job.phases)
  assert.equal(during.indexed, true)

  fs.rmSync(SLOW)
  const job = await finished(start.body.id)
  assert.equal(job.state, 'done', job.error ?? '')
  const text = job.phases.find(p => p.id === 'text')
  assert.equal(text.state, 'done')
  assert.ok(text.total > 0 && text.done === text.total)
  assert.match(text.note, /files with text/)
  const docs = job.phases.find(p => p.id === 'documents')
  assert.equal(docs.state, 'done')
  assert.equal(docs.total, 1)
  assert.match(docs.note, /1 converted/)
  for (const p of job.phases.filter(x => x.state === 'done')) assert.ok(p.ms >= 0)

  const after = (await j('GET', '/api/index/status')).body
  assert.equal(after.running, false)
  assert.equal(after.job, null)
  assert.equal(after.lastBuild.id, job.id)
  assert.equal(after.last.id, job.id)
  assert.equal(after.runs[0].id, job.id)
  assert.equal(after.incomplete, false)
  assert.equal(after.threads > 0, true)
  if (REAL) {
    const hits = await j('GET', '/api/search?q=quokkafish')
    assert.ok(hits.body.hits.some(h => h.path === 'beta/inner/note.md'), JSON.stringify(hits.body))
  }

  // The log names the job and its phases.
  const log = await j('GET', '/api/index/log?lines=50')
  assert.match(log.text, new RegExp(`\\[${job.id} build\\] Building the index: started on \\d+ threads`))
  assert.match(log.text, /Building the index: done in/)
  assert.match(log.text, / names: done in /)
  assert.equal((await j('GET', '/api/index/log?full=1')).text.includes(`[${job.id} build]`), true)
  // Showing it in the Finder is for a Mac.
  if (process.platform !== 'darwin') assert.equal((await j('POST', '/api/index/log/reveal')).status, 409)
})

test('stopping a build stops its programs, and a job waiting behind it is dropped', { timeout: 120_000 }, async () => {
  fs.writeFileSync(SLOW, '')
  fs.rmSync(PIDS, { force: true })
  const build = (await j('POST', '/api/index/build')).body
  await until(() => fs.existsSync(PIDS))
  const pid = Number(fs.readFileSync(PIDS, 'utf8').trim().split('\n')[0])
  assert.ok(alive(pid))
  // An upload's pass queues behind the build; stopping it drops it at once.
  const queued = (await j('POST', '/api/index/job', { path: 'gamma' })).body
  assert.equal(queued.state, 'queued')
  const dropped = (await j('POST', `/api/index/job/${queued.id}/cancel`)).body
  assert.equal(dropped.state, 'cancelled')
  assert.equal((await j('GET', '/api/index/status')).body.queued.length, 0)

  const stop = await j('POST', '/api/index/cancel')
  assert.equal(stop.body.stopped, 1)
  const job = await finished(build.id)
  assert.equal(job.state, 'cancelled')
  assert.equal(job.phases.find(p => p.id === 'text').note, 'stopped')
  await until(() => !alive(pid), 10_000)
  fs.rmSync(SLOW)

  const s = (await j('GET', '/api/index/status')).body
  assert.equal(s.running, false)
  assert.equal(s.incomplete, true, 'a stopped build leaves the index marked incomplete')
  assert.equal(s.lastBuild.state, 'cancelled')
  assert.equal(s.lastError, null, 'stopping is not an error')
  assert.equal((await j('POST', '/api/index/job/99999/cancel')).status, 404)

  // Updating it again finishes it.
  const again = await finished((await j('POST', '/api/index/build')).body.id)
  assert.equal(again.state, 'done')
  assert.equal(again.label, 'Updating the index')
  assert.equal((await j('GET', '/api/index/status')).body.incomplete, false)
})

test('deleting the index needs confirming, and leaves the folder\'s files alone', { timeout: 60_000 }, async () => {
  const refused = await j('DELETE', '/api/index')
  assert.equal(refused.status, 400)
  assert.match(refused.body.error, /confirm=1/)
  assert.ok(fs.existsSync(path.join(IX, 'gufi', path.basename(KB), 'db.db')))

  const del = await j('DELETE', '/api/index?confirm=1')
  assert.equal(del.status, 200)
  assert.equal(del.body.kind, 'delete')
  const job = await finished(del.body.id)
  assert.equal(job.state, 'done')
  assert.equal(fs.existsSync(path.join(IX, 'gufi', path.basename(KB))), false)
  assert.equal(fs.existsSync(path.join(IX, 'extract')), false)
  assert.equal(fs.existsSync(path.join(IX, 'sweep-state.json')), false)
  // Every file in the folder is still there, as it was.
  for (const d of ['alpha', 'beta', 'beta/inner', 'gamma']) assert.equal(fs.readFileSync(path.join(KB, d, 'note.md'), 'utf8'), `notes in ${d} about a quokkafish\n`)
  assert.ok(fs.existsSync(path.join(KB, 'alpha', 'handbook.docx')))
  const s = (await j('GET', '/api/index/status')).body
  assert.equal(s.indexed, false)
  assert.equal(s.lastBuild.kind, 'delete')
})

test('settings: threads checked, the readers this host has, and where the index is kept', async () => {
  assert.equal((await j('PUT', '/api/index/settings', { threads: 0.5 })).status, 400)
  assert.equal((await j('PUT', '/api/index/settings', { ocr: 'yes' })).status, 400)
  const put = await j('PUT', '/api/index/settings', { threads: 3, embeddings: false })
  assert.equal(put.status, 200)
  assert.equal(put.body.threads, 3)
  assert.equal(put.body.settings.embeddings, false)
  const got = (await j('GET', '/api/index/settings')).body
  assert.equal(got.settings.threads, 3)
  assert.ok(got.cpus >= 1)
  assert.equal(got.location.current, IX)
  assert.equal(got.location.canChange, false, 'only where a launcher can restart the Studio')
  // Readers: PDFs are covered by downmark's binary or a fallback; a reader
  // something else covers is optional, and missing lists formats, not tools.
  const pdf = got.readers.formats.find(f => f.formats === '.pdf')
  assert.ok(pdf)
  if (pdf.readers.includes('downmark')) assert.equal(got.readers.extractors.find(x => x.name === 'pdftotext').optional, true)
  assert.ok(got.readers.missing.every(m => !m.ok))
  // Back to one per core for the tests after this.
  await j('PUT', '/api/index/settings', { threads: null, embeddings: true })
  assert.equal((await j('GET', '/api/index/settings')).body.settings.threads, null)
})

test('a folder pointed at again updates its earlier index instead of building from nothing', { timeout: 180_000 }, async () => {
  const A = tmp('ixjobs-a-'), B = tmp('ixjobs-b-'), base = tmp('ixjobs-repoint-')
  for (const d of ['one', 'two']) {
    fs.mkdirSync(path.join(A, d))
    fs.writeFileSync(path.join(A, d, 'page.md'), `page ${d}\n`)
    fs.copyFileSync(path.join(ROOT, 'testdata', 'corpus', 'docs', 'handbook.docx'), path.join(A, d, 'handbook.docx'))
  }
  fs.writeFileSync(path.join(B, 'other.md'), 'another folder\n')
  const build = async (kb, earlier = '') => {
    const { stdout } = await run(process.execPath, [path.join(ROOT, 'server', 'test', 'fixtures', 'build-folder.mjs')], {
      env: { ...process.env, KB_ROOT: kb, INDEX_BASE: base, GUFI_BIN: GUFI, PYTHON_BIN: 'python3', EARLIER: earlier },
      timeout: 120_000, maxBuffer: 16 * 1024 * 1024,
    })
    return JSON.parse(stdout.trim().split('\n').pop())
  }
  const docsOf = job => job.phases.find(p => p.id === 'documents').note
  const first = await build(A)
  assert.equal(first.job.state, 'done', first.job.error ?? '')
  assert.equal(first.job.label, 'Building the index')
  assert.match(docsOf(first.job), /^2 converted/)
  const cached = path.join(base, 'extract', 'one', 'handbook.docx.txt')
  const before = fs.statSync(cached).mtimeMs

  // Another folder: A's index is put aside, not deleted, and B starts clean.
  const other = await build(B, A)
  assert.equal(other.job.state, 'done')
  assert.equal(other.job.label, 'Building the index')
  assert.ok(other.earlier, 'the index of the first folder is kept')
  assert.equal(fs.existsSync(path.join(base, 'extract', 'one')), false, 'no cache entry of A stands in for B')

  // Back to A: its index comes back and the build updates it, reconverting nothing.
  const back = await build(A, B)
  assert.equal(back.job.state, 'done', back.job.error ?? '')
  assert.equal(back.job.label, 'Updating the index')
  assert.match(docsOf(back.job), /^0 converted, 2 unchanged/)
  assert.equal(fs.statSync(cached).mtimeMs, before, 'the cached text was reused, not written again')
  assert.ok(back.earlier, 'and B\'s index is kept in turn')
  assert.equal(back.status.indexed, true)
})
