// A refined search: a name or path field (words, patterns, OR, NOT) and a
// text field (fts5 syntax for the words in files, and the phrase without it
// for meaning), with either kind listed ('any') or text looked for only in
// the files the name field keeps ('all'), inside the same one-pass walk.
// Each kind keeps its own limit. Needs GUFI with sqlite-vec (GUFI_BIN, else
// /opt/gufi/bin) and a Python with fts5 for the real-index tests.
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

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'refine-'))
test.after(() => fs.rmSync(dir, { recursive: true, force: true }))
process.env.GUFI_BIN = realGufi
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
fs.mkdirSync(process.env.KB_ROOT)

const G = await import('../dist/gufi.js')
const F = await import('../dist/refine.js')

/** A unit vector leaning toward one axis, as a blob. */
function unit(axis) {
  const v = new Float32Array(384).fill(0.01)
  v[axis] = 1
  const n = Math.hypot(...v)
  return Buffer.from(new Float32Array(v.map(x => x / n)).buffer)
}

/** A folder tree indexed by gufi_dir2index, with the Studio's words for the
 *  files in `text` and a vector for those in `vectors`. */
function library(name, files, { text = {}, vectors = {} } = {}) {
  const src = path.join(dir, name)
  for (const f of files) { fs.mkdirSync(path.dirname(path.join(src, f)), { recursive: true }); fs.writeFileSync(path.join(src, f), 'x') }
  const ix = path.join(dir, `${name}-ix`)
  fs.mkdirSync(ix)
  execFileSync(path.join(realGufi, 'gufi_dir2index'), ['-x', src, ix], { stdio: 'ignore' })
  const root = path.join(ix, name)
  const vec = Object.fromEntries(Object.entries(vectors).map(([k, b]) => [k, b.toString('hex')]))
  const script = execFileSync('python3', ['-c', `
import json, os, sqlite3, sys
root, text, vec = sys.argv[1], json.loads(sys.argv[2]), json.loads(sys.argv[3])
out = []
for d, _, names in os.walk(root):
    if 'db.db' not in names: continue
    rel = os.path.relpath(d, root)
    db = sqlite3.connect(os.path.join(d, 'db.db'))
    rows = db.execute('SELECT name, inode FROM entries').fetchall()
    key = lambda n: n if rel == '.' else rel + '/' + n
    if any(key(n) in text for n, _ in rows):
        db.execute('CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)')
        for n, inode in rows:
            if key(n) in text: db.execute('INSERT INTO words VALUES (?, ?, ?)', (str(inode), n, text[key(n)]))
    mine = [(n, inode) for n, inode in rows if key(n) in vec]
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
print('\\n'.join(out))
`, root, JSON.stringify(text), JSON.stringify(vec)]).toString()
  if (script.trim()) execFileSync(path.join(realGufi, 'gufi_sqlite3'), [], { input: script })
  return root
}

const LIMITS = { names: 10, paths: 10, words: 30, wordsPerDir: 10, vectors: 10, vectorsPerDir: 3, studioText: true, studioVectors: true, external: null }
const queryVec = axis => [{ pattern: 'minilm-l6-mean-int8', hex: unit(axis).toString('hex') }]
const kinds = rows => rows.map(r => `${r.kind}:${r.path}`).sort()

test('the text field without its syntax is the phrase searched by meaning', () => {
  assert.equal(F.meaningPhrase('"wind turbine" AND blade NOT crack*'), 'wind turbine blade crack')
  assert.equal(F.meaningPhrase('NEAR(blade fatigue, 5) OR (rotor AND gearbox)'), 'blade fatigue rotor gearbox')
  assert.equal(F.meaningPhrase('title:budget ^first'), 'budget first')
  assert.equal(F.meaningPhrase('why did the blades crack'), 'why did the blades crack', 'a plain question passes as is')
})

test('the text field is checked first, and a mistake is said plainly', async () => {
  for (const ok of ['budget', '"exact phrase" NOT draft', 'pre*', 'a OR b', 'NEAR(a b, 5)', '(a OR b) AND c']) await F.validateText(ok)
  for (const [bad, why] of [['a AND (b', /bracket|syntax/], ['"open quote', /quotation mark/], ['a OR', /words on both sides/], ['covid-19', /column name/]]) {
    await assert.rejects(F.validateText(bad), e => e.status === 400 && why.test(e.message) && /double quotes/.test(e.message), bad)
  }
})

test('a refined search needs one of its fields; match is any unless all', () => {
  assert.equal(F.refineOf({}), null)
  assert.equal(F.refineOf({ name: '  ', text: '' }), null)
  assert.deepEqual(F.refineOf({ name: '*.docx' }), { name: '*.docx', text: '', match: 'any' })
  assert.deepEqual(F.refineOf({ text: 'budget', match: 'ALL' }), { name: '', text: 'budget', match: 'all' })
})

test('the name field: patterns on the name, words on the name or a folder, OR between groups, NOT and - exclude', () => {
  const g = G.parseNameFilter('report *.docx OR "q3 plan" -draft NOT old')
  assert.deepEqual(g, [
    [{ kind: 'word', text: 'report', neg: false }, { kind: 'glob', text: '*.docx', neg: false }],
    [{ kind: 'word', text: 'q3 plan', neg: false }, { kind: 'word', text: 'draft', neg: true }, { kind: 'word', text: 'old', neg: true }],
  ])
  const sql = G.nameFilterSql(G.parseNameFilter('*.docx'), ['/ix/lib'], '', 'name', 'list')
  assert.equal(sql, "name LIKE '%.docx' ESCAPE '\\'")
  assert.match(G.nameFilterSql(G.parseNameFilter('50%_off?.txt'), ['/ix/lib'], 'e.', 'name', 'list'), /e\.name LIKE '50\\%\\_off_\.txt' ESCAPE/, 'LIKE wildcards in a pattern are escaped')
  assert.match(G.nameFilterSql(G.parseNameFilter('reports/*.pdf'), ['/ix/lib'], '', 'name', 'list'), /ltrim\(substr\(rpath\(sname, sroll\), 9\)\|\|'\/'\|\|name, '\/'\) LIKE 'reports\/%\.pdf'/, 'a pattern with a slash matches the path')
  assert.equal(G.nameFilterSql(G.parseNameFilter('-draft'), ['/ix/lib'], '', 'either', 'list'), '0', 'a list needs something to look for')
  assert.match(G.nameFilterSql(G.parseNameFilter('-draft'), ['/ix/lib'], '', 'either', 'filter'), /^\(1 AND NOT /, 'a filter keeps all but what it excludes')
})

test('the walk: any lists names and words each to their own; all looks for words only in the files the name field keeps', { skip }, async () => {
  const root = library('lib', ['docs/report.docx', 'docs/notes.md', 'budget/plan.docx', 'slides/deck.pptx', 'old.docx'], {
    text: { 'docs/report.docx': 'quarterly budget and fusion', 'docs/notes.md': 'budget notes', 'budget/plan.docx': 'fusion plan', 'old.docx': 'budget draft' },
  })
  const any = await G.walkSearch('', LIMITS, [root], [], { name: '*.docx', text: 'budget', match: 'any' })
  assert.deepEqual(kinds(any), ['fts:docs/notes.md', 'fts:docs/report.docx', 'fts:old.docx', 'name:budget/plan.docx', 'name:docs/report.docx', 'name:old.docx'])
  const all = await G.walkSearch('', LIMITS, [root], [], { name: '*.docx', text: 'budget', match: 'all' })
  assert.deepEqual(kinds(all), ['fts:docs/report.docx', 'fts:old.docx'], 'words only in .docx files, and no name rows')
  const ops = await G.walkSearch('', LIMITS, [root], [], { name: '*.docx -old', text: 'budget NOT draft', match: 'all' })
  assert.deepEqual(kinds(ops), ['fts:docs/report.docx'], 'NOT in the text and -old in the name both apply')
  const folder = await G.walkSearch('', LIMITS, [root], [], { name: 'budget', text: 'fusion', match: 'all' })
  assert.deepEqual(kinds(folder), ['fts:budget/plan.docx'], 'a word in the name field also matches a folder on the path')
  const nameOnly = await G.walkSearch('', LIMITS, [root], [], { name: 'budget', text: '', match: 'all' })
  assert.deepEqual(kinds(nameOnly), ['path:budget/plan.docx'], 'all with no text lists the name and folder matches')
})

test('with all, meaning is scored only over the kept files\' chunks, on vec0\'s scale', { skip: skip || (!haveVec && 'GUFI without sqlite-vec') }, async () => {
  // Vectors come from text, so a directory with vectors has words too.
  const root = library('vecs', ['a/plan.docx', 'a/near.md', 'b/far.docx'], {
    text: { 'a/plan.docx': 'plan', 'a/near.md': 'near', 'b/far.docx': 'far' },
    vectors: { 'a/plan.docx': unit(3), 'a/near.md': unit(3), 'b/far.docx': unit(7) },
  })
  const all = await G.walkSearch('', LIMITS, [root], queryVec(3), { name: '*.docx', text: 'turbine blades', match: 'all' })
  const v = all.filter(r => r.kind === 'vector')
  assert.deepEqual(v.map(r => r.path).sort(), ['a/plan.docx', 'b/far.docx'], 'the .md file near the query is left out')
  const plain = await G.walkSearch('', LIMITS, [root], queryVec(3), { name: '', text: 'turbine blades', match: 'any' })
  const near = plain.find(r => r.kind === 'vector' && r.path === 'a/plan.docx')
  const kept = v.find(r => r.path === 'a/plan.docx')
  assert.ok(Math.abs(near.r - kept.r) < 1e-5, `the same distance either way (${near.r} vs ${kept.r})`)
  assert.ok(kept.r < v.find(r => r.path === 'b/far.docx').r)
})

test('each kind keeps its own limit: many name matches leave room for the words', { skip }, async () => {
  const files = Array.from({ length: 30 }, (_, i) => `many/budget-${String(i).padStart(2, '0')}.txt`)
  const root = library('crowd', [...files, 'text/one.md', 'text/two.md'], { text: { 'text/one.md': 'budget review', 'text/two.md': 'budget totals' } })
  const rows = await G.walkSearch('', LIMITS, [root], [], { name: 'budget', text: 'budget', match: 'any' })
  const caps = { name: 10, path: 10, fts: 5, vector: 10 }
  // As searchScope fuses a refined search: every kind to its own limit.
  const hits = G.fuseRows(rows, caps, 10 * 2 + 5 + 10)
  assert.equal(hits.filter(h => h.mode === 'name').length, 10, 'names to their limit')
  assert.deepEqual(hits.filter(h => h.mode === 'fts').map(h => h.path).sort(), ['text/one.md', 'text/two.md'], 'and the words still listed')
  // The single box, fused to its one limit, lets names take it all.
  const single = G.fuseRows(rows, caps, 10)
  assert.ok(single.length === 10)
})

test('the single box is unchanged by the refined form', { skip }, async () => {
  const root = library('same', ['a/budget.md', 'b/notes.md'], { text: { 'b/notes.md': 'budget notes' } })
  const before = G.onePassStages('budget', LIMITS, [root])
  const again = G.onePassStages('budget', LIMITS, [root], [], null)
  assert.deepEqual(again, before)
  const rows = await G.walkSearch('budget', LIMITS, [root])
  assert.deepEqual(kinds(rows), ['fts:b/notes.md', 'name:a/budget.md'])
})

test('meaning a walk could not carry is filtered here as the walk filters', () => {
  assert.equal(F.namePathMatches('*.docx', 'a/plan.docx'), true)
  assert.equal(F.namePathMatches('*.docx', 'a/near.md'), false)
  assert.equal(F.namePathMatches('budget', 'budget/plan.docx'), true, 'a folder on the path')
  assert.equal(F.namePathMatches('reports/*.pdf', 'reports/q3.pdf'), true)
  assert.equal(F.namePathMatches('*.docx -old', 'old.docx'), false)
  assert.equal(F.namePathMatches('*.pdf OR *.docx', 'x.docx'), true)
})
