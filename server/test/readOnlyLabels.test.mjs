// Labels on a library whose files the Studio does not write: kept by the
// Studio per library, read back with the folders' labels inherited, shown
// in the tree, Details and the vocabulary, filtered on in search, compiled
// into Query's SQL, and dropped when the file is gone. With All libraries
// selected, a delete reaches the file's own library, and an action that
// spans libraries says so plainly.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'rol-kb-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'rol-ix-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = IX
process.env.STUDIO_SECTIONS = 'library,search,overview'
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'rol-bin-'))
fs.writeFileSync(path.join(bin, 'gufi_query'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
process.env.GUFI_BIN = bin

// A read-only library: an index and its source folder on this computer.
const SRC = fs.mkdtempSync(path.join(os.tmpdir(), 'rol-src-'))
const IDX = fs.mkdtempSync(path.join(os.tmpdir(), 'rol-idx-'))
fs.mkdirSync(path.join(SRC, 'reports'))
fs.writeFileSync(path.join(SRC, 'reports', 'q3.md'), '# q3\n')
fs.writeFileSync(path.join(SRC, 'reports', 'q4.md'), '# q4\n')
fs.writeFileSync(path.join(SRC, 'readme.md'), '# readme\n')
fs.writeFileSync(path.join(IDX, 'db.db'), '')
fs.mkdirSync(path.join(IDX, 'reports'))
fs.writeFileSync(path.join(IDX, 'reports', 'db.db'), '')
fs.writeFileSync(path.join(KB, 'mine.md'), '# mine\n')

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { tagRoutes } = await import('../dist/tags.js')
const { libraryDetailsRoutes } = await import('../dist/libraryDetails.js')
const { queryRoutes } = await import('../dist/query.js')
const LL = await import('../dist/libraryLabels.js')
const { getLibrary } = await import('../dist/libraries.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(tagRoutes)
await app.register(libraryDetailsRoutes)
await app.register(queryRoutes)
await app.ready()
test.after(async () => { await app.close() })
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}

test('the library is added read only', async () => {
  const r = await j('POST', '/api/libraries', { id: 'site', label: 'Site index', indexRoot: IDX, sourceRoot: SRC })
  assert.equal(r.status, 200)
  assert.equal(r.body.library.writable, false)
})

test('labels apply on a read-only library, kept by the Studio, and the files are untouched', async () => {
  const before = fs.statSync(path.join(SRC, 'reports', 'q3.md')).mtimeMs
  const r = await j('POST', '/api/kb/tags?library=site', { paths: ['reports/q3.md'], add: ['Review'] })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.deepEqual(r.body.updated, { 'reports/q3.md': ['review'] })
  assert.equal(r.body.stored, 'studio')
  assert.equal(fs.statSync(path.join(SRC, 'reports', 'q3.md')).mtimeMs, before)
  assert.ok(fs.existsSync(path.join(IX, 'libraries', 'site', 'labels.json')))
  // A folder's label is inherited below it.
  assert.equal((await j('POST', '/api/kb/tags?library=site', { paths: ['reports'], add: ['finance'] })).status, 200)
  const got = await j('GET', '/api/kb/tags?library=site&path=reports/q3.md')
  assert.deepEqual(got.body, { own: ['review'], inherited: ['finance'], stored: 'studio' })
  // Removing one leaves the rest.
  await j('POST', '/api/kb/tags?library=site', { paths: ['reports/q3.md'], remove: ['review'] })
  assert.deepEqual((await j('GET', '/api/kb/tags?library=site&path=reports/q3.md')).body.own, [])
  await j('POST', '/api/kb/tags?library=site', { paths: ['reports/q3.md'], add: ['review'] })
  // The main library's labels are still where they were: on the files.
  assert.equal((await j('GET', '/api/kb/tags?path=mine.md')).body.stored, 'files')
})

test('a path that is not in the library is refused, and none with a dot segment', async () => {
  assert.equal((await j('POST', '/api/kb/tags?library=site', { paths: ['nope.md'], add: ['x'] })).status, 404)
  assert.equal((await j('POST', '/api/kb/tags?library=site', { paths: ['../escape'], add: ['x'] })).status, 400)
})

test('with All libraries the combined paths reach their own library, and one request may span two', async () => {
  const r = await j('POST', '/api/kb/tags?library=all', { paths: ['site/readme.md'], add: ['start-here'] })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.deepEqual(r.body.updated, { 'site/readme.md': ['start-here'] })
  assert.deepEqual((await j('GET', '/api/kb/tags?library=all&path=site/readme.md')).body.own, ['start-here'])
  // The same label on a file in each: the main library's goes on the file
  // (where xattrs work; the overlay otherwise), the other is kept here.
  const both = await j('POST', '/api/kb/tags?library=all', { paths: ['site/reports/q4.md', 'kb/mine.md'], add: ['shared'] })
  assert.equal(both.status, 200, JSON.stringify(both.body))
  assert.deepEqual(Object.keys(both.body.updated).sort(), ['kb/mine.md', 'site/reports/q4.md'])
  assert.equal(both.body.stored, 'studio')
})

test('the tree, Details and the vocabulary show the kept labels', async () => {
  const tree = await j('GET', '/api/kb/tree?library=site&path=reports')
  assert.deepEqual(tree.body.entries.find(e => e.path === 'reports/q3.md').tags, ['review'])
  const all = await j('GET', '/api/kb/tree?library=all&path=site/reports')
  assert.deepEqual(all.body.entries.find(e => e.path === 'site/reports/q3.md').tags, ['review'])
  const vocab = (await j('GET', '/api/tags')).body.tags.map(t => t.tag)
  for (const t of ['review', 'finance', 'start-here', 'shared']) assert.ok(vocab.includes(t), `${t} in ${vocab}`)
  const lib = getLibrary('site')
  const { describe } = await import('../dist/libraryDetails.js')
  const d = await describe(lib, 'reports', null)
  assert.deepEqual(d.labels.own, ['finance'])
  assert.ok(d.labels.within.some(w => w.label === 'review'), JSON.stringify(d.labels))
})

test('search hits from the library carry its labels, and a label filter keeps them', () => {
  const hits = [
    { path: 'reports/q3.md', library: { id: 'site', label: 'Site index' } },
    { path: 'readme.md', library: { id: 'site', label: 'Site index' } },
  ]
  const out = LL.annotateLibraryHits(hits)
  assert.deepEqual(out[0].tags.sort(), ['finance', 'review'])
  assert.deepEqual(out[1].tags, ['start-here'])
  assert.deepEqual(LL.annotateLibraryHits(hits, ['Finance']).map(h => h.path), ['reports/q3.md'])
})

test('Query compiles the kept labels into its SQL for that library, and not the main library\'s', async () => {
  const builder = { source: 'files', filters: [{ field: 'label', op: 'eq', value: 'review' }], groupBy: 'none', sort: 'size', dir: 'desc', limit: 10 }
  const res = await app.inject({ method: 'POST', url: '/api/query/sql', payload: { builder, library: 'site' } })
  assert.equal(res.statusCode, 200, res.body)
  const e = res.json().stages.find(s => s.flag === '-E').sql
  assert.ok(e.includes(`IN ('${path.resolve(IDX)}/reports/q3.md')`), e)
  // A folder label becomes a path prefix.
  const fin = await app.inject({ method: 'POST', url: '/api/query/sql', payload: { builder: { ...builder, filters: [{ field: 'label', op: 'eq', value: 'finance' }] }, library: 'site' } })
  assert.ok(fin.json().stages.find(s => s.flag === '-E').sql.includes(`LIKE '${path.resolve(IDX)}/reports/%'`))
  // On the main library the kept labels play no part.
  const main = await app.inject({ method: 'POST', url: '/api/query/sql', payload: { builder } })
  assert.ok(!main.json().stages.find(s => s.flag === '-E').sql.includes(path.resolve(IDX)))
})

test('under match "any" the label condition is one group of its own, OR\'d with the others', async () => {
  const builder = { source: 'files', match: 'any', groupBy: 'none', sort: 'size', dir: 'desc', limit: 10,
    filters: [{ field: 'label', op: 'eq', value: 'review' }, { field: 'name', op: 'eq', value: 'readme.md' }] }
  const res = await app.inject({ method: 'POST', url: '/api/query/sql', payload: { builder, library: 'site' } })
  assert.equal(res.statusCode, 200, res.body)
  const e = res.json().stages.find(s => s.flag === '-E').sql
  const where = e.slice(e.indexOf('WHERE '))
  const [labelGroup, nameGroup] = where.slice('WHERE '.length).split(") OR (vrpentries.name")
  assert.ok(labelGroup.startsWith('((EXISTS'), where)
  assert.ok(labelGroup.includes(`IN ('${path.resolve(IDX)}/reports/q3.md')`), where)
  assert.ok(nameGroup?.startsWith(" = 'readme.md')"), where)
  // A label on the library's top folder holds for every file in it.
  assert.equal((await j('POST', '/api/kb/tags?library=site', { paths: [''], add: ['everything'] })).status, 200)
  const all = await app.inject({ method: 'POST', url: '/api/query/sql', payload: { builder: { ...builder, filters: [{ field: 'label', op: 'eq', value: 'everything' }, builder.filters[1]] }, library: 'site' } })
  assert.ok(all.json().stages.find(s => s.flag === '-E').sql.includes("WHERE ((1=1)) OR (vrpentries.name = 'readme.md')"))
  await j('POST', '/api/kb/tags?library=site', { paths: [''], remove: ['everything'] })
})

test('a label on a remote library is kept as given; nothing there is looked at', () => {
  const remote = { id: 'u-far', label: 'Far', indexRoot: 'ssh://far/idx', sourceRoot: null, site: 'far', primary: false, writable: false, pinned: false, caps: { index: true, fullText: false, vectors: false } }
  assert.deepEqual(LL.setLibraryLabels(remote, 'data/run1.h5', ['Raw']), ['raw'])
  assert.deepEqual(LL.libraryEffectiveLabels('u-far', 'data/run1.h5').own, ['raw'])
  assert.equal(LL.pruneLibraryLabels(remote, true), 0)
  assert.deepEqual(LL.libraryLabelsForIndexRoot('ssh://far/idx').fileTags.get('data/run1.h5'), ['raw'])
})

test('labels of files that are gone are dropped, but not when the whole folder is missing', () => {
  const lib = getLibrary('site')
  fs.rmSync(path.join(SRC, 'reports', 'q4.md'))
  assert.equal(LL.pruneLibraryLabels(lib, true), 1)
  assert.deepEqual(LL.libraryLabelsOf('site', 'reports/q4.md'), [])
  assert.deepEqual(LL.libraryLabelsOf('site', 'reports/q3.md'), ['review'])
  // An unmounted folder looks like every file gone: nothing is dropped.
  const moved = `${SRC}-away`
  fs.renameSync(SRC, moved)
  try {
    assert.equal(LL.pruneLibraryLabels(lib, true), 0)
    assert.deepEqual(LL.libraryLabelsOf('site', 'reports/q3.md'), ['review'])
  } finally {
    fs.renameSync(moved, SRC)
  }
})

test('with All libraries, Delete reaches the file in its own library; a read-only one is refused', async () => {
  fs.writeFileSync(path.join(KB, 'gone.md'), 'x\n')
  const r = await app.inject({ method: 'DELETE', url: '/api/kb/file?library=all&path=kb/gone.md' })
  assert.doesNotMatch(r.body, /no library named all/)
  assert.ok(!fs.existsSync(path.join(KB, 'gone.md')), r.body)
  const ro = await j('DELETE', '/api/kb/file?library=all&path=site/readme.md')
  assert.equal(ro.status, 403)
  assert.match(ro.body.error ?? ro.body.message ?? '', /read only/)
  assert.ok(fs.existsSync(path.join(SRC, 'readme.md')))
})

test('an action that names All libraries where one is needed says so', async () => {
  const r = await j('POST', '/api/kb/rename?library=all', { path: 'kb/mine.md', name: 'x.md' })
  assert.equal(r.status, 400)
  assert.match(r.body.error ?? r.body.message ?? '', /one library at a time/)
})

test('removing the library removes its labels', async () => {
  assert.equal((await j('DELETE', '/api/libraries/site')).status, 200)
  assert.ok(!fs.existsSync(path.join(IX, 'libraries', 'site', 'labels.json')))
  assert.deepEqual(LL.libraryLabelsOf('site', 'reports/q3.md'), [])
})
