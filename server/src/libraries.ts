/**
 * Libraries: the indexes the Studio can mount at once.
 *
 * The knowledge base the server builds and owns is the first library,
 * writable, and everything that exists today keeps working against it
 * without naming it. Any other library is read only: a root-built site
 * index, or an index someone handed over, reached through a local tree.
 * Each carries what it supports, which the Studio learns by probing the
 * tree once when the library is added; nothing is asked of GUFI and
 * nothing is written into the index.
 *
 * Where they come from, in order: the primary from KB_ROOT, pinned ones
 * from STUDIO_LIBRARIES (a JSON array set by the deployment), and ones an
 * administrator added in Settings, kept in INDEX_BASE/libraries.json.
 */
import fs from 'node:fs'
import path from 'node:path'
import { execFile } from 'node:child_process'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, KB_ROOT, gufiAvailable } from './config.js'
import { KbError } from './kb.js'

export interface LibraryCaps {
  /** A GUFI tree: a db.db at the root. */
  index: boolean
  /** The Studio's full-text tables are present in at least one directory. */
  fullText: boolean
  /** Vector tables are present in at least one directory. */
  vectors: boolean
}

export interface Library {
  id: string
  label: string
  indexRoot: string
  /** Where the files are, when they are reachable from this host. */
  sourceRoot: string | null
  /** Only the primary may add files, index, and enrich. */
  writable: boolean
  primary: boolean
  /** Pinned by the deployment; cannot be removed from Settings. */
  pinned: boolean
  caps: LibraryCaps
}

export interface LibraryDef {
  id: string
  label?: string
  indexRoot: string
  sourceRoot?: string | null
}

/** The shape the client sees. Paths stay on the server. */
export interface PublicLibrary {
  id: string
  label: string
  primary: boolean
  writable: boolean
  pinned: boolean
  source: boolean
  caps: LibraryCaps
}

export const PRIMARY_ID = 'kb'
const FILE = path.join(INDEX_BASE, 'libraries.json')
const ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/

let added: LibraryDef[] | null = null
let probeCache = new Map<string, { caps: LibraryCaps; at: number }>()

function loadAdded(): LibraryDef[] {
  if (added) return added
  try {
    const raw = JSON.parse(fs.readFileSync(FILE, 'utf8')) as unknown
    added = Array.isArray(raw) ? raw.filter(isDef) : []
  } catch { added = [] }
  return added
}

function saveAdded(defs: LibraryDef[]): void {
  added = defs
  fs.mkdirSync(path.dirname(FILE), { recursive: true })
  fs.writeFileSync(FILE, JSON.stringify(defs, null, 2))
}

function isDef(x: unknown): x is LibraryDef {
  if (!x || typeof x !== 'object') return false
  const d = x as Record<string, unknown>
  return typeof d.id === 'string' && ID_RE.test(d.id) && typeof d.indexRoot === 'string' && d.indexRoot.length > 0
}

function pinnedDefs(): LibraryDef[] {
  const raw = process.env.STUDIO_LIBRARIES
  if (!raw) return []
  try {
    const arr = JSON.parse(raw) as unknown
    return Array.isArray(arr) ? arr.filter(isDef) : []
  } catch {
    return []
  }
}

function sqlite(script: string, timeoutMs = 10_000): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: timeoutMs, maxBuffer: 1 << 20 },
      (err, so) => (err && !so ? reject(err) : resolve(so)))
    child.stdin?.end(script)
  })
}

/** Up to `max` db.db files under a tree, breadth first, without walking everything. */
function sampleDbs(root: string, max = 12): string[] {
  const out: string[] = []
  const queue = [root]
  let visited = 0
  while (queue.length && out.length < max && visited < 400) {
    const dir = queue.shift()!
    visited++
    let entries: fs.Dirent[]
    try { entries = fs.readdirSync(dir, { withFileTypes: true }) } catch { continue }
    if (entries.some(e => e.name === 'db.db')) out.push(path.join(dir, 'db.db'))
    for (const e of entries) if (e.isDirectory()) queue.push(path.join(dir, e.name))
  }
  return out
}

/**
 * Learn what a tree supports. A GUFI index has a db.db at its root; the
 * Studio's own tables are looked for in a sample of directory databases,
 * which is enough to say whether search will answer without walking the
 * whole tree of a large index.
 */
export async function probeLibrary(indexRoot: string): Promise<LibraryCaps> {
  const abs = path.resolve(indexRoot)
  const cached = probeCache.get(abs)
  if (cached && Date.now() - cached.at < 300_000) return cached.caps
  const caps: LibraryCaps = { index: false, fullText: false, vectors: false }
  if (fs.existsSync(path.join(abs, 'db.db'))) {
    caps.index = true
    if (gufiAvailable()) {
      for (const db of sampleDbs(abs)) {
        try {
          const out = await sqlite(`ATTACH '${db.replace(/'/g, "''")}' AS d;\nSELECT name FROM d.sqlite_master WHERE name IN ('words','gvec');\n`)
          if (/\bwords\b/.test(out)) caps.fullText = true
          if (/\bgvec\b/.test(out)) caps.vectors = true
          if (caps.fullText && caps.vectors) break
        } catch { /* an unreadable db says nothing */ }
      }
    }
  }
  probeCache.set(abs, { caps, at: Date.now() })
  return caps
}

function primaryLibrary(label: string): Library {
  return {
    id: PRIMARY_ID,
    label,
    indexRoot: GUFI_INDEX,
    sourceRoot: KB_ROOT,
    writable: true,
    primary: true,
    pinned: true,
    caps: probeCache.get(path.resolve(GUFI_INDEX))?.caps ?? { index: gufiAvailable(), fullText: gufiAvailable(), vectors: false },
  }
}

function fromDef(d: LibraryDef, pinned: boolean): Library {
  const abs = path.resolve(d.indexRoot)
  return {
    id: d.id,
    label: d.label || path.basename(abs) || d.id,
    indexRoot: abs,
    sourceRoot: d.sourceRoot ? path.resolve(d.sourceRoot) : null,
    writable: false,
    primary: false,
    pinned,
    caps: probeCache.get(abs)?.caps ?? { index: fs.existsSync(path.join(abs, 'db.db')), fullText: false, vectors: false },
  }
}

/** Every mounted library, primary first. `label` names the primary. */
export function listLibraries(primaryLabel = path.basename(KB_ROOT)): Library[] {
  const seen = new Set<string>([PRIMARY_ID])
  const out: Library[] = [primaryLibrary(primaryLabel)]
  for (const [defs, pinned] of [[pinnedDefs(), true], [loadAdded(), false]] as const) {
    for (const d of defs) {
      if (seen.has(d.id)) continue
      seen.add(d.id)
      out.push(fromDef(d, pinned))
    }
  }
  return out
}

/** Refresh the capabilities of every library; called at startup and after a change. */
export async function probeAll(primaryLabel?: string): Promise<Library[]> {
  const libs = listLibraries(primaryLabel)
  for (const l of libs) l.caps = await probeLibrary(l.indexRoot)
  return libs
}

export function getLibrary(id: string | undefined | null, primaryLabel?: string): Library {
  const want = (id || PRIMARY_ID).trim()
  const lib = listLibraries(primaryLabel).find(l => l.id === want)
  if (!lib) throw new KbError(404, `no library named ${want}`)
  return lib
}

/** The guard every mutation route runs before touching files or the index. */
export function requireWritable(lib: Library): void {
  if (!lib.writable) throw new KbError(403, `${lib.label} is read only`)
}

export async function addLibrary(def: LibraryDef): Promise<Library> {
  if (!isDef(def)) throw new KbError(400, 'a library needs an id (letters, digits, dash, underscore) and an index root')
  if (def.id === PRIMARY_ID || listLibraries().some(l => l.id === def.id)) throw new KbError(409, `a library named ${def.id} already exists`)
  const abs = path.resolve(def.indexRoot)
  if (!fs.existsSync(abs)) throw new KbError(400, `no such directory: ${def.indexRoot}`)
  if (def.sourceRoot && !fs.existsSync(path.resolve(def.sourceRoot))) throw new KbError(400, `no such directory: ${def.sourceRoot}`)
  const caps = await probeLibrary(abs)
  if (!caps.index) throw new KbError(400, `${def.indexRoot} does not look like a GUFI index (no db.db at its root)`)
  saveAdded([...loadAdded(), { id: def.id, label: def.label?.slice(0, 60) || undefined, indexRoot: abs, sourceRoot: def.sourceRoot ? path.resolve(def.sourceRoot) : null }])
  return getLibrary(def.id)
}

export function removeLibrary(id: string): void {
  const lib = getLibrary(id)
  if (lib.primary || lib.pinned) throw new KbError(403, `${lib.label} is set by the deployment and cannot be removed here`)
  saveAdded(loadAdded().filter(d => d.id !== id))
}

export function publicLibrary(l: Library): PublicLibrary {
  return { id: l.id, label: l.label, primary: l.primary, writable: l.writable, pinned: l.pinned, source: !!l.sourceRoot, caps: l.caps }
}

/** Tests replace the stored set and the probe memory between cases. */
export function resetLibrariesForTests(): void {
  added = null
  probeCache = new Map()
}
