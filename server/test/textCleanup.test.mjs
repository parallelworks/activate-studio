// The line the converter's cache entries start with ("<!-- extracted by
// downmark x.y.z -->") is not the document's text: it is not written to
// the words table, not cut into passages, and not shown in a snippet. An
// index written before that is cleaned in place at its next sync, once,
// without converting anything again. GUFI's real tools where they are
// installed (search is checked then), the stand-in from testdata otherwise.
import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { DatabaseSync } from 'node:sqlite'

const ROOT = path.resolve(import.meta.dirname, '..', '..')
const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const HOME = tmp('tclean-home-')
const KB = tmp('tclean-kb-')
const IX = tmp('tclean-ix-')
const REAL_GUFI = process.env.GUFI_BIN || '/opt/gufi/bin'
const GUFI = fs.existsSync(path.join(REAL_GUFI, 'gufi_dir2index')) ? REAL_GUFI : path.join(ROOT, 'testdata', 'fake-gufi')
const REAL = GUFI === REAL_GUFI && fs.existsSync(path.join(GUFI, 'gufi_query'))

Object.assign(process.env, { HOME, KB_ROOT: KB, INDEX_BASE: IX, GUFI_BIN: GUFI, SWEEP_INTERVAL_SEC: '300' })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR']) delete process.env[k]

fs.mkdirSync(path.join(KB, 'docs'), { recursive: true })
fs.copyFileSync(path.join(ROOT, 'testdata', 'corpus', 'docs', 'handbook.docx'), path.join(KB, 'docs', 'handbook.docx'))
fs.writeFileSync(path.join(KB, 'notes.md'), 'notes about a quokkafish\n')

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const T = await import('../dist/textCleanup.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
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
const search = async q => (await j('GET', `/api/search?q=${encodeURIComponent(q)}`)).body.hits
const status = async () => (await j('GET', '/api/index/status')).body
const syncNow = async () => (await j('POST', '/api/index/sweep')).body.changed
const INDEX = () => path.join(IX, 'gufi', path.basename(KB))
const MARK = () => path.join(INDEX(), '.studio-text-clean')
const HEADER = '<!-- extracted by downmark 0.10.0 -->'

/** Every words row under an index, read directly. */
function wordsRows(root) {
  const out = []
  const walk = d => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.name === 'db.db') {
        const db = new DatabaseSync(p)
        try {
          if (db.prepare("SELECT 1 FROM sqlite_master WHERE name = 'words'").get()) out.push(...db.prepare('SELECT fname, wordf FROM words').all().map(r => ({ ...r, db: p })))
        } finally { db.close() }
      }
    }
  }
  walk(root)
  return out
}

test('the snippet filter takes the line off whole or cut short, and leaves other text alone', () => {
  assert.equal(T.withoutProvenance(`${HEADER}\n\nInlet [pressure] notes`), 'Inlet [pressure] notes')
  assert.equal(T.withoutProvenance('<!-- extracted by downmark 0.10.0 \u2014>Inlet [pressure] notes'), 'Inlet [pressure] notes')
  assert.equal(T.withoutProvenance('by downmark 0.10.0 (no OCR) --> Quarterly [budget]'), 'Quarterly [budget]')
  assert.equal(T.withoutProvenance('the downmark converter is fast'), 'the downmark converter is fast')
  assert.equal(T.withoutProvenance(''), '')
})

test("enrich.py's and embed.py's index_text drop the line and nothing else", () => {
  for (const mod of ['enrich', 'embed']) {
    const r = spawnSync('python3', ['-c', `import sys; sys.path.insert(0, ${JSON.stringify(path.join(ROOT, 'indexer'))}); from ${mod} import index_text
print(repr(index_text(${JSON.stringify(`${HEADER}\n\nSolver Handbook`)})))
print(repr(index_text('plain text')))
print(repr(index_text(${JSON.stringify(HEADER)})))`], { encoding: 'utf8' })
    assert.equal(r.status, 0, r.stderr)
    assert.deepEqual(r.stdout.trim().split('\n'), ["'Solver Handbook'", "'plain text'", "''"], mod)
  }
})

test('a build writes the text without the line, and marks the index clean', { timeout: 120_000 }, async () => {
  assert.equal((await j('PUT', '/api/index/settings', { depth: 'text' })).status, 200)
  const job = await finished((await j('POST', '/api/index/build')).body.id)
  assert.equal(job.state, 'done', job.error ?? '')
  assert.ok(fs.existsSync(MARK()), 'marked clean')
  assert.equal(T.textCleanNeeded(INDEX()), false)
  const cache = fs.readFileSync(path.join(IX, 'extract', 'docs', 'handbook.docx.txt'), 'utf8')
  assert.ok(cache.startsWith('<!-- extracted by downmark '), 'the cache entry keeps its provenance line')
  const rows = wordsRows(INDEX())
  if (REAL) {
    const doc = rows.find(r => r.fname === 'handbook.docx')
    assert.ok(doc, 'the document has words')
    assert.ok(!doc.wordf.includes('downmark'), doc.wordf.slice(0, 80))
    assert.deepEqual(await search('downmark'), [], 'the converter is not searchable text')
    const hit = (await search('VELOCITY-INLET-7734')).find(h => h.path === 'docs/handbook.docx')
    assert.ok(hit, 'found by its own words')
    assert.ok(!hit.snippet.includes('<!--') && !hit.snippet.includes('downmark'), hit.snippet)
  }
})

test('an index from before is cleaned in place at its next sync, once, with nothing converted again', { timeout: 120_000 }, async () => {
  // As an earlier Studio wrote it: the line at the start of the converted
  // document's row, and no marker.
  let put = 0
  for (const r of wordsRows(INDEX())) {
    if (r.fname !== 'handbook.docx') continue
    const db = new DatabaseSync(r.db)
    try { put += Number(db.prepare('UPDATE words SET wordf = ? WHERE fname = ?').run(`${HEADER}\n\n${r.wordf}`, r.fname).changes) } finally { db.close() }
  }
  fs.rmSync(MARK())
  assert.equal(T.textCleanNeeded(INDEX()), true)
  if (REAL) {
    assert.ok(put > 0)
    assert.ok((await search('downmark')).some(h => h.path === 'docs/handbook.docx'), 'the old index matches the converter')
  }
  const cacheBefore = fs.statSync(path.join(IX, 'extract', 'docs', 'handbook.docx.txt')).mtimeMs
  await syncNow()
  const job = (await status()).last
  assert.equal(job.state, 'done', job.error ?? '')
  assert.equal(job.label, 'Tidying the indexed text')
  assert.ok(fs.existsSync(MARK()))
  assert.equal(fs.statSync(path.join(IX, 'extract', 'docs', 'handbook.docx.txt')).mtimeMs, cacheBefore, 'nothing converted again')
  for (const r of wordsRows(INDEX())) assert.ok(!r.wordf.startsWith('<!--'), `${r.fname}: ${r.wordf.slice(0, 60)}`)
  if (REAL) {
    assert.deepEqual(await search('downmark'), [])
    const hit = (await search('VELOCITY-INLET-7734')).find(h => h.path === 'docs/handbook.docx')
    assert.ok(hit && !hit.snippet.includes('downmark'), hit?.snippet)
  }
  // Once: the next sync has nothing to do.
  const last = (await status()).last.id
  assert.deepEqual(await syncNow(), [])
  assert.equal((await status()).last.id, last, 'no job')
})

test('cleanIndexedText passes directories without a words table and counts what it changed', async () => {
  const root = tmp('tclean-tree-')
  const mk = (rel, rows) => {
    fs.mkdirSync(path.join(root, rel), { recursive: true })
    const db = new DatabaseSync(path.join(root, rel, 'db.db'))
    try {
      db.exec('CREATE TABLE entries (name TEXT)')
      if (rows) {
        db.exec('CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)')
        for (const [i, t] of rows.entries()) db.prepare('INSERT INTO words VALUES (?, ?, ?)').run(String(i), `f${i}`, t)
      }
    } finally { db.close() }
  }
  mk('.', [`${HEADER}\n\nalpha text`, 'beta text'])
  mk('a', null)
  mk('a/b', ['<!-- extracted by downmark 0.10.0 (no OCR) -->\n\ngamma', HEADER])
  const r = await T.cleanIndexedText(root)
  assert.deepEqual(r, { dbs: 3, rows: 2 }, 'a header with no text after it is left for the reader to drop')
  const texts = wordsRows(root).map(x => x.wordf).sort()
  assert.deepEqual(texts, [HEADER, 'alpha text', 'beta text', 'gamma'])
  await T.markTextClean(root)
  assert.equal(T.textCleanNeeded(root), false)
})
