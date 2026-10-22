import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { PYTHON_BIN } from './config.js'
import { downmarkNative } from './preextract.js'

/**
 * Which readers this host has for each kind of file, so a format that
 * would index by its name alone is said out loud rather than discovered.
 * The macOS app carries downmark (documents, PDFs) and pypdf (the PDF
 * fallback); LibreOffice and tesseract are used where installed, not
 * bundled.
 */

export interface ExtractorReport {
  name: string
  available: boolean
  covers: string
  /** A plain sentence about where it comes from, or why it is missing. */
  note: string
  /** Something else covers its formats, so its absence loses nothing. */
  optional: boolean
}

export interface FormatReadiness {
  formats: string
  readers: string[]
  ok: boolean
  /** What happens to these files, in a sentence. */
  note: string
}

export interface ReaderOptions {
  documents: boolean
  ocr: boolean
  legacyOffice: boolean
  pdfReader: 'auto' | 'pdftotext' | 'pypdf'
  /** A vision model will describe images (AI on, a model set, the setting on). */
  vision: boolean
}

const which = (bin: string): string | null => {
  try {
    const out = execFileSync('sh', ['-c', `command -v ${bin}`], { encoding: 'utf8', timeout: 10_000 }).trim()
    return out || null
  } catch { return null }
}

/** LibreOffice's soffice: SOFFICE_BIN, the PATH, then the app on macOS,
 *  which installs to /Applications (or ~/Applications) and puts nothing on
 *  the PATH. */
export function findSoffice(env: NodeJS.ProcessEnv = process.env, exists: (p: string) => boolean = fs.existsSync): string | null {
  if (env.SOFFICE_BIN) return env.SOFFICE_BIN
  const onPath = which('soffice')
  if (onPath) return onPath
  for (const app of ['/Applications/LibreOffice.app', path.join(os.homedir(), 'Applications', 'LibreOffice.app')]) {
    const bin = path.join(app, 'Contents', 'MacOS', 'soffice')
    if (exists(bin)) return bin
  }
  return null
}

// Each looked up once, when first asked: a build needs only LibreOffice's
// path, and asking Python about pypdf costs a process start.
const found = new Map<string, unknown>()
function once<T>(key: string, look: () => T): T {
  if (!found.has(key)) found.set(key, look())
  return found.get(key) as T
}

function probe() {
  return {
    get pypdf() {
      return once('pypdf', () => {
        try { execFileSync(PYTHON_BIN, ['-c', 'import pypdf'], { stdio: 'ignore', timeout: 30_000 }); return true } catch { return false }
      })
    },
    get pdftotext() { return once('pdftotext', () => which('pdftotext')) },
    get tesseract() { return once('tesseract', () => which('tesseract')) },
    get soffice() { return once('soffice', () => findSoffice()) },
    get native() { return once('native', () => downmarkNative()) },
  }
}

/** Look again, as after installing LibreOffice or tesseract. */
export function forgetExtractors(): void { found.clear() }

export function sofficeFound(): string | null { return probe().soffice }

const LEGACY = '.ppt, .xls, .odt, .odp, .ods'

export function extractorReport(opts: ReaderOptions): { extractors: ExtractorReport[]; formats: FormatReadiness[]; missing: FormatReadiness[] } {
  const p = probe()
  const pdfVia = [
    ...(p.native ? ['downmark'] : []),
    ...(opts.pdfReader !== 'pypdf' && p.pdftotext ? ['pdftotext'] : []),
    ...(opts.pdfReader !== 'pdftotext' && p.pypdf ? ['pypdf'] : []),
  ]
  const extractors: ExtractorReport[] = [
    {
      name: 'downmark', available: true, optional: false,
      covers: p.native ? '.docx, .pptx, .xlsx, .doc, .pdf' : '.docx, .pptx, .xlsx, .doc',
      note: p.native ? 'Bundled. Converts documents and PDFs to Markdown, keeping headings and tables.'
        : 'Bundled, without its native program for this platform, so it reads Office documents but not PDFs.',
    },
    {
      name: 'pypdf', available: p.pypdf, optional: p.native || !!p.pdftotext,
      covers: '.pdf',
      note: p.pypdf ? 'Reads the PDFs downmark could not.' : `Not installed for ${PYTHON_BIN}.`,
    },
    {
      name: 'pdftotext', available: !!p.pdftotext, optional: p.native || p.pypdf,
      covers: '.pdf',
      note: p.pdftotext ? `Found at ${p.pdftotext}.` : 'Not installed (poppler). Not needed while downmark or pypdf reads PDFs.',
    },
    {
      name: 'LibreOffice', available: !!p.soffice, optional: false,
      covers: `${LEGACY}, and previews of Office documents`,
      note: p.soffice ? `Found at ${p.soffice}.`
        : process.platform === 'darwin' ? 'Not found in /Applications/LibreOffice.app. Install LibreOffice to read these formats and preview Office documents.'
          : 'soffice is not on the PATH. Install LibreOffice to read these formats and preview Office documents.',
    },
    {
      name: 'tesseract', available: !!p.tesseract, optional: false,
      covers: 'text inside images and scanned PDF pages (OCR)',
      note: p.tesseract ? `Found at ${p.tesseract}.` : 'Not installed. Install tesseract (for example with Homebrew) to read text in images and scans.',
    },
  ]
  const formats: FormatReadiness[] = []
  if (!opts.documents) {
    formats.push({ formats: '.pdf, .docx, .pptx, .xlsx, .doc', readers: [], ok: false, note: 'Document text is turned off under Settings, Indexing; these index by name only.' })
  } else {
    formats.push({ formats: '.docx, .pptx, .xlsx', readers: ['downmark'], ok: true, note: 'Read by downmark, with a standard-library reader for any it cannot.' })
    const doc = ['downmark', ...(opts.legacyOffice && p.soffice ? ['LibreOffice'] : [])]
    formats.push({ formats: '.doc', readers: doc, ok: true, note: `Read by ${doc.join(', then ')}.` })
    formats.push({
      formats: '.pdf', readers: pdfVia, ok: pdfVia.length > 0,
      note: pdfVia.length ? `Read by ${pdfVia.join(', then ')}.` : 'No PDF reader on this host; PDFs index by name only. Install poppler (pdftotext) or pypdf.',
    })
    const legacy = opts.legacyOffice && !!p.soffice
    formats.push({
      formats: LEGACY, readers: legacy ? ['LibreOffice'] : [], ok: legacy,
      note: legacy ? 'Read through LibreOffice.' : !opts.legacyOffice ? 'Reading these through LibreOffice is turned off; they index by name only.'
        : 'These index by name only until LibreOffice is installed.',
    })
  }
  const imageReaders = [...(opts.ocr && p.tesseract ? ['tesseract'] : []), ...(opts.vision ? ['vision model'] : [])]
  formats.push({
    formats: 'images', readers: imageReaders, ok: imageReaders.length > 0,
    note: imageReaders.length ? `Text in images is read by ${imageReaders.join(' and ')}.`
      : !opts.ocr ? 'OCR is turned off; images index by name only.'
        : 'Images index by name only: tesseract is not installed, and no vision model describes them.',
  })
  return { extractors, formats, missing: formats.filter(f => !f.ok) }
}
