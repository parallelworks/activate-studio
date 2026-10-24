import fs from 'node:fs'
import path from 'node:path'
import { INDEX_BASE } from './config.js'
import { onLibraryRemoved, type Library } from './libraries.js'
import { normalizeTag } from './tags.js'

/**
 * Labels on a library whose files the Studio does not write: an index it
 * reads as it is (here, over ssh or pw ssh), and a library it built of a
 * folder here, which it reads but does not change. The main library keeps
 * its labels on the files (the user.studio.tags xattr, tags.ts) and in the
 * index built from them; these are kept by the Studio instead, one file per
 * library beside its other state, keyed by the file's path in the library.
 *
 * They are not written into the index in GUFI's external database form,
 * the way the main library's extracted text is (a words table in each
 * directory's db): a read-only or remote index cannot be written, and an
 * external database is found through the index's own directories. So
 * search, the tree, Details and Query read them from here and join them to
 * the index's rows by path; in Query that is a path condition in the SQL
 * (query.ts).
 */

interface Entry { tags: string[]; at: string }
interface Store { version: 1; indexRoot: string; entries: Record<string, Entry> }

export const labelsFile = (id: string) => path.join(INDEX_BASE, 'libraries', id, 'labels.json')

const stores = new Map<string, Store>()
const pruned = new Map<string, number>()
const PRUNE_EVERY_MS = 10 * 60_000

const clean = (rel: string) => String(rel ?? '').replace(/^\/+|\/+$/g, '')

function load(id: string): Store {
  const have = stores.get(id)
  if (have) return have
  let store: Store = { version: 1, indexRoot: '', entries: {} }
  try {
    const raw = JSON.parse(fs.readFileSync(labelsFile(id), 'utf8')) as Store
    if (raw && raw.version === 1 && raw.entries && typeof raw.entries === 'object') store = raw
  } catch { /* none yet */ }
  stores.set(id, store)
  return store
}

function persist(id: string): void {
  const file = labelsFile(id)
  fs.mkdirSync(path.dirname(file), { recursive: true })
  const tmp = `${file}.${process.pid}.tmp`
  fs.writeFileSync(tmp, JSON.stringify(load(id), null, 1))
  fs.renameSync(tmp, file)
}

/** A label set for one path in a library; an empty set removes the entry.
 *  Whether the path is a folder is not recorded: an entry is the path's own
 *  label and is inherited by whatever is below it, and below a file there
 *  is nothing, so a remote library need not be asked which it is. */
export function setLibraryLabels(lib: Library, rel: string, tags: string[]): string[] {
  const store = load(lib.id)
  const key = clean(rel)
  const next = [...new Set(tags.map(normalizeTag).filter(Boolean))]
  store.indexRoot = lib.indexRoot
  if (next.length) store.entries[key] = { tags: next, at: new Date().toISOString() }
  else delete store.entries[key]
  persist(lib.id)
  return next
}

/** The labels set on this very path. */
export function libraryLabelsOf(id: string, rel: string): string[] {
  return load(id).entries[clean(rel)]?.tags ?? []
}

/** Labels by path for a library, in the shape tags.ts gives the main
 *  library's: every entry serves as a file's own labels and as a folder's,
 *  inherited below it (see setLibraryLabels). */
export function libraryLabelMaps(id: string): { dirTags: Map<string, string[]>; fileTags: Map<string, string[]> } {
  const byPath = new Map<string, string[]>()
  for (const [rel, e] of Object.entries(load(id).entries)) byPath.set(rel, e.tags)
  return { dirTags: byPath, fileTags: byPath }
}

/** Own labels plus those of every folder above, as tags.ts gives the main library's. */
export function libraryEffectiveLabels(id: string, rel: string, indexOwn: string[] = []): { own: string[]; inherited: string[] } {
  const key = clean(rel)
  const { dirTags } = libraryLabelMaps(id)
  const own = [...new Set([...indexOwn, ...libraryLabelsOf(id, key)])]
  const inherited = new Set<string>()
  const parts = key.split('/').slice(0, -1)
  for (let i = 0; i <= parts.length; i++) {
    for (const t of dirTags.get(parts.slice(0, i).join('/')) ?? []) inherited.add(t)
  }
  return { own, inherited: [...inherited].filter(t => !own.includes(t)) }
}

/** Search hits from libraries other than the main one, with their labels;
 *  with `filter`, only those carrying one of its labels. */
export function annotateLibraryHits<T extends { path: string; library: { id: string }; tags?: string[] }>(hits: T[], filter?: string[]): T[] {
  const maps = new Map<string, ReturnType<typeof libraryLabelMaps>>()
  const mapsOf = (id: string) => { let m = maps.get(id); if (!m) { m = libraryLabelMaps(id); maps.set(id, m) } return m }
  const out = hits.map(h => {
    const m = mapsOf(h.library.id)
    const tags = new Set<string>([...(h.tags ?? []), ...(m.fileTags.get(h.path) ?? m.dirTags.get(h.path) ?? [])])
    const parts = h.path.split('/')
    for (let i = 0; i < parts.length; i++) for (const t of m.dirTags.get(parts.slice(0, i).join('/')) ?? []) tags.add(t)
    return { ...h, tags: [...tags] }
  })
  if (!filter?.length) return out
  const want = filter.map(normalizeTag).filter(Boolean)
  return out.filter(h => want.some(t => h.tags!.includes(t)))
}

/** Every library's labels with how often each is used, for the vocabulary. */
export function libraryLabelCounts(ids: string[]): Map<string, number> {
  const counts = new Map<string, number>()
  for (const id of ids) {
    for (const e of Object.values(load(id).entries)) for (const t of e.tags) counts.set(t, (counts.get(t) ?? 0) + 1)
  }
  return counts
}

/** The labels below a folder of a library, counted, for Details. */
export function libraryLabelsBelow(id: string, rel: string): Map<string, number> {
  const key = clean(rel)
  const counts = new Map<string, number>()
  for (const [p, e] of Object.entries(load(id).entries)) {
    if (key && p !== key && !p.startsWith(`${key}/`)) continue
    for (const t of e.tags) counts.set(t, (counts.get(t) ?? 0) + 1)
  }
  return counts
}

/**
 * Drop the labels of files that are gone, for a library with its files on
 * this computer. A library whose labeled paths are all missing is taken to
 * be unmounted or moved, not emptied, and is left alone; the file is backed
 * up before anything is removed. A library on another machine is not
 * pruned here: its paths cannot be looked at cheaply, and a label on a path
 * that is gone is simply never shown.
 */
export function pruneLibraryLabels(lib: Library, force = false): number {
  if (!lib.sourceRoot || lib.site) return 0
  const last = pruned.get(lib.id) ?? 0
  if (!force && Date.now() - last < PRUNE_EVERY_MS) return 0
  pruned.set(lib.id, Date.now())
  const store = load(lib.id)
  const keys = Object.keys(store.entries)
  if (!keys.length) return 0
  const gone = keys.filter(rel => !fs.existsSync(path.join(lib.sourceRoot!, rel)))
  if (!gone.length || gone.length === keys.length) return 0
  try { fs.copyFileSync(labelsFile(lib.id), `${labelsFile(lib.id)}.bak`) } catch { /* best effort */ }
  for (const rel of gone) delete store.entries[rel]
  persist(lib.id)
  return gone.length
}

/** Labels kept for the library whose index is at `root`, for Query; null
 *  when no library there has any. */
export function libraryLabelsForIndexRoot(root: string): { dirTags: Map<string, string[]>; fileTags: Map<string, string[]> } | null {
  const dir = path.join(INDEX_BASE, 'libraries')
  let ids: string[] = []
  try { ids = fs.readdirSync(dir) } catch { return null }
  for (const id of ids) {
    if (!fs.existsSync(labelsFile(id))) continue
    const store = load(id)
    if (store.indexRoot && store.indexRoot === root) return libraryLabelMaps(id)
  }
  return null
}

/** Forget a library's labels, as it is removed. */
export function forgetLibraryLabels(id: string): void {
  stores.delete(id)
  pruned.delete(id)
  fs.rmSync(labelsFile(id), { force: true })
  fs.rmSync(`${labelsFile(id)}.bak`, { force: true })
}

onLibraryRemoved(forgetLibraryLabels)

/** For tests: drop what is held in memory. */
export function resetLibraryLabelsForTests(): void {
  stores.clear()
  pruned.clear()
}
