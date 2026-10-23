import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { INDEX_DEPTHS, depthParts, isIndexDepth, type IndexDepth, type IndexSettings, type MeaningSpeed } from './indexSettings.js'
import { tesseractAvailable } from './preextract.js'

/**
 * What an index was last built or synced at, so the sync can tell that the
 * depth changed since and apply it: a change of depth touches no file, so
 * the sync's look for changed folders finds nothing to do.
 *
 * The record holds the depth, whether OCR could run (the depth reads images
 * and tesseract was found), and the speed of search by meaning. The main
 * folder keeps it beside its sync state (indexing.ts), a library the Studio
 * builds in its own folder (libraryBuild.ts). An index with no record, one
 * from before this, counts as built at its settings now, so an upgrade
 * re-indexes nothing.
 */

export interface DepthRecord { depth: IndexDepth; ocr: boolean; meaningSpeed: MeaningSpeed }

/** What applying a new depth runs over the whole library: the text phase
 *  (documents, then enrich.py), the meaning phase (embed.py), or neither
 *  (the depth went down, which search follows with nothing re-indexed). */
export interface DepthChange { text: boolean; meaning: boolean; label: string }

/** The record for settings an index was built with now. */
export function depthRecordOf(s: IndexSettings): DepthRecord {
  return { depth: s.depth, ocr: s.ocr && tesseractAvailable(), meaningSpeed: s.meaningSpeed }
}

export function readDepthRecord(file: string): DepthRecord | null {
  try {
    const r = JSON.parse(fs.readFileSync(file, 'utf8')) as Partial<DepthRecord>
    if (!isIndexDepth(r.depth)) return null
    return { depth: r.depth, ocr: r.ocr === true, meaningSpeed: r.meaningSpeed === 'faster' ? 'faster' : 'standard' }
  } catch { return null }
}

export async function writeDepthRecord(file: string, rec: DepthRecord): Promise<void> {
  await fsp.mkdir(path.dirname(file), { recursive: true })
  const tmp = `${file}.${process.pid}.tmp`
  await fsp.writeFile(tmp, JSON.stringify(rec))
  await fsp.rename(tmp, file)
}

const sameRecord = (a: DepthRecord, b: DepthRecord) => a.depth === b.depth && a.ocr === b.ocr && a.meaningSpeed === b.meaningSpeed

/**
 * What going from one record to the other runs; null when they are the
 * same. Going up runs only what the new depth adds: text that was never
 * read (from Names), OCR that never ran (images and scanned pages read
 * again, the rest from the extraction cache), and search by meaning where
 * there was none, or over more files, or at another speed; new text is
 * embedded too. Going down runs nothing: search stops at the new depth, and
 * what was read before stays in the index until its folder changes.
 */
export function depthChange(was: DepthRecord, now: DepthRecord): DepthChange | null {
  if (sameRecord(was, now)) return null
  const rank = (d: IndexDepth) => INDEX_DEPTHS.indexOf(d)
  const newText = rank(was.depth) < rank('text') && rank(now.depth) >= rank('text')
  const newOcr = now.ocr && !was.ocr
  const text = newText || newOcr
  const m0 = depthParts(was.depth).meaning, m1 = depthParts(now.depth).meaning
  const meaning = m1 !== 'off' && (m0 === 'off' || (m0 === 'documents' && m1 === 'full') || was.meaningSpeed !== now.meaningSpeed || text)
  const parts = [
    text ? (newText ? (newOcr ? 'text and OCR' : 'text') : 'OCR') : '',
    meaning ? 'search by meaning' : '',
  ].filter(Boolean)
  return { text, meaning, label: parts.join(' and ') }
}
