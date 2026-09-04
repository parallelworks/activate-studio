import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { parseSearchQuery, ftsExprFor, nameClause, nameWhere, wantsSemantic } = await import('../dist/gufi.js')
const fts = s => ftsExprFor(parseSearchQuery(s))

test('bare words are whole words that must all be present', () => {
  assert.equal(fts('tax identification'), '("tax" AND "identification")')
  assert.equal(fts('TIN'), '"TIN"')
})
test('stopwords drop from bare terms but never from a phrase', () => {
  assert.equal(fts('what is the TIN'), '"TIN"')
  assert.equal(fts('"what is the TIN"'), '"what is the TIN"')
})
test('a quoted phrase is exact, OR separates alternatives, minus and NOT exclude, star is a prefix', () => {
  const p = parseSearchQuery('"tax identification number"')
  assert.equal(p.exact, true)
  assert.equal(ftsExprFor(p), '"tax identification number"')
  assert.equal(fts('TIN OR EIN'), '"TIN" OR "EIN"')
  assert.equal(fts('TIN -DUNS'), '("TIN" NOT "DUNS")')
  assert.equal(fts('TIN NOT DUNS'), '("TIN" NOT "DUNS")')
  assert.equal(fts('ident*'), '"ident"*')
  assert.equal(fts('"cage code" OR UEI -sam'), '"cage code" OR ("UEI" NOT "sam")')
})
test('a short filename term must start a word; a longer one may sit inside one', () => {
  const short = nameClause('TIN')
  assert.match(short, /name LIKE 'TIN%'/)
  assert.match(short, /name LIKE '% TIN%'/)
  assert.doesNotMatch(short, /LIKE '%TIN%'/)
  assert.equal(nameClause('routine'), "name LIKE '%routine%'")
  assert.equal(nameWhere(parseSearchQuery('-only')), '0', 'nothing positive means no name search')
})
test('semantic hits join a question or topic, not an identifier or an operator query', () => {
  assert.equal(wantsSemantic('how do we handle tax ids in proposals'), true)
  assert.equal(wantsSemantic('TIN'), false)
  assert.equal(wantsSemantic('7DVN5'), false)
  assert.equal(wantsSemantic('"exact phrase"'), false)
  assert.equal(wantsSemantic('TIN OR EIN'), false)
  assert.equal(wantsSemantic('federation'), true)
})
