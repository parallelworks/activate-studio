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
}
