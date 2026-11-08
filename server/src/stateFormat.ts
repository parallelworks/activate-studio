import fs from 'node:fs'
import path from 'node:path'
import { INDEX_BASE } from './config.js'

/**
 * The format of the Studio's own state, and the snapshots the updater takes
 * of it before any change of version (docs/UPDATES.md, "State").
 *
 * The state is what a newer version might migrate: settings, conversations,
 * the credential vault, the library list and labels, per-user data, tasks,
 * the fleet, extensions, branding, and the launcher's studio.env. Indexes,
 * index history, caches, models, logs and agent workspaces are not state:
 * they are rebuilt, or carry their own format.
 *
 * INDEX_BASE/state-format.json says which format the state is in. A version
 * reads the formats up to its own STATE_FORMAT. One that finds a newer
 * format (an older version started after a newer one migrated the state)
 * puts back the newest snapshot in a format it reads, keeping what it
 * replaces as a snapshot too. All functions are synchronous, so the check
 * at start runs before any module reads the state.
 */

/** The state format this version writes and reads. Raise it only with a
 *  migration, and only after a snapshot (docs/UPDATES.md). */
export const STATE_FORMAT = 1
export const MARKER = 'state-format.json'
/** Snapshots kept besides the newest of each format. */
const KEEP = 3

/** Directories of INDEX_BASE that are state, copied whole. */
const STATE_DIRS = ['users', 'tasks', 'fleet', 'extensions', 'branding', 'ssh']
/** Top-level files of INDEX_BASE that are caches, locks or logs. */
const SKIP_FILES = new Set(['.studio-instance', 'embed-cache.db', 'embed-cache.db-wal', 'embed-cache.db-shm', 'semantic-map.json', 'runs.jsonl'])
/** Below users/<id>/ the semantic maps are caches, and below tasks/<id>/
 *  the agents' workspaces (work/) are scratch, not state. Index history
 *  (INDEX_BASE/history) is not a state directory at all. */
const SKIP_BELOW: Record<string, Set<string>> = { users: new Set(['maps']), tasks: new Set(['work']) }

export interface Marker { format: number; writtenBy?: string; at?: string }
export interface Snapshot {
  id: string
  /** The version running when it was taken, and the one it was taken for. */
  from: string
  to: string | null
  reason: string
  /** The state format of what it holds. */
  format: number
  at: string
  files: string[]
}

const dataDir = () => process.env.STUDIO_DATA_DIR || (process.env.STUDIO_CONFIG_FILE ? path.dirname(process.env.STUDIO_CONFIG_FILE) : '')
export const snapshotsDir = () => { const d = dataDir(); return d ? path.join(d, 'updates', 'state-snapshots') : path.join(INDEX_BASE, 'updates', 'state-snapshots') }
/** The launcher's settings file, which is state too, when there is one. */
const configFile = () => process.env.STUDIO_CONFIG_FILE || (process.env.STUDIO_DATA_DIR ? path.join(process.env.STUDIO_DATA_DIR, 'studio.env') : '')
const CONFIG_NAME = '@config/studio.env'

/** The format the state is in: the marker's, or 1 for state from before markers. */
export function stateFormat(base = INDEX_BASE): number {
  try {
    const m = JSON.parse(fs.readFileSync(path.join(base, MARKER), 'utf8')) as Marker
    return Number.isSafeInteger(m.format) && m.format >= 1 ? m.format : 1
  } catch { return 1 }
}

export function writeMarker(format: number, version: string, base = INDEX_BASE): void {
  fs.mkdirSync(base, { recursive: true })
  const file = path.join(base, MARKER)
  const tmp = `${file}.${process.pid}.tmp`
  fs.writeFileSync(tmp, JSON.stringify({ format, writtenBy: version, at: new Date().toISOString() }, null, 1) + '\n')
  fs.renameSync(tmp, file)
}

const isFile = (p: string) => { try { return fs.lstatSync(p).isFile() } catch { return false } }

/** Every file of the state, relative to INDEX_BASE, the launcher's file as CONFIG_NAME. */
export function stateFiles(base = INDEX_BASE): string[] {
  const out: string[] = []
  const walk = (rel: string, depth: number) => {
    let entries: fs.Dirent[] = []
    try { entries = fs.readdirSync(path.join(base, rel), { withFileTypes: true }) } catch { return }
    for (const e of entries) {
      const r = path.join(rel, e.name)
      if (e.isFile()) out.push(r)
      else if (e.isDirectory() && !(depth === 1 && SKIP_BELOW[rel.split(path.sep)[0]]?.has(e.name))) walk(r, depth + 1)
    }
  }
  let top: fs.Dirent[] = []
  try { top = fs.readdirSync(base, { withFileTypes: true }) } catch { /* no state yet */ }
  for (const e of top) {
    if (e.isFile() && !SKIP_FILES.has(e.name) && !e.name.endsWith('.tmp')) out.push(e.name)
    else if (e.isDirectory() && STATE_DIRS.includes(e.name)) walk(e.name, 0)
    else if (e.isDirectory() && e.name === 'libraries') {
      // A built library's own files (labels and settings), not its index.
      for (const lib of fs.readdirSync(path.join(base, 'libraries'), { withFileTypes: true })) {
        if (!lib.isDirectory()) continue
        for (const f of fs.readdirSync(path.join(base, 'libraries', lib.name), { withFileTypes: true })) {
          if (f.isFile() && f.name.endsWith('.json')) out.push(path.join('libraries', lib.name, f.name))
        }
      }
    }
  }
  const cfg = configFile()
  if (cfg && isFile(cfg)) out.push(CONFIG_NAME)
  return out.sort()
}

const sourceOf = (rel: string, base: string) => rel === CONFIG_NAME ? configFile() : path.join(base, rel)

function copy(from: string, to: string): void {
  fs.mkdirSync(path.dirname(to), { recursive: true, mode: 0o700 })
  fs.copyFileSync(from, to)
}

/** Copies the state into a new snapshot and prunes old ones. */
export function takeSnapshot(opts: { from: string; to?: string | null; reason: string; keep?: string }, base = INDEX_BASE): Snapshot {
  const root = snapshotsDir()
  fs.mkdirSync(root, { recursive: true, mode: 0o700 })
  const at = new Date().toISOString()
  const id = `${at.replace(/[:.]/g, '-')}-${process.pid}-${Math.random().toString(36).slice(2, 6)}`
  const dir = path.join(root, id)
  const work = `${dir}.part`
  fs.mkdirSync(path.join(work, 'files'), { recursive: true, mode: 0o700 })
  const files: string[] = []
  for (const rel of stateFiles(base)) {
    try { copy(sourceOf(rel, base), path.join(work, 'files', rel)); files.push(rel) } catch { /* gone meanwhile */ }
  }
  const snap: Snapshot = { id, from: opts.from, to: opts.to ?? null, reason: opts.reason, format: stateFormat(base), at, files }
  fs.writeFileSync(path.join(work, 'snapshot.json'), JSON.stringify(snap, null, 1) + '\n', { mode: 0o600 })
  fs.renameSync(work, dir)
  prune(opts.keep)
  return snap
}

export function listSnapshots(): Snapshot[] {
  const root = snapshotsDir()
  let names: string[] = []
  try { names = fs.readdirSync(root) } catch { return [] }
  const out: Snapshot[] = []
  for (const n of names) {
    if (n.endsWith('.part')) continue
    try {
      const s = JSON.parse(fs.readFileSync(path.join(root, n, 'snapshot.json'), 'utf8')) as Snapshot
      if (s.id === n && Array.isArray(s.files) && Number.isSafeInteger(s.format)) out.push(s)
    } catch { /* not a snapshot */ }
  }
  return out.sort((a, b) => (a.at < b.at ? 1 : a.at > b.at ? -1 : 0))
}

/** The newest snapshot a version reading formats up to `format` can use. */
export function compatibleSnapshot(format: number): Snapshot | null {
  return listSnapshots().find(s => s.format <= format) ?? null
}

/** Keeps the KEEP newest, and the newest of each format, so the snapshot
 *  taken before a first move to a newer format survives later updates. */
function prune(also?: string): void {
  const all = listSnapshots()
  const keep = new Set(all.slice(0, KEEP).map(s => s.id))
  if (also) keep.add(also)
  const formats = new Set<number>()
  for (const s of all) if (!formats.has(s.format)) { formats.add(s.format); keep.add(s.id) }
  for (const s of all) if (!keep.has(s.id)) fs.rmSync(path.join(snapshotsDir(), s.id), { recursive: true, force: true })
}

/** Puts a snapshot's state back: what is there now is kept as a snapshot
 *  first, then the state's files are replaced by the snapshot's, and the
 *  marker says the snapshot's format. */
export function restoreSnapshot(id: string, running: string, base = INDEX_BASE): Snapshot {
  const snap = listSnapshots().find(s => s.id === id)
  if (!snap) throw new Error(`no state snapshot ${id}`)
  takeSnapshot({ from: running, to: running, reason: `replaced by the snapshot of ${snap.at}`, keep: snap.id }, base)
  const src = path.join(snapshotsDir(), id, 'files')
  // The update policy stays as it is now: it chose this change of version,
  // and the snapshot's (Preview, say) would undo it.
  const settingsFile = path.join(base, 'settings.json')
  let updates: unknown
  try { updates = (JSON.parse(fs.readFileSync(settingsFile, 'utf8')) as { updates?: unknown }).updates } catch { /* no settings */ }
  for (const rel of stateFiles(base)) if (!snap.files.includes(rel)) fs.rmSync(sourceOf(rel, base), { force: true })
  for (const rel of snap.files) {
    const dest = sourceOf(rel, base)
    if (!dest) continue
    const tmp = `${dest}.${process.pid}.restore`
    copy(path.join(src, rel), tmp)
    fs.renameSync(tmp, dest)
  }
  if (updates !== undefined) {
    try {
      const restored = JSON.parse(fs.readFileSync(settingsFile, 'utf8')) as Record<string, unknown>
      fs.writeFileSync(settingsFile, JSON.stringify({ ...restored, updates }, null, 2) + '\n')
    } catch { /* the snapshot had no settings */ }
  }
  writeMarker(snap.format, running, base)
  return snap
}

/** At start: a missing marker is written; a state in a newer format than
 *  this version reads is replaced by the newest snapshot it can read. */
export function reconcileState(version: string, log: (msg: string) => void = () => {}, base = INDEX_BASE): 'ok' | 'marked' | 'restored' | 'unreadable' {
  if (!fs.existsSync(path.join(base, MARKER))) {
    if (!fs.existsSync(base)) return 'ok'
    writeMarker(STATE_FORMAT, version, base)
    return 'marked'
  }
  const have = stateFormat(base)
  if (have <= STATE_FORMAT) return 'ok'
  const snap = compatibleSnapshot(STATE_FORMAT)
  if (!snap) {
    log(`state: the state is in format ${have}, newer than this version reads (${STATE_FORMAT}), and no snapshot in a readable format is kept`)
    return 'unreadable'
  }
  restoreSnapshot(snap.id, version, base)
  log(`state: format ${have} is newer than this version reads; the snapshot taken ${snap.at} (before ${snap.from} changed to ${snap.to ?? 'another version'}) was put back`)
  return 'restored'
}
