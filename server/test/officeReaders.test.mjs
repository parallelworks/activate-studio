// The readers for the Office formats downmark does not read, run in Python
// with nothing else installed: OpenDocument through the standard library
// (files made here), .xls through xlrd and .ppt through olefile (small
// fixtures in testdata/office, written with LibreOffice from text made up
// for them). xlrd and olefile come from indexer/.deps, as CI installs them
// (fetch_embed.py --readers-only); without them these tests fail rather
// than skip. Then the readiness report Settings, Indexing shows.
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import zlib from 'node:zlib'
import { fileURLToPath } from 'node:url'

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const indexer = path.join(root, 'indexer')
const fixtures = path.join(root, 'testdata', 'office')
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'office-'))
const PY = process.env.PYTHON_BIN || 'python3'

/** Runs Python with enrich.py importable; the code's last expression is printed as JSON. */
const py = (code, input) => JSON.parse(execFileSync(PY, ['-B', '-c',
  `import sys, json; sys.path.insert(0, ${JSON.stringify(indexer)}); import enrich\nfrom pathlib import Path\n${code}`],
{ encoding: 'utf8', input }))
const read = (fn, file) => py(`print(json.dumps(enrich.${fn}(Path(${JSON.stringify(file)}))))`)

// A minimal OpenDocument package: the mimetype first and stored, then the parts.
const NS = 'xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" '
  + 'xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" '
  + 'xmlns:presentation="urn:oasis:names:tc:opendocument:xmlns:presentation:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" '
  + 'xmlns:dc="http://purl.org/dc/elements/1.1/" office:version="1.3"'
function odf(file, mimetype, body, styles = '') {
  const content = `<?xml version="1.0" encoding="UTF-8"?><office:document-content ${NS}><office:body>${body}</office:body></office:document-content>`
  const stylesXml = `<?xml version="1.0" encoding="UTF-8"?><office:document-styles ${NS}><office:master-styles>${styles}</office:master-styles></office:document-styles>`
  execFileSync(PY, ['-B', '-c', `import sys, zipfile
with zipfile.ZipFile(sys.argv[1], 'w') as z:
    z.writestr(zipfile.ZipInfo('mimetype'), sys.argv[2])
    z.writestr('content.xml', sys.argv[3], zipfile.ZIP_DEFLATED)
    z.writestr('styles.xml', sys.argv[4], zipfile.ZIP_DEFLATED)`, file, mimetype, content, stylesXml])
  return file
}

test('xlrd and olefile are importable where enrich.py looks', () => {
  assert.deepEqual(py(`print(json.dumps([enrich.native_reader_available(s) for s in ('.xls', '.ppt', '.odt', '.ods', '.odp', '.odg', '.doc')]))`),
    [true, true, true, true, true, true, false], 'install them with: python3 indexer/fetch_embed.py --deps indexer/.deps --readers-only')
})

test('a text document: headers and footers, headings, paragraphs with breaks and spaces, lists and tables; comments left out', () => {
  const file = odf(path.join(tmp, 'report.odt'), 'application/vnd.oasis.opendocument.text', `<office:text>
    <text:h text:outline-level="1">Inspection findings</text:h>
    <text:p>Cracks near the<text:s text:c="2"/>root<text:line-break/>after forty thousand cycles<office:annotation><dc:creator>Reviewer</dc:creator><text:p>Check this number</text:p></office:annotation>.</text:p>
    <text:list><text:list-item><text:p>Replace blade three</text:p></text:list-item><text:list-item><text:p>Tighten the finish, Ångström scale</text:p></text:list-item></text:list>
    <table:table table:name="T1"><table:table-row><table:table-cell><text:p>Blade</text:p></table:table-cell><table:table-cell><text:p>Status</text:p></table:table-cell></table:table-row>
    <table:table-row><table:table-cell><text:p>B3</text:p></table:table-cell><table:table-cell><text:p>replace</text:p></table:table-cell><table:table-cell/></table:table-row></table:table>
  </office:text>`, `<style:master-page style:name="Standard"><style:header><text:p>Quarterly header alpha</text:p></style:header><style:footer><text:p>Confidential footer beta</text:p></style:footer></style:master-page>`)
  const text = read('extract_odf', file)
  assert.deepEqual(text.split('\n'), [
    'Quarterly header alpha', 'Confidential footer beta', 'Inspection findings',
    'Cracks near the  root', 'after forty thousand cycles.',
    'Replace blade three', 'Tighten the finish, Ångström scale',
    'Blade\tStatus', 'B3\treplace',
  ])
  assert.doesNotMatch(text, /Reviewer|Check this number/)
})

test('a spreadsheet: each sheet under its name, rows of tab-separated cells as shown', () => {
  const file = odf(path.join(tmp, 'budget.ods'), 'application/vnd.oasis.opendocument.spreadsheet', `<office:spreadsheet>
    <table:table table:name="Budget"><table:table-row><table:table-cell office:value-type="string"><text:p>Item</text:p></table:table-cell><table:table-cell><text:p>Amount</text:p></table:table-cell></table:table-row>
    <table:table-row><table:table-cell><text:p>Travel</text:p></table:table-cell><table:table-cell office:value-type="float" office:value="1250.5"><text:p>1,250.50</text:p></table:table-cell><table:table-cell table:number-columns-repeated="1000"/></table:table-row>
    <table:table-row table:number-rows-repeated="1000"><table:table-cell table:number-columns-repeated="1000"/></table:table-row>
    <table:table-row><table:table-cell table:number-columns-spanned="2"><text:p>Merged banner</text:p></table:table-cell><table:covered-table-cell/></table:table-row></table:table>
    <table:table table:name="Inventory"><table:table-row><table:table-cell><text:p>flange</text:p></table:table-cell><table:table-cell office:value-type="float" office:value="7"><text:p>7</text:p></table:table-cell></table:table-row></table:table>
  </office:spreadsheet>`)
  assert.deepEqual(read('extract_odf', file).split('\n'),
    ['Sheet: Budget', 'Item\tAmount', 'Travel\t1,250.50', 'Merged banner', 'Sheet: Inventory', 'flange\t7'])
})

test('a presentation: each slide in turn, with its speaker notes', () => {
  const frame = t => `<draw:frame><draw:text-box><text:p>${t}</text:p></draw:text-box></draw:frame>`
  const file = odf(path.join(tmp, 'plan.odp'), 'application/vnd.oasis.opendocument.presentation', `<office:presentation>
    <draw:page draw:name="page1">${frame('Harbor plan')}${frame('Sound the channel at low tide')}<presentation:notes>${frame('Bring the echo sounder')}</presentation:notes></draw:page>
    <draw:page draw:name="Crew">${frame('Skipper Åsa')}</draw:page>
  </office:presentation>`)
  assert.deepEqual(read('extract_odf', file).split('\n'),
    ['Slide 1', 'Harbor plan', 'Sound the channel at low tide', 'Notes:', 'Bring the echo sounder', 'Slide 2: Crew', 'Skipper Åsa'])
})

test('an Excel 97-2003 workbook: sheets, numbers as shown, dates as dates, merged cells, non-ASCII text', () => {
  const text = read('extract_xls', path.join(fixtures, 'budget.xls'))
  const lines = text.split('\n')
  for (const want of ['Sheet: Budget 2026', 'Item\tAmount\tDate\tNote', 'Travel\t1250.5\t2026-03-14\tKonferenz in München',
    'Total\t9960.5\t\tcomputed', 'Merged banner delta', 'Sheet: Inventory', 'gasket\t14\tBin-02', 'Sheet: Notes Ünïcode',
    'Observations: 风速 high in sector epsilon']) {
    assert.ok(lines.includes(want), `missing ${JSON.stringify(want)} in:\n${text}`)
  }
})

test('a PowerPoint 97-2003 file: slide text, text boxes and speaker notes, without the master\'s prompts', () => {
  const file = path.join(tmp, 'harbor-survey.ppt')
  fs.writeFileSync(file, zlib.gunzipSync(fs.readFileSync(path.join(fixtures, 'harbor-survey.ppt.gz'))))
  const text = read('extract_ppt', file)
  assert.deepEqual(text.split('\n'), ['Harbor Survey Plan', 'Sound the channel at low tide — Tiefenmessung', 'Crew',
    'Skipper Åsa, deckhand Joaquín', 'Speaker note: bring the echo sounder'])
  assert.doesNotMatch(text, /Click to edit/)
})

test('a damaged or mislabeled file reads as nothing, and the dispatcher does not raise', () => {
  const junk = path.join(tmp, 'junk')
  fs.writeFileSync(junk, Buffer.from('not an office file at all, just bytes'))
  for (const ext of ['.xls', '.ppt', '.odt', '.ods', '.odp']) fs.copyFileSync(junk, junk + ext)
  const out = py(`print(json.dumps([enrich.extract(Path(${JSON.stringify(junk)} + e)) for e in ('.xls', '.ppt', '.odt', '.ods', '.odp')]))`)
  assert.deepEqual(out, ['', '', '', '', ''])
  // An Office lock file beside a document is never read, whichever reader its suffix names.
  for (const name of ['~$budget.xls', '~$plan.ppt', '.~lock.report.odt#']) fs.copyFileSync(path.join(fixtures, 'budget.xls'), path.join(tmp, name))
  const locks = py(`print(json.dumps([enrich.extract(Path(${JSON.stringify(tmp)}) / n) for n in ('~$budget.xls', '~$plan.ppt', '.~lock.report.odt#')]))`)
  assert.deepEqual(locks, [null, null, null])
})

test('the readiness report: these formats are read without LibreOffice, and each missing tool has its command here', async () => {
  process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'office-ix-'))
  const { extractorReport, installInfo } = await import('../dist/extractors.js')
  const r = extractorReport({ documents: true, ocr: true, legacyOffice: false, pdfReader: 'auto', vision: false })
  const row = f => r.formats.find(x => x.formats === f)
  assert.deepEqual([row('.odt, .ods, .odp').ok, row('.xls').readers, row('.ppt').readers], [true, ['xlrd'], ['olefile']])
  assert.ok(r.formats.every(f => !/until LibreOffice is installed/.test(f.note)))
  assert.ok(!r.missing.some(m => /\.xls|\.ppt|\.odt/.test(m.formats)))
  assert.ok(r.install && Array.isArray(r.install.items))

  const none = { tesseract: false, poppler: false, soffice: false, xlrd: true, olefile: true }
  const cmds = i => i.items.map(x => [x.name, x.command, x.link])
  assert.deepEqual(cmds(installInfo(none, '/opt/homebrew/bin/brew', false, 'darwin')), [
    ['tesseract', 'brew install tesseract', null], ['poppler', 'brew install poppler', null], ['LibreOffice', 'brew install --cask libreoffice', null],
  ])
  assert.deepEqual(cmds(installInfo(none, null, false, 'darwin')), [
    ['tesseract', null, 'https://brew.sh'], ['poppler', null, 'https://brew.sh'], ['LibreOffice', null, 'https://brew.sh'],
  ])
  assert.deepEqual(cmds(installInfo(none, null, true, 'linux')), [
    ['tesseract', 'sudo apt-get install tesseract-ocr', null], ['poppler', 'sudo apt-get install poppler-utils', null],
    ['LibreOffice', 'sudo apt-get install libreoffice', null],
  ])
  assert.deepEqual(cmds(installInfo({ ...none, tesseract: true, poppler: true, soffice: true }, null, false, 'linux')), [])
  assert.deepEqual(cmds(installInfo({ tesseract: true, poppler: true, soffice: true, xlrd: false, olefile: true }, null, false, 'linux')),
    [['xlrd and olefile', 'python3 indexer/fetch_embed.py --deps indexer/.deps --readers-only', null]])
  assert.match(installInfo(none, null, true, 'linux').items[0].adds, /OCR reads scanned PDFs and text in images/)
})
