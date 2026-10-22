import fs from 'node:fs'
import fsp from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { INDEX_BASE } from './config.js'
import { KbError } from './kb.js'

/**
 * How a library's index is built: how many threads, which readers run, and
 * whether embeddings are made. Kept per library in INDEX_BASE/indexing.json
 * and applied at the next build. Settings, General, Indexing edits the main
 * folder's, and libraries built of a folder on this computer follow those.
 */

export interface IndexSettings {
  /** Threads for every phase; null is one per processor core. */
  threads: number | null
  /** Read the text of PDFs and Office documents (off: by name only). */
  documents: boolean
  /** What reads a PDF downmark could not. */
  pdfReader: 'auto' | 'pdftotext' | 'pypdf'
  /** tesseract on images and scanned PDF pages, where installed. */
  ocr: boolean
  /** Describe images with the vision model, when AI features are on. */
  imageDescriptions: boolean
  /** .ppt, .xls and OpenDocument files through an installed LibreOffice. */
  legacyOffice: boolean
  /** Embeddings for search by meaning. */
  embeddings: boolean
}

export const INDEX_SETTINGS_DEFAULTS: IndexSettings = {
  threads: null, documents: true, pdfReader: 'auto', ocr: true, imageDescriptions: true, legacyOffice: true, embeddings: true,
}

const FILE = path.join(INDEX_BASE, 'indexing.json')

/** The processor cores this process may use. */
export function cpuCount(): number {
  return Math.max(1, typeof os.availableParallelism === 'function' ? os.availableParallelism() : os.cpus().length)
}
export const MAX_THREADS = 256

let cache: { libraries: Record<string, Partial<IndexSettings>> } | null = null

function load(): { libraries: Record<string, Partial<IndexSettings>> } {
  if (cache) return cache
  try {
    const raw = JSON.parse(fs.readFileSync(FILE, 'utf8')) as { libraries?: Record<string, Partial<IndexSettings>> }
    cache = { libraries: raw && typeof raw.libraries === 'object' && raw.libraries ? raw.libraries : {} }
  } catch { cache = { libraries: {} } }
  return cache
}

export function indexSettings(library = 'kb'): IndexSettings {
  return { ...INDEX_SETTINGS_DEFAULTS, ...(load().libraries[library] ?? {}) }
}

/** The thread count a build uses. */
export function threadsFor(library = 'kb'): number {
  const t = indexSettings(library).threads
  return t && t > 0 ? Math.min(t, MAX_THREADS) : cpuCount()
}

/** Checked and saved; returns the settings now in force. */
export async function saveIndexSettings(library: string, body: Partial<Record<keyof IndexSettings, unknown>>): Promise<IndexSettings> {
  const next: Partial<IndexSettings> = { ...(load().libraries[library] ?? {}) }
  if (body.threads !== undefined) {
    if (body.threads === null || body.threads === '' || body.threads === 0) next.threads = null
    else {
      const n = Number(body.threads)
      if (!Number.isInteger(n) || n < 1 || n > MAX_THREADS) throw new KbError(400, `threads: a whole number from 1 to ${MAX_THREADS}, or empty for one per core`)
      next.threads = n
    }
  }
  for (const k of ['documents', 'ocr', 'imageDescriptions', 'legacyOffice', 'embeddings'] as const) {
    if (body[k] !== undefined) {
      if (typeof body[k] !== 'boolean') throw new KbError(400, `${k}: true or false`)
      next[k] = body[k] as boolean
    }
  }
  if (body.pdfReader !== undefined) {
    if (!['auto', 'pdftotext', 'pypdf'].includes(String(body.pdfReader))) throw new KbError(400, 'pdfReader: auto, pdftotext or pypdf')
    next.pdfReader = body.pdfReader as IndexSettings['pdfReader']
  }
  const all = { libraries: { ...load().libraries, [library]: next } }
  await fsp.mkdir(path.dirname(FILE), { recursive: true })
  await fsp.writeFile(FILE, JSON.stringify(all, null, 1))
  cache = all
  return indexSettings(library)
}
