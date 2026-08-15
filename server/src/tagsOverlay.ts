import fs from 'node:fs'
import path from 'node:path'
import { INDEX_BASE, KB_ROOT } from './config.js'

/**
 * Durable label store for filesystems that refuse user xattrs (NFS and EFS
 * return ENOTSUP). Where xattrs work they are the source of truth and the
 * index is fully recreatable from the source tree; this overlay is the
 * stand-in where they do not.
 *
 * Keyed by fsid.inode rather than by path, following the GUFI convention
 * for information that cannot be recovered by walking the tree: a rebuilt
 * index is re-joined to it on inode, so labels survive a full reindex and
 * follow files through renames and moves. The path is kept only as a hint,
 * refreshed whenever the index reports a file somewhere new. Nothing here
 * is authoritative about where a file lives.
 *
 * Each entry also records the file's creation time, because filesystems
 * recycle inodes aggressively: deleting a labeled file and creating another
 * in its place can hand the newcomer the same inode (observed immediately
 * on ext4), and without this check it would inherit labels that were never
 * meant for it. A mismatch retires the entry. Where a filesystem reports no
 * creation time the check is skipped rather than guessed at.
 */

const FILE = path.join(INDEX_BASE, 'tags-overlay.json')

export interface OverlayEntry {
  tags: string[]
  /** Last known KB-relative path; a hint for lookups, not an identity. */
  path: string
  dir: boolean
  /** Creation time in ns, as a string; '0' when the filesystem has none. */
  btime: string
  at: string
}

interface OverlayFile { version: number; entries: Record<string, OverlayEntry> }

let cache: OverlayFile | null = null

/** Identity of a path: fsid.inode plus creation time and kind. */
export function identify(abs: string): { key: string; btime: string; dir: boolean } | null {
  try {
    const st = fs.statSync(abs, { bigint: true })
    return { key: `${st.dev}.${st.ino}`, btime: String(st.birthtimeNs ?? 0n), dir: st.isDirectory() }
  } catch {
    return null
  }
}

/** fsid.inode for a path, or null when it cannot be stat'ed. */
export function overlayKey(abs: string): string | null {
  return identify(abs)?.key ?? null
}

/** Same file object? Inode equality plus creation time where both sides
 *  report one, so a recycled inode is not mistaken for the original. */
export function sameObject(entry: OverlayEntry, id: { btime: string }): boolean {
  if (!entry.btime || entry.btime === '0' || !id.btime || id.btime === '0') return true
  return entry.btime === id.btime
}

function migrate(raw: unknown): OverlayFile {
  const obj = raw as Record<string, unknown>
  if (obj && typeof obj === 'object' && (obj as unknown as OverlayFile).version === 2) {
    return obj as unknown as OverlayFile
  }
  // v1 was a flat path -> tags map; key what still exists by inode.
  const out: OverlayFile = { version: 2, entries: {} }
  for (const [rel, tags] of Object.entries(obj ?? {})) {
    if (!Array.isArray(tags)) continue
    const abs = path.join(KB_ROOT, rel)
    const id = identify(abs)
    if (!id) continue
    const key = id.key
    const dir = id.dir
    out.entries[key] = { tags: tags as string[], path: rel, dir, btime: id.btime, at: new Date().toISOString() }
  }
  return out
}

function load(): OverlayFile {
  if (cache) return cache
  try { cache = migrate(JSON.parse(fs.readFileSync(FILE, 'utf8'))) } catch { cache = { version: 2, entries: {} } }
  return cache!
}

function persist(): void {
  fs.writeFileSync(FILE, JSON.stringify(load(), null, 1))
}

export function overlayGet(rel: string): string[] | null {
  const id = identify(path.join(KB_ROOT, rel))
  if (!id) return null
  const all = load()
  const entry = all.entries[id.key]
  if (!entry) return null
  if (!sameObject(entry, id)) {
    // The inode was recycled into a different file; retire the stale entry.
    delete all.entries[id.key]
    persist()
    return null
  }
  return entry.tags
}

export function overlaySet(rel: string, tags: string[]): void {
  const id = identify(path.join(KB_ROOT, rel))
  if (!id) return
  const all = load()
  if (tags.length) {
    all.entries[id.key] = { tags, path: rel, dir: id.dir, btime: id.btime, at: new Date().toISOString() }
  } else if (all.entries[id.key]) {
    delete all.entries[id.key]
  } else {
    return
  }
  persist()
}

/** Record where the index currently finds an inode, so the hint stays useful
 *  after renames. Returns true when something changed. */
export function overlayNotePath(key: string, rel: string, dir: boolean): boolean {
  const e = load().entries[key]
  if (!e || (e.path === rel && e.dir === dir)) return false
  e.path = rel
  e.dir = dir
  return true
}

export function overlayFlush(): void {
  if (cache) persist()
}

export function overlayEntries(): Record<string, OverlayEntry> {
  return load().entries
}

/** Inode (without the fsid) to the keys carrying it; the index reports
 *  inodes, and a single corpus rarely spans devices, so the fsid is
 *  confirmed by stat only for the rows that match. */
export function overlayByInode(): Map<string, string[]> {
  const out = new Map<string, string[]>()
  for (const key of Object.keys(load().entries)) {
    const ino = key.slice(key.indexOf('.') + 1)
    const list = out.get(ino)
    if (list) list.push(key)
    else out.set(ino, [key])
  }
  return out
}

/** Drop entries whose file is gone from both the index and the filesystem,
 *  and those whose inode now belongs to a different file. */
export function overlayPrune(seenKeys: Set<string>): number {
  const all = load()
  let dropped = 0
  for (const [key, e] of Object.entries(all.entries)) {
    const id = identify(path.join(KB_ROOT, e.path))
    const stillThere = id?.key === key && sameObject(e, id)
    if (seenKeys.has(key) || stillThere) continue
    delete all.entries[key]
    dropped++
  }
  if (dropped) persist()
  return dropped
}

export function overlayEmpty(): boolean {
  return Object.keys(load().entries).length === 0
}
