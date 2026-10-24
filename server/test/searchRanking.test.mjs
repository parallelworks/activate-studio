// Search walks a library's index once for both names and words, brings
// back the best matches rather than the first found (word matches by bm25,
// names by how closely they match), finds the same files the separate
// queries did, and orders word matches across libraries by rank. Needs
// GUFI (GUFI_BIN, else /opt/gufi/bin) and a Python with fts5; skips without.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'

const realGufi = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(realGufi, 'gufi_query')) && fs.existsSync(path.join(realGufi, 'gufi_dir2index'))
const haveFts = spawnSync('python3', ['-c', "import sqlite3; sqlite3.connect(':memory:').execute('CREATE VIRTUAL TABLE t USING fts5(x)')"]).status === 0
const skip = (!haveGufi && 'GUFI not installed') || (!haveFts && 'no Python with fts5')

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'rank-'))
test.after(() => fs.rmSync(dir, { recursive: true, force: true }))
// GUFI through a wrapper that notes each gufi_query it runs.
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

const filler = n => Array.from({ length: n }, (_, i) => `filler${i % 17}`).join(' ')

/**
 * A folder tree indexed by gufi_dir2index, with fts5 `words` tables made
 * as enrich.py makes them, for the files `text` names. Returns the index
 * root a search takes.
 */
function library(name, files, text) {
  const src = path.join(dir, name)
  for (const f of files) { fs.mkdirSync(path.dirname(path.join(src, f)), { recursive: true }); fs.writeFileSync(path.join(src, f), 'x') }
  const ix = path.join(dir, `${name}-ix`)
  fs.mkdirSync(ix)
  execFileSync(path.join(realGufi, 'gufi_dir2index'), ['-x', src, ix], { stdio: 'ignore' })
  const root = path.join(ix, name)
  if (text) {
    execFileSync('python3', ['-c', `
import json, os, sqlite3, sys
root, text = sys.argv[1], json.loads(sys.argv[2])
for d, _, names in os.walk(root):
    if 'db.db' not in names: continue
    rel = os.path.relpath(d, root)
    db = sqlite3.connect(os.path.join(d, 'db.db'))
    db.execute('CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)')
    for n, inode in db.execute('SELECT name, inode FROM entries').fetchall():
        key = n if rel == '.' else rel + '/' + n
        if key in text: db.execute('INSERT INTO words (tinode, fname, wordf) VALUES (?, ?, ?)', (str(inode), n, text[key]))
    db.commit()
`, root, JSON.stringify(text)])
  }
  return root
}

const dist = f => new URL(`../dist/${f}`, import.meta.url).href
const runs = () => (fs.existsSync(gufiLog) ? fs.readFileSync(gufiLog, 'utf8').trim().split('\n').filter(Boolean).length : 0)

// Thirty folders each with a document that mentions "velocity" once, and
// one, late in the walk, that mentions it twenty times; report.pdf,
// report-2026.txt and annual_report.md for the name order.
const files = [], text = {}
for (let i = 0; i < 30; i++) { files.push(`d${String(i).padStart(2, '0')}/light-${i}.txt`); text[`d${String(i).padStart(2, '0')}/light-${i}.txt`] = `velocity ${filler(200)}` }
files.push('d29/heavy.txt'); text['d29/heavy.txt'] = `${'velocity '.repeat(20)} ${filler(200)}`
files.push('d03/report.pdf', 'd05/report-2026.txt', 'd09/annual_report.md', 'd11/reports-old.txt')
text['d03/report.pdf'] = `quarterly report on velocity budgets ${filler(40)}`
let root = null, G = null
test.before(async () => {
  if (skip) return
  root = library('main', files, text)
  G = await import(dist('gufi.js'))
})

test('a search walks the index once for names and words', { skip }, async () => {
  const before = runs()
  const r = await G.searchWordsAndNames('report', { ftsLimit: 10, nameLimit: 10 }, root)
  assert.equal(runs(), before + 1, 'one gufi_query')
  assert.deepEqual(r.fts.map(h => h.path), ['d03/report.pdf'])
  assert.ok(r.names.length >= 3)
  assert.ok(r.fts.every(h => h.mode === 'fts' && typeof h.rank === 'number'))
  assert.ok(r.names.every(h => h.mode === 'name'))
})

test('word matches come back best bm25 first, from the whole tree, not the first found', { skip }, async () => {
  // Three kept of 31 matches; the walk meets thirty single mentions first.
  const hits = await G.searchFts('velocity', 1, root)
  assert.equal(hits.length, 3)
  assert.equal(hits[0].name, 'heavy.txt', hits.map(h => h.name).join(' '))
  assert.ok(hits[0].rank < hits[1].rank)
  assert.match(hits[0].snippet, /\[velocity\]/)
})

test('name matches: the name itself, then names starting with it, then the rest', { skip }, async () => {
  const names = (await G.searchNames('report', 10, root)).map(h => h.name)
  assert.deepEqual(names.slice(0, 3), ['report.pdf', 'report-2026.txt', 'reports-old.txt'])
  assert.equal(names[3], 'annual_report.md')
  // Kept to the limit, the best ones stay.
  assert.deepEqual((await G.searchNames('report', 1, root)).map(h => h.name), ['report.pdf'])
})

test('the same files as the separate queries found, when nothing is cut off', { skip }, async () => {
  const D = '\x1e'
  const old = (sql, cols) => {
    const out = execFileSync(path.join(realGufi, 'gufi_query'), ['-n', '4', '-d', D, '-a', '1',
      '-I', `CREATE TABLE t (${cols});`, '-K', `CREATE TABLE agg (${cols});`,
      '-E', `INSERT INTO t SELECT * FROM (${sql}) WHERE (SELECT count(*) FROM t) < 1000;`,
      '-J', 'INSERT INTO agg SELECT * FROM t;', '-G', 'SELECT * FROM agg LIMIT 1000;', root], { stdio: ['ignore', 'pipe', 'ignore'] }).toString()
    return out.split('\n').filter(Boolean).map(l => path.relative(root, l.split(D)[0])).sort()
  }
  for (const q of ['velocity', 'report', 'velocity OR budgets', '"quarterly report"', 'repo*', 'velocity -budgets', 'heavy']) {
    const parsed = G.parseSearchQuery(q)
    const expr = G.ftsExprFor(parsed), where = G.nameWhere(parsed)
    const before = {
      fts: expr ? old(`SELECT rpath(sname, sroll)||'/'||vrpentries.name, vrpentries.name, size, mtime, '' FROM vrpentries INNER JOIN words ON inode=CAST(tinode AS TEXT) WHERE wordf MATCH '${expr.replace(/'/g, "''")}' LIMIT 1000`, 'p TEXT, n TEXT, s INT64, m INT64, x TEXT') : [],
      names: where !== '0' ? old(`SELECT rpath(sname, sroll)||'/'||name, name, size, mtime FROM vrpentries WHERE ${where} LIMIT 1000`, 'p TEXT, n TEXT, s INT64, m INT64') : [],
    }
    const now = await G.searchWordsAndNames(q, { ftsLimit: 1000, nameLimit: 1000 }, root)
    assert.deepEqual(now.fts.map(h => h.path).sort(), before.fts, `words for ${q}`)
    assert.deepEqual(now.names.map(h => h.path).sort(), before.names, `names for ${q}`)
  }
})

test('an index without words tables still answers its names', { skip }, async () => {
  const bare = library('bare', ['a/report.txt', 'b/other.txt'], null)
  const r = await G.searchWordsAndNames('report', { ftsLimit: 10, nameLimit: 10 }, bare)
  assert.deepEqual(r.names.map(h => h.path), ['a/report.txt'])
  assert.deepEqual(r.fts, [])
  // Asked for names only, it does not touch the words at all.
  const before = runs()
  assert.deepEqual((await G.searchWordsAndNames('report', { ftsLimit: 0, nameLimit: 10 }, bare)).names.map(h => h.path), ['a/report.txt'])
  assert.doesNotMatch(fs.readFileSync(gufiLog, 'utf8').trim().split('\n').slice(before).join('\n'), /words/)
})

test('across libraries, word matches go best rank first whichever library comes first', { skip }, async () => {
  const lightText = {}, lightFiles = []
  for (let i = 0; i < 5; i++) { lightFiles.push(`x${i}/note-${i}.txt`); lightText[`x${i}/note-${i}.txt`] = `velocity ${filler(300)}` }
  const other = library('other', lightFiles, lightText)
  const S = await import(dist('libraryScope.js'))
  const lib = (id, indexRoot) => ({ id, label: id.toUpperCase(), indexRoot, caps: { fullText: true, vectors: false, index: true }, primary: false })
  const hits = await S.searchScope([lib('other', other), lib('main', root)], 'velocity', 5, { semantic: false })
  assert.equal(hits[0].name, 'heavy.txt', hits.map(h => `${h.library.id}:${h.name}`).join(' '))
  assert.equal(hits[0].library.id, 'main')
})
