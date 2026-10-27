// The depth a new library starts at. A library built of a folder on this
// computer, read only or not, starts at Meaning on documents, stored with it
// when it is added; one built on another system starts at Text + OCR
// (remoteBuild.test.mjs). A library added before the depth was stored at
// creation keeps what it had after an update: Text + OCR, or what its
// settings said. The build and the check run GUFI for real and skip without
// it; the rest runs anywhere.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const skip = !fs.existsSync(path.join(gufiBin, 'gufi_dir2index')) && 'GUFI not installed'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'newdepth-'))
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
delete process.env.AUTH_JWKS_URL
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
write(path.join(dir, 'kb', 'main.md'), '# main\n')

// As an earlier version left them: three libraries built here, one with no
// settings at all, one with a level of search by meaning, one with a depth.
const old = id => ({
  id, label: id, indexRoot: path.join(dir, 'old', id, id), sourceRoot: path.join(dir, 'old-src', id),
  build: { indexDir: path.join(dir, 'old', id), cacheRoot: path.join(dir, 'old', id, '.extract'), managed: false, state: 'ready', error: null, builtAt: '2026-01-01T00:00:00.000Z', files: 0, dirs: 1 },
})
write(path.join(dir, 'ix', 'libraries.json'), JSON.stringify([old('plain'), old('with-meaning'), old('with-depth')]))
const before = { libraries: { kb: { threads: 2 }, 'with-meaning': { meaning: 'documents' }, 'with-depth': { depth: 'text' } } }
write(path.join(dir, 'ix', 'indexing.json'), JSON.stringify(before))

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const B = await import('../dist/libraryBuild.js')
const S = await import('../dist/indexSettings.js')
const M = await import('../dist/semanticMap.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(B.libraryBuildRoutes)
await app.ready()
test.after(() => app.close())
const call = async (method, url, body) => {
  const r = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: r.statusCode, body: r.json() }
}
const listed = async id => (await call('GET', '/api/libraries')).body.libraries.find(l => l.id === id)
const waitReady = async id => {
  for (let i = 0; i < 300; i++) {
    const l = await listed(id)
    if (l?.build && (l.build.state === 'ready' || l.build.state === 'failed') && !l.build.progress) return l
    await new Promise(r => setTimeout(r, 100))
  }
  throw new Error('the build did not finish')
}
const stored = () => JSON.parse(fs.readFileSync(path.join(dir, 'ix', 'indexing.json'), 'utf8')).libraries

test('the defaults: Meaning on documents for a library built here, Text + OCR for one built on another system', () => {
  assert.equal(S.NEW_LIBRARY_DEPTH, 'meaning-documents')
  assert.equal(S.NEW_REMOTE_LIBRARY_DEPTH, 'ocr')
  assert.equal(S.indexSettings('kb').depth, 'meaning-documents', 'the main folder, as before')
})

test('a library from before the change keeps its depth after the update', async () => {
  const depths = async id => { const l = await listed(id); return [l.depth, l.meaning] }
  assert.deepEqual(await depths('plain'), ['ocr', 'off'], 'no depth stored: Text + OCR, as it was built')
  assert.deepEqual(await depths('with-meaning'), ['meaning-documents', 'documents'])
  assert.deepEqual(await depths('with-depth'), ['text', 'off'])
  assert.deepEqual(stored(), before.libraries, 'reading them writes nothing')
})

test('the check reads at the depth a new library would get', { skip }, async () => {
  const model = path.join(dir, 'ix', 'models', 'minilm384.gguf')
  write(model, '')
  try {
    const c = (await call('POST', '/api/libraries/build/check', { sourceRoot: path.join(dir, 'kb2') })).body
    assert.match(c.steps.find(s => s.name === 'extract').detail, /so search covers what the files say, and by meaning\.$/)
    const ocr = (await call('POST', '/api/libraries/build/check', { sourceRoot: path.join(dir, 'kb2'), depth: 'ocr' })).body
    assert.doesNotMatch(ocr.steps.find(s => s.name === 'extract').detail, /by meaning/)
  } finally { fs.rmSync(model) }
})

test('a library built here with no depth chosen starts at Meaning on documents, stored with it, and gets a semantic map', { skip }, async () => {
  const src = path.join(dir, 'reports')
  write(path.join(src, 'summary.md'), '# Summary\nThe flow study.\n')
  const r = await call('POST', '/api/libraries/build', { sourceRoot: src, indexRoot: path.join(dir, 'indexes', 'reports'), label: 'Reports', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const id = r.body.library.id
  assert.equal(stored()[id].depth, 'meaning-documents', 'stored at creation, so a later default leaves it as built')
  const l = await waitReady(id)
  assert.equal(l.build.state, 'ready', l.build.error ?? '')
  assert.deepEqual([l.depth, l.meaning], ['meaning-documents', 'documents'])
  // Its map is made once it is embedded; a library at Text + OCR has none.
  assert.notEqual(M.semanticMapOverview(id).reason, 'off')
  assert.equal(M.semanticMapOverview('plain').reason, 'off')
  // The libraries from before are untouched by the new one.
  assert.equal((await listed('plain')).depth, 'ocr')
  assert.equal(stored().plain, undefined)
})

test('a folder of read-only files starts at Meaning on documents too, and is read only in the Studio', { skip }, async () => {
  const src = path.join(dir, 'archive')
  const files = [path.join(src, 'old.md'), path.join(src, 'year', 'notes.txt')]
  for (const f of files) { write(f, '# Old\n'); fs.chmodSync(f, 0o444) }
  const r = await call('POST', '/api/libraries/build', { sourceRoot: src, indexRoot: path.join(dir, 'indexes', 'archive'), label: 'Archive', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const l = await waitReady(r.body.library.id)
  assert.equal(l.build.state, 'ready', l.build.error ?? '')
  assert.equal(l.writable, false)
  assert.equal(l.depth, 'meaning-documents')
})

test('a depth chosen at creation is kept as chosen', { skip }, async () => {
  const src = path.join(dir, 'code')
  write(path.join(src, 'main.c'), 'int main(void) { return 0; }\n')
  const r = await call('POST', '/api/libraries/build', { sourceRoot: src, indexRoot: path.join(dir, 'indexes', 'code'), label: 'Code', depth: 'text', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(stored()[r.body.library.id].depth, 'text')
  await waitReady(r.body.library.id)
})
