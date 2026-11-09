import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { PROVENANCE_PREFIX } from './preextract.js'

/**
 * The line preextract.ts writes first in each cache entry it converts
 * ("<!-- extracted by downmark 0.10.0 -->") names the converter, for the
 * cache's own use. It is not the document's text. Indexes written before
 * enrich.py dropped it carry it at the start of each such file's `words`
 * row, so search snippets began with it and "downmark" or "extracted"
 * matched every converted document.
 *
 * An index whose words are known to be without it has a marker in its root
 * folder. A build writes the marker; an index without one is cleaned in
 * place once, at its next sync (each directory's words rows that start
 * with the line lose it), and then marked. Nothing is extracted again.
 * Vectors already embedded from a passage that held the line are left as
 * they are; a passage embedded later is cut from the cleaned text.
 */

const MARK = '.studio-text-clean'

/** Whether an index has words that may still start with the line. */
export function textCleanNeeded(indexRoot: string): boolean {
  return fs.existsSync(path.join(indexRoot, 'db.db')) && !fs.existsSync(path.join(indexRoot, MARK))
}

/** Records that an index's words are without the line. */
export async function markTextClean(indexRoot: string): Promise<void> {
  if (!fs.existsSync(path.join(indexRoot, 'db.db'))) return
  await fsp.writeFile(path.join(indexRoot, MARK), `${JSON.stringify({ provenanceStripped: true, at: new Date().toISOString() })}\n`)
}

/** The provenance line off the front of a snippet or a passage, whole or
 *  cut short at either end (a snippet window can start or stop inside it). */
const HEADER_RE = /(?:<!--\s*)?(?:extracted\s+)?(?:by\s+)?downmark\s+\d+(?:\.\d+)*(?:\s*\(no OCR\))?\s*(?:-->|[\u2013\u2014]>)\s*/g
export function withoutProvenance(text: string): string {
  if (!text || !text.includes('downmark')) return text
  return text.replace(HEADER_RE, '')
}

const CLEAN_SQL =
  'UPDATE words SET wordf = ltrim(substr(wordf, instr(wordf, char(10)) + 1), char(10)) ' +
  `WHERE wordf LIKE '${PROVENANCE_PREFIX.replace(/'/g, "''")}%' AND instr(wordf, char(10)) > 0`

type Db = { exec(sql: string): void; prepare(sql: string): { run(): { changes: number | bigint }; get(...a: unknown[]): unknown }; close(): void }
let opener: Promise<((file: string) => Db) | null> | null = null
function openDb(): Promise<((file: string) => Db) | null> {
  opener ??= import('node:sqlite').then(m => (file: string) => new m.DatabaseSync(file, { timeout: 30_000 }) as unknown as Db).catch(() => null)
  return opener
}

/** Every db.db under an index root, each once; symlinks are not followed. */
async function* dbFiles(root: string): AsyncGenerator<string> {
  const stack = [root]
  while (stack.length) {
    const dir = stack.pop()!
    let entries: fs.Dirent[]
    try { entries = await fsp.readdir(dir, { withFileTypes: true }) } catch { continue }
    for (const e of entries) {
      if (e.isDirectory()) stack.push(path.join(dir, e.name))
      else if (e.isFile() && e.name === 'db.db') yield path.join(dir, e.name)
    }
  }
}

/**
 * Takes the provenance line off the start of every words row under an
 * index root, in place. Directories without a words table are passed by.
 * Returns how many databases were looked at and rows changed; null where
 * this Node has no SQLite to do it with (the next build writes clean text).
 */
export async function cleanIndexedText(indexRoot: string, signal?: AbortSignal): Promise<{ dbs: number; rows: number } | null> {
  const open = await openDb()
  if (!open) return null
  let dbs = 0, rows = 0
  for await (const file of dbFiles(indexRoot)) {
    if (signal?.aborted) throw new Error('stopped')
    let db: Db | null = null
    try {
      db = open(file)
      if (db.prepare("SELECT 1 FROM sqlite_master WHERE name = 'words'").get()) rows += Number(db.prepare(CLEAN_SQL).run().changes)
    } catch { /* a database that cannot be written here is left as it is */ } finally { db?.close() }
    // The SQLite calls are synchronous: let requests in between directories.
    if (++dbs % 50 === 0) await new Promise(r => setImmediate(r))
  }
  return { dbs, rows }
}
