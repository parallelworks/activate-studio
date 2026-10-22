import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { authEnabled } from './auth.js'
import { entryStats, treeStats } from './gufi.js'
import { KbError, resolveKb } from './kb.js'
import { getLibrary, PRIMARY_ID, type Library } from './libraries.js'
import { ALL, scopeLibraries } from './libraryScope.js'
import { parseSshSite, siteName } from './remoteExec.js'
import { effectiveSettings } from './settings.js'
import { effectiveTags, tagMaps, TAG_XATTR } from './tags.js'
import { loadUserLibraries, ownerOf, remoteTarget } from './userLibraries.js'

/**
 * What the Library view's side panel shows for the folder or file selected
 * in the tree, read from the library's GUFI index: for a folder, its size,
 * file and folder counts, the latest change below it, the extensions
 * taking the most space, and the labels applied below it; for a file, its
 * size, modified time and labels; for both, which library and index it
 * comes from. One aggregate gufi_query per folder (two with labels), run
 * where the index is, and kept for a short while, so moving through the
 * tree stays quick.
 */

export interface Details {
  path: string
  name: string
  type: 'dir' | 'file'
  library: { id: string; label: string; kind: string; site: string | null; writable: boolean }
  /** Where the index is: a path here, or host:path for a remote one. Left
   *  out where users sign in, for indexes on the server. */
  index: string | null
  fullText: boolean
  size: number
  files: number
  dirs: number
  /** Seconds. For a folder, the latest change of anything below it. */
  mtime: number
  types: { ext: string; count: number; bytes: number }[]
  labels: { own: string[]; inherited: string[]; within: { label: string; count: number }[] }
  ms: number
}

const TTL_MS = 20_000
const cache = new Map<string, { at: number; value: Details }>()

function cleanRel(rel: string): string {
  const parts = String(rel ?? '').split('/').filter(p => p && p !== '.')
  if (parts.some(p => p === '..' || p.includes('\0'))) throw new KbError(400, 'invalid path')
  return parts.join('/')
}

/** A library's kind in a few plain words, as Settings, Libraries names it. */
function kindOf(lib: Library, viewerId: string | null): string {
  if (lib.primary) return 'Main folder'
  if (lib.build) return 'Built from a folder on this computer'
  if (!lib.site) return 'GUFI index on this computer'
  const owner = ownerOf(viewerId)
  const d = owner ? loadUserLibraries(owner).find(x => x.id === lib.id) : null
  if (d?.build) return `Built on ${lib.site} over ${d.transport === 'ssh' ? 'ssh' : 'pw ssh'}`
  if (d?.transport === 'ssh') return `GUFI index on ${lib.site} over ssh`
  if (d?.existing) return `GUFI index on ${lib.site} over pw ssh`
  return `Built on ${lib.site} by the index workflow`
}

/** Where a library's index is, for the panel. */
function indexOf(lib: Library): string | null {
  if (lib.site) {
    const t = remoteTarget(lib.indexRoot)
    return t ? `${parseSshSite(t.site)?.destination ?? siteName(t.site)}:${t.indexRoot}` : null
  }
  return authEnabled() ? null : lib.indexRoot
}

export async function describe(lib: Library, sub: string, viewerId: string | null): Promise<Details> {
  const key = `${viewerId ?? ''}|${lib.id}|${sub}`
  const hit = cache.get(key)
  if (hit && Date.now() - hit.at < TTL_MS) return hit.value
  const t0 = Date.now()
  const name = sub ? path.posix.basename(sub) : lib.label
  const base = {
    path: sub, name,
    library: { id: lib.id, label: lib.label, kind: kindOf(lib, viewerId), site: lib.site ?? null, writable: lib.writable },
    index: indexOf(lib), fullText: lib.caps.fullText,
  }
  const dir = sub.includes('/') ? sub.slice(0, sub.lastIndexOf('/')) : ''
  // A file has an entry in its folder's index; a folder has a db.db of its
  // own. On a remote system the entry is asked for once and decides.
  let entry: Awaited<ReturnType<typeof entryStats>> | undefined
  let isDir: boolean
  if (lib.site) {
    entry = sub ? await entryStats(lib.indexRoot, dir, name, TAG_XATTR).catch(() => null) : null
    isDir = !entry
  } else {
    isDir = fs.existsSync(path.join(lib.indexRoot, sub, 'db.db'))
      || (!!lib.sourceRoot && (() => { try { return fs.statSync(resolveKb(sub, lib.sourceRoot!)).isDirectory() } catch { return false } })())
    if (isDir && !fs.existsSync(path.join(lib.indexRoot, sub, 'db.db'))) throw new KbError(404, `${name} is not in the index yet`)
  }
  let value: Details
  if (isDir) {
    const st = await treeStats(lib.indexRoot, sub, TAG_XATTR)
    let own: string[] = [], inherited: string[] = [], within = st.labels
    if (lib.primary) {
      ;({ own, inherited } = await effectiveTags(sub))
      within = await labelsBelow(sub)
    }
    value = { ...base, type: 'dir', size: st.bytes, files: st.files, dirs: st.dirs, mtime: st.mtime, types: st.byExt.slice(0, 5), labels: { own, inherited, within }, ms: 0 }
  } else {
    const e = entry !== undefined ? entry : await entryStats(lib.indexRoot, dir, name, TAG_XATTR).catch(() => null)
    let size = e?.size ?? 0, mtime = e?.mtime ?? 0
    if (!e && lib.sourceRoot) {
      // Not indexed yet (an upload a moment ago): the file itself.
      try { const s = fs.statSync(resolveKb(sub, lib.sourceRoot)); size = s.size; mtime = Math.floor(s.mtimeMs / 1000) } catch { throw new KbError(404, `no such file: ${sub}`) }
    } else if (!e) throw new KbError(404, `${name} is not in the index`)
    const tags = lib.primary ? await effectiveTags(sub) : { own: e?.labels ?? [], inherited: [] }
    const ext = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1).toLowerCase() : ''
    value = { ...base, type: 'file', size, files: 1, dirs: 0, mtime, types: [{ ext: ext || '(none)', count: 1, bytes: size }], labels: { ...tags, within: [] }, ms: 0 }
  }
  value.ms = Date.now() - t0
  cache.set(key, { at: Date.now(), value })
  if (cache.size > 300) for (const k of cache.keys()) { cache.delete(k); if (cache.size <= 200) break }
  return value
}

/** The main folder's labels below a folder, with the label overlay, from
 *  the maps search uses. */
async function labelsBelow(sub: string): Promise<{ label: string; count: number }[]> {
  const maps = await tagMaps()
  const counts = new Map<string, number>()
  for (const m of [maps.dirTags, maps.fileTags]) {
    for (const [p, tags] of m) {
      if (sub && p !== sub && !p.startsWith(`${sub}/`)) continue
      for (const t of tags) counts.set(t, (counts.get(t) ?? 0) + 1)
    }
  }
  return [...counts].map(([label, count]) => ({ label, count })).sort((a, b) => b.count - a.count).slice(0, 12)
}

export async function libraryDetailsRoutes(app: FastifyInstance): Promise<void> {
  // ?path=<path>&library=<id>; with library=all the path starts with the
  // library's id, as in the combined tree.
  app.get('/api/kb/details', async req => {
    const q = (req.query ?? {}) as { path?: string; library?: string }
    const viewer = (req as { user?: { id?: string } }).user?.id ?? null
    const label = effectiveSettings().kbLabel
    let lib: Library, sub: string
    if (String(q.library ?? '').toLowerCase() === ALL) {
      const [id, ...rest] = cleanRel(q.path ?? '').split('/')
      const found = scopeLibraries(ALL, viewer, label).find(l => l.id === id)
      if (!found) throw new KbError(404, `no library named ${id}`)
      lib = found
      sub = rest.join('/')
    } else {
      lib = getLibrary(q.library ?? PRIMARY_ID, label, viewer)
      sub = cleanRel(q.path ?? '')
    }
    if (!lib.caps.index && !lib.site) throw new KbError(409, `${lib.label} has no index yet`)
    return describe(lib, sub, viewer)
  })
}
