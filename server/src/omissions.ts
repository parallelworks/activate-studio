import fs from 'node:fs'
import path from 'node:path'
import { INDEX_BASE } from './config.js'
import { builtLibraryDir, knownCaps, lacks, PRIMARY_ID, type Library } from './libraries.js'

/**
 * Libraries a search or a query left out, or searched only in part, and
 * why. Every search and Query response carries them, so a library never
 * drops out of the results silently: one being built, one whose index is
 * missing, one whose words are not read yet, one that did not answer in
 * time or could not be reached, and one whose folder macOS has not allowed
 * the Studio to read.
 */
export interface Omission {
  id: string
  label: string
  /** Why, in plain words. */
  reason: string
  /** Searched in part (by name, say, but not its words); false when the
   *  library added nothing. */
  partial: boolean
}

/** Adds an omission, one per library: a second reason for the same library
 *  joins the first, and it stays partial only while both are. */
export function addOmission(list: Omission[], o: Omission): void {
  const was = list.find(x => x.id === o.id)
  if (!was) { list.push(o); return }
  if (!was.reason.split('; ').includes(o.reason)) was.reason = `${was.reason}; ${o.reason}`
  was.partial = was.partial && o.partial
}

const short = (m: string, n = 160) => {
  const line = String(m ?? '').trim().split('\n').filter(Boolean).pop() ?? ''
  return line.length > n ? `${line.slice(0, n - 1)}…` : line
}

// ---- folders the Studio may not read ----

/** EPERM is what macOS answers where its privacy settings (Files and
 *  Folders, Full Disk Access) have not let the app in; EACCES is a plain
 *  permission. */
export const isDeniedError = (e: unknown) => {
  const code = (e as NodeJS.ErrnoException | null)?.code
  return code === 'EPERM' || code === 'EACCES' || /Operation not permitted|Permission denied/i.test(String((e as Error)?.message ?? e))
}

/** The reason for a folder the Studio may not read, with where to allow it. */
export function deniedReason(folder: string, platform = process.platform): string {
  return platform === 'darwin'
    ? `macOS has not allowed ACTIVATE Studio to read ${folder}; allow it in System Settings, Privacy & Security, Files and Folders (or Full Disk Access)`
    : `permission denied reading ${folder}`
}

const deniedRoots = new Map<string, { denied: boolean; at: number }>()
/** Whether a library's folder cannot be listed at all, looked at once a minute. */
export function rootDenied(root: string): boolean {
  const k = deniedRoots.get(root)
  if (k && Date.now() - k.at < 60_000) return k.denied
  let denied = false
  try { fs.readdirSync(root) } catch (e) { denied = isDeniedError(e) }
  deniedRoots.set(root, { denied, at: Date.now() })
  return denied
}

/** Folders inside a library that the last build or sync could not read. */
export interface DeniedRecord { folders: string[]; files: number; at: string }
const deniedFile = (id: string) => (id === PRIMARY_ID ? path.join(INDEX_BASE, 'denied.json') : path.join(builtLibraryDir(id), 'denied.json'))

export function readDenied(id: string): DeniedRecord | null {
  try {
    const r = JSON.parse(fs.readFileSync(deniedFile(id), 'utf8')) as DeniedRecord
    return Array.isArray(r.folders) && (r.folders.length || r.files) ? r : null
  } catch { return null }
}

/** Records what a build or sync could not read; nothing denied clears it. */
export function recordDenied(id: string, folders: string[], files = 0): void {
  const f = deniedFile(id)
  try {
    if (!folders.length && !files) { fs.rmSync(f, { force: true }); return }
    fs.mkdirSync(path.dirname(f), { recursive: true })
    fs.writeFileSync(f, JSON.stringify({ folders: [...new Set(folders)].slice(0, 200), files, at: new Date().toISOString() }))
  } catch { /* a record that cannot be written is only a missing note */ }
}

/** Absolute paths named in permission errors a tool printed (gufi_dir2index,
 *  the text pass), one per line. */
export function deniedPaths(stderr: string): string[] {
  const out: string[] = []
  for (const line of String(stderr ?? '').split('\n')) {
    if (!/Operation not permitted|Permission denied|EPERM|EACCES/i.test(line)) continue
    const m = line.match(/["'`]?(\/[^"'`\n]*?)["'`]?(?::\s|\s\(|$)/)
    if (m) out.push(m[1].replace(/\/+$/, ''))
  }
  return [...new Set(out)]
}

/** Whether a tool's failure was only folders or files it may not read:
 *  gufi_dir2index exits 1 when one folder cannot be opened, after writing
 *  the rest of the tree. Then the build stands, and the folders are named. */
export function onlyDenied(stderr: string): boolean {
  const lines = String(stderr ?? '').split('\n').map(l => l.trim()).filter(Boolean)
  const errors = lines.filter(l => /^Error\b|Could not|cannot|failed/i.test(l))
  const denied = errors.filter(l => /Operation not permitted|Permission denied|EPERM|EACCES/i.test(l))
  return denied.length > 0 && denied.length === errors.length
}

// ---- what a library's state leaves out ----

/** Whether a library is meant to have words: one the Studio builds at a
 *  depth that reads text. */
const expectsWords = (l: Library) => !l.primary && l.depth != null && l.depth !== 'names'
const building = (l: Library) => l.build?.state === 'building' || l.build?.state === 'updating'

/** Whether the main folder's index is being built (indexing.ts says). */
let primaryIndexing: () => boolean = () => false
export function setPrimaryIndexingSource(fn: () => boolean): void { primaryIndexing = fn }
/** Whether the main folder's index was ever built (indexing.ts says), so
 *  one with no index is told apart from one whose index has gone. */
let primaryBuilt: () => boolean = () => false
export function setPrimaryBuiltSource(fn: () => boolean): void { primaryBuilt = fn }

/**
 * What a library's state leaves out of a search or a query, before
 * anything runs: null when it is searched in full. `words` and `meaning`
 * say what the request reads. A library left out entirely (no index) is
 * not to be searched; one searched in part still is.
 */
export function libraryGap(l: Library, want: { words: boolean; meaning: boolean }): Omission | null {
  const o = (reason: string, partial: boolean): Omission => ({ id: l.id, label: l.label, reason, partial })
  const root = l.sourceRoot
  if (root && !l.site && rootDenied(root)) {
    return l.caps.index ? o(`${deniedReason(root)}; results are from its last pass`, true) : o(deniedReason(root), false)
  }
  if (!l.caps.index) {
    if (l.build?.state === 'building' || (l.primary && primaryIndexing())) return o('being built; searchable once its first pass is done', false)
    if (l.build?.state === 'failed') return o(`its build failed${l.build.error ? `: ${short(l.build.error)}` : ''}`, false)
    if (l.primary && !primaryBuilt()) return o('not indexed yet', false)
    return o('index not found', false)
  }
  const reasons: string[] = []
  const denied = readDenied(l.id)
  if (denied?.folders.length) {
    const n = denied.folders.length
    reasons.push(`${n === 1 ? 'a folder' : `${n} folders`} could not be read (${deniedReason(denied.folders[0])})`)
  }
  // Named only where search will not look, or looks while a build reads it.
  if (want.words && expectsWords(l) && !l.caps.fullText && (building(l) || lacks(l, 'fullText'))) {
    reasons.push(building(l) ? 'words not searched yet: its text is being read' : 'words not searched: its index has no text yet')
  }
  if (want.meaning && !l.primary && l.meaning && l.meaning !== 'off' && !l.caps.vectors && (building(l) || lacks(l, 'vectors'))) {
    // While a build runs: its text first, then meaning, which comes last.
    reasons.push(!building(l) ? 'not searched by meaning: its index has no vectors yet'
      : l.caps.fullText ? 'not searched by meaning yet: meaning is still being computed'
      : 'not searched by meaning yet: its text is being read')
  }
  return reasons.length ? o(reasons.join('; '), true) : null
}

/** A plain reason for a library that failed while it was searched or queried. */
export function failureReason(l: Library, e: unknown): string {
  const m = short(String((e as Error)?.message ?? e))
  if (isDeniedError(e)) return deniedReason(l.sourceRoot ?? l.label)
  if (l.site && /ssh|connect|resolve|timed out|refused|unreachable|host key|broken pipe|reset by peer|No route/i.test(m)) {
    return `not reachable over ssh: ${m}`
  }
  if (/ENOENT|no such file|index not built|the index is not there/i.test(m)) return 'index not found'
  return `search failed: ${m}`
}

/** One line for an omission list, as the assistant's tools say it. */
export function omissionsText(list: Omission[]): string {
  if (!list.length) return ''
  return `Left out or searched in part: ${list.map(o => `${o.label} (${o.reason})`).join('; ')}.`
}

/** One line on a library's state, for the index log at startup: whether it
 *  has an index, words and vectors, its depth and build, whether what the
 *  Studio knows of it is stale, and anything that leaves it out of search. */
export function libraryStateLine(l: Library): string {
  const yes = (b: boolean) => (b ? 'yes' : 'no')
  const k = knownCaps(l.indexRoot)
  const gap = libraryGap(l, { words: true, meaning: true })
  const parts = [
    `index ${yes(l.caps.index)}`, `words ${yes(l.caps.fullText)}`, `vectors ${yes(l.caps.vectors)}`,
    ...(l.depth ? [`depth ${l.depth}`] : []),
    ...(l.build ? [`build ${l.build.state}${l.build.builtAt ? ` (built ${l.build.builtAt})` : ''}`] : []),
    ...(l.site ? [`on ${l.site}`] : []),
    ...(k?.stale ? [`capabilities stale${k.error ? ` (${k.error})` : ''}`] : []),
  ]
  return `library ${l.id} (${l.label}): ${parts.join(', ')}${gap ? `; ${gap.partial ? 'searched in part' : 'left out of search'}: ${gap.reason}` : ''}`
}
