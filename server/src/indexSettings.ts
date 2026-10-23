import fs from 'node:fs'
import fsp from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { INDEX_BASE } from './config.js'
import { KbError } from './kb.js'

/**
 * How a library's index is built: how many threads, which readers run, and
 * how deep it goes. Kept per library in INDEX_BASE/indexing.json and
 * applied at the next build; a new depth or speed also at the next sync
 * (depthState.ts). Settings, General, Indexing edits the main
 * folder's; libraries built of a folder on this computer follow those,
 * except for the depth, which each library sets for itself (Settings,
 * Libraries).
 */

export interface IndexSettings {
  /** Threads for every phase; null is one per processor core. */
  threads: number | null
  /** How far the index goes (see IndexDepth); each library has its own. */
  depth: IndexDepth
  /** Follows the depth: the words inside files are read (every depth but names). */
  documents: boolean
  /** What reads a PDF downmark could not. */
  pdfReader: 'auto' | 'pdftotext' | 'pypdf'
  /** Follows the depth: tesseract on images and scanned PDF pages, where installed. */
  ocr: boolean
  /** Describe images with the vision model, when AI features are on. */
  imageDescriptions: boolean
  /** An installed LibreOffice, for Office files the built-in readers cannot
   *  read (and a .doc downmark cannot); previews use it either way. */
  legacyOffice: boolean
  /** Follows the depth: which passages get embeddings (see MeaningLevel). */
  meaning: MeaningLevel
  /** How much of each passage is embedded: standard, its first 256 tokens,
   *  or faster, its first 128, for well under half the time and slightly
   *  lower ranking. A change embeds an index again, once, at its next sync
   *  (the two are different models to the index; depthState.ts). Set for the main
   *  folder and followed by the libraries built of folders here. */
  meaningSpeed: MeaningSpeed
}

export type MeaningSpeed = 'standard' | 'faster'

/**
 * How deep a library's index goes, as stops on one scale, each adding to
 * the one before:
 *
 *   names              names, sizes, dates and owners of every file and folder
 *   text               the words inside files: text, code, PDFs, Office documents
 *   ocr                text in images and scanned PDF pages, read by tesseract
 *   meaning-documents  search by meaning over documents and images
 *   meaning-full       search by meaning over every file with text
 *
 * Each stop costs more indexing time than the one before; search by meaning
 * is the slowest phase by far, and many libraries (code, data, simulation
 * output) gain little from it. The main folder starts at meaning-documents,
 * the libraries built here at ocr.
 */
export type IndexDepth = 'names' | 'text' | 'ocr' | 'meaning-documents' | 'meaning-full'
export const INDEX_DEPTHS: readonly IndexDepth[] = ['names', 'text', 'ocr', 'meaning-documents', 'meaning-full']
export const isIndexDepth = (v: unknown): v is IndexDepth => INDEX_DEPTHS.includes(v as IndexDepth)
const rank = (d: IndexDepth) => INDEX_DEPTHS.indexOf(d)
const atLeast = (d: IndexDepth, floor: IndexDepth) => (rank(d) < rank(floor) ? floor : d)
const atMost = (d: IndexDepth, ceiling: IndexDepth) => (rank(d) > rank(ceiling) ? ceiling : d)

/**
 * How much of a library search by meaning covers, as the depth sets it.
 * Off: none; search finds names and words. Documents: the first four
 * passages of documents and images (PDF, Office, text, Markdown, image
 * descriptions), which is most of what people ask about in their own words,
 * at about a third of the work. Full: up to twelve passages of every file
 * with text.
 */
export type MeaningLevel = 'off' | 'documents' | 'full'
export const MEANING_LEVELS: readonly MeaningLevel[] = ['off', 'documents', 'full']
export const isMeaningLevel = (v: unknown): v is MeaningLevel => MEANING_LEVELS.includes(v as MeaningLevel)

/** What a depth reads and makes. */
export function depthParts(depth: IndexDepth): Pick<IndexSettings, 'depth' | 'documents' | 'ocr' | 'meaning'> {
  return {
    depth, documents: rank(depth) >= rank('text'), ocr: rank(depth) >= rank('ocr'),
    meaning: depth === 'meaning-full' ? 'full' : depth === 'meaning-documents' ? 'documents' : 'off',
  }
}

/** A library's level of search by meaning when none was chosen, as an
 *  earlier version had it. */
function defaultMeaning(library: string): MeaningLevel {
  return library === 'kb' ? 'documents' : 'off'
}

/** What embed.py is told for a level and a speed; null when nothing is embedded. */
export function meaningArgs(level: MeaningLevel, speed: MeaningSpeed = 'standard'): string[] | null {
  if (level === 'off') return null
  return [...(level === 'documents' ? ['--max-chunks', '4', '--documents-only'] : []), ...(speed === 'faster' ? ['--max-tokens', '128'] : [])]
}

/** What enrich.py is told for a depth, besides its usual options; null
 *  when the depth reads no text at all. */
export function textArgs(depth: IndexDepth): string[] | null {
  if (depth === 'names') return null
  return depth === 'text' ? ['--no-ocr'] : []
}

export const INDEX_SETTINGS_DEFAULTS: IndexSettings = {
  threads: null, ...depthParts('meaning-documents'), pdfReader: 'auto', imageDescriptions: true, legacyOffice: true, meaningSpeed: 'standard',
}

/** As saved. An earlier version kept no depth: a level of search by meaning
 *  per library (before that, an on/off `embeddings`), and the main folder's
 *  `documents` and `ocr` switches, which every library followed. */
type Saved = Partial<Omit<IndexSettings, 'documents' | 'ocr' | 'meaning'>> & {
  meaning?: unknown; embeddings?: boolean; documents?: boolean; ocr?: boolean
}

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
  const { embeddings: _e, meaning: _m, documents: _d, ocr: _o, depth: _depth, ...saved } = load().libraries[library] ?? {}
  return { ...INDEX_SETTINGS_DEFAULTS, ...saved, ...depthParts(depthOf(library)) }
}

/** A library's depth: the one chosen, or, for one that has none yet, what
 *  an earlier version's settings come to. */
function depthOf(library: string): IndexDepth {
  const saved = load().libraries[library] ?? {}
  if (isIndexDepth(saved.depth)) return saved.depth
  const meaning = isMeaningLevel(saved.meaning) ? saved.meaning : saved.embeddings === false ? 'off' : defaultMeaning(library)
  if (meaning !== 'off') return meaning === 'full' ? 'meaning-full' : 'meaning-documents'
  const main = load().libraries.kb ?? {}
  return main.documents === false ? 'names' : main.ocr === false ? 'text' : 'ocr'
}

/** The settings a library built of a folder on this computer is indexed
 *  with: the main folder's threads, readers and speed of search by meaning
 *  (Settings, General, Indexing), and its own depth. */
export function builtLibrarySettings(library: string): IndexSettings {
  return { ...indexSettings('kb'), ...depthParts(depthOf(library)) }
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
  // The depth, or what an earlier client sends instead, moving the depth
  // to the nearest stop: a level of search by meaning, the on/off switch
  // before it, and the reader switches.
  let depth = depthOf(library)
  if (body.depth !== undefined) {
    if (!isIndexDepth(body.depth)) throw new KbError(400, `depth: ${INDEX_DEPTHS.join(', ')}`)
    depth = body.depth
  }
  for (const k of ['documents', 'ocr', 'embeddings'] as const) {
    if (body[k] !== undefined && typeof body[k] !== 'boolean') throw new KbError(400, `${k}: true or false`)
  }
  if (body.meaning !== undefined && !isMeaningLevel(body.meaning)) throw new KbError(400, 'meaning: off, documents or full')
  if (body.depth === undefined) {
    if (body.documents !== undefined) depth = body.documents ? atLeast(depth, 'text') : 'names'
    if (body.ocr !== undefined) depth = body.ocr ? atLeast(depth, 'ocr') : atMost(depth, 'text')
    const meaning = body.meaning ?? (body.embeddings === undefined ? undefined : body.embeddings ? 'documents' : 'off')
    if (meaning !== undefined) depth = meaning === 'off' ? atMost(depth, 'ocr') : `meaning-${meaning as 'documents' | 'full'}`
  }
  if (depth !== depthOf(library) || body.depth !== undefined) {
    next.depth = depth
    delete next.meaning
    delete next.embeddings
  }
  for (const k of ['imageDescriptions', 'legacyOffice'] as const) {
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
