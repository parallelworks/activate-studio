// Libraries: the primary knowledge base plus read-only indexes mounted
// beside it, addressed by name on every request and defaulting to the
// primary so nothing that exists today has to change.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'lib-kb-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'lib-ix-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = IX
process.env.STUDIO_SECTIONS = 'library,search,overview'

// A second corpus with its own "index": a db.db at the root is what makes
// a tree look like GUFI to the probe; no GUFI binaries are needed for that.
const SITE_SRC = fs.mkdtempSync(path.join(os.tmpdir(), 'lib-site-src-'))
const SITE_IDX = fs.mkdtempSync(path.join(os.tmpdir(), 'lib-site-idx-'))
fs.writeFileSync(path.join(SITE_SRC, 'report.md'), '# site report\n')
fs.mkdirSync(path.join(SITE_SRC, 'notes'))
fs.writeFileSync(path.join(SITE_IDX, 'db.db'), '')
// One pinned by the deployment, with no files on this host.
const REMOTE_IDX = fs.mkdtempSync(path.join(os.tmpdir(), 'lib-remote-idx-'))
fs.writeFileSync(path.join(REMOTE_IDX, 'db.db'), '')
process.env.STUDIO_LIBRARIES = JSON.stringify([{ id: 'scratch', label: 'Scratch filesystem', indexRoot: REMOTE_IDX }])

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { uploadRoutes } = await import('../dist/uploads.js')
const { listLibraries, getLibrary, resetLibrariesForTests } = await import('../dist/libraries.js')

const app = Fastify()
// The same error shape production uses: a KbError's status and message.
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(uploadRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}

test('the primary is always first and writable; a pinned library is read only with no files', () => {
  resetLibrariesForTests()
  const libs = listLibraries('Knowledge base')
  assert.equal(libs[0].id, 'kb')
  assert.equal(libs[0].primary, true)
  assert.equal(libs[0].writable, true)
  assert.equal(libs[0].label, 'Knowledge base')
  const scratch = libs.find(l => l.id === 'scratch')
  assert.ok(scratch)
  assert.equal(scratch.writable, false)
  assert.equal(scratch.pinned, true)
  assert.equal(scratch.sourceRoot, null)
  assert.equal(scratch.caps.index, true)
})

test('an unnamed request means the primary; an unknown name is a 404', async () => {
  assert.equal(getLibrary(undefined).id, 'kb')
  assert.equal(getLibrary('').id, 'kb')
  assert.throws(() => getLibrary('nope'), /no library named nope/)
  const r = await j('GET', '/api/kb/tree?path=&library=nope')
  assert.equal(r.status, 404)
})

test('an administrator adds a library by path and it is probed', async () => {
  const bad = await j('POST', '/api/libraries', { id: 'site', indexRoot: path.join(os.tmpdir(), 'does-not-exist') })
  assert.equal(bad.status, 400)
  const notIndex = await j('POST', '/api/libraries', { id: 'site', indexRoot: SITE_SRC })
  assert.equal(notIndex.status, 400)
  assert.match(notIndex.body.error ?? notIndex.body.message ?? '', /does not look like a GUFI index/)
  const ok = await j('POST', '/api/libraries', { id: 'Site', label: 'Site index', indexRoot: SITE_IDX, sourceRoot: SITE_SRC })
  assert.equal(ok.status, 200)
  assert.equal(ok.body.library.id, 'site')
  assert.equal(ok.body.library.writable, false)
  assert.equal(ok.body.library.source, true)
  assert.equal(ok.body.library.caps.index, true)
  // It survives a reload from disk.
  resetLibrariesForTests()
  assert.equal(getLibrary('site').sourceRoot, SITE_SRC)
  const dup = await j('POST', '/api/libraries', { id: 'site', indexRoot: SITE_IDX })
  assert.equal(dup.status, 409)
})

test('listing follows the library named on the request', async () => {
  const primary = await j('GET', '/api/kb/tree?path=')
  assert.equal(primary.status, 200)
  assert.ok(!primary.body.entries.some(e => e.name === 'report.md'))
  const site = await j('GET', '/api/kb/tree?path=&library=site')
  assert.equal(site.status, 200)
  assert.deepEqual(site.body.entries.map(e => e.name).sort(), ['notes', 'report.md'])
  const file = await j('GET', '/api/kb/file?path=report.md&library=site')
  assert.equal(file.status, 200)
  assert.equal(file.body.content, '# site report\n')
})

test('a library without files on this host can be described but not opened', async () => {
  const r = await j('GET', '/api/kb/tree?path=&library=scratch')
  assert.equal(r.status, 409)
  assert.match(r.body.error ?? r.body.message ?? '', /no files on this host/)
})

test('every mutation is refused on a read-only library, and allowed on the primary', async () => {
  for (const [method, url, body] of [
    ['POST', '/api/kb/delete?library=site', { paths: ['report.md'] }],
    ['POST', '/api/kb/rename?library=site', { path: 'report.md', name: 'x.md' }],
    ['POST', '/api/kb/move?library=site', { paths: ['report.md'], dest: 'notes' }],
    ['DELETE', '/api/kb/file?path=report.md&library=site'],
    ['POST', '/api/kb/mkdir?library=site', { path: 'new' }],
    ['POST', '/api/index/dir?library=site', { path: '' }],
  ]) {
    const r = await j(method, url, body)
    assert.equal(r.status, 403, `${method} ${url} should be refused`)
    assert.match(r.body.error ?? r.body.message ?? '', /read only/)
  }
  assert.ok(fs.existsSync(path.join(SITE_SRC, 'report.md')), 'nothing was touched')
  const mk = await j('POST', '/api/kb/mkdir', { path: 'made-here' })
  assert.equal(mk.status, 200)
  assert.ok(fs.existsSync(path.join(KB, 'made-here')))
})

test('the client config carries the libraries and the visible sections', async () => {
  const r = await j('GET', '/api/config')
  assert.equal(r.status, 200)
  assert.deepEqual(r.body.sections, ['library', 'search', 'overview'])
  const ids = r.body.libraries.map(l => l.id)
  assert.equal(ids[0], 'kb')
  assert.ok(ids.includes('scratch') && ids.includes('site'))
  for (const l of r.body.libraries) assert.ok(!('indexRoot' in l) && !('sourceRoot' in l), 'paths stay on the server')
})

test('Settings lists where each library points', async () => {
  const r = await j('GET', '/api/libraries')
  assert.equal(r.status, 200)
  const by = Object.fromEntries(r.body.libraries.map(l => [l.id, l]))
  assert.equal(by.kb.sourceRoot, KB)
  assert.ok(by.kb.indexRoot)
  assert.equal(by.site.indexRoot, SITE_IDX)
  assert.equal(by.site.sourceRoot, SITE_SRC)
  assert.equal(by.scratch.sourceRoot, null, 'a library without files here has no source root')
})

test('a pinned library cannot be removed from settings; an added one can', async () => {
  const pinned = await j('DELETE', '/api/libraries/scratch')
  assert.equal(pinned.status, 403)
  const added = await j('DELETE', '/api/libraries/site')
  assert.equal(added.status, 200)
  assert.throws(() => getLibrary('site'), /no library named site/)
})

test('a library added without an identifier is named from its label, else its directory', async () => {
  const one = await j('POST', '/api/libraries', { label: 'Lab archive', indexRoot: SITE_IDX })
  assert.equal(one.status, 200)
  assert.equal(one.body.library.id, 'lab-archive')
  assert.equal(one.body.library.label, 'Lab archive')
  const two = await j('POST', '/api/libraries', { id: '', label: 'Lab archive', indexRoot: SITE_IDX })
  assert.equal(two.body.library.id, 'lab-archive-2')
  const bare = await j('POST', '/api/libraries', { indexRoot: SITE_IDX })
  assert.equal(bare.status, 200)
  assert.equal(bare.body.library.id, path.basename(SITE_IDX).toLowerCase().replace(/[^a-z0-9]+/g, '-'))
  // Names a request already means something by are never taken.
  const all = await j('POST', '/api/libraries', { label: 'All', indexRoot: SITE_IDX })
  assert.equal(all.body.library.id, 'all-2')
  const kb = await j('POST', '/api/libraries', { label: 'KB', indexRoot: SITE_IDX })
  assert.equal(kb.body.library.id, 'kb-2')
  const reserved = await j('POST', '/api/libraries', { id: 'all', indexRoot: SITE_IDX })
  assert.equal(reserved.status, 409)
  for (const id of ['lab-archive', 'lab-archive-2', bare.body.library.id, 'all-2', 'kb-2']) {
    assert.equal((await j('DELETE', `/api/libraries/${id}`)).status, 200)
  }
})

test.after(async () => { await app.close() })
