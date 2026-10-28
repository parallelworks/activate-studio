import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { INDEX_BASE } from './config.js'

/**
 * The index log: every index job and sweep writes what it does here, one
 * timestamped line at a time, so a build that seems stuck can be looked at
 * from the Studio (GET /api/index/log) or opened as a file. It sits with
 * the app's other logs where there is a data folder (the macOS app), and
 * beside the index otherwise. One previous file is kept.
 */

/** Where the Studio's logs go: the data folder's, else beside the index. */
export const LOG_DIR = process.env.STUDIO_DATA_DIR ? path.join(process.env.STUDIO_DATA_DIR, 'logs') : path.join(INDEX_BASE, 'logs')
export const INDEX_LOG = path.join(LOG_DIR, 'indexing.log')
const MAX_BYTES = 10 * 1024 * 1024

let size = -1

function rotate(): void {
  try {
    if (size < 0) size = fs.statSync(INDEX_LOG).size
  } catch { size = 0 }
  if (size < MAX_BYTES) return
  try { fs.renameSync(INDEX_LOG, `${INDEX_LOG}.1`) } catch { /* nothing to rotate */ }
  size = 0
}

/** One line, or several, each with the time in front. Never throws: a log
 *  that cannot be written must not stop the indexing it describes. */
export function indexLog(msg: string): void {
  const at = new Date().toISOString().replace('T', ' ').slice(0, 19)
  const text = String(msg).split('\n').filter(l => l.trim()).map(l => `${at} ${l}\n`).join('')
  if (!text) return
  try {
    fs.mkdirSync(path.dirname(INDEX_LOG), { recursive: true })
    rotate()
    fs.appendFileSync(INDEX_LOG, text)
    size += Buffer.byteLength(text)
  } catch { /* the log is a convenience */ }
}

/** The last `lines` lines of the log, read from its end. */
export async function tailIndexLog(lines = 400): Promise<string> {
  let fh: fsp.FileHandle | undefined
  try {
    fh = await fsp.open(INDEX_LOG, 'r')
    const { size: total } = await fh.stat()
    // Lines are short; a generous slice from the end covers the request.
    const want = Math.min(total, Math.max(64 * 1024, lines * 400))
    const buf = Buffer.alloc(want)
    await fh.read(buf, 0, want, total - want)
    const all = buf.toString('utf8').split('\n')
    if (want < total) all.shift()
    if (all[all.length - 1] === '') all.pop()
    return all.slice(-lines).join('\n') + (all.length ? '\n' : '')
  } catch {
    return ''
  } finally {
    await fh?.close()
  }
}

export function indexLogInfo(): { path: string; bytes: number } {
  let bytes = 0
  try { bytes = fs.statSync(INDEX_LOG).size } catch { /* not written yet */ }
  return { path: INDEX_LOG, bytes }
}
