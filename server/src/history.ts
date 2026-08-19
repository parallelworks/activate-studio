import path from 'node:path'
import fs from 'node:fs'
import zlib from 'node:zlib'
import type { FastifyInstance } from 'fastify'
import { GUFI_INDEX, INDEX_BASE, gufiAvailable } from './config.js'
import { queryPerDir } from './gufi.js'

/**
 * Corpus history: a timeline of what the knowledge base held, captured
 * from the index rather than from the files.
 *
 * Each snapshot is the census the index already carries, one row per file
 * with its size, mtime, and inode. That is small (tens of bytes per file)
 * where the corpus and its extracted text are not, so keeping many of them
 * is cheap. Snapshots answer what changed and when; they do not hold file
 * contents and cannot restore one.
 *
 * Capture is tied to indexing: a pass rewrites the index, so a snapshot is
 * taken when the index is newer than the last one. Nothing captures on a
 * timer, which means the timeline has a point per index pass and not per
 * hour.
 */

const HISTORY_DIR = path.join(INDEX_BASE, 'history')
// Each snapshot of a 10,000-file corpus is roughly 200 KB compressed, so
// the default retention costs a few megabytes. Raise it where the corpus
// is small and the history matters more than the space.
const KEEP = Number(process.env.HISTORY_KEEP ?? 30)

export interface Entry { path: string; size: number; mtime: number; inode: number }
export interface SnapshotMeta { id: string; takenAt: number; files: number; bytes: number }
interface Snapshot extends SnapshotMeta { entries: Entry[] }

function kbRel(p: string): string {
  const idx = path.resolve(GUFI_INDEX)
  const abs = path.resolve(p)
  return (abs.startsWith(idx) ? abs.slice(idx.length) : p).replace(/^\/+/, '')
}

function ensureDir(): void {
  fs.mkdirSync(HISTORY_DIR, { recursive: true })
}

function fileFor(id: string): string {
  return path.join(HISTORY_DIR, `${id}.json.gz`)
}

export function listSnapshots(): SnapshotMeta[] {
  if (!fs.existsSync(HISTORY_DIR)) return []
  return fs.readdirSync(HISTORY_DIR)
    .filter(n => n.endsWith('.json.gz'))
    .map(n => {
      const id = n.replace(/\.json\.gz$/, '')
      try {
        const s = readSnapshot(id)
        return { id, takenAt: s.takenAt, files: s.files, bytes: s.bytes }
      } catch {
        return null
      }
    })
    .filter((s): s is SnapshotMeta => s !== null)
    .sort((a, b) => a.takenAt - b.takenAt)
}

function readSnapshot(id: string): Snapshot {
  const raw = zlib.gunzipSync(fs.readFileSync(fileFor(id)))
  return JSON.parse(raw.toString('utf8')) as Snapshot
}

/** The newest mtime under the index tree, which moves on every pass. */
function indexMtime(): number {
  let newest = 0
  const walk = (dir: string, depth: number) => {
    if (depth > 3) return
    let names: fs.Dirent[] = []
    try { names = fs.readdirSync(dir, { withFileTypes: true }) } catch { return }
    for (const n of names) {
      const p = path.join(dir, n.name)
      try {
        const st = fs.statSync(p)
        if (st.mtimeMs > newest) newest = st.mtimeMs
      } catch { /* raced with a rebuild */ }
      if (n.isDirectory()) walk(p, depth + 1)
    }
  }
  walk(GUFI_INDEX, 0)
  return Math.floor(newest / 1000)
}

export async function capture(): Promise<SnapshotMeta | null> {
  if (!gufiAvailable()) return null
  const rows = await queryPerDir(
    `SELECT rpath(sname, sroll, name), size, mtime, inode FROM vrpentries;`)
  const entries: Entry[] = rows
    .map(r => ({
      path: kbRel(r[0] ?? ''),
      size: Number(r[1]) || 0,
      mtime: Number(r[2]) || 0,
      inode: Number(r[3]) || 0,
    }))
    .filter(e => e.path !== '')
  entries.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0))
  const takenAt = Math.floor(Date.now() / 1000)
  const snap: Snapshot = {
    id: String(takenAt),
    takenAt,
    files: entries.length,
    bytes: entries.reduce((n, e) => n + e.size, 0),
    entries,
  }
  ensureDir()
  fs.writeFileSync(fileFor(snap.id), zlib.gzipSync(Buffer.from(JSON.stringify(snap))))
  prune()
  return { id: snap.id, takenAt: snap.takenAt, files: snap.files, bytes: snap.bytes }
}

function prune(): void {
  const all = listSnapshots()
  for (const s of all.slice(0, Math.max(0, all.length - KEEP))) {
    try { fs.unlinkSync(fileFor(s.id)) } catch { /* already gone */ }
  }
}

/** Capture when the index has been rebuilt since the last snapshot. */
export async function captureIfStale(): Promise<SnapshotMeta | null> {
  if (!gufiAvailable()) return null
  const all = listSnapshots()
  const last = all[all.length - 1]
  if (last && indexMtime() <= last.takenAt) return null
  return capture()
}

export interface Diff {
  from: SnapshotMeta
  to: SnapshotMeta
  added: Entry[]
  removed: Entry[]
  modified: { path: string; size: number; mtime: number; wasSize: number; wasMtime: number }[]
  renamed: { from: string; to: string; size: number }[]
  counts: { added: number; removed: number; modified: number; renamed: number }
}

const LIST_CAP = 500

export function diff(fromId: string, toId: string): Diff {
  const a = readSnapshot(fromId)
  const b = readSnapshot(toId)
  const byPathA = new Map(a.entries.map(e => [e.path, e]))
  const byPathB = new Map(b.entries.map(e => [e.path, e]))

  const addedRaw = b.entries.filter(e => !byPathA.has(e.path))
  const removedRaw = a.entries.filter(e => !byPathB.has(e.path))
  const modified = b.entries
    .map(e => ({ e, was: byPathA.get(e.path) }))
    .filter(({ e, was }) => was && (was.size !== e.size || was.mtime !== e.mtime))
    .map(({ e, was }) => ({ path: e.path, size: e.size, mtime: e.mtime, wasSize: was!.size, wasMtime: was!.mtime }))

  // A file that left one path and arrived at another with the same inode
  // was moved, not deleted and recreated; saying so keeps a reorganised
  // corpus from reading as wholesale churn.
  const removedByInode = new Map(removedRaw.filter(e => e.inode).map(e => [e.inode, e]))
  const renamed: Diff['renamed'] = []
  const added: Entry[] = []
  for (const e of addedRaw) {
    const prior = e.inode ? removedByInode.get(e.inode) : undefined
    if (prior) {
      renamed.push({ from: prior.path, to: e.path, size: e.size })
      removedByInode.delete(e.inode)
    } else {
      added.push(e)
    }
  }
  const removed = removedRaw.filter(e => !e.inode || removedByInode.has(e.inode))

  return {
    from: { id: a.id, takenAt: a.takenAt, files: a.files, bytes: a.bytes },
    to: { id: b.id, takenAt: b.takenAt, files: b.files, bytes: b.bytes },
    added: added.slice(0, LIST_CAP),
    removed: removed.slice(0, LIST_CAP),
    modified: modified.slice(0, LIST_CAP),
    renamed: renamed.slice(0, LIST_CAP),
    counts: { added: added.length, removed: removed.length, modified: modified.length, renamed: renamed.length },
  }
}

export async function historyRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/kb/history', async () => {
    // A visit is the moment we know indexing has finished, so it is also
    // when a missed snapshot gets taken.
    await captureIfStale().catch(() => null)
    return { available: gufiAvailable(), snapshots: listSnapshots(), keep: KEEP }
  })

  app.post('/api/kb/history/capture', async () => {
    const snap = await capture()
    return { captured: snap }
  })

  app.get('/api/kb/history/diff', async req => {
    const { from, to } = req.query as { from?: string; to?: string }
    const all = listSnapshots()
    if (all.length < 2) return { available: false }
    const toId = to ?? all[all.length - 1].id
    const fromId = from ?? all[all.length - 2].id
    return { available: true, ...diff(fromId, toId) }
  })
}
