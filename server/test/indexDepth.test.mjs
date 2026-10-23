// The index depth: what each stop reads and makes, what enrich.py is told,
// and what the settings of an earlier version, which kept a level of search
// by meaning per library and the main folder's reader switches, come to.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const base = fs.mkdtempSync(path.join(os.tmpdir(), 'ixdepth-'))
process.env.INDEX_BASE = base
fs.writeFileSync(path.join(base, 'indexing.json'), JSON.stringify({
  libraries: { kb: { threads: 2, ocr: false, meaning: 'off' }, full: { meaning: 'full' }, old: { embeddings: false }, docs: { meaning: 'documents' } },
}))
const S = await import('../dist/indexSettings.js')

test('each stop adds to the one before', () => {
  assert.deepEqual(S.INDEX_DEPTHS, ['names', 'text', 'ocr', 'meaning-documents', 'meaning-full'])
  assert.deepEqual(S.INDEX_DEPTHS.map(d => { const p = S.depthParts(d); return [p.documents, p.ocr, p.meaning] }),
    [[false, false, 'off'], [true, false, 'off'], [true, true, 'off'], [true, true, 'documents'], [true, true, 'full']])
  assert.deepEqual(S.INDEX_DEPTHS.map(S.textArgs), [null, ['--no-ocr'], [], [], []])
})

test('an earlier version\'s settings come to the nearest stop', async () => {
  // OCR was off for the main folder, and every library followed it.
  assert.equal(S.indexSettings('kb').depth, 'text')
  assert.equal(S.indexSettings('kb').threads, 2)
  assert.equal(S.indexSettings('full').depth, 'meaning-full')
  assert.equal(S.indexSettings('docs').depth, 'meaning-documents')
  assert.equal(S.indexSettings('old').depth, 'text')
  assert.equal(S.indexSettings('added-later').depth, 'text')
  // Choosing the main folder's depth leaves the others as they were.
  await S.saveIndexSettings('kb', { depth: 'meaning-documents' })
  assert.equal(S.indexSettings('kb').depth, 'meaning-documents')
  assert.equal(S.indexSettings('old').depth, 'text')
  assert.equal(S.builtLibrarySettings('old').threads, 2, 'a built library follows the main folder\'s threads')
  assert.equal(S.builtLibrarySettings('old').ocr, false, 'and has its own depth')
  const saved = JSON.parse(fs.readFileSync(path.join(base, 'indexing.json'), 'utf8')).libraries.kb
  assert.equal(saved.depth, 'meaning-documents')
  assert.equal(saved.meaning, undefined, 'the depth stands for the level it replaces')
})
