import fs from 'node:fs'
import path from 'node:path'
import { LOG_DIR } from './indexLog.js'
import { effectiveSettings } from './settings.js'
import type { GufiCommand, GufiSource } from './gufiTrace.js'

/**
 * The GUFI command log, for a deployer: while Settings, General, Indexing,
 * "Log GUFI commands" is on (STUDIO_GUFI_LOG=1 turns it on by default),
 * every GUFI command the Studio runs to read an index (gufiTrace.ts) is
 * appended here as one JSON object: when it started, what asked for it,
 * how long it took, how it ended, the command, and the end of its error
 * output. The indexing programs write theirs to the index log instead
 * (indexLog.ts), which this does not repeat. One previous file is kept.
 */

export const GUFI_LOG = path.join(LOG_DIR, 'gufi-commands.log')
const MAX_BYTES = 10 * 1024 * 1024
/** What one entry keeps of a long command, and of its error output. */
const COMMAND_KEEP = 16 * 1024
const STDERR_KEEP = 2 * 1024

let size = -1

export function gufiLogOn(): boolean {
  try { return effectiveSettings().gufiLog } catch { return false }
}

function rotate(): void {
  try {
    if (size < 0) size = fs.statSync(GUFI_LOG).size
  } catch { size = 0 }
  if (size < MAX_BYTES) return
  try { fs.renameSync(GUFI_LOG, `${GUFI_LOG}.1`) } catch { /* nothing to rotate */ }
  size = 0
}

/** One entry. Never throws: a log that cannot be written must not stop
 *  the search it describes. */
export function appendGufiLog(c: GufiCommand, source: GufiSource): void {
  const entry = {
    time: c.at ?? new Date().toISOString(),
    source,
    engine: c.engine,
    ...(c.site ? { site: c.site } : {}),
    ms: c.ms ?? null,
    exit: c.exit ?? null,
    ...(c.failed ? { failed: c.failed } : {}),
    command: c.command.length > COMMAND_KEEP ? c.command.slice(0, COMMAND_KEEP) : c.command,
    ...(c.command.length > COMMAND_KEEP ? { commandCut: true } : {}),
    // null: the Studio does not keep this command's error output.
    stderr: c.stderr === undefined ? null : c.stderr.slice(-STDERR_KEEP),
    ...(c.stderrCut || (c.stderr?.length ?? 0) > STDERR_KEEP ? { stderrCut: true } : {}),
    ...(c.expected ? { expected: c.expected, expectedExample: c.expectedExample } : {}),
  }
  const text = JSON.stringify(entry) + '\n'
  try {
    fs.mkdirSync(path.dirname(GUFI_LOG), { recursive: true })
    rotate()
    fs.appendFileSync(GUFI_LOG, text)
    size += Buffer.byteLength(text)
  } catch { /* the log is a convenience */ }
}

export function gufiLogInfo(): { path: string; bytes: number } {
  let bytes = 0
  try { bytes = fs.statSync(GUFI_LOG).size } catch { /* not written yet */ }
  return { path: GUFI_LOG, bytes }
}
