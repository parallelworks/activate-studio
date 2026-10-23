import fs from 'node:fs'
import fsp from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { INDEX_BASE } from './config.js'
import { KbError } from './kb.js'

/**
 * How a library's index is built: how many threads, which readers run, and
 * how much of it search by meaning covers. Kept per library in
 * INDEX_BASE/indexing.json and applied at the next build. Settings, General,
 * Indexing edits the main folder's; libraries built of a folder on this
 * computer follow those, except for search by meaning, which each library
 * sets for itself (Settings, Libraries).
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
  /** An installed LibreOffice, for Office files the built-in readers cannot
   *  read (and a .doc downmark cannot); previews use it either way. */
  legacyOffice: boolean
  /** Search by meaning: which passages get embeddings (see MeaningLevel). */
  meaning: MeaningLevel
  /** How much of each passage is embedded: standard, its first 256 tokens,
   *  or faster, its first 128, for well under half the time and slightly
   *  lower ranking. A change embeds an index again, once, at its next build
   *  or sync (the two are different models to the index). Set for the main
   *  folder and followed by the libraries built of folders here. */
  meaningSpeed: MeaningSpeed
}

export type MeaningSpeed = 'standard' | 'faster'

/**
 * How much of a library search by meaning covers. Off: none; search finds
 * names and words. Documents: the first four passages of documents and
 * images (PDF, Office, text, Markdown, image descriptions), which is most of
 * what people ask about in their own words, at about a third of the work.
 * Full: up to twelve passages of every file with text. Search by meaning is
 * the slowest phase of a build by far, and many libraries (code, data,
 * simulation output) gain little from it, so it is a choice per library:
 * Documents for the main folder, off for the others until turned on.
 */
export type MeaningLevel = 'off' | 'documents' | 'full'
export const MEANING_LEVELS: readonly MeaningLevel[] = ['off', 'documents', 'full']
export const isMeaningLevel = (v: unknown): v is MeaningLevel => MEANING_LEVELS.includes(v as MeaningLevel)

/** A library's level when none was chosen. */
export function defaultMeaning(library: string): MeaningLevel {
  return library === 'kb' ? 'documents' : 'off'
}

/** What embed.py is told for a level and a speed; null when nothing is embedded. */
export function meaningArgs(level: MeaningLevel, speed: MeaningSpeed = 'standard'): string[] | null {
  if (level === 'off') return null
  return [...(level === 'documents' ? ['--max-chunks', '4', '--documents-only'] : []), ...(speed === 'faster' ? ['--max-tokens', '128'] : [])]
}

export const INDEX_SETTINGS_DEFAULTS: IndexSettings = {
  threads: null, documents: true, pdfReader: 'auto', ocr: true, imageDescriptions: true, legacyOffice: true, meaning: 'documents',
  meaningSpeed: 'standard',
}

/** As saved: an earlier version kept an on/off `embeddings` instead of a level. */
type Saved = Partial<IndexSettings> & { embeddings?: boolean }

const FILE = path.join(INDEX_BASE, 'indexing.json')

/** The processor cores this process may use. */
export function cpuCount(): number {
  return Math.max(1, typeof os.availableParallelism === 'function' ? os.availableParallelism() : os.cpus().length)
}
export const MAX_THREADS = 256

let cache: { libraries: Record<string, Saved> } | null = null

function load(): { libraries: Record<string, Saved> } {
  if (cache) return cache
  try {
    const raw = JSON.parse(fs.readFileSync(FILE, 'utf8')) as { libraries?: Record<string, Saved> }
    cache = { libraries: raw && typeof raw.libraries === 'object' && raw.libraries ? raw.libraries : {} }
  } catch { cache = { libraries: {} } }
  return cache
}

export function indexSettings(library = 'kb'): IndexSettings {
  const { embeddings, ...saved } = load().libraries[library] ?? {}
  const meaning = isMeaningLevel(saved.meaning) ? saved.meaning : embeddings === false ? 'off' : defaultMeaning(library)
  return { ...INDEX_SETTINGS_DEFAULTS, ...saved, meaning }
}

/** The settings a library built of a folder on this computer is indexed
 *  with: the main folder's threads, readers and speed of search by meaning
 *  (Settings, General, Indexing), and its own level of search by meaning. */
export function builtLibrarySettings(library: string): IndexSettings {
  return { ...indexSettings('kb'), meaning: indexSettings(library).meaning }
}

/** The thread count a build uses. */
export function threadsFor(library = 'kb'): number {
  const t = indexSettings(library).threads
  return t && t > 0 ? Math.min(t, MAX_THREADS) : cpuCount()
}

/** A removed library's settings, so one added later under its name starts
 *  from the defaults. */
export async function forgetIndexSettings(library: string): Promise<void> {
  if (library === 'kb' || !load().libraries[library]) return
  const { [library]: _gone, ...rest } = load().libraries
  const all = { libraries: rest }
  await fsp.writeFile(FILE, JSON.stringify(all, null, 1))
  cache = all
}

/** Checked and saved; returns the settings now in force. */
export async function saveIndexSettings(library: string, body: Partial<Record<keyof IndexSettings | 'embeddings', unknown>>): Promise<IndexSettings> {
  const next: Saved = { ...(load().libraries[library] ?? {}) }
  if (body.threads !== undefined) {
    if (body.threads === null || body.threads === '' || body.threads === 0) next.threads = null
    else {
      const n = Number(body.threads)
      if (!Number.isInteger(n) || n < 1 || n > MAX_THREADS) throw new KbError(400, `threads: a whole number from 1 to ${MAX_THREADS}, or empty for one per core`)
      next.threads = n
    }
  }
  if (body.meaning !== undefined) {
    if (!isMeaningLevel(body.meaning)) throw new KbError(400, 'meaning: off, documents or full')
    next.meaning = body.meaning
    delete next.embeddings
  } else if (body.embeddings !== undefined) {
    // The earlier on/off switch: on is the library's usual level.
    if (typeof body.embeddings !== 'boolean') throw new KbError(400, 'embeddings: true or false')
    next.meaning = body.embeddings ? (defaultMeaning(library) === 'off' ? 'documents' : defaultMeaning(library)) : 'off'
    delete next.embeddings
  }
  for (const k of ['documents', 'ocr', 'imageDescriptions', 'legacyOffice'] as const) {
    if (body[k] !== undefined) {
      if (typeof body[k] !== 'boolean') throw new KbError(400, `${k}: true or false`)
      next[k] = body[k] as boolean
    }
  }
  if (body.pdfReader !== undefined) {
    if (!['auto', 'pdftotext', 'pypdf'].includes(String(body.pdfReader))) throw new KbError(400, 'pdfReader: auto, pdftotext or pypdf')
    next.pdfReader = body.pdfReader as IndexSettings['pdfReader']
  }
  if (body.meaningSpeed !== undefined) {
    if (body.meaningSpeed !== 'standard' && body.meaningSpeed !== 'faster') throw new KbError(400, 'meaningSpeed: standard or faster')
    next.meaningSpeed = body.meaningSpeed
  }
  const all = { libraries: { ...load().libraries, [library]: next } }
  await fsp.mkdir(path.dirname(FILE), { recursive: true })
  await fsp.writeFile(FILE, JSON.stringify(all, null, 1))
  cache = all
  return indexSettings(library)
}
