/**
 * Integration test: the server's own indexing path, over HTTP.
 *
 * extraction.test.mjs drives preextract.js and enrich.py directly, which
 * proves the converters work but not that the server calls them. This boots
 * the real server against a throwaway knowledge base, asks it to index a
 * directory the way an upload does, and reads the document back through the
 * API the viewer uses. It covers the wiring in indexing.ts — the arguments,
 * the extract-cache path, the fork surviving in the server's process — that
 * nothing else exercises.
 *
 * gufi_dir2index is stubbed (testdata/fake-gufi): GUFI is a metadata
 * indexer that does not build on macOS and takes minutes to build in CI,
 * and it is not what this test is about. Everything else is the real thing.
 *
 *     node --test testdata/server.test.mjs
 */
import { execFileSync, spawn } from 'node:child_process'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO = path.resolve(HERE, '..')
const CORPUS = path.join(HERE, 'corpus')
const MAIN = path.join(REPO, 'server', 'dist', 'main.js')
const FAKE_GUFI = path.join(HERE, 'fake-gufi')

function findPython() {
  for (const cand of [process.env.PYTHON_BIN, 'python3.13', 'python3.12', 'python3.11', 'python3.10', 'python3']) {
    if (!cand) continue
    try {
      execFileSync(cand, ['-c', 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)'],
        { stdio: 'ignore', timeout: 15_000 })
      return cand
    } catch { /* next */ }
  }
  return null
}
const PYTHON = findPython()
const SKIP = !fs.existsSync(MAIN) ? 'server not built (pnpm --filter @activate-studio/server build)'
  : !PYTHON ? 'no python 3.10+ on PATH'
  : false

async function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.once('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address()
      srv.close(() => resolve(port))
    })
  })
}

/** Boot the real server against a throwaway KB; resolves once it answers. */
const staged = []
process.on('exit', () => {
  for (const dir of staged) { try { fs.rmSync(dir, { recursive: true, force: true }) } catch { /* best effort */ } }
})

async function boot() {
  const root = await fsp.mkdtemp(path.join(os.tmpdir(), 'server-it-'))
  staged.push(root)
  const kb = path.join(root, 'kb')
  const indexBase = path.join(root, 'index')
  await fsp.cp(CORPUS, kb, { recursive: true })
  await fsp.mkdir(indexBase, { recursive: true })
  const port = await freePort()

  const child = spawn(process.execPath, [MAIN], {
    env: {
      ...process.env,
      KB_ROOT: kb,
      INDEX_BASE: indexBase,
      GUFI_BIN: FAKE_GUFI,
      PORT: String(port),
      HOST: '127.0.0.1',
      PYTHON_BIN: PYTHON,
      // Keep the test hermetic: no gateway calls, no vision captions, and
      // no background sweep racing the explicit index request below.
      PW_API_KEY: '',
      ADE_VISION_MODEL: '',
      SWEEP_INTERVAL_SEC: '86400',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let log = ''
  child.stdout.on('data', d => { log += d })
  child.stderr.on('data', d => { log += d })

  const base = `http://127.0.0.1:${port}`
  const deadline = Date.now() + 60_000
  for (;;) {
    if (child.exitCode !== null) throw new Error(`server exited ${child.exitCode}:\n${log}`)
    try {
      const res = await fetch(`${base}/healthz`)
      if (res.ok) break
    } catch { /* not listening yet */ }
    if (Date.now() > deadline) {
      child.kill('SIGKILL')
      throw new Error(`server did not come up within 60s:\n${log}`)
    }
    await new Promise(r => setTimeout(r, 200))
  }
  return { base, kb, indexBase, stop: () => child.kill('SIGKILL'), getLog: () => log }
}

/** POST /api/index/dir, which indexes synchronously. It reports refusals in
 *  the body with a 200, so the body is what has to be checked. */
async function indexDir(server, rel) {
  const res = await fetch(`${server.base}/api/index/dir`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ path: rel }),
  })
  const body = await res.json()
  assert.equal(res.status, 200, `index request failed: ${res.status} ${JSON.stringify(body)}`)
  assert.ok(body.indexed === rel,
    `index request was refused: ${JSON.stringify(body)}\nserver log:\n${server.getLog().slice(-2000)}`)
  return body
}

test('the server indexes a directory and serves the extracted text', { skip: SKIP }, async (t) => {
  const server = await boot()
  t.after(() => server.stop())

  // The report the Stats page reads: proves the server process can load
  // preextract.js and resolve the native binary, not just the test runner.
  const extractors = await (await fetch(`${server.base}/api/index/extractors`)).json()
  const downmark = extractors.extractors.find(e => e.name === 'downmark')
  assert.ok(downmark, `downmark missing from the extractor report: ${JSON.stringify(extractors)}`)
  assert.equal(downmark.available, true)

  // Index the way an upload does, through the server's own code path.
  await indexDir(server, 'docs')

  // The document comes back as Markdown, from the cache the server filled.
  const file = await (await fetch(
    `${server.base}/api/kb/file?path=${encodeURIComponent('docs/handbook.docx')}`)).json()
  assert.equal(file.source, 'extracted',
    `expected extracted text, got source=${file.source}\nserver log:\n${server.getLog().slice(-2000)}`)
  assert.match(file.content, /# Solver Handbook/, 'the heading should have survived into the served text')
  assert.match(file.content, /VELOCITY-INLET-7734/)
  assert.match(file.content, /\| inlet/, 'the table should have survived into the served text')

  // And the extract cache landed where the rest of the system looks for it.
  const cached = path.join(server.indexBase, 'extract', 'docs', 'handbook.docx.txt')
  assert.ok(fs.existsSync(cached), `no cache entry at ${cached}`)
})

test('a PDF indexed by the server keeps its text', { skip: SKIP }, async (t) => {
  const server = await boot()
  t.after(() => server.stop())

  await indexDir(server, 'docs')

  const file = await (await fetch(
    `${server.base}/api/kb/file?path=${encodeURIComponent('docs/report.pdf')}`)).json()
  assert.equal(file.source, 'extracted',
    `expected extracted text, got source=${file.source}\nserver log:\n${server.getLog().slice(-2000)}`)
  assert.match(file.content, /BOUNDARY-LAYER-4417/)
  assert.match(file.content, /CONVERGENCE-HISTORY-5502/, 'the second page should be present too')
})
