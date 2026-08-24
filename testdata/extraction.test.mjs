/**
 * End-to-end extraction test over testdata/corpus.
 *
 * Runs the pipeline a deployment runs — preextract.ts converts documents to
 * Markdown in the extract cache, enrich.py indexes that cache into per-
 * directory fts5 tables — and asserts on the text the index ends up holding,
 * because that is what search, chat and the viewer read. Testing the cache
 * files alone would miss enrich.py declining to reuse an entry, which is a
 * failure mode we have already had.
 *
 * Requires the built server (`pnpm --filter @activate-studio/server build`).
 * Tests whose dependency is missing skip with a reason rather than fail:
 * tesseract for OCR, python3.10+ for the indexing half. A bare checkout on a
 * machine with neither still verifies conversion.
 *
 *     pnpm test
 */
import { execFile, execFileSync } from 'node:child_process'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const execFileAsync = promisify(execFile)
const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO = path.resolve(HERE, '..')
const CORPUS = path.join(HERE, 'corpus')
const PREEXTRACT = path.join(REPO, 'server', 'dist', 'preextract.js')
const ENRICH = path.join(REPO, 'indexer', 'enrich.py')
const HELPER = path.join(HERE, 'index-helper.py')
const EXPECTATIONS = JSON.parse(fs.readFileSync(path.join(HERE, 'expectations.json'), 'utf8'))

if (!fs.existsSync(PREEXTRACT)) {
  throw new Error(`build the server first: pnpm --filter @activate-studio/server build (missing ${PREEXTRACT})`)
}

/** A python3 that satisfies the indexer's 3.10+ syntax, or null. */
function findPython() {
  for (const cand of [process.env.PYTHON_BIN, 'python3.13', 'python3.12', 'python3.11', 'python3.10', 'python3']) {
    if (!cand) continue
    try {
      execFileSync(cand, ['-c', 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)'],
        { stdio: 'ignore', timeout: 15_000 })
      return cand
    } catch { /* try the next one */ }
  }
  return null
}

function have(bin) {
  try { execFileSync('sh', ['-c', `command -v ${bin}`], { stdio: 'ignore', timeout: 10_000 }); return true }
  catch { return false }
}

const PYTHON = findPython()
const TESSERACT = have('tesseract')
const { binaryPath } = await import(path.join(REPO, 'server', 'node_modules', '@giraffesyo', 'downmark', 'dist', 'index.js'))
const NATIVE = binaryPath() !== null

const staged = []
process.on('exit', () => {
  for (const dir of staged) { try { fs.rmSync(dir, { recursive: true, force: true }) } catch { /* best effort */ } }
})

/** A throwaway KB, index tree and extract cache seeded from the corpus. */
async function stage(name) {
  const root = await fsp.mkdtemp(path.join(os.tmpdir(), `extraction-${name}-`))
  staged.push(root)
  const kb = path.join(root, 'kb')
  const index = path.join(root, 'index')
  const cache = path.join(root, 'extract')
  await fsp.cp(CORPUS, kb, { recursive: true })
  await fsp.mkdir(cache, { recursive: true })
  if (PYTHON) await execFileAsync(PYTHON, [HELPER, 'init', index, kb])
  return { root, kb, index, cache }
}

async function preextract({ kb, cache }, env = {}) {
  const { stdout, stderr } = await execFileAsync(process.execPath,
    [PREEXTRACT, '--kb-root', kb, '--extract-cache', cache],
    { env: { ...process.env, ...env }, maxBuffer: 32 * 1024 * 1024, timeout: 10 * 60_000 })
  return { summary: JSON.parse(stdout.trim().split('\n').pop()), log: stderr }
}

async function preextractScoped({ kb, cache }, subdir) {
  const { stdout, stderr } = await execFileAsync(process.execPath,
    [PREEXTRACT, '--kb-root', kb, '--extract-cache', cache, '--subdir', subdir],
    { maxBuffer: 32 * 1024 * 1024, timeout: 10 * 60_000 })
  return { summary: JSON.parse(stdout.trim().split('\n').pop()), log: stderr }
}

async function enrich({ kb, index, cache }) {
  await execFileAsync(PYTHON, [ENRICH, '--kb-root', kb, '--index', index, '--extract-cache', cache],
    { maxBuffer: 32 * 1024 * 1024, timeout: 10 * 60_000 })
  const { stdout } = await execFileAsync(PYTHON, [HELPER, 'dump', index], { maxBuffer: 64 * 1024 * 1024 })
  return JSON.parse(stdout)
}

/** The pipeline as deployed, run once and shared by the assertions below. */
let indexed = null
let summary = null
let log = ''

test('the corpus converts and indexes', { skip: !PYTHON && 'no python 3.10+ on PATH' }, async () => {
  const dirs = await stage('full')
  const pre = await preextract(dirs)
  summary = pre.summary
  log = pre.log
  indexed = await enrich(dirs)
  assert.ok(Object.keys(indexed).length >= 9, `indexed too few files: ${Object.keys(indexed).length}`)
})

for (const [rel, spec] of Object.entries(EXPECTATIONS)) {
  if (rel.startsWith('_')) continue
  test(`${rel} — ${spec.why}`, { skip: !PYTHON && 'no python 3.10+ on PATH' }, () => {
    assert.ok(indexed, 'the pipeline run failed; see the failure above')
    const text = indexed[rel]
    assert.ok(text !== undefined, `${rel} is missing from the index entirely`)
    assert.ok(text.length >= spec.minChars,
      `${rel}: ${text.length} chars indexed, expected at least ${spec.minChars}\n---\n${text.slice(0, 400)}`)
    for (const needle of spec.contains ?? []) {
      assert.ok(text.includes(needle),
        `${rel}: expected to contain ${JSON.stringify(needle)}\n---\n${text.slice(0, 800)}`)
    }
    for (const needle of spec.absent ?? []) {
      assert.ok(!text.includes(needle), `${rel}: expected NOT to contain ${JSON.stringify(needle)}`)
    }
  })
}

test('a file whose extension lies is reported, not fatal', { skip: !PYTHON && 'no python 3.10+ on PATH' }, () => {
  assert.ok(summary, 'the pipeline run failed; see the failure above')
  assert.equal(summary.failed, 1, `expected exactly one conversion failure, got ${summary.failed}`)
  assert.match(log, /truncated\.docx/, 'the failing file should be named in the log')
  // It must not reach the index as garbage either: enrich.py's standard
  // library reader cannot read it, so it indexes as nothing at all.
  assert.ok(!('broken/truncated.docx' in indexed), 'a file nothing can read should index no text')
})

test('a scanned PDF is read through OCR and marked as such', {
  skip: (!PYTHON && 'no python 3.10+ on PATH') || (!NATIVE && 'no native downmark binary') || (!TESSERACT && 'tesseract not installed'),
}, () => {
  assert.ok(indexed, 'the pipeline run failed; see the failure above')
  const text = indexed['scans/invoice-scan.pdf']
  assert.ok(text, 'the scan produced no indexed text at all')
  assert.match(text, /includes OCR text/, 'OCR text must be marked so consumers can weigh it differently')
  assert.match(text, /SCANNED PAGE 9931/, 'the scanned reference should have been read')
})

test('a scan under a typed header is OCR\'d too (the thin policy)', {
  skip: (!PYTHON && 'no python 3.10+ on PATH') || (!NATIVE && 'no native downmark binary') || (!TESSERACT && 'tesseract not installed'),
}, () => {
  assert.ok(indexed, 'the pipeline run failed; see the failure above')
  const text = indexed['scans/form-mixed.pdf']
  assert.ok(text, 'the mixed document produced no indexed text at all')
  // The header comes from the content stream, so a textless-only policy
  // would call this page done and lose the body. That regression is the
  // entire reason this fixture exists.
  assert.match(text, /TYPED HEADER 6620/, 'the typed header should survive')
  assert.match(text, /SCANNED BODY 7742/,
    'the scanned body under the typed header was lost: is the OCR policy still "thin"?')
})

test('text inside an image is indexed', {
  skip: (!PYTHON && 'no python 3.10+ on PATH') || (!TESSERACT && 'tesseract not installed'),
}, () => {
  assert.ok(indexed, 'the pipeline run failed; see the failure above')
  const text = indexed['images/diagram.png']
  assert.ok(text, 'the image produced no indexed text at all')
  assert.match(text, /DIAGRAM LABEL 3308/, 'OCR should have read the label')
})

test('a second pass reuses the cache instead of reconverting', { skip: !PYTHON && 'no python 3.10+ on PATH' }, async () => {
  const dirs = await stage('reuse')
  const first = await preextract(dirs)
  const second = await preextract(dirs)
  assert.ok(first.summary.converted > 0, 'the first pass should convert something')
  assert.equal(second.summary.converted, 0, 'the second pass reconverted files it had already cached')
  assert.equal(second.summary.reused, first.summary.converted + first.summary.reused,
    'every previously converted file should have been reused')
})

test('a scoped pass reuses the cache too (what the server actually runs)', { skip: !PYTHON && 'no python 3.10+ on PATH' }, async () => {
  const dirs = await stage('reuse-scoped')
  // The server always indexes a subtree or the root db, never the whole
  // tree, so whole-tree reuse passing says nothing about the real path:
  // a version marker that only a full pass could write left every scoped
  // pass reconverting its subtree, OCR and all, on every upload.
  const first = await preextractScoped(dirs, 'docs')
  const second = await preextractScoped(dirs, 'docs')
  assert.ok(first.summary.converted > 0, 'the first scoped pass should convert something')
  assert.equal(second.summary.converted, 0,
    'the second scoped pass reconverted its subtree: is the version marker gating reuse again?')
  assert.equal(second.summary.reused, first.summary.converted)
})

test('without the native binary, PDFs fall back to enrich.py', {
  skip: (!PYTHON && 'no python 3.10+ on PATH') || (!have('pdftotext') && 'poppler not installed'),
}, async () => {
  const dirs = await stage('wasm')
  const pre = await preextract(dirs, { DOWNMARK_FORCE_WASM: '1' })
  // The wasm cannot execute an OCR engine, so preextract leaves PDFs alone.
  const cached = await fsp.readdir(path.join(dirs.cache, 'docs'))
  assert.ok(!cached.includes('report.pdf.txt'), 'the wasm path should not pre-extract PDFs')
  assert.ok(pre.summary.converted >= 3, 'Office documents should still convert on the wasm')
  // enrich.py must still index them, through pdftotext.
  const rows = await enrich(dirs)
  assert.match(rows['docs/report.pdf'] ?? '', /BOUNDARY-LAYER-4417/,
    'pdftotext should have indexed the PDF the wasm path skipped')
  assert.match(rows['docs/handbook.docx'] ?? '', /VELOCITY-INLET-7734/,
    'the wasm conversion should still reach the index')
})
