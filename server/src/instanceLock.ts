import fs from 'node:fs'
import path from 'node:path'
import { execFileSync } from 'node:child_process'
import { INDEX_BASE } from './config.js'
import { KbError } from './kb.js'

/**
 * One Studio at a time does index work in an index folder. A build writes a
 * staging folder, moves the old tree aside and the new one in, and a resumed
 * build clears the staging folder it finds, so a second Studio started on the
 * same folder while the first built (the app opened twice, a restart that
 * overlapped its predecessor, a second port) took the first one's tree from
 * under it: the first build ended "failed" with the index moved aside. The
 * Studio that holds the folder (a file there naming its process) runs the
 * background sync and the index jobs; a second one still searches and
 * queries, and refuses index jobs, saying which process holds the folder.
 */

const LOCK = path.join(INDEX_BASE, '.studio-instance')

interface Holder { pid: number; port: number | null; startedAt: string }

let held = false
let other: Holder | null = null

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))

function read(): Holder | null {
  try {
    const h = JSON.parse(fs.readFileSync(LOCK, 'utf8')) as Holder
    return Number.isInteger(h.pid) && h.pid > 0 ? h : null
  } catch { return null }
}

/** Whether a process is a Studio still running: it exists, and is a node
 *  running main.js or the Go server (studio-server), which a process that
 *  was given the number of one that died is not. */
function isStudio(pid: number): boolean {
  try { process.kill(pid, 0) } catch (e) { if ((e as NodeJS.ErrnoException).code !== 'EPERM') return false }
  try {
    const cmd = execFileSync('ps', ['-o', 'command=', '-p', String(pid)], { encoding: 'utf8', timeout: 3000 })
    return (/\bnode\b/.test(cmd) && /main\.(js|ts)\b/.test(cmd)) || /\bstudio-server\b/.test(cmd)
  } catch { return false }
}

/**
 * Takes the index folder for this process. A Studio that is still stopping
 * (a restart, or start.sh's kill and start) is waited for, up to `waitMs`;
 * one that keeps it is named, and this one goes on without index jobs.
 */
export async function acquireIndexLock(log: (m: string) => void, waitMs = 20_000): Promise<boolean> {
  const mine = JSON.stringify({ pid: process.pid, port: Number(process.env.PORT) || null, startedAt: new Date().toISOString() } satisfies Holder)
  const deadline = Date.now() + waitMs
  fs.mkdirSync(INDEX_BASE, { recursive: true })
  for (;;) {
    const h = read()
    if (!h || h.pid === process.pid || !isStudio(h.pid)) {
      try {
        // A lock that is not one (damaged, or of a process that is gone) is cleared; a free folder has nothing to clear.
        if (h || fs.existsSync(LOCK)) fs.rmSync(LOCK, { force: true })
        fs.writeFileSync(LOCK, mine, { flag: 'wx' })
        held = true
        other = null
        process.once('exit', () => { if (read()?.pid === process.pid) fs.rmSync(LOCK, { force: true }) })
        return true
      } catch (e) {
        // Another Studio wrote it between the look and the write: look again.
        if ((e as NodeJS.ErrnoException).code !== 'EEXIST') { log(`index folder lock not written: ${(e as Error).message}`); held = true; return true }
      }
    }
    if (Date.now() >= deadline) {
      other = read()
      if (other && other.pid !== process.pid) {
        log(`another Studio (process ${other.pid}${other.port ? `, port ${other.port}` : ''}) is using the index folder ${INDEX_BASE}; this one searches and queries but does no index work`)
        return false
      }
      held = true; other = null
      return true
    }
    await sleep(300)
  }
}

/** Whether this process may change the index folder. A process that never
 *  asked (a test of a module) is allowed. */
export function indexWorkAllowed(): boolean { return other === null }

/** Throws the reason when another Studio holds the index folder. */
export function requireIndexWork(): void {
  if (other) throw new KbError(409, `Another Studio (process ${other.pid}${other.port ? `, port ${other.port}` : ''}) is using this index folder, and indexes it. Stop it, or use this one only to search and query.`)
}

/** For tests: forget who holds the folder. */
export function resetIndexLockForTests(): void { held = false; other = null; fs.rmSync(LOCK, { force: true }) }
export const indexLockHeld = () => held
