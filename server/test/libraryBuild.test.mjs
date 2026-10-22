// A library built of a folder on this computer: the check before the
// build (GUFI, the folders, which ones are made), the build as an index
// job, the library it adds (read only, searchable, its files opening with
// their own extracted text), the sync that keeps it in step with the
// folder, a rebuild, and removal. Runs GUFI and enrich.py for real; skips
// without GUFI.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const skip = !fs.existsSync(path.join(gufiBin, 'gufi_dir2index')) && 'GUFI not installed'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'libbuild-'))
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
delete process.env.AUTH_JWKS_URL
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
write(path.join(dir, 'kb', 'main.md'), '# main\n')
write(path.join(dir, 'kb', 'notes', 'a.md'), '# a\n')
write(path.join(dir, 'kb', 'notes', 'b.md'), '# b\n')
// Labels on the main folder: one on a folder, one on a file in it.
if (!skip) {
  execFileSync('setfattr', ['-n', 'user.studio.tags', '-v', 'project', path.join(dir, 'kb', 'notes')])
  execFileSync('setfattr', ['-n', 'user.studio.tags', '-v', 'draft,review', path.join(dir, 'kb', 'notes', 'a.md')])
  fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
}
const papers = path.join(dir, 'papers')
write(path.join(papers, 'intro.md'), '# Introduction\nThe turbine blade study.\n')
write(path.join(papers, 'data', 'runs.csv'), 'a,b\n1,2\n')
write(path.join(papers, 'data', 'big.bin'), Buffer.alloc(4096, 1))

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const B = await import('../dist/libraryBuild.js')
const D = await import('../dist/libraryDetails.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
const S = await import('../dist/indexSettings.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(B.libraryBuildRoutes)
await app.register(D.libraryDetailsRoutes)
await app.ready()
const call = async (method, url, body) => {
  const r = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: r.statusCode, body: r.json() }
}
const waitReady = async id => {
  for (let i = 0; i < 300; i++) {
    const l = (await call('GET', '/api/libraries')).body.libraries.find(x => x.id === id)
    if (l?.build && (l.build.state === 'ready' || l.build.state === 'failed') && !l.build.progress) return l
    await new Promise(r => setTimeout(r, 100))
  }
  throw new Error('the build did not finish')
}

test('the listing says libraries can be built here, with sign-in off', async () => {
  assert.equal((await call('GET', '/api/libraries')).body.canBuild, true)
})

test('a library is extracted and embedded under the indexing settings, with an embedding cache of its own', () => {
  const def = { id: 'notes', sourceRoot: '/src/notes', indexRoot: '/ix/notes', build: { cacheRoot: '/ix/extract', indexDir: '/ix' } }
  const model = path.join(dir, 'ix', 'models', 'minilm384.gguf')
  write(model, '')
  try {
    const on = { ...S.INDEX_SETTINGS_DEFAULTS, pdfReader: 'pypdf' }
    let s = B.enrichSteps(def, {}, on, 3)
    assert.deepEqual(s.documents, { kbRoot: '/src/notes', cacheRoot: '/ix/extract', jobs: 3, ocr: true })
    const arg = (argv, k) => argv[argv.indexOf(k) + 1]
    assert.equal(arg(s.text, '--jobs'), '3')
    assert.equal(arg(s.text, '--pdf-reader'), 'pypdf')
    assert.ok(!s.text.includes('--no-ocr') && !s.text.includes('--no-documents'))
    assert.equal(arg(s.meaning, '--jobs'), '3')
    assert.equal(arg(s.meaning, '--cache'), path.join(L.builtLibraryDir('notes'), 'embed-cache.db'))
    assert.ok(s.meaning.includes('--prune'), 'a whole build drops cache entries the index no longer uses')
    assert.equal(arg(s.meaning, '--max-chunks'), '4', 'Documents: the first passages of each file')
    assert.ok(s.meaning.includes('--documents-only'))
    s = B.enrichSteps(def, {}, { ...on, meaning: 'full' }, 3)
    assert.ok(!s.meaning.includes('--max-chunks') && !s.meaning.includes('--documents-only'), 'Full: every file with text')
    s = B.enrichSteps(def, { subdir: 'a/b' }, on, 3)
    assert.equal(arg(s.meaning, '--subdir'), 'a/b')
    assert.ok(!s.meaning.includes('--prune'), 'one folder never prunes the cache')

    const off = { ...on, ocr: false, documents: false, meaning: 'off', legacyOffice: false }
    s = B.enrichSteps(def, { noRecurse: true }, off, 1)
    assert.equal(s.documents, null, 'documents read by name only')
    assert.ok(s.text.includes('--no-ocr') && s.text.includes('--no-documents') && s.text.includes('--no-recurse'))
    assert.ok(!s.text.includes('--soffice'))
    assert.equal(s.meaning, null, 'search by meaning off')
    assert.equal(B.enrichSteps(def, {}).meaning, null, 'a built library starts with search by meaning off')
    assert.equal(S.indexSettings('notes').meaning, 'off')
  } finally { fs.rmSync(model) }
  assert.equal(B.enrichSteps(def, {}).meaning, null, 'no embedding model, no embeddings')
})

test('the check lists the folders it would make, and refuses unusable ones', { skip }, async () => {
  const fresh = path.join(dir, 'new', 'notes')
  const idx = path.join(dir, 'indexes', 'notes')
  const c = (await call('POST', '/api/libraries/build/check', { sourceRoot: fresh, indexRoot: idx })).body
  assert.equal(c.ok, true, JSON.stringify(c.steps))
  assert.deepEqual(c.steps.map(s => [s.name, s.ok]), [['gufi', true], ['source', true], ['index', true], ['extract', true]])
  assert.deepEqual(c.create, [fresh, idx])
  assert.match(c.steps[1].detail, /does not exist yet; it is created, empty/)
  assert.equal(fs.existsSync(fresh), false, 'a check makes nothing')

  const fail = async (body, re) => {
    const r = (await call('POST', '/api/libraries/build/check', body)).body
    assert.equal(r.ok, false)
    assert.match(r.steps.find(s => !s.ok).detail, re)
  }
  await fail({ sourceRoot: papers, indexRoot: path.join(papers, 'idx') }, /inside the folder being indexed/)
  await fail({ sourceRoot: process.env.KB_ROOT }, /is the main folder/)
  await fail({ sourceRoot: path.join(dir, 'ix', 'x') }, /Studio's own data/)
  write(path.join(dir, 'busy', 'other.txt'), 'x')
  await fail({ sourceRoot: papers, indexRoot: path.join(dir, 'busy') }, /holds files the Studio did not write/)
  write(path.join(dir, 'afile'), 'x')
  await fail({ sourceRoot: path.join(dir, 'afile') }, /is a file, not a folder/)
})

test('missing folders are made only once confirmed', { skip }, async () => {
  const fresh = path.join(dir, 'drop')
  const r = await call('POST', '/api/libraries/build', { sourceRoot: fresh })
  assert.equal(r.status, 409)
  assert.match(r.body.error, /do not exist yet: .*drop\. Confirm to create them/)
  assert.equal(fs.existsSync(fresh), false)
  const ok = await call('POST', '/api/libraries/build', { sourceRoot: fresh, label: 'Drop box', create: true })
  assert.equal(ok.status, 200, JSON.stringify(ok.body))
  assert.equal(fs.statSync(fresh).isDirectory(), true)
  const l = await waitReady(ok.body.library.id)
  assert.equal(l.build.state, 'ready', l.build.error ?? '')
  assert.equal(l.build.managed, true, 'no index folder given: the Studio keeps it')
  assert.equal(l.build.files, 0)
})

let papersId
test('a build runs as an index job and adds a read-only, searchable library', { skip }, async () => {
  const idx = path.join(dir, 'indexes', 'papers')
  const r = await call('POST', '/api/libraries/build', { sourceRoot: papers, indexRoot: idx, label: 'Papers', create: true })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  papersId = r.body.library.id
  assert.equal(papersId, 'papers')
  assert.ok(r.body.job.id, 'an index job to follow')
  assert.match(r.body.job.dir, /^library:papers$/)
  const l = await waitReady(papersId)
  assert.equal(l.build.state, 'ready', l.build.error ?? '')
  assert.equal(l.writable, false)
  assert.equal(l.indexRoot, path.join(idx, 'papers'))
  assert.equal(l.sourceRoot, fs.realpathSync(papers))
  assert.equal(l.build.files, 3)
  assert.equal(l.build.dirs, 2)
  assert.ok(fs.existsSync(path.join(idx, '.studio-index.json')))
  assert.equal(JSON.parse(fs.readFileSync(path.join(idx, '.studio-index.json'), 'utf8')).source, fs.realpathSync(papers))
  // Searched by name and by text; its files list and open from the folder.
  const lib = L.getLibrary(papersId)
  assert.deepEqual((await G.searchNames('runs', 5, lib.indexRoot)).map(h => h.path), ['data/runs.csv'])
  assert.deepEqual((await G.searchFts('turbine', 5, lib.indexRoot)).map(h => h.path), ['intro.md'])
  const tree = (await call('GET', `/api/kb/tree?path=&library=${papersId}`)).body.entries.map(e => e.name)
  assert.deepEqual(tree, ['data', 'intro.md'])
  // Read only: the write routes refuse it.
  const up = await call('POST', `/api/kb/mkdir?library=${papersId}`, { path: 'x' })
  assert.notEqual(up.status, 200)
})

test('the details panel describes a folder and a file from the index', { skip }, async () => {
  const d = (await call('GET', `/api/kb/details?path=data&library=${papersId}`)).body
  assert.equal(d.type, 'dir')
  assert.equal(d.files, 2)
  assert.equal(d.dirs, 0, 'folders inside, not the folder itself')
  assert.equal(d.size, 4096 + 8)
  assert.deepEqual(d.types.map(t => t.ext), ['bin', 'csv'], 'largest first')
  assert.ok(d.mtime > 0)
  assert.equal(d.library.label, 'Papers')
  assert.equal(d.library.kind, 'Built from a folder on this computer')
  assert.equal(d.index, path.join(dir, 'indexes', 'papers', 'papers'))
  const top = (await call('GET', `/api/kb/details?path=&library=${papersId}`)).body
  assert.equal(top.files, 3)
  assert.equal(top.dirs, 1)
  assert.equal(top.name, 'Papers')
  const f = (await call('GET', `/api/kb/details?path=data/runs.csv&library=${papersId}`)).body
  assert.equal(f.type, 'file')
  assert.equal(f.size, 8)
  // With All libraries, the path starts with the library's id.
  const all = (await call('GET', `/api/kb/details?path=${papersId}/data&library=all`)).body
  assert.equal(all.files, 2)
  assert.equal((await call('GET', `/api/kb/details?path=../x&library=${papersId}`)).status, 400)
})

test('the details of a folder in the main folder carry its labels and those below it', { skip }, async () => {
  const d = (await call('GET', '/api/kb/details?path=notes')).body
  assert.equal(d.type, 'dir')
  assert.equal(d.files, 2)
  assert.equal(d.library.kind, 'Main folder')
  assert.deepEqual(d.labels.own, ['project'])
  assert.deepEqual(d.labels.within.map(l => l.label).sort(), ['draft', 'project', 'review'])
  const f = (await call('GET', '/api/kb/details?path=notes/a.md')).body
  assert.equal(f.type, 'file')
  assert.deepEqual(f.labels.own, ['draft', 'review'])
  assert.deepEqual(f.labels.inherited, ['project'])
})

test('the sync indexes a folder that changed, and drops one that is gone', { skip }, async () => {
  write(path.join(papers, 'data', 'later.md'), '# later\nA compressor note.\n')
  // Folder times have a resolution; make the change unmistakable.
  const future = new Date(Date.now() + 5000)
  fs.utimesSync(path.join(papers, 'data'), future, future)
  write(path.join(papers, 'extra', 'x.txt'), 'x')
  let changed = await B.syncLibrary(papersId)
  assert.ok(changed.includes('data'), JSON.stringify(changed))
  const lib = L.getLibrary(papersId)
  assert.deepEqual((await G.searchFts('compressor', 5, lib.indexRoot)).map(h => h.path), ['data/later.md'])
  assert.equal((await call('GET', '/api/libraries')).body.libraries.find(x => x.id === papersId).build.files, 5)
  assert.deepEqual(await B.syncLibrary(papersId), [], 'nothing new')
  fs.rmSync(path.join(papers, 'extra'), { recursive: true })
  changed = await B.syncLibrary(papersId)
  assert.ok(changed.includes('extra (removed)'), JSON.stringify(changed))
  assert.equal(fs.existsSync(path.join(lib.indexRoot, 'extra')), false)
})

test('search by meaning is a level per library the Studio builds, set in Settings, Libraries', { skip }, async () => {
  const lib = id => call('GET', '/api/libraries').then(r => r.body.libraries.find(l => l.id === id))
  assert.equal((await lib('kb')).meaning, 'documents', 'the main folder starts at Documents')
  assert.equal((await lib(papersId)).meaning, 'off', 'another library starts off')
  let r = await call('PUT', `/api/libraries/${papersId}/meaning`, { level: 'full' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.meaning, 'full')
  assert.equal((await lib(papersId)).meaning, 'full')
  assert.equal((await call('PUT', `/api/libraries/${papersId}/meaning`, { level: 'most' })).status, 400)
  assert.equal((await call('PUT', '/api/libraries/nothing-here/meaning', { level: 'off' })).status, 404)
  // An index the Studio only reads keeps the vectors it has; there is nothing to set.
  r = await call('POST', '/api/libraries', { id: 'kb-copy', indexRoot: path.join(dir, 'ix', 'gufi', 'kb') })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.library.meaning, null)
  assert.equal((await call('PUT', '/api/libraries/kb-copy/meaning', { level: 'full' })).status, 409)
  assert.equal((await call('DELETE', '/api/libraries/kb-copy')).status, 200)
  // Search looks by meaning only where it is on.
  const { meaningSearched } = await import('../dist/libraryScope.js')
  const papers = L.listLibraries('kb').find(l => l.id === papersId)
  assert.equal(meaningSearched({ ...papers, caps: { ...papers.caps, vectors: true } }), true)
  assert.equal(meaningSearched({ ...papers, meaning: 'off', caps: { ...papers.caps, vectors: true } }), false, 'vectors left from before are not searched')
  assert.equal(meaningSearched({ ...papers, meaning: null, caps: { ...papers.caps, vectors: true } }), true, 'a read-only index with vectors')
  await call('PUT', `/api/libraries/${papersId}/meaning`, { level: 'off' })
})

test('a rebuild runs again and keeps the index searchable', { skip }, async () => {
  const r = await call('POST', `/api/libraries/${papersId}/rebuild`)
  assert.equal(r.status, 200)
  const l = await waitReady(papersId)
  assert.equal(l.build.state, 'ready')
  assert.equal(l.build.files, 4)
  assert.equal((await call('POST', '/api/libraries/kb/rebuild')).status, 404)
})

test('removing forgets the library and the Studio\'s own files for it, not a chosen index folder', { skip }, async () => {
  const drop = (await call('GET', '/api/libraries')).body.libraries.find(l => l.label === 'Drop box')
  const own = L.builtLibraryDir(drop.id)
  assert.ok(fs.existsSync(own))
  assert.equal((await call('DELETE', `/api/libraries/${drop.id}`)).status, 200)
  assert.equal(fs.existsSync(own), false)
  assert.equal((await call('DELETE', `/api/libraries/${papersId}`)).status, 200)
  assert.ok(fs.existsSync(path.join(dir, 'indexes', 'papers', 'papers', 'db.db')), 'the chosen index folder stays')
  assert.ok(fs.existsSync(papers), 'the folder itself is never touched')
})
