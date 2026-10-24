// Word, PowerPoint and Excel files, and their OpenDocument counterparts,
// previewed without LibreOffice: slides with their titles, text, tables and
// pictures in order; a document's headings, paragraphs, lists, tables and
// pictures; a workbook's sheets. Pictures are served from the package, and
// nothing else in it. Files are made here with Python's zipfile, deflated.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'op-kb-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'op-ix-'))
// No LibreOffice here, whatever the host has.
process.env.SOFFICE_BIN = path.join(KB, 'no-soffice')

const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='
const P = 'xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"'
const rel = (id, target) => `<Relationship Id="${id}" Type="t" Target="${target}"/>`
const rels = (...r) => `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${r.join('')}</Relationships>`
const sp = (body, ph = '') => `<p:sp><p:nvSpPr><p:cNvPr id="1" name="s"/><p:cNvSpPr/><p:nvPr>${ph}</p:nvPr></p:nvSpPr><p:txBody>${body}</p:txBody></p:sp>`
const para = (t, lvl) => `<a:p>${lvl ? `<a:pPr lvl="${lvl}"/>` : ''}<a:r><a:t>${t}</a:t></a:r></a:p>`

const files = {
  'deck.pptx': {
    'ppt/presentation.xml': `<p:presentation ${P}><p:sldIdLst><p:sldId id="257" r:id="rId3"/><p:sldId id="256" r:id="rId2"/></p:sldIdLst></p:presentation>`,
    'ppt/_rels/presentation.xml.rels': rels(rel('rId2', 'slides/slide1.xml'), rel('rId3', 'slides/slide2.xml')),
    // Listed second in the deck's order, though its file is slide1.
    'ppt/slides/slide1.xml': `<p:sld ${P}><p:cSld><p:spTree>${sp(para('Second slide'), '<p:ph type="title"/>')}${sp(para('Tom &amp; Jerry'))}</p:spTree></p:cSld></p:sld>`,
    'ppt/slides/slide2.xml': `<p:sld ${P}><p:cSld><p:spTree>${sp(para('Opening'), '<p:ph type="ctrTitle"/>')}${sp(para('Point one') + para('Detail', 1))}<p:grpSp>${sp(para('In a group'))}</p:grpSp><p:graphicFrame><a:graphic><a:graphicData><a:tbl><a:tr><a:tc><a:txBody>${para('h1')}</a:txBody></a:tc><a:tc><a:txBody>${para('h2')}</a:txBody></a:tc></a:tr></a:tbl></a:graphicData></a:graphic></p:graphicFrame><p:pic><p:blipFill><a:blip r:embed="rId9"/></p:blipFill></p:pic><p:pic><p:blipFill><a:blip r:embed="rId8"/></p:blipFill></p:pic></p:spTree></p:cSld></p:sld>`,
    'ppt/slides/_rels/slide2.xml.rels': rels(rel('rId9', '../media/image1.png'), rel('rId8', '../media/image2.emf')),
    'ppt/media/image1.png': { b64: PNG },
    'ppt/media/image2.emf': 'not shown',
  },
  'report.docx': {
    'word/document.xml': `<w:document xmlns:w="w" xmlns:a="a" xmlns:r="r"><w:body>
      <w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr><w:r><w:t>Annual report</w:t></w:r></w:p>
      <w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Results</w:t></w:r></w:p>
      <w:p><w:r><w:t xml:space="preserve">Revenue </w:t></w:r><w:r><w:t>grew.</w:t></w:r></w:p>
      <w:p><w:pPr><w:numPr><w:ilvl w:val="1"/></w:numPr></w:pPr><w:r><w:t>A bullet</w:t></w:r></w:p>
      <w:tbl><w:tr><w:tc><w:p><w:r><w:t>a</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>b</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
      <w:p><w:r><w:drawing><a:blip r:embed="rIdImg"/></w:drawing></w:r></w:p>
    </w:body></w:document>`,
    'word/_rels/document.xml.rels': rels(rel('rIdImg', 'media/chart.png')),
    'word/media/chart.png': { b64: PNG },
  },
  'book.xlsx': {
    'xl/workbook.xml': '<workbook xmlns:r="r"><sheets><sheet name="Budget" r:id="rId1"/><sheet name="Notes" r:id="rId2"/></sheets></workbook>',
    'xl/_rels/workbook.xml.rels': rels(rel('rId1', 'worksheets/sheet1.xml'), rel('rId2', 'worksheets/sheet2.xml')),
    'xl/sharedStrings.xml': '<sst><si><t>Item</t></si><si><r><t>Co</t></r><r><t>st</t></r></si><si><t>Paper</t></si></sst>',
    'xl/worksheets/sheet1.xml': '<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="C2"><v>12.5</v></c><c r="D2" t="b"><v>1</v></c></row></sheetData></worksheet>',
    'xl/worksheets/sheet2.xml': '<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Free text</t></is></c></row></sheetData></worksheet>',
  },
  'talk.odp': {
    'content.xml': '<office:document-content xmlns:office="o" xmlns:draw="d" xmlns:text="t" xmlns:presentation="p" xmlns:xlink="x"><office:body><office:presentation><draw:page draw:name="p1"><draw:frame presentation:class="title"><draw:text-box><text:p>ODP title</text:p></draw:text-box></draw:frame><draw:frame><draw:text-box><text:p>Body<text:s text:c="2"/>text</text:p></draw:text-box></draw:frame><draw:frame><draw:image xlink:href="Pictures/p.png"/></draw:frame></draw:page></office:presentation></office:body></office:document-content>',
    'Pictures/p.png': { b64: PNG },
  },
  'paper.odt': {
    'content.xml': '<office:document-content xmlns:office="o" xmlns:text="t" xmlns:table="tb"><office:body><office:text><text:h text:outline-level="1">Intro</text:h><text:p>First<text:tab/>para</text:p><text:list><text:list-item><text:p>Item one</text:p></text:list-item></text:list><table:table><table:table-row><table:table-cell><text:p>c1</text:p></table:table-cell><table:table-cell table:number-columns-repeated="2"><text:p>r</text:p></table:table-cell></table:table-row></table:table></office:text></office:body></office:document-content>',
  },
}

execFileSync('python3', ['-c', `
import base64, json, sys, zipfile
spec = json.loads(sys.stdin.read())
for name, members in spec['files'].items():
    with zipfile.ZipFile(spec['root'] + '/' + name, 'w', zipfile.ZIP_DEFLATED) as z:
        for member, body in members.items():
            z.writestr(member, base64.b64decode(body['b64']) if isinstance(body, dict) else body)
`], { input: JSON.stringify({ root: KB, files }) })
fs.writeFileSync(path.join(KB, 'old.ppt'), 'binary 97-2003 file')
fs.writeFileSync(path.join(KB, 'broken.pptx'), 'not a zip at all')

const { default: Fastify } = await import('fastify')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.ready()
test.after(async () => { await app.close() })
const get = async url => { const r = await app.inject({ url }); return { status: r.statusCode, body: r.headers['content-type']?.startsWith('application/json') ? r.json() : r.rawPayload, type: r.headers['content-type'] } }

test('a deck: its slides in the deck\'s order, titles, text by level, a grouped shape, a table and its displayable pictures', async () => {
  const r = await get('/api/kb/office-preview?path=deck.pptx')
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.kind, 'slides')
  const [one, two] = r.body.slides
  assert.equal(one.title, 'Opening')
  assert.deepEqual(one.blocks, [
    { t: 'p', text: 'Point one', level: 0 },
    { t: 'p', text: 'Detail', level: 1 },
    { t: 'p', text: 'In a group', level: 0 },
    { t: 'table', rows: [['h1', 'h2']] },
    { t: 'img', media: 'ppt/media/image1.png' },
  ])
  assert.equal(two.title, 'Second slide')
  assert.deepEqual(two.blocks, [{ t: 'p', text: 'Tom & Jerry', level: 0 }])
})

test('a picture is served from the package, and nothing that is not a picture in it', async () => {
  const img = await get('/api/kb/office-media?path=deck.pptx&name=ppt/media/image1.png')
  assert.equal(img.status, 200)
  assert.equal(img.type, 'image/png')
  assert.deepEqual(img.body.subarray(0, 4), Buffer.from([0x89, 0x50, 0x4e, 0x47]))
  for (const name of ['ppt/slides/slide1.xml', 'ppt/media/image2.emf', '../../etc/passwd', 'ppt/media/missing.png']) {
    const r = await get(`/api/kb/office-media?path=deck.pptx&name=${encodeURIComponent(name)}`)
    assert.ok(r.status === 400 || r.status === 404, `${name}: ${r.status}`)
  }
})

test('a Word document: headings, runs joined, a list item, a table and a picture', async () => {
  const r = await get('/api/kb/office-preview?path=report.docx')
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.deepEqual(r.body.blocks, [
    { t: 'h', level: 1, text: 'Annual report' },
    { t: 'h', level: 2, text: 'Results' },
    { t: 'p', text: 'Revenue grew.' },
    { t: 'p', text: 'A bullet', list: true, level: 1 },
    { t: 'table', rows: [['a', 'b']] },
    { t: 'img', media: 'word/media/chart.png' },
  ])
})

test('a workbook: each sheet by name, shared and inline strings, gaps kept, booleans', async () => {
  const r = await get('/api/kb/office-preview?path=book.xlsx')
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.deepEqual(r.body.sheets, [
    { name: 'Budget', rows: [['Item', 'Cost'], ['Paper', '', '12.5', 'TRUE']], truncated: false },
    { name: 'Notes', rows: [['Free text']], truncated: false },
  ])
})

test('OpenDocument slides and text', async () => {
  const odp = await get('/api/kb/office-preview?path=talk.odp')
  assert.equal(odp.status, 200, JSON.stringify(odp.body))
  assert.deepEqual(odp.body.slides, [{ n: 1, title: 'ODP title', blocks: [{ t: 'p', text: 'Body  text' }, { t: 'img', media: 'Pictures/p.png' }] }])
  const odt = await get('/api/kb/office-preview?path=paper.odt')
  assert.deepEqual(odt.body.blocks, [
    { t: 'h', level: 1, text: 'Intro' },
    { t: 'p', text: 'First\tpara' },
    { t: 'p', text: 'Item one', list: true },
    { t: 'table', rows: [['c1', 'r', 'r']] },
  ])
})

test('a 97-2003 file and a file that is not a package are refused, with a reason', async () => {
  const old = await get('/api/kb/office-preview?path=old.ppt')
  assert.equal(old.status, 415)
  const broken = await get('/api/kb/office-preview?path=broken.pptx')
  assert.equal(broken.status, 422)
  assert.match(broken.body.error ?? broken.body.message ?? '', /not a readable Office package/)
})

test('without LibreOffice the page images are refused with the reason, which sends the viewer to the outline', async () => {
  const r = await get('/api/kb/pdf-info?path=deck.pptx')
  assert.equal(r.status, 415)
  assert.match(r.body.error ?? r.body.message ?? '', /LibreOffice/)
})
