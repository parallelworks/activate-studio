import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { INDEX_BASE } from './config.js'

export const MIME: Record<string, string> = {
  '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg',
  '.gif': 'image/gif', '.webp': 'image/webp', '.svg': 'image/svg+xml',
  '.pdf': 'application/pdf',
  '.md': 'text/plain; charset=utf-8', '.txt': 'text/plain; charset=utf-8',
  '.html': 'text/html; charset=utf-8', '.json': 'application/json',
  '.csv': 'text/csv; charset=utf-8',
}

export function mimeFor(p: string): string {
  return MIME[path.extname(p).toLowerCase()] ?? 'application/octet-stream'
}

export const CONVERTIBLE = new Set(['.docx', '.pptx', '.xlsx', '.doc', '.ppt', '.xls', '.odt', '.odp', '.ods'])

const PDF_CACHE = path.join(INDEX_BASE, 'pdf-preview')
const SOFFICE_HOME = path.join(INDEX_BASE, '.soffice')

// LibreOffice instances fight over the user profile; run one at a time.
let conversionQueue: Promise<unknown> = Promise.resolve()

/**
 * Convert an office document to PDF for in-browser preview, cached by
 * source mtime. Returns the cached PDF path.
 */
export function officeToPdf(absSource: string, relPath: string): Promise<string> {
  const run = async (): Promise<string> => {
    const srcStat = await fsp.stat(absSource)
    const outDir = path.join(PDF_CACHE, path.dirname(relPath))
    const outFile = path.join(outDir, path.basename(relPath, path.extname(relPath)) + '.pdf')
    try {
      const cached = await fsp.stat(outFile)
      if (cached.mtimeMs >= srcStat.mtimeMs) return outFile
    } catch { /* not cached */ }
    await fsp.mkdir(outDir, { recursive: true })
    await fsp.mkdir(SOFFICE_HOME, { recursive: true })
    await new Promise<void>((resolve, reject) => {
      execFile('soffice', ['--headless', '--convert-to', 'pdf', '--outdir', outDir, absSource], {
        timeout: 120_000,
        env: { ...process.env, HOME: SOFFICE_HOME },
      }, (err, _so, se) => err ? reject(new Error(`conversion failed: ${se || err.message}`)) : resolve())
    })
    if (!fs.existsSync(outFile)) throw new Error('conversion produced no output')
    return outFile
  }
  const next = conversionQueue.then(run, run)
  conversionQueue = next.catch(() => {})
  return next
}

export async function removePdfPreview(relPath: string): Promise<void> {
  const outFile = path.join(PDF_CACHE, path.dirname(relPath), path.basename(relPath, path.extname(relPath)) + '.pdf')
  await fsp.rm(outFile, { force: true }).catch(() => {})
  await fsp.rm(pageDirFor(relPath), { recursive: true, force: true }).catch(() => {})
}

/* PDF page rendering: the browser's built-in PDF viewer is unavailable
 * inside the platform's sandboxed session iframe, so PDFs preview as
 * server-rendered page PNGs (poppler pdftoppm), cached by source mtime. */

const PDF_PAGES = path.join(INDEX_BASE, 'pdf-pages')

function pageDirFor(relPath: string): string {
  return path.join(PDF_PAGES, relPath)
}

/** Resolve the PDF that backs a preview: the file itself, or the cached
 *  LibreOffice conversion for office documents. */
export async function previewPdfFor(absSource: string, relPath: string): Promise<string> {
  return CONVERTIBLE.has(path.extname(relPath).toLowerCase())
    ? officeToPdf(absSource, relPath)
    : absSource
}

export async function pdfPageCount(absPdf: string): Promise<number> {
  const out = await new Promise<string>((resolve, reject) => {
    execFile('pdfinfo', [absPdf], { timeout: 30_000 }, (err, so, se) =>
      err ? reject(new Error(se || err.message)) : resolve(so))
  })
  const m = out.match(/^Pages:\s+(\d+)/m)
  if (!m) throw new Error('pdfinfo reported no page count')
  return Number(m[1])
}

/** Render one page (1-based) to a cached PNG and return its path. */
export async function pdfPagePng(absSource: string, relPath: string, page: number): Promise<string> {
  const absPdf = await previewPdfFor(absSource, relPath)
  const srcStat = await fsp.stat(absPdf)
  const dir = pageDirFor(relPath)
  const outFile = path.join(dir, `p${page}.png`)
  try {
    const cached = await fsp.stat(outFile)
    if (cached.mtimeMs >= srcStat.mtimeMs) return outFile
  } catch { /* not cached */ }
  await fsp.mkdir(dir, { recursive: true })
  await new Promise<void>((resolve, reject) => {
    execFile('pdftoppm', ['-f', String(page), '-l', String(page), '-r', '130', '-png',
      absPdf, path.join(dir, 'p')], { timeout: 60_000 }, (err, _so, se) =>
      err ? reject(new Error(`page render failed: ${se || err.message}`)) : resolve())
  })
  // pdftoppm writes <prefix>-<page>.png and zero-pads once the count needs
  // it (p-1.png, p-01.png); normalize whatever it produced to pN.png.
  if (!fs.existsSync(outFile)) {
    const made = (await fsp.readdir(dir)).find(f => {
      const m = f.match(/^p-?0*(\d+)\.png$/)
      return m !== undefined && m !== null && Number(m[1]) === page
    })
    if (!made) throw new Error('page render produced no output')
    await fsp.rename(path.join(dir, made), outFile)
  }
  return outFile
}
