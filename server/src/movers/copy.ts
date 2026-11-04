import crypto from 'node:crypto'
import { constants as FS } from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { Transform, type Readable } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import { isDirect, noShell, quietStderr, remoteStream } from '../remoteExec.js'
import { registerMover, type ManifestFile, type Mover, type MoverRequest, type MoverStatus, type SourceRoute } from './types.js'

/**
 * The first mover: copies files to this computer, from a folder here or
 * over the ssh path the libraries use, one file at a time.
 *
 * Over ssh each file is one `cat` on the host, its bytes taken from ssh's
 * standard output and written here by the Studio, with up to STREAMS at
 * once on the connection the library already shares. That is the whole
 * command, chosen by what is on both ends:
 *
 * - ssh, sh and cat are on every macOS and Linux system, and the libraries
 *   over ssh already need them. rsync is not on minimal Linux images, and
 *   on macOS it is either 2.6.9 or Apple's openrsync, depending on the
 *   release: neither has the flags that keep a file name from the remote
 *   shell (--protect-args) and report overall progress (--info=progress2).
 *   It would also have to be on the host. scp and sftp need the sftp
 *   subsystem or, on older releases, pass names through the remote shell.
 * - A file name is one quoted argument of a fixed script, always an absolute
 *   path, so it cannot become an option or a command.
 * - The host chooses no path here. With rsync or tar the far end names what
 *   gets written; a host that has been tampered with could write outside the
 *   folder or plant a link. Here the Studio opens each file itself, under
 *   its own staging folder, from a list it has checked.
 * - It runs over `pw ssh` the same way, which an rsync `-e` cannot rely on.
 * - The Studio counts the bytes, so progress needs nothing from the tool.
 *
 * What it gives up is rsync's speed on many small files (each costs a round
 * trip on the shared connection, which STREAMS spreads) and resuming a
 * stopped transfer.
 */

/** Files in flight at once, over all jobs, so a few jobs stay under sshd's
 *  default of ten sessions on a connection. */
const STREAMS = Math.min(Math.max(Number(process.env.STUDIO_TRANSFER_STREAMS) || 4, 1), 8)
/** A file that sends nothing for this long has stalled. */
const STALL_MS = Number(process.env.STUDIO_TRANSFER_STALL_MS) || 120_000
/** How long a finished transfer's state is kept for the job to read. */
const KEEP_MS = 5 * 60_000
/** Login output ahead of the marker; past this, the marker is not coming. */
const MAX_PREAMBLE = 1024 * 1024

// Refuses anything that is no longer a plain file (a link put there since
// the listing, say), then prints the marker and hands over to cat.
const READ_SCRIPT = '[ -f "$1" ] && [ ! -L "$1" ] || { echo "not a plain file any more" >&2; exit 5; }; printf "%s\\n" "$2"; exec cat -- "$1"'

let free = STREAMS
const waiting: (() => void)[] = []
const slot = () => free > 0 ? (free--, Promise.resolve()) : new Promise<void>(r => waiting.push(r))
const unslot = () => { const next = waiting.shift(); if (next) next(); else free++ }

interface Running { status: MoverStatus; stops: Set<() => void>; stopped: boolean }
const running = new Map<string, Running>()

/** Drops everything up to and including the marker: a login shell's rc files
 *  can print on standard output, ahead of the file. */
function afterMarker(marker: string): Transform {
  const mark = Buffer.from(`${marker}\n`)
  let seen = false
  let head = Buffer.alloc(0)
  return new Transform({
    transform(chunk: Buffer, _enc, done) {
      if (seen) { done(null, chunk); return }
      head = Buffer.concat([head, chunk])
      const at = head.indexOf(mark)
      if (at >= 0) { seen = true; const rest = head.subarray(at + mark.length); head = Buffer.alloc(0); done(null, rest); return }
      done(head.length > MAX_PREAMBLE ? new Error('the host sent output in place of the file') : null)
    },
    flush(done) { done(seen ? null : new Error('the host did not start sending the file')) },
  })
}

/** Writes a readable to `tmp`, counting bytes, and ending on stall or stop. */
async function writeOut(t: Running, src: Readable, tmp: string, f: ManifestFile, stall: () => void): Promise<void> {
  // wx: fails rather than opens anything already there, a link included.
  const fh = await fsp.open(tmp, 'wx', (f.mode & 0o777) | 0o600)
  let timer = setTimeout(stall, STALL_MS)
  const count = new Transform({
    transform(chunk: Buffer, _enc, done) {
      clearTimeout(timer)
      timer = setTimeout(stall, STALL_MS)
      t.status.bytes = (t.status.bytes ?? 0) + chunk.length
      done(null, chunk)
    },
  })
  try {
    // The stream closes the file when it ends or fails.
    await pipeline(src, count, fh.createWriteStream())
    if (f.mtime) await fsp.utimes(tmp, f.mtime, f.mtime).catch(() => {})
  } finally {
    clearTimeout(timer)
  }
}

async function fromHost(t: Running, route: Extract<SourceRoute, { kind: 'ssh' }>, f: ManifestFile, tmp: string, marker: string): Promise<void> {
  const abs = path.posix.join(route.root, f.rel)
  const child = remoteStream(route.site, route.key, ['sh', '-c', READ_SCRIPT, '_', abs, marker])
  const stop = () => child.kill()
  t.stops.add(stop)
  let err = ''
  child.stderr.on('data', d => { err = (err + String(d)).slice(-2000) })
  child.stdin.on('error', () => {})
  child.stdin.end()
  const exited = new Promise<number | null>(resolve => {
    child.on('error', () => resolve(-1))
    child.on('close', code => resolve(code))
  })
  let stalled = false
  try {
    await writeOut(t, child.stdout.pipe(afterMarker(marker)), tmp, f, () => { stalled = true; child.kill() })
  } catch (e) {
    child.kill()
    await exited
    if (stalled) throw new Error(`${f.rel}: nothing arrived for ${Math.round(STALL_MS / 1000)} s`)
    throw new Error(`${f.rel}: ${quietStderr(err).trim().split('\n').pop() || (e as Error).message}`)
  } finally { t.stops.delete(stop) }
  const code = await exited
  if (code !== 0) throw new Error(`${f.rel}: ${quietStderr(err).trim().split('\n').pop() || `the copy ended with status ${code}`}`)
}

async function fromHere(t: Running, root: string, f: ManifestFile, tmp: string): Promise<void> {
  // The listing's real root; O_NOFOLLOW so a link put there since is refused.
  const fh = await fsp.open(path.join(await fsp.realpath(root), f.rel), FS.O_RDONLY | FS.O_NOFOLLOW)
  try {
    if (!(await fh.stat()).isFile()) throw new Error(`${f.rel} is no longer a plain file`)
    const src = fh.createReadStream()
    const stop = () => src.destroy(new Error('cancelled'))
    t.stops.add(stop)
    try { await writeOut(t, src, tmp, f, () => src.destroy(new Error(`${f.rel}: stalled`))) } finally { t.stops.delete(stop) }
  } finally { await fh.close().catch(() => {}) }
}

async function runCopy(t: Running, req: MoverRequest): Promise<void> {
  const marker = `STUDIO-${crypto.randomBytes(12).toString('hex')}`
  const tmpDir = path.join(path.dirname(req.dest), 'tmp')
  await fsp.mkdir(tmpDir, { recursive: true })
  let next = 0
  let failed: Error | null = null
  const worker = async () => {
    for (;;) {
      if (failed || t.stopped) return
      const i = next++
      if (i >= req.files.length) return
      const f = req.files[i]
      await slot()
      try {
        if (t.stopped) return
        // Written under a number, in a folder of its own, and put under its
        // name only when it has all arrived.
        const tmp = path.join(tmpDir, String(i))
        if (req.source.kind === 'ssh') await fromHost(t, req.source, f, tmp, marker)
        else await fromHere(t, req.source.root, f, tmp)
        const to = path.join(req.dest, f.out)
        await fsp.mkdir(path.dirname(to), { recursive: true })
        await fsp.rename(tmp, to)
        t.status.files = (t.status.files ?? 0) + 1
      } catch (e) {
        failed ??= e as Error
        for (const stop of t.stops) stop()
      } finally { unslot() }
    }
  }
  await Promise.all(Array.from({ length: Math.min(STREAMS, req.files.length) }, worker))
  if (failed && !t.stopped) throw failed
}

export const copyMover: Mover = {
  id: 'studio-copy',
  label: 'Studio copy',
  pollMs: 250,
  canReach(source) {
    if (source.kind === 'local') return { ok: true }
    if (isDirect(source.site)) return { ok: false, why: noShell(source.site) }
    return { ok: true }
  },
  async start(req) {
    const id = crypto.randomUUID()
    const t: Running = { status: { state: 'running', bytes: 0, files: 0 }, stops: new Set(), stopped: false }
    running.set(id, t)
    void runCopy(t, req).then(
      () => { t.status.state = t.stopped ? 'cancelled' : 'done' },
      e => { t.status.state = 'failed'; t.status.message = String((e as Error).message ?? e).slice(0, 500) },
    ).finally(() => { setTimeout(() => running.delete(id), KEEP_MS).unref?.() })
    return id
  },
  async status(id) { return { ...(running.get(id)?.status ?? { state: 'lost' }) } },
  async cancel(id) {
    const t = running.get(id)
    if (!t) return false
    t.stopped = true
    for (const stop of t.stops) stop()
    return true
  },
}

registerMover(copyMover)
