// What embed.py embeds at each level of search by meaning: Full takes up to
// twelve passages of every file with text but markup; Documents (the
// --documents-only and --max-chunks 4 the Studio passes) takes the first
// four passages of documents and images only. Runs embed.py's own functions
// in Python, with no model and no index.
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const indexer = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'indexer')
const py = code => JSON.parse(execFileSync(process.env.PYTHON_BIN || 'python3', ['-c', `import sys, json; sys.path.insert(0, ${JSON.stringify(indexer)}); import embed; print(json.dumps(${code}))`], { encoding: 'utf8' }))

test('Documents embeds documents and images, Full every file with text but markup', () => {
  const files = ['report.PDF', 'notes.md', 'slides.pptx', 'photo.jpg', 'model.py', 'data.parquet', 'table.xlsx', 'page.html', 'data.csv']
  const full = py(`[embed.embeds_file(f, False) for f in ${JSON.stringify(files)}]`)
  const docs = py(`[embed.embeds_file(f, True) for f in ${JSON.stringify(files)}]`)
  assert.deepEqual(Object.fromEntries(files.map((f, i) => [f, full[i]])),
    { 'report.PDF': true, 'notes.md': true, 'slides.pptx': true, 'photo.jpg': true, 'model.py': true, 'data.parquet': true, 'table.xlsx': true, 'page.html': false, 'data.csv': false })
  assert.deepEqual(Object.fromEntries(files.map((f, i) => [f, docs[i]])),
    { 'report.PDF': true, 'notes.md': true, 'slides.pptx': true, 'photo.jpg': true, 'model.py': false, 'data.parquet': false, 'table.xlsx': false, 'page.html': false, 'data.csv': false })
})

test('a file gives at most the passages asked for, from its start', () => {
  const text = 'The turbine blade study measured fatigue under load. '.repeat(400)
  assert.equal(py(`len(embed.chunk(${JSON.stringify(text)}))`), 12)
  assert.equal(py(`len(embed.chunk(${JSON.stringify(text)}, 4))`), 4)
  assert.deepEqual(py(`embed.chunk(${JSON.stringify(text)}, 4)`), py(`embed.chunk(${JSON.stringify(text)})[:4]`))
})
