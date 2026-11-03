import type { FastifyInstance } from 'fastify'
import type { Library } from './libraries.js'
import { getLibrary, setAddedReadThrough } from './libraries.js'
import { KbError, readFileContent, type FileContent } from './kb.js'
import { sshOptionsText, sshSite } from './remoteExec.js'
import {
  ONLY_GUFI_QUERY, connectStep, downloadRemoteFile, reachedOver, readRemoteFile, remoteDownload, remoteTarget, runner,
  setUserReadThrough, sshAdvice, sshAllowed, sshDest, type ProbeHost, type SshStep,
} from './userLibraries.js'

/**
 * Reading a library's original files, and the second place to look for them.
 *
 * A library's own location is where its files are meant to be read: the
 * folder on this computer, or the host of a library over ssh or pw ssh.
 * Some sites keep the index where GUFI runs, on a host whose login runs only
 * gufi_query, and mount the same file systems on another host, often a file
 * transfer host. A library can name that host (`readThrough`): the original
 * is tried at the library's own location first and, when that cannot give
 * it, on that host over ssh, as the user, with the same ssh machinery the
 * libraries over ssh use (remoteExec.ts: the user's keys and ~/.ssh/config,
 * a connection shared between commands, no prompts, bounded waits).
 *
 * Nothing here is read by a shell on either side. The host is a
 * destination ssh cannot take for an option (sshDest), the folder is an
 * absolute path with no `..`, and a file's path goes to the host as an
 * argument of a script, joined to the folder after the same checks every
 * remote read makes.
 */

/** Another host that has a library's files: where its originals are also read. */
export interface ReadThrough {
  /** user@host, or a Host alias from ~/.ssh/config. */
  destination: string
  port?: number | null
  /** ssh options as argv (parseSshOptions), for a jump host or a key. */
  sshOptions?: string[] | null
  /** The library's folder on that host, when its path differs from the
   *  library's own; empty means the same path as the library's. */
  root?: string | null
}

/** Why a request that sets the host is refused where the Studio signs people in. */
export const READ_THROUGH_SINGLE_USER = 'Reading originals through another host uses the ssh keys of the account the Studio runs as, so it is offered only on a Studio with sign-in off.'

/** An absolute folder on a host, with no `..` and no control characters. */
export function readThroughRoot(v: unknown): string | null {
  const s = String(v ?? '').trim()
  if (!s) return null
  if (s.length > 1024 || /[\0-\x1f\x7f]/.test(s) || !s.startsWith('/') || s.split('/').includes('..')) {
    throw new KbError(400, 'folder: an absolute path on that host, with no ".."')
  }
  const clean = s.replace(/\/+/g, '/')
  return clean.length > 1 ? clean.replace(/\/$/, '') : clean
}

/** A request's host, checked: null when no destination is given (which
 *  clears the setting). The destination is held to what ssh cannot read as
 *  an option, and the options to what libraries over ssh may carry. */
export function parseReadThrough(a: unknown): ReadThrough | null {
  const b = (a && typeof a === 'object' ? a : {}) as Record<string, unknown>
  if (!String(b.destination ?? '').trim()) return null
  const { destination, port, sshOptions } = sshDest(b)
  return { destination, port, ...(sshOptions.length ? { sshOptions } : {}), root: readThroughRoot(b.root) }
}

/** A stored setting, checked again as it is read (a library named in
 *  STUDIO_LIBRARIES is typed by hand): null, with a note in the log, when it
 *  would not pass the checks a request does. Never throws. */
const warned = new Set<string>()
export function storedReadThrough(x: unknown, library: string): ReadThrough | null {
  if (x == null) return null
  try { return parseReadThrough(x) } catch (e) {
    const note = `${library}: readThrough ignored: ${(e as Error).message}`
    if (!warned.has(note)) { warned.add(note); console.warn(note) }
    return null
  }
}

/** What Settings shows of the setting. */
export function publicReadThrough(rt: ReadThrough | null | undefined) {
  return rt ? { destination: rt.destination, port: rt.port ?? null, sshOptions: rt.sshOptions?.length ? sshOptionsText(rt.sshOptions) : null, root: rt.root ?? null } : null
}

const hostName = (rt: ReadThrough) => `${rt.destination}${rt.port ? `:${rt.port}` : ''}`

/** A library's folder where its own location holds it: on this computer, or
 *  on its own host. Null when none is known. */
export function ownRootOf(lib: Library): string | null {
  if (lib.sourceRoot) return lib.sourceRoot
  if (!lib.site) return null
  try { return remoteTarget(lib.indexRoot)?.sourceRoot ?? null } catch { return null }
}

export interface Attempt { where: string; error: string; /** The host answered and does not have the file (or has no folder for it); false when it could not be reached. */ missing: boolean }

/** An original no host could give: the message names every host tried. */
export class OriginalUnreachable extends KbError {
  constructor(status: number, message: string, public attempts: Attempt[]) { super(status, message) }
}

export interface Opened {
  file: FileContent
  /** Where it was read: "this computer", or the host's name. */
  where: string
  /** Whether it came from the library's own location or from the host named for reading originals. */
  via: 'library' | 'read-through'
}

/** Why a read failed, in a few words and without this computer's paths. */
function reason(e: unknown): string {
  const code = (e as { code?: string })?.code
  if (code === 'ENOENT' || code === 'ENOTDIR') return 'no such file'
  if (code === 'EACCES' || code === 'EPERM') return 'permission denied'
  return String((e as Error)?.message ?? e).split('\n')[0].slice(0, 300)
}
/** Whether a failure means the place was reached and does not have the file. */
const isMissing = (e: unknown) => {
  const code = (e as { code?: string })?.code
  const status = e instanceof KbError ? e.status : 0
  return code === 'ENOENT' || code === 'ENOTDIR' || status === 404 || status === 409
}
/** A remote error without the "host: " it begins with, which `where` says, and without its last full stop. */
const without = (where: string, msg: string) => (msg.startsWith(`${where}: `) ? msg.slice(where.length + 2) : msg).replace(/[.\s]+$/, '')

const READ_MS = 60_000

/**
 * Opens a library's original file: at the library's own location, then, if
 * that cannot give it, on the host the library names for reading originals.
 * A failure that says the path itself is wrong (400: outside the library's
 * folder, a directory) is final and tries nothing more. When no host has
 * the file, the error names each place tried and why, so a model, or a
 * person in the viewer, knows what to fix.
 *
 * `viewerId` is the person asking: the second host is reached with the ssh
 * keys of the account the Studio runs as, which only a Studio with sign-in
 * off lends (sshAllowed).
 */
export async function openOriginal(lib: Library, rel: string, o: { viewerId?: string | null } = {}): Promise<Opened> {
  const tried: Attempt[] = []
  // 1. The library's own location.
  if (lib.site) {
    const where = lib.site
    try {
      return { file: await readOwnRemote(lib, rel), where, via: 'library' }
    } catch (e) {
      if (e instanceof KbError && e.status === 400) throw e
      tried.push({ where, error: without(where, reason(e)), missing: isMissing(e) })
    }
  } else if (lib.sourceRoot) {
    try {
      return { file: await readFileContent(rel, lib.sourceRoot, lib.cacheRoot ?? undefined), where: 'this computer', via: 'library' }
    } catch (e) {
      if (e instanceof KbError && e.status === 400) throw e
      tried.push({ where: 'this computer', error: reason(e), missing: isMissing(e) })
    }
  } else {
    tried.push({ where: 'this computer', error: 'this library has no files here', missing: true })
  }
  // 2. The host named for reading originals.
  const rt = lib.readThrough
  if (rt) {
    const where = hostName(rt)
    try {
      if (!sshAllowed(o.viewerId)) throw new KbError(403, READ_THROUGH_SINGLE_USER)
      const root = rt.root || ownRootOf(lib)
      if (!root) throw new KbError(409, 'the library\'s folder on that host is not known: name it in Settings, Libraries')
      const site = sshSite(rt.destination, rt.port ?? null, rt.sshOptions ?? [])
      return { file: await readRemoteFile(site, '', root, rel, READ_MS), where, via: 'read-through' }
    } catch (e) {
      if (e instanceof KbError && e.status === 400) throw e
      tried.push({ where, error: without(where, reason(e)), missing: isMissing(e) })
    }
  }
  const list = tried.map(t => `${t.where}: ${t.error}`).join('; ')
  const status = tried.every(t => t.missing) ? 404 : 502
  throw new OriginalUnreachable(status, `${rel} could not be opened. Tried ${tried.length === 1 ? 'only ' : ''}${list}.`, tried)
}

async function readOwnRemote(lib: Library, rel: string): Promise<FileContent> {
  const t = remoteTarget(lib.indexRoot)!
  return readRemoteFile(t.site, t.key, t.sourceRoot, rel, READ_MS)
}

/** A whole original for download, from the same places in the same order. */
export async function downloadOriginal(lib: Library, rel: string, o: { viewerId?: string | null } = {}): Promise<{ name: string; body: Buffer }> {
  const tried: Attempt[] = []
  if (lib.site) {
    try { return await remoteDownload(lib.indexRoot, rel) } catch (e) {
      if (e instanceof KbError && (e.status === 400 || e.status === 413)) throw e
      tried.push({ where: lib.site, error: without(lib.site, reason(e)), missing: isMissing(e) })
    }
  } else tried.push({ where: 'this computer', error: 'this library has no files here', missing: true })
  const rt = lib.readThrough
  if (rt) {
    const where = hostName(rt)
    try {
      if (!sshAllowed(o.viewerId)) throw new KbError(403, READ_THROUGH_SINGLE_USER)
      const root = rt.root || ownRootOf(lib)
      if (!root) throw new KbError(409, 'the library\'s folder on that host is not known: name it in Settings, Libraries')
      return await downloadRemoteFile(sshSite(rt.destination, rt.port ?? null, rt.sshOptions ?? []), '', root, rel)
    } catch (e) {
      if (e instanceof KbError && (e.status === 400 || e.status === 413)) throw e
      tried.push({ where, error: without(where, reason(e)), missing: isMissing(e) })
    }
  }
  const status = tried.every(t => t.missing) ? 404 : 502
  throw new OriginalUnreachable(status, `${rel} could not be downloaded. Tried ${tried.map(t => `${t.where}: ${t.error}`).join('; ')}.`, tried)
}

// ---- the setting, and its connection test ----

export interface ReadThroughProbe { ok: boolean; steps: SshStep[]; root: string | null }

/**
 * The connection test for the host: ssh reaches it and it runs a shell
 * (a host whose login runs only gufi_query cannot open files), and the
 * library's folder there, the one given or the library's own path, is a
 * directory the login can read.
 */
export async function probeReadThrough(rt: ReadThrough, ownRoot: string | null): Promise<ReadThroughProbe> {
  const site = sshSite(rt.destination, rt.port ?? null, rt.sshOptions ?? [])
  const h: ProbeHost = { site, key: '', name: rt.destination, reached: await reachedOver(site, rt.destination, rt.port ?? null), advice: m => sshAdvice(m, { destination: rt.destination, port: rt.port ?? null }) }
  const run = runner(h)
  const steps: SshStep[] = []
  const hello = await connectStep(h, run, steps)
  if (!hello) {
    // A login that runs only gufi_query reaches the host but opens no files.
    const last = steps[steps.length - 1]
    if (last?.detail === ONLY_GUFI_QUERY) last.detail = `${rt.destination} runs only gufi_query for this login (no shell), so it cannot open files. Name a host whose login gives a shell, such as a file transfer host.`
    return { ok: false, steps, root: null }
  }
  const root = rt.root || ownRoot
  if (!root) {
    steps.push({ name: 'source', ok: false, detail: 'The library has no folder of its own to look for there: give the folder on that host where its files are.' })
    return { ok: false, steps, root: null }
  }
  const r = await run(['sh', '-c', 'if [ ! -d "$1" ]; then exit 3; elif [ ! -r "$1" ] || [ ! -x "$1" ]; then exit 4; fi; ls -A -- "$1" 2>/dev/null | head -n 1 | wc -l', '_', root], 45_000)
  if (r.code === 3) steps.push({ name: 'source', ok: false, detail: `${root} is not a directory on ${rt.destination}. Give the folder where this library's files are on that host.` })
  else if (r.code === 4) steps.push({ name: 'source', ok: false, detail: `The login on ${rt.destination} cannot read ${root}.` })
  else if (r.code !== 0) steps.push({ name: 'source', ok: false, detail: `${root} could not be checked on ${rt.destination}: ${r.stderr.trim().split('\n').pop() || `exit ${r.code}`}` })
  else {
    const empty = r.stdout.trim() === '0'
    steps.push({ name: 'source', ok: true, warn: empty, detail: `${root} is readable on ${rt.destination}${rt.root ? '' : ' (the same path as the library\'s own)'}${empty ? ', but it is empty there: check that the file systems are mounted on that host' : ''}.` })
  }
  return { ok: steps.every(s => s.ok), steps, root }
}

/** The library a request names, checked for a read-through host: not the
 *  main folder (its files are here), not one the deployment pinned, and not
 *  an index of a folder built on this computer. */
function settable(viewerId: string | null | undefined, id: string): Library {
  if (!sshAllowed(viewerId)) throw new KbError(403, READ_THROUGH_SINGLE_USER)
  const lib = getLibrary(id, undefined, viewerId ?? null)
  if (lib.primary) throw new KbError(409, 'The main folder is on this computer: there is nothing to read through.')
  if (lib.build && !lib.site) throw new KbError(409, `${lib.label} is an index of a folder on this computer, kept in step with it; its files are here.`)
  if (lib.pinned) throw new KbError(403, `${lib.label} is set by the deployment: add readThrough to its entry in STUDIO_LIBRARIES and restart.`)
  return lib
}

/** Tests the host for a library, as Settings asks before saving it. */
export async function testReadThrough(viewerId: string | null | undefined, id: string, body: unknown): Promise<ReadThroughProbe> {
  const lib = settable(viewerId, id)
  const rt = parseReadThrough(body)
  if (!rt) throw new KbError(400, 'destination: user@host, or a Host alias from your ~/.ssh/config')
  return probeReadThrough(rt, ownRootOf(lib))
}

/** Sets a library's host for reading originals after its connection test
 *  passes, or clears it when no destination is given. */
export async function saveReadThrough(viewerId: string | null | undefined, id: string, body: unknown): Promise<{ readThrough: ReturnType<typeof publicReadThrough>; probe?: ReadThroughProbe }> {
  const lib = settable(viewerId, id)
  const rt = parseReadThrough(body)
  let probe: ReadThroughProbe | undefined
  if (rt) {
    probe = await probeReadThrough(rt, ownRootOf(lib))
    if (!probe.ok) throw new KbError(400, `The connection test did not pass. ${probe.steps.find(s => !s.ok)?.detail ?? ''}`.trim())
  }
  const saved = lib.site ? setUserReadThrough(viewerId, id, rt) : setAddedReadThrough(id, rt)
  if (!saved) throw new KbError(404, `no library named ${id}`)
  return { readThrough: publicReadThrough(rt), ...(probe ? { probe } : {}) }
}

export async function originalsRoutes(app: FastifyInstance): Promise<void> {
  const viewer = (req: { user?: { id?: string } }) => req.user?.id ?? null
  const idOf = (req: { params: unknown }) => String((req.params as { id: string }).id)
  app.put('/api/libraries/:id/read-through', async req => saveReadThrough(viewer(req), idOf(req), req.body ?? {}))
  app.post('/api/libraries/:id/read-through/test', async req => {
    const p = await testReadThrough(viewer(req), idOf(req), req.body ?? {})
    return { ok: p.ok, steps: p.steps }
  })
}
