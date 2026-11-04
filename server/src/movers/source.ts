import type { Stats } from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { KbError, VISIBLE_DOT_DIRS } from '../kb.js'
import type { Library } from '../libraries.js'
import { ownRootOf } from '../originals.js'
import { parseSshSite, remoteRun, siteName, sshSite } from '../remoteExec.js'
import { remoteTarget, sshAllowed } from '../userLibraries.js'
import type { ManifestFile, SourceRoute } from './types.js'

/**
 * Where a library's files can be read, and what is there.
 *
 * The places are those read_original tries, in the same order: the
 * library's own location, then the host named under Originals via, with
 * that host's `root` in place of the library's own folder. What a job
 * copies comes from the source itself (listSource), not from the index,
 * which can lag behind it; the destination check compares against this.
 *
 * Nothing here trusts a path. A model's paths are held to relCheck first;
 * a name from a host is checked as if it came from the model, since a host
 * is only as trustworthy as whoever can write to it; and links are not
 * followed, so a link inside a library cannot reach outside it.
 */

/** Files one transfer may hold: the list is kept in memory and walked twice. */
export const MAX_FILES = 50_000
const LIST_MS = 10 * 60_000
const LOOK_MS = 45_000

/** A path a person or a model gave, as segments under a library's root.
 *  Throws with the reason. Only `..`, an absolute path, a NUL and the root
 *  itself are refused: spaces, quotes, newlines and leading dashes are legal
 *  in a file name, and no name reaches a shell or an option unprotected. */
export function relCheck(v: unknown): string {
  if (typeof v !== 'string' || !v) throw new KbError(400, 'each path must be a path relative to the library\'s root')
  if (v.length > 4096 || v.includes('\0')) throw new KbError(400, 'a path is too long or holds a NUL')
  if (v.startsWith('/') || v.startsWith('~')) throw new KbError(400, 'paths are relative to the library\'s root, as search gives them, not absolute')
  const parts = v.split('/').filter(p => p && p !== '.')
  if (parts.includes('..')) throw new KbError(400, 'a path may not contain ".."')
  if (!parts.length) throw new KbError(400, 'name a file or folder inside the library, not the library itself')
  return parts.join('/')
}

const hiddenName = (name: string) => name.startsWith('.') && !VISIBLE_DOT_DIRS.has(name)

/** Whether the path holds a segment the Library does not show. */
export function hiddenPath(rel: string): boolean { return rel.split('/').some(hiddenName) }

/** A route with what it must not repeat left out, for a record or a plan. */
export function publicRoute(r: SourceRoute): { kind: SourceRoute['kind']; name: string; via: SourceRoute['via']; root: string } {
  return { kind: r.kind, name: r.name, via: r.via, root: r.root }
}

/** Identifies where a route reads, so an approval given for one place does
 *  not run against another. The key is left out: it says who, not where. */
export function routeId(r: SourceRoute): string {
  return JSON.stringify(r.kind === 'ssh' ? ['ssh', r.site, r.root, r.via] : ['local', r.root])
}

/** The places a library's files may be read, best first, and for each that
 *  cannot be used, why. Asks nothing of any host. */
export function sourceRoutes(lib: Library, viewerId: string | null): { routes: SourceRoute[]; tried: string[] } {
  const routes: SourceRoute[] = []
  const tried: string[] = []
  if (lib.site) {
    try {
      const t = remoteTarget(lib.indexRoot)
      if (t?.sourceRoot) routes.push({ kind: 'ssh', site: t.site, key: t.key, root: t.sourceRoot, name: siteName(t.site), via: 'library' })
      else tried.push(`${lib.site}: the library's folder there is not known`)
    } catch (e) { tried.push(`${lib.site}: ${(e as Error).message}`) }
  } else if (lib.sourceRoot) {
    routes.push({ kind: 'local', root: lib.sourceRoot, name: 'this computer', via: 'library' })
  } else tried.push('this computer: this library has no files here')
  const rt = lib.readThrough
  if (rt) {
    const name = `${rt.destination}${rt.port ? `:${rt.port}` : ''}`
    const root = rt.root || ownRootOf(lib)
    if (!sshAllowed(viewerId)) tried.push(`${name}: another host is read with the ssh keys of the account the Studio runs as, so only a Studio with sign-in off uses one`)
    else if (!root) tried.push(`${name}: the library's folder on that host is not known`)
    else {
      try {
        routes.push({ kind: 'ssh', site: sshSite(rt.destination, rt.port ?? null, rt.sshOptions ?? []), key: '', root, name, via: 'read-through' })
      } catch (e) { tried.push(`${name}: ${(e as Error).message}`) }
    }
  }
  return { routes, tried }
}

/** What Settings says about how a library's files are copied: read from the
 *  same list a plan uses, so the two cannot disagree. */
export function routeSummary(lib: Library, viewerId: string | null): string | null {
  const { routes } = sourceRoutes(lib, viewerId)
  if (!routes.length) return null
  return routes.map(r => (r.kind === 'local' ? 'this computer' : `ssh from ${r.name}${r.via === 'read-through' ? ' (Originals via)' : ''}`)).join(', then ')
}

export type ItemKind = 'file' | 'dir'

export interface Listing {
  files: ManifestFile[]
  bytes: number
  /** Links and special files, which are not copied. */
  links: number
  /** Dot files and folders, which the Library does not show either. */
  hidden: number
  /** Entries that could not be read, or whose name is not valid text. */
  unreadable: string[]
}

/** The path an item's file has at the destination: the item's own name, then
 *  the rest of the path below it. */
function outOf(item: string, rel: string): string {
  const base = item.slice(item.lastIndexOf('/') + 1)
  return rel === item ? base : `${base}/${rel.slice(item.length + 1)}`
}

/** A file's path from a host, held to the item it should be under. */
function underItem(item: string, rel: string): boolean {
  if (rel !== item && !rel.startsWith(`${item}/`)) return false
  return !rel.split('/').some(p => !p || p === '.' || p === '..')
}

const missing = (where: string, rel: string) => new KbError(404, `${where}: no such file or folder: ${rel}`)
const escapes = (rel: string) => new KbError(400, `${rel} leads outside the library, through a link. Nothing was copied.`)
const overLimit = () => new KbError(413, `This is more than ${MAX_FILES.toLocaleString('en-US')} files, which is the most one transfer takes. Copy a smaller folder.`)

// ---- on this computer ----

/** The real path of an item, checked to be inside the library's real root
 *  and not itself a link. */
async function localItem(rootReal: string, rel: string, where: string): Promise<{ abs: string; st: Stats }> {
  const abs = path.join(rootReal, rel)
  let parent: string
  try { parent = await fsp.realpath(path.dirname(abs)) } catch { throw missing(where, rel) }
  if (parent !== rootReal && !parent.startsWith(rootReal + path.sep)) throw escapes(rel)
  const real = path.join(parent, path.basename(abs))
  const st = await fsp.lstat(real).catch(() => { throw missing(where, rel) })
  if (st.isSymbolicLink()) throw escapes(rel)
  if (!st.isFile() && !st.isDirectory()) throw new KbError(400, `${rel} is not a file or folder.`)
  return { abs: real, st }
}

async function listLocal(route: SourceRoute, items: string[]): Promise<Listing> {
  const out: Listing = { files: [], bytes: 0, links: 0, hidden: 0, unreadable: [] }
  const rootReal = await fsp.realpath(route.root).catch(() => { throw new KbError(404, `${route.name}: the library's folder is not there`) })
  const add = (item: string, rel: string, st: { size: number; mtimeMs: number; mode: number }) => {
    if (out.files.length >= MAX_FILES) throw overLimit()
    out.files.push({ rel, out: outOf(item, rel), size: st.size, mtime: Math.floor(st.mtimeMs / 1000), mode: st.mode & 0o777 })
    out.bytes += st.size
  }
  const walk = async (item: string, rel: string, abs: string): Promise<void> => {
    let names: string[]
    try { names = await fsp.readdir(abs) } catch { out.unreadable.push(rel); return }
    for (const name of names.sort()) {
      if (hiddenName(name)) { out.hidden++; continue }
      const childRel = `${rel}/${name}`, child = path.join(abs, name)
      let st
      try { st = await fsp.lstat(child) } catch { out.unreadable.push(childRel); continue }
      if (st.isDirectory()) await walk(item, childRel, child)
      else if (st.isFile()) add(item, childRel, st)
      else out.links++
    }
  }
  for (const item of items) {
    const { abs, st } = await localItem(rootReal, item, route.name)
    if (st.isDirectory()) await walk(item, item, abs)
    else add(item, item, st)
  }
  return out
}

// ---- over ssh ----

// Runs as a script on the host: every path is an argument, sent encoded by
// the session loop, and never read by a shell. Records are NUL separated, so
// any file name survives. stat differs: GNU and busybox take -c, macOS and
// BSD -f. One stat per file is slower than GNU find's -printf, which macOS
// does not have; this one path runs on both, and it runs in a job, off the
// chat. `cd -P` and `pwd -P` give the real folders, so a folder that is a
// link out of the library is caught by the same test as a named one.
const LIST_SCRIPT = `
root=$1; max=$2; walk=$3; shift 3
if stat -c %s / >/dev/null 2>&1; then st() { stat -c '%s %Y %a' -- "$1"; }; else st() { stat -f '%z %m %Lp' -- "$1"; }; fi
cd -P -- "$root" 2>/dev/null || exit 3
R=$(pwd -P); R=\${R%/}
n=0
one() {
  r=\${1#"$R"/}
  if [ -L "$1" ] || [ ! -f "$1" ]; then printf 'L\\0%s\\0' "$r"; return 0; fi
  n=$((n+1)); [ "$n" -le "$max" ] || { printf 'X\\0'; exit 0; }
  if s=$(st "$1" 2>/dev/null); then printf 'F\\0%s\\0%s\\0' "$s" "$r"; else printf 'U\\0%s\\0' "$r"; fi
}
for rel in "$@"; do
  abs=$R/$rel
  P=$(cd -P -- "\${abs%/*}" 2>/dev/null && pwd -P) || { printf 'M\\0%s\\0' "$rel"; continue; }
  case "$P/" in "$R"/*) ;; *) printf 'E\\0%s\\0' "$rel"; continue ;; esac
  if [ -L "$abs" ]; then printf 'E\\0%s\\0' "$rel"
  elif [ -d "$abs" ]; then
    if [ "$walk" = 0 ]; then printf 'D\\0%s\\0' "$rel"; continue; fi
    while IFS= read -r -d '' f; do
      case "\${f##*/}" in .agents) ;; .*) printf 'H\\0%s\\0' "\${f#"$R"/}"; continue ;; esac
      one "$f"
    done < <(find "$abs" -mindepth 1 \\( -name '.*' ! -name .agents \\) -prune -print0 -o \\( -type f -o -type l \\) -print0)
  elif [ -f "$abs" ]; then
    if [ "$walk" = 0 ]; then printf 'I\\0%s\\0' "$rel"; else one "$abs"; fi
  else printf 'M\\0%s\\0' "$rel"
  fi
done
`.trimStart()

/** The fields of a listing, with each name kept as its bytes, so a name that
 *  is not valid text is noticed rather than turned into another name. */
function fields(raw: Buffer): Buffer[] {
  const out: Buffer[] = []
  let at = 0
  for (let i = raw.indexOf(0); i >= 0; i = raw.indexOf(0, at)) { out.push(raw.subarray(at, i)); at = i + 1 }
  return out
}
const textOf = (b: Buffer): string | null => { const s = b.toString('utf8'); return Buffer.from(s).equals(b) ? s : null }

/** Runs the listing on the host. Throws a KbError that names the host when
 *  it cannot be asked. */
async function askHost(route: Extract<SourceRoute, { kind: 'ssh' }>, items: string[], walk: boolean, timeoutMs: number): Promise<{ raw: Buffer; stderr: string }> {
  const r = await remoteRun(route.site, route.key, ['bash', '-c', LIST_SCRIPT, '_', route.root, String(MAX_FILES), walk ? '1' : '0', ...items], timeoutMs)
  if (r.code === 3) throw new KbError(404, `${route.name}: the library's folder ${route.root} is not there`)
  if (r.code !== 0) throw new KbError(502, `${route.name}: ${r.stderr.trim().split('\n').pop() || `the listing ended with status ${r.code}`}`)
  return { raw: r.raw, stderr: r.stderr }
}

async function listSsh(route: Extract<SourceRoute, { kind: 'ssh' }>, items: string[]): Promise<Listing> {
  const out: Listing = { files: [], bytes: 0, links: 0, hidden: 0, unreadable: [] }
  const { raw, stderr } = await askHost(route, items, true, LIST_MS)
  const f = fields(raw)
  const itemOf = (rel: string) => items.find(i => underItem(i, rel))
  for (let i = 0; i < f.length;) {
    const tag = f[i++].toString('latin1')
    if (tag === 'X') throw overLimit()
    if (tag === 'F') {
      const [size, mtime, mode] = f[i++].toString('latin1').split(' ').map(Number)
      const name = f[i++]
      const rel = name ? textOf(name) : null
      if (rel === null) { out.unreadable.push(name?.toString('utf8') ?? '(no name)'); continue }
      const item = itemOf(rel)
      if (!item) throw new KbError(502, `${route.name} listed a path that is not under what was asked for. Nothing was copied.`)
      if (!Number.isSafeInteger(size) || size < 0) { out.unreadable.push(rel); continue }
      if (out.files.length >= MAX_FILES) throw overLimit()
      out.files.push({ rel, out: outOf(item, rel), size, mtime: mtime || 0, mode: (Number.parseInt(String(mode), 8) || 0o644) & 0o777 })
      out.bytes += size
      continue
    }
    const name = f[i++]?.toString('utf8') ?? ''
    if (tag === 'L') out.links++
    else if (tag === 'H') out.hidden++
    else if (tag === 'U') out.unreadable.push(name)
    else if (tag === 'E') throw escapes(name)
    else if (tag === 'M') throw missing(route.name, name)
    else throw new KbError(502, `${route.name} gave a listing that could not be read.`)
  }
  // find says on stderr which folders it could not open.
  for (const line of stderr.split('\n').filter(l => /^find: /.test(l)).slice(0, 20)) out.unreadable.push(line.replace(/^find: /, ''))
  return out
}

/** The files a job copies, read at the source. Throws, with the reason, when
 *  a path is missing, leads outside the library, or there are too many. */
export function listSource(route: SourceRoute, items: string[]): Promise<Listing> {
  return route.kind === 'local' ? listLocal(route, items) : listSsh(route, items)
}

/** Whether each item is there on the route, and what it is: one short round
 *  trip, for a plan. Throws as listSource does. */
export async function lookAt(route: SourceRoute, items: string[]): Promise<Map<string, ItemKind>> {
  const kinds = new Map<string, ItemKind>()
  if (route.kind === 'local') {
    const rootReal = await fsp.realpath(route.root).catch(() => { throw new KbError(404, `${route.name}: the library's folder is not there`) })
    for (const item of items) kinds.set(item, (await localItem(rootReal, item, route.name)).st.isDirectory() ? 'dir' : 'file')
    return kinds
  }
  const f = fields((await askHost(route, items, false, LOOK_MS)).raw)
  for (let i = 0; i + 1 < f.length; i += 2) {
    const tag = f[i].toString('latin1'), name = f[i + 1].toString('utf8')
    if (tag === 'E') throw escapes(name)
    if (tag === 'M') throw missing(route.name, name)
    if (tag === 'D' || tag === 'I') kinds.set(name, tag === 'D' ? 'dir' : 'file')
  }
  for (const item of items) if (!kinds.has(item)) throw missing(route.name, item)
  return kinds
}

/** The host name a plan shows for a route. */
export function routeLabel(r: SourceRoute): string {
  if (r.kind === 'local') return `${r.root} on this computer`
  const h = parseSshSite(r.site)
  return `${r.root} on ${r.name}${h ? ' over ssh' : ' over pw ssh'}${r.via === 'read-through' ? ' (Originals via)' : ''}`
}
