import fs from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'
import { KbError } from './kb.js'

/**
 * A readable preview of Word, PowerPoint and Excel files, and their
 * OpenDocument counterparts, without LibreOffice: the text of each slide,
 * paragraph, table and sheet, and the pictures the file carries, read
 * straight from the zipped XML. Page images (preview.ts) need LibreOffice
 * and poppler; where they are missing, as in the macOS app, this is what
 * the viewer shows. The 97-2003 binary formats (.ppt, .doc, .xls) are not
 * read here; their indexed text is what the viewer has for them.
 *
 * Only the members a preview needs are inflated, each under a size cap, so
 * a large deck costs its XML and not its media.
 */

export const OUTLINE_SUFFIXES = new Set(['.pptx', '.docx', '.xlsx', '.odp', '.odt', '.ods'])

/** Pictures a browser shows in an <img>. EMF, WMF and TIFF are left out. */
const IMAGE_TYPES: Record<string, string> = {
  '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.gif': 'image/gif',
  '.webp': 'image/webp', '.bmp': 'image/bmp',
}

const MAX_FILE_BYTES = 512 * 1024 * 1024
const MAX_XML_BYTES = 64 * 1024 * 1024
const MAX_MEDIA_BYTES = 32 * 1024 * 1024
const MAX_SLIDES = 300
const MAX_BLOCKS = 5000
const MAX_SHEETS = 12
const MAX_ROWS = 200
const MAX_COLS = 30

export type Block =
  | { t: 'h'; level: number; text: string }
  | { t: 'p'; text: string; level?: number; list?: boolean }
  | { t: 'table'; rows: string[][] }
  | { t: 'img'; media: string }

export interface Slide { n: number; title: string; blocks: Block[] }
export interface Sheet { name: string; rows: string[][]; truncated: boolean }

export type Outline =
  | { kind: 'slides'; slides: Slide[]; truncated: boolean }
  | { kind: 'document'; blocks: Block[]; truncated: boolean }
  | { kind: 'sheets'; sheets: Sheet[]; truncated: boolean }

/* ---- zip: the central directory, and one member at a time ---- */

interface ZipEntry { name: string; method: number; csize: number; usize: number; offset: number; encrypted: boolean }

export class Zip {
  private constructor(private readonly fd: number, readonly entries: Map<string, ZipEntry>) {}

  static open(file: string): Zip {
    const fd = fs.openSync(file, 'r')
    try {
      const size = fs.fstatSync(fd).size
      if (size > MAX_FILE_BYTES) throw new KbError(413, 'This file is too large to preview here; use Download.')
      const tailLen = Math.min(size, 65_557)
      const tail = Buffer.alloc(tailLen)
      fs.readSync(fd, tail, 0, tailLen, size - tailLen)
      let eocd = -1
      for (let i = tailLen - 22; i >= 0; i--) {
        if (tail.readUInt32LE(i) === 0x06054b50) { eocd = i; break }
      }
      if (eocd < 0) throw new KbError(422, 'This file is not a readable Office package.')
      const count = tail.readUInt16LE(eocd + 10)
      const cdSize = tail.readUInt32LE(eocd + 12)
      const cdOffset = tail.readUInt32LE(eocd + 16)
      if (cdSize === 0xffffffff || cdOffset === 0xffffffff) throw new KbError(413, 'This file is too large to preview here; use Download.')
      const cd = Buffer.alloc(cdSize)
      fs.readSync(fd, cd, 0, cdSize, cdOffset)
      const entries = new Map<string, ZipEntry>()
      let p = 0
      for (let i = 0; i < count && p + 46 <= cd.length; i++) {
        if (cd.readUInt32LE(p) !== 0x02014b50) break
        const flags = cd.readUInt16LE(p + 8)
        const nameLen = cd.readUInt16LE(p + 28)
        const extraLen = cd.readUInt16LE(p + 30)
        const commentLen = cd.readUInt16LE(p + 32)
        const name = cd.toString(flags & 0x800 ? 'utf8' : 'latin1', p + 46, p + 46 + nameLen)
        entries.set(name, {
          name, method: cd.readUInt16LE(p + 10), csize: cd.readUInt32LE(p + 20), usize: cd.readUInt32LE(p + 24),
          offset: cd.readUInt32LE(p + 42), encrypted: (flags & 1) === 1,
        })
        p += 46 + nameLen + extraLen + commentLen
      }
      return new Zip(fd, entries)
    } catch (e) {
      fs.closeSync(fd)
      throw e
    }
  }

  /** A member's bytes, or null when it is missing, encrypted, of a method
   *  this does not read, or larger than `max`. */
  read(name: string, max = MAX_XML_BYTES): Buffer | null {
    const e = this.entries.get(name)
    if (!e || e.encrypted || e.usize > max || (e.method !== 0 && e.method !== 8)) return null
    const head = Buffer.alloc(30)
    fs.readSync(this.fd, head, 0, 30, e.offset)
    if (head.readUInt32LE(0) !== 0x04034b50) return null
    const start = e.offset + 30 + head.readUInt16LE(26) + head.readUInt16LE(28)
    const raw = Buffer.alloc(e.csize)
    fs.readSync(this.fd, raw, 0, e.csize, start)
    if (e.method === 0) return raw
    try { return zlib.inflateRawSync(raw, { maxOutputLength: max }) } catch { return null }
  }

  text(name: string): string | null {
    const b = this.read(name)
    return b ? b.toString('utf8') : null
  }

  close(): void { try { fs.closeSync(this.fd) } catch { /* already closed */ } }
}

/* ---- XML: a small tree, enough for these documents ---- */

export interface El { name: string; attrs: Record<string, string>; kids: (El | string)[] }

const ENTITY: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'" }
function decode(s: string): string {
  return s.replace(/&(#x[0-9a-fA-F]+|#\d+|\w+);/g, (m, e: string) => {
    if (e[0] === '#') {
      const code = e[1] === 'x' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10)
      return Number.isFinite(code) && code >= 0 && code <= 0x10ffff ? String.fromCodePoint(code) : m
    }
    return ENTITY[e] ?? m
  })
}

const TOKEN = /<!--[\s\S]*?-->|<!\[CDATA\[([\s\S]*?)\]\]>|<\?[\s\S]*?\?>|<!(?:[^>"']|"[^"]*"|'[^']*')*>|<(\/?)([A-Za-z_][\w:.-]*)((?:\s+[^\s=/>]+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*(\/?)>|([^<]+)/g
const ATTR = /([^\s=/>]+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g

export function parseXml(xml: string): El {
  const root: El = { name: '#root', attrs: {}, kids: [] }
  const stack: El[] = [root]
  TOKEN.lastIndex = 0
  for (let m = TOKEN.exec(xml); m; m = TOKEN.exec(xml)) {
    const top = stack[stack.length - 1]
    if (m[1] !== undefined) { top.kids.push(m[1]); continue }
    if (m[6] !== undefined) { top.kids.push(decode(m[6])); continue }
    if (!m[3]) continue
    if (m[2] === '/') {
      // Close the nearest open element of that name; tolerate stray closes.
      for (let i = stack.length - 1; i > 0; i--) {
        if (stack[i].name === m[3]) { stack.length = i; break }
      }
      continue
    }
    const attrs: Record<string, string> = {}
    ATTR.lastIndex = 0
    for (let a = ATTR.exec(m[4] ?? ''); a; a = ATTR.exec(m[4] ?? '')) attrs[a[1]] = decode(a[2] ?? a[3] ?? '')
    const el: El = { name: m[3], attrs, kids: [] }
    top.kids.push(el)
    if (m[5] !== '/') stack.push(el)
  }
  return root
}

const isEl = (k: El | string): k is El => typeof k !== 'string'
const kidsOf = (el: El) => el.kids.filter(isEl)
/** Every descendant named `name`, in document order. */
function* all(el: El, name: string): Generator<El> {
  for (const k of el.kids) {
    if (!isEl(k)) continue
    if (k.name === name) yield k
    yield* all(k, name)
  }
}
const first = (el: El, name: string): El | undefined => all(el, name).next().value

/* ---- package relationships: an id to the member it points at ---- */

function relsOf(zip: Zip, part: string): Map<string, string> {
  const dir = path.posix.dirname(part)
  const xml = zip.text(path.posix.join(dir, '_rels', `${path.posix.basename(part)}.rels`))
  const out = new Map<string, string>()
  if (!xml) return out
  for (const r of all(parseXml(xml), 'Relationship')) {
    if (r.attrs.TargetMode === 'External') continue
    const target = r.attrs.Target ?? ''
    out.set(r.attrs.Id, target.startsWith('/') ? target.slice(1) : path.posix.normalize(path.posix.join(dir, target)))
  }
  return out
}

const displayable = (zip: Zip, member: string | undefined): member is string =>
  !!member && zip.entries.has(member) && (path.posix.extname(member).toLowerCase() in IMAGE_TYPES)

/* ---- DrawingML text (PowerPoint shapes and tables) ---- */

/** A paragraph's text: runs, tabs and line breaks. */
function drawingText(p: El): string {
  let out = ''
  const visit = (el: El) => {
    for (const k of el.kids) {
      if (!isEl(k)) continue
      if (k.name === 'a:t') out += k.kids.filter(x => !isEl(x)).join('')
      else if (k.name === 'a:br') out += '\n'
      else if (k.name === 'a:tab') out += '\t'
      else visit(k)
    }
  }
  visit(p)
  return out
}

function drawingTable(tbl: El): string[][] {
  return [...all(tbl, 'a:tr')].map(tr => kidsOf(tr).filter(k => k.name === 'a:tc').map(tc => [...all(tc, 'a:p')].map(drawingText).join('\n').trim()))
}

/* ---- PowerPoint ---- */

function pptxSlides(zip: Zip): Outline {
  const pres = zip.text('ppt/presentation.xml')
  const presRels = relsOf(zip, 'ppt/presentation.xml')
  let parts = pres ? [...all(parseXml(pres), 'p:sldId')].map(s => presRels.get(s.attrs['r:id'])).filter((x): x is string => !!x) : []
  if (!parts.length) {
    parts = [...zip.entries.keys()].filter(n => /^ppt\/slides\/slide\d+\.xml$/.test(n))
      .sort((a, b) => Number(a.match(/\d+/)![0]) - Number(b.match(/\d+/)![0]))
  }
  const slides: Slide[] = []
  for (const [i, part] of parts.slice(0, MAX_SLIDES).entries()) {
    const xml = zip.text(part)
    if (!xml) continue
    const doc = parseXml(xml)
    const rels = relsOf(zip, part)
    let title = ''
    const blocks: Block[] = []
    const visit = (el: El) => {
      for (const k of kidsOf(el)) {
        if (k.name === 'p:sp') {
          const ph = first(k, 'p:ph')
          const isTitle = !!ph && /^(title|ctrTitle)$/.test(ph.attrs.type ?? '')
          const paras = [...all(k, 'a:p')].map(p => ({ text: drawingText(p).trim(), level: Number(first(p, 'a:pPr')?.attrs.lvl ?? 0) || 0 }))
            .filter(p => p.text)
          if (isTitle && !title) title = paras.map(p => p.text).join(' ')
          else for (const p of paras) blocks.push({ t: 'p', text: p.text, level: p.level })
        } else if (k.name === 'p:graphicFrame') {
          const tbl = first(k, 'a:tbl')
          if (tbl) blocks.push({ t: 'table', rows: drawingTable(tbl) })
        } else if (k.name === 'p:pic') {
          const media = rels.get(first(k, 'a:blip')?.attrs['r:embed'] ?? '')
          if (displayable(zip, media)) blocks.push({ t: 'img', media })
        } else {
          visit(k)
        }
      }
    }
    const tree = first(doc, 'p:spTree')
    if (tree) visit(tree)
    slides.push({ n: i + 1, title, blocks })
  }
  return { kind: 'slides', slides, truncated: parts.length > MAX_SLIDES }
}

/* ---- Word ---- */

function headingLevel(style: string): number {
  if (/^title$/i.test(style)) return 1
  if (/^subtitle$/i.test(style)) return 2
  const m = style.match(/heading\s*(\d)/i)
  return m ? Math.min(6, Math.max(1, Number(m[1]))) : 0
}

function docxBlocks(zip: Zip): Outline {
  const xml = zip.text('word/document.xml')
  if (!xml) throw new KbError(422, 'This file is not a readable Word document.')
  const rels = relsOf(zip, 'word/document.xml')
  const body = first(parseXml(xml), 'w:body')
  const blocks: Block[] = []
  const paraText = (p: El, images: string[]): string => {
    let out = ''
    const visit = (el: El) => {
      for (const k of kidsOf(el)) {
        if (k.name === 'w:t') out += k.kids.filter(x => !isEl(x)).join('')
        else if (k.name === 'w:tab') out += '\t'
        else if (k.name === 'w:br' || k.name === 'w:cr') out += '\n'
        else if (k.name === 'w:pPr' || k.name === 'w:rPr' || k.name === 'w:instrText') continue
        else if (k.name === 'a:blip' || k.name === 'v:imagedata') {
          const media = rels.get(k.attrs['r:embed'] ?? k.attrs['r:id'] ?? '')
          if (displayable(zip, media)) images.push(media)
        } else visit(k)
      }
    }
    visit(p)
    return out
  }
  const visit = (el: El) => {
    for (const k of kidsOf(el)) {
      if (blocks.length >= MAX_BLOCKS) return
      if (k.name === 'w:p') {
        const images: string[] = []
        const text = paraText(k, images).trim()
        const pPr = first(k, 'w:pPr')
        const style = pPr ? first(pPr, 'w:pStyle')?.attrs['w:val'] ?? '' : ''
        const numPr = pPr ? first(pPr, 'w:numPr') : undefined
        const level = headingLevel(style)
        if (text) {
          if (level) blocks.push({ t: 'h', level, text })
          else if (numPr) blocks.push({ t: 'p', text, list: true, level: Number(first(numPr, 'w:ilvl')?.attrs['w:val'] ?? 0) || 0 })
          else blocks.push({ t: 'p', text })
        }
        for (const media of images) blocks.push({ t: 'img', media })
      } else if (k.name === 'w:tbl') {
        const rows = kidsOf(k).filter(r => r.name === 'w:tr').map(tr =>
          kidsOf(tr).filter(c => c.name === 'w:tc').map(tc => [...all(tc, 'w:p')].map(p => paraText(p, [])).join('\n').trim()))
        blocks.push({ t: 'table', rows })
      } else {
        visit(k)
      }
    }
  }
  if (body) visit(body)
  return { kind: 'document', blocks, truncated: blocks.length >= MAX_BLOCKS }
}

/* ---- Excel ---- */

function colIndex(ref: string): number {
  const letters = ref.match(/^[A-Z]+/)?.[0] ?? ''
  let n = 0
  for (const ch of letters) n = n * 26 + (ch.charCodeAt(0) - 64)
  return n - 1
}

const trimRows = (rows: string[][]) => rows.map(r => { let e = r.length; while (e > 0 && !r[e - 1]) e--; return r.slice(0, e) })

function xlsxSheets(zip: Zip): Outline {
  const wb = zip.text('xl/workbook.xml')
  if (!wb) throw new KbError(422, 'This file is not a readable Excel workbook.')
  const rels = relsOf(zip, 'xl/workbook.xml')
  const sstXml = zip.text('xl/sharedStrings.xml')
  const shared = sstXml ? [...all(parseXml(sstXml), 'si')].map(si => [...all(si, 't')].map(t => t.kids.filter(x => !isEl(x)).join('')).join('')) : []
  const defs = [...all(parseXml(wb), 'sheet')]
  const sheets: Sheet[] = []
  for (const s of defs.slice(0, MAX_SHEETS)) {
    const part = rels.get(s.attrs['r:id'])
    const xml = part ? zip.text(part) : null
    if (!xml) continue
    // Only the first rows are wanted: cut the XML after them rather than
    // parse a sheet of a million rows.
    let cut = xml
    let truncated = false
    let at = -1
    for (let i = 0; i <= MAX_ROWS; i++) {
      at = xml.indexOf('</row>', at + 1)
      if (at < 0) break
      if (i === MAX_ROWS - 1) { cut = xml.slice(0, at + 6); truncated = xml.indexOf('<row', at) >= 0; break }
    }
    const rows: string[][] = []
    for (const r of all(parseXml(cut), 'row')) {
      if (rows.length >= MAX_ROWS) { truncated = true; break }
      const row: string[] = []
      for (const c of kidsOf(r).filter(k => k.name === 'c')) {
        const col = c.attrs.r ? colIndex(c.attrs.r) : row.length
        if (col >= MAX_COLS) continue
        const v = first(c, 'v')?.kids.filter(x => !isEl(x)).join('') ?? ''
        const type = c.attrs.t ?? ''
        const value = type === 's' ? shared[Number(v)] ?? ''
          : type === 'inlineStr' ? [...all(c, 't')].map(t => t.kids.filter(x => !isEl(x)).join('')).join('')
            : type === 'b' ? (v === '1' ? 'TRUE' : 'FALSE') : v
        while (row.length < col) row.push('')
        row[col] = value
      }
      rows.push(row)
    }
    sheets.push({ name: s.attrs.name ?? `Sheet ${sheets.length + 1}`, rows: trimRows(rows), truncated })
  }
  return { kind: 'sheets', sheets, truncated: defs.length > MAX_SHEETS }
}

/* ---- OpenDocument ---- */

function odfText(el: El): string {
  let out = ''
  for (const k of el.kids) {
    if (!isEl(k)) { out += k; continue }
    if (k.name === 'text:s') out += ' '.repeat(Math.min(Number(k.attrs['text:c'] ?? 1) || 1, 64))
    else if (k.name === 'text:tab') out += '\t'
    else if (k.name === 'text:line-break') out += '\n'
    else if (k.name === 'office:annotation' || k.name === 'text:note') continue
    else out += odfText(k)
  }
  return out
}

function odfTable(t: El): string[][] {
  const rows: string[][] = []
  for (const r of all(t, 'table:table-row')) {
    if (rows.length >= MAX_ROWS) break
    const reps = Math.min(Number(r.attrs['table:number-rows-repeated'] ?? 1) || 1, MAX_ROWS - rows.length)
    const row: string[] = []
    for (const c of kidsOf(r).filter(k => k.name === 'table:table-cell' || k.name === 'table:covered-table-cell')) {
      const n = Number(c.attrs['table:number-columns-repeated'] ?? 1) || 1
      const v = kidsOf(c).filter(k => k.name === 'text:p' || k.name === 'text:h').map(odfText).join('\n').trim()
      for (let i = 0; i < n && row.length < MAX_COLS; i++) row.push(v)
    }
    const trimmed = trimRows([row])[0]
    // A run of repeated empty rows is the sheet's unused tail.
    if (!trimmed.length && reps > 1) continue
    for (let i = 0; i < reps; i++) rows.push(trimmed)
  }
  while (rows.length && !rows[rows.length - 1].length) rows.pop()
  return rows
}

function odfBlocks(zip: Zip, container: El, blocks: Block[]): void {
  for (const k of kidsOf(container)) {
    if (blocks.length >= MAX_BLOCKS) return
    if (k.name === 'text:h') {
      const text = odfText(k).trim()
      if (text) blocks.push({ t: 'h', level: Math.min(6, Number(k.attrs['text:outline-level'] ?? 1) || 1), text })
    } else if (k.name === 'text:p') {
      const text = odfText(k).trim()
      if (text) blocks.push({ t: 'p', text })
      for (const img of all(k, 'draw:image')) {
        const media = img.attrs['xlink:href']
        if (displayable(zip, media)) blocks.push({ t: 'img', media })
      }
    } else if (k.name === 'text:list') {
      for (const item of all(k, 'text:list-item')) {
        const text = kidsOf(item).filter(x => x.name === 'text:p' || x.name === 'text:h').map(odfText).join(' ').trim()
        if (text) blocks.push({ t: 'p', text, list: true })
      }
    } else if (k.name === 'table:table') {
      blocks.push({ t: 'table', rows: odfTable(k) })
    } else if (k.name === 'draw:image') {
      const media = k.attrs['xlink:href']
      if (displayable(zip, media)) blocks.push({ t: 'img', media })
    } else {
      odfBlocks(zip, k, blocks)
    }
  }
}

function odfOutline(zip: Zip, suffix: string): Outline {
  const xml = zip.text('content.xml')
  if (!xml) throw new KbError(422, 'This file is not a readable OpenDocument file.')
  const doc = parseXml(xml)
  if (suffix === '.odp') {
    const pages = [...all(doc, 'draw:page')]
    const slides: Slide[] = pages.slice(0, MAX_SLIDES).map((page, i) => {
      let title = ''
      const blocks: Block[] = []
      for (const frame of kidsOf(page)) {
        if (frame.attrs['presentation:class'] === 'title' && !title) { title = [...all(frame, 'text:p')].map(odfText).join(' ').trim(); continue }
        odfBlocks(zip, frame, blocks)
      }
      return { n: i + 1, title, blocks }
    })
    return { kind: 'slides', slides, truncated: pages.length > MAX_SLIDES }
  }
  if (suffix === '.ods') {
    const tables = [...all(doc, 'table:table')]
    return {
      kind: 'sheets',
      sheets: tables.slice(0, MAX_SHEETS).map((t, i) => ({ name: t.attrs['table:name'] ?? `Sheet ${i + 1}`, rows: odfTable(t), truncated: false })),
      truncated: tables.length > MAX_SHEETS,
    }
  }
  const body = first(doc, 'office:text')
  const blocks: Block[] = []
  if (body) odfBlocks(zip, body, blocks)
  return { kind: 'document', blocks, truncated: blocks.length >= MAX_BLOCKS }
}

/* ---- entry points ---- */

const cache = new Map<string, Outline>()

/** The outline of an Office file, cached by path, size and mtime. */
export function officeOutline(abs: string): Outline {
  const suffix = path.extname(abs).toLowerCase()
  if (!OUTLINE_SUFFIXES.has(suffix)) throw new KbError(415, 'This file type has no preview here; its indexed text is shown instead.')
  const st = fs.statSync(abs)
  const key = `${abs}\0${st.size}\0${st.mtimeMs}`
  const hit = cache.get(key)
  if (hit) return hit
  const zip = Zip.open(abs)
  let out: Outline
  try {
    out = suffix === '.pptx' ? pptxSlides(zip)
      : suffix === '.docx' ? docxBlocks(zip)
        : suffix === '.xlsx' ? xlsxSheets(zip)
          : odfOutline(zip, suffix)
  } finally {
    zip.close()
  }
  cache.set(key, out)
  if (cache.size > 16) cache.delete(cache.keys().next().value!)
  return out
}

/** A picture inside an Office file, by its name in the package. Only the
 *  pictures an outline can point at are served. */
export function officeMedia(abs: string, member: string): { body: Buffer; type: string } {
  const suffix = path.extname(abs).toLowerCase()
  if (!OUTLINE_SUFFIXES.has(suffix)) throw new KbError(415, 'no pictures for this file type')
  const ext = path.posix.extname(member).toLowerCase()
  if (!(ext in IMAGE_TYPES) || !/^(ppt|word|xl)\/media\/[^/]+$|^Pictures\/[^/]+$/.test(member)) throw new KbError(400, 'not a picture in this file')
  const zip = Zip.open(abs)
  try {
    const body = zip.read(member, MAX_MEDIA_BYTES)
    if (!body) throw new KbError(404, 'no such picture in this file')
    return { body, type: IMAGE_TYPES[ext] }
  } finally {
    zip.close()
  }
}
