import fs from 'node:fs'
import fsp from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { authEnabled } from '../auth.js'
import { INDEX_BASE } from '../config.js'
import { KbError } from '../kb.js'
import { effectiveSettings } from '../settings.js'
import type { ManifestFile } from './types.js'

/**
 * Where copies land, and how they become files a person can see.
 *
 * A destination is a folder on this computer, under one of the allowed
 * folders: the person's Downloads, Desktop and Documents, and the ones named
 * in Settings, General, AI features or in STUDIO_TRANSFER_FOLDERS. A model can pick among
 * those and nothing else, and nothing a model can reach adds to the list.
 * Where the Studio signs people in, this computer is a server and those
 * folders are not any one person's, so only what the deployment names in
 * STUDIO_TRANSFER_FOLDERS is allowed.
 *
 * A job writes into a hidden folder of its own beside the final place
 * (stagingOf). Nothing appears under its real name until every file is
 * there and has been checked, so a copy that stops part way leaves nothing
 * that looks complete, and the only thing removed afterwards is that folder.
 */

const STAGE_PREFIX = '.studio-transfer-'

/** The person's Downloads folder: XDG_DOWNLOAD_DIR from
 *  ~/.config/user-dirs.dirs where that names one, else ~/Downloads. */
export function downloadsFolder(home = os.homedir()): string {
  try {
    const m = /^XDG_DOWNLOAD_DIR="(.+)"\s*$/m.exec(fs.readFileSync(path.join(home, '.config', 'user-dirs.dirs'), 'utf8'))
    const named = m?.[1].replace(/^\$HOME(?=\/|$)/, home)
    if (named && path.isAbsolute(named) && named !== home) return path.normalize(named)
  } catch { /* the usual place */ }
  return path.join(home, 'Downloads')
}

/** The folders copies may go to, or below. */
export function allowedFolders(): string[] {
  const env = String(process.env.STUDIO_TRANSFER_FOLDERS ?? '').split(':').map(s => s.trim()).filter(s => path.isAbsolute(s))
  if (authEnabled()) return env
  const home = os.homedir()
  return [...new Set([downloadsFolder(home), path.join(home, 'Desktop'), path.join(home, 'Documents'), ...env, ...(effectiveSettings().transferFolders ?? []).map(expandHome)])]
}

/** ~ and ~/x as the person's home, anything else as it is. */
export function expandHome(p: string): string {
  return p === '~' ? os.homedir() : p.startsWith('~/') ? path.join(os.homedir(), p.slice(2)) : p
}

/** A destination given as a bare name, as a person says it: the allowed
 *  folder of that name, Documents for ~/Documents. Anything else as given. */
export function namedFolder(p: string): string {
  if (p.includes('/')) return p
  return allowedFolders().find(f => path.basename(f).toLowerCase() === p.toLowerCase()) ?? p
}

/** The folder a transfer goes to when none is named: Downloads, or where the
 *  Studio signs people in, the first folder the deployment allows. */
export function defaultFolder(): string | null {
  return authEnabled() ? allowedFolders()[0] ?? null : downloadsFolder()
}

/** How a folder reads in a plan: ~ for the home folder. */
export function shownFolder(p: string): string {
  const home = os.homedir()
  return p === home ? '~' : p.startsWith(home + path.sep) ? `~/${p.slice(home.length + 1)}` : p
}

/** The real path of the deepest part of a path that exists, with the rest
 *  of the path after it: a link anywhere along the way is resolved, so it
 *  cannot carry a destination out of an allowed folder. */
async function realAsFar(p: string): Promise<{ real: string; existing: string }> {
  let at = p
  const rest: string[] = []
  for (;;) {
    try {
      const existing = await fsp.realpath(at)
      return { real: path.join(existing, ...rest), existing }
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'ENOENT') throw new KbError(403, `${at} cannot be read: ${(e as NodeJS.ErrnoException).code}`)
      const up = path.dirname(at)
      if (up === at) throw new KbError(400, `${p} is not on this computer`)
      rest.unshift(path.basename(at))
      at = up
    }
  }
}

const inside = (p: string, root: string) => p === root || p.startsWith(root === path.sep ? root : root + path.sep)

/** Checks a destination as a model gave it. Returns the folder's real path,
 *  and whether it is there yet. Called when a plan is made and again before
 *  a job writes anything. */
export async function checkDestination(raw: string): Promise<{ folder: string; exists: boolean; existing: string }> {
  const roots = allowedFolders()
  const list = roots.map(shownFolder).join(', ') || 'none are set'
  if (/[\0-\x1f\x7f]/.test(raw) || raw.length > 4096) throw new KbError(400, 'That destination is not a folder name.')
  const p = expandHome(raw)
  if (!path.isAbsolute(p) || p.split('/').includes('..')) throw new KbError(400, `The destination must be a full path with no "..", under one of the allowed folders: ${list}.`)
  const { real, existing } = await realAsFar(path.normalize(p))
  const realRoots = await Promise.all(roots.map(async r => (await realAsFar(r)).real))
  if (!realRoots.some(r => inside(real, r))) {
    throw new KbError(403, `${shownFolder(p)} is not an allowed folder, so nothing was copied. Copies may go to: ${list}. The person can add a folder in Settings, General, AI features.`)
  }
  const data = (await realAsFar(INDEX_BASE)).real
  if (inside(real, data)) throw new KbError(403, 'The destination is inside the Studio\'s own data folder.')
  const exists = real === existing
  if (exists && !(await fsp.stat(real)).isDirectory()) throw new KbError(409, `${shownFolder(real)} is a file, not a folder.`)
  return { folder: real, exists, existing }
}

/** Free space where a folder is, or will be. Null when it cannot be read. */
export async function freeBytes(existing: string): Promise<number | null> {
  try { const s = await fsp.statfs(existing); return Number(s.bavail) * Number(s.bsize) } catch { return null }
}

/** Refuses a copy that would not fit, leaving room for the disk to go on working. */
export function assertFits(bytes: number, free: number | null, folder: string): void {
  if (free !== null && bytes + Math.min(free * 0.02, 1024 ** 3) > free) {
    throw new KbError(507, `${size(bytes)} will not fit in ${shownFolder(folder)}, which has ${size(free)} free.`)
  }
}

export function size(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let v = n, i = -1
  do { v /= 1024; i++ } while (v >= 1024 && i < units.length - 1)
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

/** The names an item list puts at the top of the destination. Two items
 *  with the same name cannot share it, and an existing name is never
 *  replaced. */
export function topNames(items: string[]): string[] {
  const names = items.map(i => i.slice(i.lastIndexOf('/') + 1))
  const twice = names.find((n, i) => names.indexOf(n) !== i)
  if (twice) throw new KbError(409, `Two of the paths are named ${twice}, which would land on the same name. Copy them one at a time, to different folders.`)
  return names
}

/** Refuses when anything at the destination already has one of the names.
 *  Copies never overwrite. */
export async function assertNoneExist(folder: string, names: string[]): Promise<void> {
  for (const n of names) {
    const there = await fsp.lstat(path.join(folder, n)).then(() => true, () => false)
    if (there) throw new KbError(409, `${shownFolder(path.join(folder, n))} already exists, and a copy never replaces anything. Nothing was copied. Choose another folder, or the person can move the existing one away.`)
  }
}

export const stagingOf = (folder: string, job: string) => path.join(folder, `${STAGE_PREFIX}${job}`)
/** The folder a mover writes into, inside the staging folder. */
export const stagedData = (stage: string) => path.join(stage, 'data')

/** Makes a job's staging folder, which must not exist yet. */
export async function makeStaging(folder: string, job: string): Promise<string> {
  await fsp.mkdir(folder, { recursive: true })
  const stage = stagingOf(folder, job)
  await fsp.mkdir(stage, { mode: 0o700 })
  await fsp.mkdir(stagedData(stage))
  return stage
}

/** Removes a staging folder, and only that: the path must have the name a
 *  job's staging folder has. fs.rm does not follow links. */
export async function removeStaging(stage: string): Promise<void> {
  if (!/^\.studio-transfer-[0-9a-f]+$/.test(path.basename(stage))) return
  await fsp.rm(stage, { recursive: true, force: true }).catch(() => {})
}

export interface Check {
  ok: boolean
  files: number
  bytes: number
  missing: string[]
  wrongSize: string[]
  extra: string[]
}

/** Walks a destination and compares it with what was listed at the source:
 *  the same files, each the same size, and nothing else. This reads the
 *  disk, not anything a mover said. A link counts as something else. */
export async function checkFiles(root: string, want: ManifestFile[]): Promise<Check> {
  const found = new Map<string, number>()
  const extra: string[] = []
  const walk = async (dir: string, rel: string): Promise<void> => {
    for (const d of await fsp.readdir(dir, { withFileTypes: true })) {
      const r = rel ? `${rel}/${d.name}` : d.name
      if (d.isDirectory()) await walk(path.join(dir, d.name), r)
      else if (d.isFile()) found.set(r, (await fsp.lstat(path.join(dir, d.name))).size)
      else extra.push(r)
    }
  }
  await walk(root, '')
  const out: Check = { ok: false, files: 0, bytes: 0, missing: [], wrongSize: [], extra }
  for (const f of want) {
    const n = found.get(f.out)
    found.delete(f.out)
    if (n === undefined) out.missing.push(f.out)
    else if (n !== f.size) out.wrongSize.push(`${f.out} (${n} bytes, expected ${f.size})`)
    else { out.files++; out.bytes += n }
  }
  out.extra.push(...found.keys())
  out.ok = !out.missing.length && !out.wrongSize.length && !out.extra.length
  return out
}

/** A check that failed, in a line, with the first few names. */
export function checkProblem(c: Check): string {
  const few = (l: string[]) => l.slice(0, 3).join(', ') + (l.length > 3 ? ` and ${l.length - 3} more` : '')
  return [
    c.missing.length ? `${c.missing.length} missing (${few(c.missing)})` : '',
    c.wrongSize.length ? `${c.wrongSize.length} of the wrong size (${few(c.wrongSize)})` : '',
    c.extra.length ? `${c.extra.length} not expected (${few(c.extra)})` : '',
  ].filter(Boolean).join('; ')
}

/** Puts a checked copy in place, never over anything: a file by a hard
 *  link, which fails if the name is taken (or, on a volume with no hard
 *  links, a rename after looking); a folder by a rename after looking. */
export async function publish(stage: string, folder: string, names: string[]): Promise<void> {
  await assertNoneExist(folder, names)
  for (const n of names) {
    const from = path.join(stagedData(stage), n), to = path.join(folder, n)
    if ((await fsp.lstat(from)).isDirectory()) { await fsp.rename(from, to); continue }
    try { await fsp.link(from, to) } catch (e) {
      const code = (e as NodeJS.ErrnoException).code
      if (code === 'EEXIST') throw new KbError(409, `${shownFolder(to)} appeared while the copy ran, and a copy never replaces anything.`)
      if (await fsp.lstat(to).then(() => true, () => false)) throw e
      await fsp.rename(from, to)
    }
  }
}

