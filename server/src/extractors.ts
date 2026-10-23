import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { PROJECT_ROOT, PYTHON_BIN } from './config.js'
import { downmarkNative } from './preextract.js'

/**
 * Which readers this host has for each kind of file, so a format that
 * would index by its name alone is said out loud rather than discovered.
 * The macOS app carries downmark (documents, PDFs), pypdf (the PDF
 * fallback), xlrd and olefile (.xls and .ppt), and tesseract (OCR, which
 * downmark also runs on scanned PDF pages); OpenDocument is read with the
 * standard library. LibreOffice (previews, and any file those cannot
 * read), poppler, and tesseract outside the app are used where installed,
 * not bundled: for each one missing, the report says what it adds and how
 * to install it on this machine.
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

/** A tool this machine lacks, what it would add, and how to install it here. */
export interface InstallItem {
  name: string
  adds: string
  /** The command for this machine, or null where there is none to give. */
  command: string | null
  /** Where to start when there is no command (Homebrew's install page). */
  link: string | null
}

export interface InstallInfo {
  platform: string
  /** Homebrew, where it is installed. */
  brew: string | null
  apt: boolean
  items: InstallItem[]
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
    get pyReaders() {
      return once('pyReaders', () => {
        // As enrich.py finds them: on the Python's path, or in indexer/.deps.
        const deps = path.join(PROJECT_ROOT, 'indexer', '.deps')
        const code = `import sys, json, importlib.util as u; sys.path.append(${JSON.stringify(deps)}); print(json.dumps({m: u.find_spec(m) is not None for m in ('xlrd', 'olefile')}))`
        try {
          const out = execFileSync(PYTHON_BIN, ['-B', '-c', code], { encoding: 'utf8', timeout: 30_000 })
          const r = JSON.parse(out.trim().split('\n').pop() ?? '{}') as { xlrd?: boolean; olefile?: boolean }
          return { xlrd: !!r.xlrd, olefile: !!r.olefile }
        } catch { return { xlrd: false, olefile: false } }
      })
    },
    get pdftotext() { return once('pdftotext', () => which('pdftotext')) },
    get pdftoppm() { return once('pdftoppm', () => which('pdftoppm')) },
    get brew() { return once('brew', () => ['/opt/homebrew/bin/brew', '/usr/local/bin/brew'].find(b => fs.existsSync(b)) ?? null) },
    get apt() { return once('apt', () => !!which('apt-get')) },
    get tesseract() { return once('tesseract', () => which('tesseract')) },
    get soffice() { return once('soffice', () => findSoffice()) },
    get native() { return once('native', () => downmarkNative()) },
  }
}

/** The macOS app's own tesseract (deploy/mac/build_ocr.sh), beside the app's
 *  code in Contents/Resources: studio-run.sh puts it first on the PATH. */
export function bundledTesseract(bin: string | null, root = PROJECT_ROOT): boolean {
  return !!bin && path.resolve(bin).startsWith(path.resolve(root, '..', 'ocr', 'bin') + path.sep)
}

/** Look again, as after installing LibreOffice or tesseract. */
export function forgetExtractors(): void { found.clear() }

export function sofficeFound(): string | null { return probe().soffice }

const ODF = '.odt, .ods, .odp'
const HOMEBREW = 'https://brew.sh'

/** The install command for a tool on this machine: Homebrew on macOS where
 *  it is installed, apt-get on Debian or Ubuntu; null otherwise. */
function installCommand(tool: 'tesseract' | 'poppler' | 'libreoffice', brew: string | null, apt: boolean, platform = process.platform): string | null {
  if (platform === 'darwin') return brew ? (tool === 'libreoffice' ? 'brew install --cask libreoffice' : `brew install ${tool}`) : null
  if (apt) return `sudo apt-get install ${{ tesseract: 'tesseract-ocr', poppler: 'poppler-utils', libreoffice: 'libreoffice' }[tool]}`
  return null
}

/** What installing each missing tool would add, and the command for it here.
 *  Shown, never run: the Studio does not install software. */
export function installInfo(have: { tesseract: boolean; poppler: boolean; soffice: boolean; xlrd: boolean; olefile: boolean; scanOcr?: boolean },
  brew: string | null, apt: boolean, platform = process.platform): InstallInfo {
  const item = (name: string, adds: string, tool: 'tesseract' | 'poppler' | 'libreoffice'): InstallItem => {
    const command = installCommand(tool, brew, apt, platform)
    return { name, adds, command, link: !command && platform === 'darwin' ? HOMEBREW : null }
  }
  const items: InstallItem[] = []
  if (!have.tesseract) items.push(item('tesseract', 'OCR reads scanned PDFs and text in images.', 'tesseract'))
  // downmark with tesseract reads scanned pages already; poppler then adds
  // only the PDFs downmark cannot read.
  if (!have.poppler) {
    items.push(item('poppler', have.scanOcr ? 'Poppler reads PDFs the bundled readers cannot.'
      : 'Poppler reads PDFs the bundled readers cannot, and lets OCR read scanned PDF pages.', 'poppler'))
  }
  if (!have.soffice) items.push(item('LibreOffice', 'LibreOffice renders previews of Office files.', 'libreoffice'))
  if (!have.xlrd || !have.olefile) {
    items.push({
      name: 'xlrd and olefile', adds: 'xlrd and olefile read .xls and .ppt files.',
      command: 'python3 indexer/fetch_embed.py --deps indexer/.deps --readers-only', link: null,
    })
  }
  return { platform, brew, apt, items }
}

export function extractorReport(opts: ReaderOptions): { extractors: ExtractorReport[]; formats: FormatReadiness[]; missing: FormatReadiness[]; install: InstallInfo } {
  const p = probe()
  const py = p.pyReaders
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
      name: 'OpenDocument reader', available: true, optional: false,
      covers: `${ODF}, .odg`,
      note: 'Part of the Studio, with nothing to install.',
    },
    {
      name: 'xlrd', available: py.xlrd, optional: false,
      covers: '.xls',
      note: py.xlrd ? 'Bundled. Reads Excel 97-2003 workbooks.' : `Not installed for ${PYTHON_BIN}.`,
    },
    {
      name: 'olefile', available: py.olefile, optional: false,
      covers: '.ppt',
      note: py.olefile ? 'Bundled. Reads PowerPoint 97-2003 slides and speaker notes.' : `Not installed for ${PYTHON_BIN}.`,
    },
    {
      name: 'LibreOffice', available: !!p.soffice, optional: false,
      covers: 'previews of Office documents, and .doc, .ppt, .xls and OpenDocument files the readers above cannot read',
      note: p.soffice ? `Found at ${p.soffice}.`
        : process.platform === 'darwin' ? 'Not found in /Applications/LibreOffice.app. Office files are still read; install LibreOffice for their previews.'
          : 'soffice is not on the PATH. Office files are still read; install LibreOffice for their previews.',
    },
    {
      name: 'tesseract', available: !!p.tesseract, optional: false,
      covers: 'text inside images and scanned PDF pages (OCR)',
      note: bundledTesseract(p.tesseract) ? 'Bundled, with its English model. Reads text in images, and scanned PDF pages with downmark.'
        : p.tesseract ? `Found at ${p.tesseract}.` : 'Not installed. Install tesseract (for example with Homebrew) to read text in images and scans.',
    },
  ]
  const formats: FormatReadiness[] = []
  if (!opts.documents) {
    formats.push({ formats: '.pdf, .docx, .pptx, .xlsx, .doc', readers: [], ok: false, note: 'The index depth is Names, so these index by name only.' })
  } else {
    formats.push({ formats: '.docx, .pptx, .xlsx', readers: ['downmark'], ok: true, note: 'Read by downmark, with a standard-library reader for any it cannot.' })
    const doc = ['downmark', ...(opts.legacyOffice && p.soffice ? ['LibreOffice'] : [])]
    formats.push({ formats: '.doc', readers: doc, ok: true, note: `Read by ${doc.join(', then ')}.` })
    const scans = opts.ocr && p.native && p.tesseract ? ' Scanned pages are read by tesseract.' : ''
    formats.push({
      formats: '.pdf', readers: pdfVia, ok: pdfVia.length > 0,
      note: pdfVia.length ? `Read by ${pdfVia.join(', then ')}.${scans}` : 'No PDF reader on this host; PDFs index by name only. Install poppler (pdftotext) or pypdf.',
    })
    // LibreOffice, where installed and allowed, reads what these cannot.
    const lo = opts.legacyOffice && p.soffice ? ['LibreOffice'] : []
    const via = (own: string[]) => [...own, ...lo]
    const odf = via(['OpenDocument reader'])
    formats.push({ formats: ODF, readers: odf, ok: true, note: `Read by ${odf.join(', then ')}: text, tables, slides and speaker notes.` })
    for (const [fmt, mod, ok] of [['.xls', 'xlrd', py.xlrd], ['.ppt', 'olefile', py.olefile]] as const) {
      const r = via(ok ? [mod] : [])
      formats.push({
        formats: fmt, readers: r, ok: r.length > 0,
        note: r.length ? `Read by ${r.join(', then ')}.` : `These index by name only: ${mod} is not installed, and LibreOffice is not used.`,
      })
    }
  }
  const imageReaders = [...(opts.ocr && p.tesseract ? ['tesseract'] : []), ...(opts.vision ? ['vision model'] : [])]
  formats.push({
    formats: 'images', readers: imageReaders, ok: imageReaders.length > 0,
    note: imageReaders.length ? `Text in images is read by ${imageReaders.join(' and ')}.`
      : !opts.ocr ? 'The index depth stops before Text + OCR, so images index by name only.'
        : 'Images index by name only: tesseract is not installed, and no vision model describes them.',
  })
  const install = installInfo({ tesseract: !!p.tesseract, poppler: !!p.pdftotext && !!p.pdftoppm, soffice: !!p.soffice, xlrd: py.xlrd, olefile: py.olefile,
    scanOcr: p.native && !!p.tesseract }, p.brew, p.apt)
  return { extractors, formats, missing: formats.filter(f => !f.ok), install }
}
