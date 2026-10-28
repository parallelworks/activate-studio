// One walk of an index for every kind of match: names, folder paths, the
// Studio's words and vectors, and external databases in the shared text and
// vector schema, fused by reciprocal rank fusion. A directory missing a
// table skips only the steps that need it; rows from several roots go back
// to their own. Needs GUFI with sqlite-vec (GUFI_BIN, else /opt/gufi/bin)
// and a Python with fts5 for the real-index tests; the rest run anywhere.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'

const realGufi = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(realGufi, 'gufi_query')) && fs.existsSync(path.join(realGufi, 'gufi_dir2index'))
const haveFts = spawnSync('python3', ['-c', "import sqlite3; sqlite3.connect(':memory:').execute('CREATE VIRTUAL TABLE t USING fts5(x)')"]).status === 0
const haveVec = haveGufi && spawnSync(path.join(realGufi, 'gufi_sqlite3'), [':memory:', 'SELECT vec_version();']).status === 0
const skip = (!haveGufi && 'GUFI not installed') || (!haveFts && 'no Python with fts5')

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'onepass-'))
test.after(() => fs.rmSync(dir, { recursive: true, force: true }))
const gufiBin = path.join(dir, 'gufi')
const gufiLog = path.join(dir, 'gufi.log')
fs.mkdirSync(gufiBin)
if (haveGufi) {
  for (const f of fs.readdirSync(realGufi)) if (f !== 'gufi_query') fs.symlinkSync(path.join(realGufi, f), path.join(gufiBin, f))
  fs.writeFileSync(path.join(gufiBin, 'gufi_query'), `#!/bin/sh\nprintf '%s\\n' "$*" >> ${gufiLog}\nexec ${realGufi}/gufi_query "$@"\n`, { mode: 0o755 })
}
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
fs.mkdirSync(process.env.KB_ROOT)

const G = await import('../dist/gufi.js')

const LIMITS = { names: 10, paths: 10, words: 30, wordsPerDir: 10, vectors: 10, vectorsPerDir: 3, studioText: true, studioVectors: true, external: null }

/** A unit vector leaning toward one axis, as hex for a blob: two vectors
 *  leaning the same way are near each other. */
function unit(axis, lean = 1) {
  const v = new Float32Array(384).fill(0.01)
  v[axis] = lean
  const n = Math.hypot(...v)
  return Buffer.from(new Float32Array(v.map(x => x / n)).buffer)
}

/**
 * A folder tree indexed by gufi_dir2index. `text` gives the Studio's words
 * for some files (a words table only in their directories), `vectors` a
 * vector for some files (gchunks, gvec and gvecmodel in their directories),
 * `external` the shared schema's text and vectors (in a file ext.db in their
 * directories). Returns the index root.
 */
function library(name, files, { text = {}, vectors = {}, external = {} } = {}) {
  const src = path.join(dir, name)
  for (const f of files) { fs.mkdirSync(path.dirname(path.join(src, f)), { recursive: true }); fs.writeFileSync(path.join(src, f), 'x') }
  const ix = path.join(dir, `${name}-ix`)
  fs.mkdirSync(ix)
  execFileSync(path.join(realGufi, 'gufi_dir2index'), ['-x', src, ix], { stdio: 'ignore' })
  const root = path.join(ix, name)
  const vec = Object.fromEntries(Object.entries(vectors).map(([k, b]) => [k, b.toString('hex')]))
  const ext = Object.fromEntries(Object.entries(external).map(([k, e]) => [k, { text: e.text, model: e.model, vector: e.vector?.toString('hex'), stale: !!e.stale }]))
  const script = execFileSync('python3', ['-c', `
import json, os, sqlite3, sys
root, text, vec, ext = sys.argv[1], json.loads(sys.argv[2]), json.loads(sys.argv[3]), json.loads(sys.argv[4])
out = []
for d, _, names in os.walk(root):
    if 'db.db' not in names: continue
    rel = os.path.relpath(d, root)
    db = sqlite3.connect(os.path.join(d, 'db.db'))
    rows = db.execute('SELECT name, inode, mtime FROM entries').fetchall()
    key = lambda n: n if rel == '.' else rel + '/' + n
    if any(key(n) in text for n, _, _ in rows):
        db.execute('CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)')
        for n, inode, _ in rows:
            if key(n) in text: db.execute('INSERT INTO words VALUES (?, ?, ?)', (str(inode), n, text[key(n)]))
    mine = [(n, inode) for n, inode, _ in rows if key(n) in vec]
    if mine:
        db.execute('CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT)')
        for i, (n, inode) in enumerate(mine):
            db.execute('INSERT INTO gchunks VALUES (?, ?, ?, 0, ?, ?)', (i + 1, str(inode), n, 'chunk of ' + n, 'h'))
        out.append("ATTACH '%s' AS d;" % os.path.join(d, 'db.db').replace("'", "''"))
        out.append('CREATE VIRTUAL TABLE d.gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);')
        for i, (n, _) in enumerate(mine):
            out.append("INSERT INTO d.gvec(cid, fp384) VALUES (%d, X'%s');" % (i + 1, vec[key(n)]))
        out.append("CREATE TABLE d.gvecmodel (model TEXT); INSERT INTO d.gvecmodel VALUES ('minilm-l6-mean-int8');")
        out.append('DETACH d;')
    db.commit()
    theirs = [(n, inode, mtime) for n, inode, mtime in rows if key(n) in ext]
    if theirs:
        x = sqlite3.connect(os.path.join(d, 'ext.db'))
        x.execute("CREATE VIRTUAL TABLE ext_bm25_by_file USING fts5(bfsid UNINDEXED, binode UNINDEXED, bmtime UNINDEXED, path_segments_json_file UNINDEXED, component_asset_id UNINDEXED, component_type UNINDEXED, path_segments_json_component UNINDEXED, chunk_sequence UNINDEXED, start_position UNINDEXED, end_position UNINDEXED, page_start UNINDEXED, page_end UNINDEXED, content, tokenize = 'porter unicode61')")
        x.execute('CREATE TABLE ext_vectors_by_file (vfsid TEXT, vinode TEXT, vmtime TEXT, page_number INT64, chunk_content TEXT, vector_mechanism TEXT, vector_model_name TEXT, vector_native_dimensions INT64, embedding BLOB)')
        for n, inode, mtime in theirs:
            e = ext[key(n)]
            m = mtime - 1 if e['stale'] else mtime
            if e.get('text'): x.execute('INSERT INTO ext_bm25_by_file VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)', ('fs', str(inode), str(m), '[]', 'a', 'body', '[]', 0, 0, 1, 1, 1, e['text']))
            if e.get('vector'): x.execute('INSERT INTO ext_vectors_by_file VALUES (?,?,?,?,?,?,?,?,?)', ('fs', str(inode), str(m), 1, 'their chunk of ' + n, 'semantic', e['model'], 384, bytes.fromhex(e['vector'])))
        x.commit()
print('\\n'.join(out))
`, root, JSON.stringify(text), JSON.stringify(vec), JSON.stringify(ext)]).toString()
  if (script.trim()) execFileSync(path.join(realGufi, 'gufi_sqlite3'), [], { input: script })
  return root
}

const queryVec = axis => [{ pattern: 'minilm-l6-mean-int8', hex: unit(axis).toString('hex') }, { pattern: '%all-MiniLM-L6-v2', hex: unit(axis).toString('hex') }]
const calls = () => (fs.existsSync(gufiLog) ? fs.readFileSync(gufiLog, 'utf8').split('\n').filter(Boolean).length : 0)

test('fusion: each kind ranked on its own, summed as 1/(60 + rank), ties to the more exact kind, then the path', () => {
  const row = (kind, r, p) => ({ kind, r, path: p, name: path.basename(p), size: 1, mtime: 1, snippet: kind === 'fts' ? `w:${p}` : kind === 'vector' ? `v:${p}` : '', root: '/' })
  const rows = [
    row('fts', -3, 'both.md'), row('fts', -1, 'words.md'),
    row('name', 2, 'notes/name.md'), row('name', 2, 'both.md'),
    row('vector', 0.2, 'meaning.md'), row('vector', 0.5, 'both.md'), row('vector', 0.1, 'meaning.md'),
    row('path', 5, 'proj/inside.md'),
  ]
  const out = G.fuseRows(rows, { name: 10, path: 10, fts: 30, vector: 10 }, 10)
  // both.md: first by words, first by name (before notes/name.md on an
  // equal score), second by meaning. meaning.md and proj/inside.md: first in
  // their kinds, so equal, the folder match first. words.md and
  // notes/name.md: second in theirs, the word match first.
  assert.deepEqual(out.map(h => [h.path, h.mode]), [
    ['both.md', 'fts'], ['proj/inside.md', 'path'], ['meaning.md', 'vector'], ['words.md', 'fts'], ['notes/name.md', 'name'],
  ])
  assert.deepEqual(out[0].kinds, ['fts', 'name', 'vector'])
  assert.equal(out[0].snippet, 'w:both.md', 'a word match lends its snippet')
  assert.ok(Math.abs(out[0].score - (2 / 61 + 1 / 62)) < 1e-12)
  assert.equal(out[2].snippet, 'v:meaning.md', 'its best chunk')
  // A kind past its limit adds nothing.
  assert.deepEqual(G.fuseRows(rows, { name: 1, path: 0, fts: 1, vector: 1 }, 10).map(h => h.path), ['both.md', 'meaning.md'])
  // The same rows in another order fuse the same way.
  assert.deepEqual(G.fuseRows([...rows].reverse(), { name: 10, path: 10, fts: 30, vector: 10 }, 10), out)
})

test('the walk runs names and folder paths first, then words, then meaning, external databases before -E', () => {
  const s = G.onePassStages('report', { ...LIMITS, external: '\\.db$' }, ['/ix/lib'], queryVec(1))
  const E = s[s.indexOf('-E') + 1]
  const at = k => E.indexOf(`SELECT '${k}'`)
  assert.ok(at('n') < at('p') && at('p') < at('f') && at('f') < at('v'), E)
  assert.ok(E.startsWith('DELETE FROM t WHERE rowid IN'), 'one trim first, which also bounds what external databases added')
  assert.equal((E.match(/INSERT INTO t /g) ?? []).length, 3, 'names and paths in one insert, then words, then meaning')
  const x = s.indexOf('--external-copy')
  assert.ok(x >= 0 && x < s.indexOf('-E'))
  assert.equal(s[x + 1], '\\.db$')
  assert.match(s[x + 2], /FROM ext_bm25_by_file JOIN vrpentries e ON CAST\(binode AS INT64\) = e\.inode AND CAST\(bmtime AS INT64\) = e\.mtime/)
  assert.match(s[s.indexOf('-I') + 1], /INSERT INTO qv VALUES \('minilm-l6-mean-int8', X'/)
  assert.match(s[s.indexOf('-G') + 1], /sum\(1\.0 \/ \(60\.0 \+ rn\)\)/)
  // A names-only index: no words, no vectors, nothing external.
  const bare = G.onePassStages('report', { ...LIMITS, studioText: false, studioVectors: false }, ['/ix/lib'], [])
  assert.doesNotMatch(bare.join(' '), /words|gvec|external-copy|qv/)
  assert.equal(G.onePassStages('-only', LIMITS, ['/ix/lib'], []), null, 'nothing to look for')
})

test('a tree with words in some directories: those without answer their names, those with their words, in one walk', { skip }, async () => {
  const root = library('mixed', ['a/report-2024.md', 'a/notes.txt', 'b/plain.md', 'b/c/deep.md'], { text: { 'a/notes.txt': 'the quarterly report is late', 'b/c/deep.md': 'report report report' } })
  const before = calls()
  const rows = await G.walkSearch('report', LIMITS, [root])
  assert.equal(calls() - before, 1, 'one gufi_query')
  const got = (k) => rows.filter(r => r.kind === k).map(r => r.path).sort()
  assert.deepEqual(got('name'), ['a/report-2024.md'])
  assert.deepEqual(got('fts'), ['a/notes.txt', 'b/c/deep.md'])
  // b has no words table: its step failed there, and nothing else was lost.
  const hits = G.fuseRows(rows, { name: 10, path: 10, fts: 30, vector: 10 }, 10)
  assert.equal(hits.length, 3)
  // The same files the separate name and word search find.
  const old = await G.searchWordsAndNames('report', { ftsLimit: 50, nameLimit: 50 }, root)
  assert.deepEqual([...old.fts.map(h => h.path), ...old.names.map(h => h.path)].sort(), hits.map(h => h.path).sort())
})

test('a folder whose path matches finds the files in it, after the names that match', { skip }, async () => {
  const root = library('paths', ['harbor/one.md', 'harbor/sub/two.md', 'elsewhere/harbor-map.md', 'other/three.md'])
  const rows = await G.walkSearch('harbor', LIMITS, [root])
  assert.deepEqual(rows.filter(r => r.kind === 'name').map(r => r.path), ['elsewhere/harbor-map.md'])
  assert.deepEqual(rows.filter(r => r.kind === 'path').map(r => r.path).sort(), ['harbor/one.md', 'harbor/sub/two.md'])
  // The index's own folder name never counts: only the path below the root.
  const none = await G.walkSearch(path.basename(path.dirname(root)).slice(0, 8), { ...LIMITS, names: 0 }, [root])
  assert.equal(none.filter(r => r.kind === 'path').length, 0)
})

test('folder paths are matched below the root when its path has spaces, quotes and characters outside the basic plane', { skip }, async () => {
  const root = library("lib it's 📁 two", ['harbor/one.md', 'other/two.md'])
  const rows = await G.walkSearch('harbor', { ...LIMITS, studioText: false, studioVectors: false }, [root])
  assert.deepEqual(rows.filter(r => r.kind === 'path').map(r => r.path), ['harbor/one.md'])
  assert.equal((await G.walkSearch('two', { ...LIMITS, names: 0, studioText: false, studioVectors: false }, [root])).filter(r => r.kind === 'path').length, 0, 'the root\'s own name is not a folder below it')
})

test('meaning comes from the same walk, nearest first, each file once', { skip: skip || (!haveVec && 'no sqlite-vec in GUFI') }, async () => {
  // As embed.py makes them: vectors only where the words they came from are.
  const root = library('meaning', ['x/near.md', 'x/far.md', 'y/other.md', 'z/plain.md'], {
    text: { 'x/near.md': 'one', 'x/far.md': 'two', 'y/other.md': 'three' },
    vectors: { 'x/near.md': unit(5), 'x/far.md': unit(200), 'y/other.md': unit(5, 0.5) },
  })
  const before = calls()
  const rows = await G.walkSearch('anything', LIMITS, [root], queryVec(5))
  assert.equal(calls() - before, 1)
  const vec = rows.filter(r => r.kind === 'vector').sort((a, b) => a.r - b.r).map(r => r.path)
  assert.equal(vec[0], 'x/near.md')
  assert.equal(vec[vec.length - 1], 'x/far.md')
  // A model with no query vector here fails that step alone.
  const other = await G.walkSearch('near', LIMITS, [root], [{ pattern: 'some-other-model', hex: unit(5).toString('hex') }])
  assert.deepEqual(other.filter(r => r.kind === 'name').map(r => r.path), ['x/near.md'])
  assert.equal(other.filter(r => r.kind === 'vector').length, 0)
})

test('a passage cut mid-word shows from its first whole word, and a full-width snippet ends on a whole word', { skip: skip || (!haveVec && 'no sqlite-vec in GUFI') }, async () => {
  const root = library('passages', ['p/later.md', 'p/first.md'], {
    text: { 'p/later.md': 'x', 'p/first.md': 'y' },
    vectors: { 'p/later.md': unit(7), 'p/first.md': unit(7, 0.9) },
  })
  // As embed.py cuts them: a later passage starts where the offset fell,
  // inside "unifying"; the first starts the text.
  const long = 'information from all the places a file system keeps it, gathered into one index that answers questions about every file, its size, its owner, its age, and the words inside it, '.repeat(2)
  execFileSync('python3', ['-c', `
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute("UPDATE gchunks SET seq = 1, ctext = ? WHERE fname = 'later.md'", ('ying\\n' + sys.argv[2],))
db.execute("UPDATE gchunks SET ctext = 'unifying a short first passage' WHERE fname = 'first.md'")
db.commit()
`, path.join(root, 'p', 'db.db'), long])
  const rows = (await G.walkSearch('anything', LIMITS, [root], queryVec(7))).filter(r => r.kind === 'vector')
  const later = rows.find(r => r.path === 'p/later.md').snippet
  assert.ok(later.startsWith('information from all the places'), later)
  assert.ok(later.endsWith(' …') && later.length <= 202, later)
  assert.ok(long.startsWith(later.slice(0, -2)), 'cut after a whole word')
  assert.equal(rows.find(r => r.path === 'p/first.md').snippet, 'unifying a short first passage', 'the first passage is shown from its start')
  assert.equal(G.passageSnippet('short text'), 'short text')
})

test('external databases in the shared schema are searched by text and by a model we embed, matched by inode and mtime', { skip: skip || (!haveVec && 'no sqlite-vec in GUFI') }, async () => {
  const root = library('shared', ['d/theirs.md', 'd/stale.md', 'd/plain.md', 'e/vec.md', 'e/othermodel.md'], {
    external: {
      'd/theirs.md': { text: 'notes on quillwort and quillwort' },
      'd/stale.md': { text: 'quillwort again', stale: true },
      'e/vec.md': { vector: unit(9), model: 'sentence-transformers/all-MiniLM-L6-v2' },
      'e/othermodel.md': { vector: unit(9), model: 'another/model-768' },
    },
  })
  const lim = { ...LIMITS, studioText: false, studioVectors: false, external: '\\.db$' }
  const rows = await G.walkSearch('quillwort', lim, [root], queryVec(9))
  assert.deepEqual(rows.filter(r => r.kind === 'fts').map(r => r.path), ['d/theirs.md'], 'a file changed since its text was extracted is not matched')
  assert.match(rows.find(r => r.kind === 'fts').snippet, /\[quillwort\]/)
  assert.deepEqual(rows.filter(r => r.kind === 'vector').map(r => r.path), ['e/vec.md'], 'only a model whose query vector is here')
  // Without the pattern, none of it.
  const off = await G.walkSearch('quillwort', { ...lim, external: null }, [root], queryVec(9))
  assert.equal(off.filter(r => r.kind === 'fts' || r.kind === 'vector').length, 0)
})

test('several roots in one walk: each row goes back to its own root', { skip }, async () => {
  const a = library('one', ['shared-name.md', 'x/only-one.md'])
  const b = library('two', ['shared-name.md', 'y/only-two.md'])
  const before = calls()
  const rows = await G.walkSearch('shared', { ...LIMITS, studioText: false, studioVectors: false }, [a, b])
  assert.equal(calls() - before, 1)
  assert.deepEqual(rows.filter(r => r.kind === 'name').map(r => [path.basename(r.root), r.path]).sort(), [['one', 'shared-name.md'], ['two', 'shared-name.md']])
})
