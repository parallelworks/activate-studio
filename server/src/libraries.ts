/**
 * Libraries: the indexes the Studio can mount at once.
 *
 * The knowledge base the server builds and owns is the first library,
 * writable, and everything that exists today keeps working against it
 * without naming it. Any other library is read only: a root-built site
 * index, an index someone handed over, reached through a local tree, or an
 * index the Studio builds of a folder here and keeps in step with it
 * (`build`, libraryBuild.ts).
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
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, KB_ROOT, gufiAvailable, gufiEnv } from './config.js'
import { forgetIndexSettings, indexSettings, type IndexDepth, type MeaningLevel } from './indexSettings.js'
import { KbError } from './kb.js'
import { loadUserLibraries, ownerOf, remoteRootOf } from './userLibraries.js'

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
  /** Extracted text of the files, for a library the Studio built. */
  cacheRoot?: string | null
  /** For a library the Studio built of a folder on this computer. */
  build?: LocalBuild | null
  /** How much of it search by meaning covers, for a library the Studio
   *  builds (the main folder, and one built here or on a remote system);
   *  null for an index it only reads, whose vectors are used as they are. */
  meaning?: MeaningLevel | null
  /** How deep it is indexed, for a library the Studio builds; null for an
   *  index it only reads. */
  depth?: IndexDepth | null
  /** Only the primary may add files, index, and enrich. */
  writable: boolean
  primary: boolean
  /** Pinned by the deployment; cannot be removed from Settings. */
  pinned: boolean
  caps: LibraryCaps
  /** A user's library on one of their clusters (userLibraries.ts). */
  site?: string | null
}

export interface LibraryDef {
  id: string
  label?: string
  indexRoot: string
  sourceRoot?: string | null
  /** Set for an index the Studio built of a folder on this computer
   *  (libraryBuild.ts), which it keeps in sync with the folder. */
  build?: LocalBuild
}

/** An index the Studio builds of a folder on this computer. */
export interface LocalBuild {
  /** The folder the index is written in: the tree is <indexDir>/<the
   *  source folder's name>, beside .studio-index.json. */
  indexDir: string
  /** The extracted text of its documents. */
  cacheRoot: string
  /** The index folder is the Studio's own, removed with the library. */
  managed: boolean
  state: 'building' | 'ready' | 'updating' | 'failed'
  /** What an 'updating' library is in the middle of: applying a new depth
   *  (which a restart finishes with a sync), or a rebuild. */
  pending?: 'depth' | null
  error: string | null
  builtAt: string | null
  files: number | null
  dirs: number | null
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
  /** The cluster a user's own library lives on. */
  site?: string | null
  /** Its level of search by meaning, for a library the Studio builds; null
   *  for an index it only reads (see caps.vectors for that one). */
  meaning: MeaningLevel | null
  /** How deep it is indexed, for a library the Studio builds; null otherwise. */
  depth: IndexDepth | null
}

/** Where a library the Studio built keeps its own files: the extraction
 *  cache, and the index unless another folder was chosen for it. */
export const builtLibraryDir = (id: string) => path.join(INDEX_BASE, 'libraries', id)

export const PRIMARY_ID = 'kb'
const FILE = path.join(INDEX_BASE, 'libraries.json')
const ID_RE = /^[a-z0-9][a-z0-9_-]{0,39}$/

let added: LibraryDef[] | null = null
/** When each tree was last probed, in this process: a probe younger than
 *  PROBE_TTL_MS is not repeated. */
let probeCache = new Map<string, { caps: LibraryCaps; at: number }>()
const PROBE_TTL_MS = 300_000
/** A probe that could read none of the tree's databases is tried again this soon. */
const PROBE_RETRY_MS = 30_000

/**
 * What each tree was last found to support, kept on disk
 * (INDEX_BASE/library-caps.json) so that neither a restart nor a rebuild
 * resets a library to "no words, no vectors" until the next probe: a
 * library's words were once left out of search after every build, until
 * Settings happened to probe it again. A record marked stale is still used;
 * it is probed again in the background.
 */
export interface KnownCaps { caps: LibraryCaps; probedAt: string; stale: boolean; error?: string }
const CAPS_FILE = path.join(INDEX_BASE, 'library-caps.json')
let known: Map<string, KnownCaps> | null = null
const probing = new Map<string, Promise<LibraryCaps>>()

function knownCapsMap(): Map<string, KnownCaps> {
  if (known) return known
  known = new Map()
  try {
    const raw = JSON.parse(fs.readFileSync(CAPS_FILE, 'utf8')) as Record<string, KnownCaps>
    for (const [root, r] of Object.entries(raw)) {
      if (r && r.caps && typeof r.caps.index === 'boolean') known.set(root, { caps: { index: !!r.caps.index, fullText: !!r.caps.fullText, vectors: !!r.caps.vectors }, probedAt: String(r.probedAt ?? ''), stale: !!r.stale, ...(r.error ? { error: String(r.error) } : {}) })
    }
  } catch { /* none yet */ }
  return known
}

function saveKnownCaps(): void {
  try {
    fs.mkdirSync(path.dirname(CAPS_FILE), { recursive: true })
    const tmp = `${CAPS_FILE}.${process.pid}.tmp`
    fs.writeFileSync(tmp, JSON.stringify(Object.fromEntries(knownCapsMap()), null, 1))
    fs.renameSync(tmp, CAPS_FILE)
  } catch (e) { console.warn(`library capabilities not saved: ${(e as Error).message}`) }
}

/** What a tree was last found to support, and whether that is stale; null
 *  when it was never probed. */
export function knownCaps(indexRoot: string): KnownCaps | null {
  return knownCapsMap().get(path.resolve(indexRoot)) ?? null
}

/** The capabilities a listing shows: this process's probe, else the record
 *  on disk, else `fallback`. Where there is no fresh probe, one runs in the
 *  background, so the next request has it. */
function capsOf(abs: string, fallback: () => LibraryCaps): LibraryCaps {
  // Whether the index is there is looked at every time (one stat); what it
  // holds comes from the probe, or the record when there is no fresh one.
  const index = fs.existsSync(path.join(abs, 'db.db'))
  const fresh = probeCache.get(abs)
  if (fresh && Date.now() - fresh.at < PROBE_TTL_MS) return { ...fresh.caps, index }
  const k = knownCapsMap().get(abs)
  if (gufiAvailable() && index) void probeLibrary(abs).catch(() => {})
  const caps = k?.caps ?? fresh?.caps ?? fallback()
  return { ...caps, index }
}

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
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: timeoutMs, maxBuffer: 1 << 20, env: gufiEnv() },
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
  if (cached && Date.now() - cached.at < PROBE_TTL_MS) return cached.caps
  const inFlight = probing.get(abs)
  if (inFlight) return inFlight
  const p = probeNow(abs).finally(() => probing.delete(abs))
  probing.set(abs, p)
  return p
}

async function probeNow(abs: string): Promise<LibraryCaps> {
  const caps: LibraryCaps = { index: false, fullText: false, vectors: false }
  let tried = 0, failed = 0, lastError = ''
  if (fs.existsSync(path.join(abs, 'db.db'))) {
    caps.index = true
    if (gufiAvailable()) {
      for (const db of sampleDbs(abs)) {
        tried++
        try {
          const out = await sqlite(`ATTACH '${db.replace(/'/g, "''")}' AS d;\nSELECT name FROM d.sqlite_master WHERE name IN ('words','gvec');\n`)
          if (/\bwords\b/.test(out)) caps.fullText = true
          if (/\bgvec\b/.test(out)) caps.vectors = true
          if (caps.fullText && caps.vectors) break
        } catch (e) { failed++; lastError = String((e as Error).message ?? e).split('\n')[0].slice(0, 200) }
      }
    }
  }
  const map = knownCapsMap()
  const was = map.get(abs)
  if (tried > 0 && failed === tried) {
    // Not one database could be read: say nothing new about the tree. The
    // last known capabilities stay, marked stale, and the probe runs again
    // soon (a busy disk, a database being swapped in).
    const keep = was?.caps ?? caps
    map.set(abs, { caps: keep, probedAt: was?.probedAt ?? '', stale: true, error: lastError })
    saveKnownCaps()
    probeCache.set(abs, { caps: keep, at: Date.now() - PROBE_TTL_MS + PROBE_RETRY_MS })
    return keep
  }
  const changed = !was || was.stale || was.caps.index !== caps.index || was.caps.fullText !== caps.fullText || was.caps.vectors !== caps.vectors
  map.set(abs, { caps, probedAt: new Date().toISOString(), stale: false })
  if (changed) saveKnownCaps()
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
    // The main folder counts as indexed where GUFI is set up for it, as it always has.
    caps: (c => ({ ...c, index: c.index || gufiAvailable() }))(capsOf(path.resolve(GUFI_INDEX), () => ({ index: gufiAvailable(), fullText: gufiAvailable(), vectors: false }))),
    meaning: indexSettings(PRIMARY_ID).meaning,
    depth: indexSettings(PRIMARY_ID).depth,
  }
}

function fromDef(d: LibraryDef, pinned: boolean): Library {
  const abs = path.resolve(d.indexRoot)
  return {
    id: d.id,
    label: d.label || path.basename(abs) || d.id,
    indexRoot: abs,
    sourceRoot: d.sourceRoot ? path.resolve(d.sourceRoot) : null,
    cacheRoot: d.build?.cacheRoot ?? null,
    build: d.build ?? null,
    writable: false,
    primary: false,
    pinned,
    caps: capsOf(abs, () => ({ index: fs.existsSync(path.join(abs, 'db.db')), fullText: false, vectors: false })),
    meaning: d.build ? indexSettings(d.id).meaning : null,
    depth: d.build ? indexSettings(d.id).depth : null,
  }
}

/** A viewer's own libraries on their clusters, once built. Only where a
 *  viewer is named (null being the one user of a Studio without sign-in);
 *  the deployment's own listings pass none and never show them. */
function userLibrariesOf(viewerId: string | null | undefined): Library[] {
  if (viewerId === undefined) return []
  const owner = ownerOf(viewerId)
  if (!owner) return []
  // gufiBin '' is gufi_query from the login's PATH, on a host whose login runs nothing else.
  return loadUserLibraries(owner).filter(d => d.indexRoot && d.gufiBin != null).map(d => ({
    id: d.id, label: d.label, indexRoot: remoteRootOf(owner, d.id), sourceRoot: null,
    writable: false, primary: false, pinned: false, caps: { index: true, fullText: !!d.fullText, vectors: !!d.vectors }, site: d.site,
    meaning: d.build ? indexSettings(d.id).meaning : null,
    depth: d.build ? indexSettings(d.id).depth : null,
  }))
}

/** Every mounted library, primary first, then the viewer's own. `label`
 *  names the primary. */
export function listLibraries(primaryLabel = path.basename(KB_ROOT), viewerId?: string | null): Library[] {
  const seen = new Set<string>([PRIMARY_ID])
  const out: Library[] = [primaryLibrary(primaryLabel)]
  for (const [defs, pinned] of [[pinnedDefs(), true], [loadAdded(), false]] as const) {
    for (const d of defs) {
      if (seen.has(d.id)) continue
      seen.add(d.id)
      out.push(fromDef(d, pinned))
    }
  }
  for (const u of userLibrariesOf(viewerId)) if (!seen.has(u.id)) { seen.add(u.id); out.push(u) }
  return out
}

/** Refresh the capabilities of every library; called at startup and after a change. */
export async function probeAll(primaryLabel?: string): Promise<Library[]> {
  const libs = listLibraries(primaryLabel)
  for (const l of libs) l.caps = await probeLibrary(l.indexRoot)
  return libs
}

export function getLibrary(id: string | undefined | null, primaryLabel?: string, viewerId?: string | null): Library {
  const want = (id || PRIMARY_ID).trim()
  // "all" names every library at once, which an action on files cannot
  // take: say so, instead of looking for a library of that name.
  if (want.toLowerCase() === 'all') throw new KbError(400, 'This action works in one library at a time, and the items are in more than one (All libraries is selected).')
  const lib = listLibraries(primaryLabel, viewerId).find(l => l.id === want)
  if (!lib) throw new KbError(404, `no library named ${want}`)
  return lib
}

/** The guard every mutation route runs before touching files or the index. */
export function requireWritable(lib: Library): void {
  if (!lib.writable) throw new KbError(403, `${lib.label} is read only`)
}

/** Names a request can use that are not a library: "all" means every one. */
const RESERVED = new Set([PRIMARY_ID, 'all'])

/** An id for a library added without one: from its label, else its index
 *  directory's name, unique among the libraries. Not "u-...", which a
 *  user's own libraries use. */
export function libraryIdFor(label: string | undefined, indexRoot: string): string {
  const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 34).replace(/-+$/, '')
  let base = slug(label ?? '') || slug(path.basename(path.resolve(indexRoot || '.'))) || 'library'
  if (base.startsWith('u-')) base = `lib-${base}`.slice(0, 34)
  const taken = new Set([...RESERVED, ...listLibraries().map(l => l.id)])
  let id = base
  for (let i = 2; taken.has(id); i++) id = `${base}-${i}`
  return id
}

/** An id a new library may take, or the reason it may not. */
export function checkNewId(id: string): void {
  if (!ID_RE.test(id)) throw new KbError(400, 'a library needs an id (letters, digits, dash, underscore) and an index root')
  if (id === 'all') throw new KbError(409, 'all is reserved: it names every library at once')
  if (id === PRIMARY_ID || listLibraries().some(l => l.id === id)) throw new KbError(409, `a library named ${id} already exists`)
}

export async function addLibrary(def: LibraryDef): Promise<Library> {
  if (!def.id && typeof def.indexRoot === 'string' && def.indexRoot) def = { ...def, id: libraryIdFor(def.label, def.indexRoot) }
  if (!isDef(def)) throw new KbError(400, 'a library needs an id (letters, digits, dash, underscore) and an index root')
  checkNewId(def.id)
  const abs = path.resolve(def.indexRoot)
  if (!fs.existsSync(abs)) throw new KbError(400, `no such directory: ${def.indexRoot}`)
  if (def.sourceRoot && !fs.existsSync(path.resolve(def.sourceRoot))) throw new KbError(400, `no such directory: ${def.sourceRoot}`)
  const caps = await probeLibrary(abs)
  if (!caps.index) throw new KbError(400, `${def.indexRoot} does not look like a GUFI index (no db.db at its root)`)
  saveAdded([...loadAdded(), { id: def.id, label: def.label?.slice(0, 60) || undefined, indexRoot: abs, sourceRoot: def.sourceRoot ? path.resolve(def.sourceRoot) : null }])
  return getLibrary(def.id)
}

/** A library the Studio is to build of a folder on this computer: listed
 *  at once, searchable once its index is there (libraryBuild.ts). */
export function addBuiltLibrary(def: LibraryDef & { build: LocalBuild }): Library {
  checkNewId(def.id)
  saveAdded([...loadAdded(), {
    id: def.id, label: def.label?.slice(0, 60) || undefined, indexRoot: path.resolve(def.indexRoot),
    sourceRoot: def.sourceRoot ? path.resolve(def.sourceRoot) : null, build: def.build,
  }])
  return getLibrary(def.id)
}

/** The stored definition of a library added in Settings. */
export function addedDef(id: string): LibraryDef | null {
  return loadAdded().find(d => d.id === id) ?? null
}

/** Records a built library's state. */
export function updateBuild(id: string, patch: Partial<LocalBuild>): LibraryDef | null {
  const defs = loadAdded()
  const d = defs.find(x => x.id === id)
  if (!d?.build) return null
  d.build = { ...d.build, ...patch }
  saveAdded(defs)
  return d
}

/** After a tree was built or changed: what it was found to support is
 *  stale, and is probed again (at once, in the background). Until then the
 *  last known capabilities stand; they are never reset to "none", which
 *  would leave the library's words and vectors out of search. */
export function staleProbe(indexRoot: string): void {
  const abs = path.resolve(indexRoot)
  probeCache.delete(abs)
  const k = knownCapsMap().get(abs)
  if (k && !k.stale) { k.stale = true; saveKnownCaps() }
  if (gufiAvailable()) void probeLibrary(abs).catch(() => {})
}

/** Set by libraryLabels.ts, which imports this module: drops what it holds
 *  in memory for a removed library. */
let forgetLabelsHook: (id: string) => void = () => {}
export function onLibraryRemoved(fn: (id: string) => void): void { forgetLabelsHook = fn }

export function removeLibrary(id: string): void {
  const lib = getLibrary(id)
  if (lib.primary || lib.pinned) throw new KbError(403, `${lib.label} is set by the deployment and cannot be removed here`)
  const def = addedDef(id)
  if (def?.build && (def.build.state === 'building' || def.build.state === 'updating')) {
    throw new KbError(409, `${lib.label} is being indexed; remove it once the build has finished`)
  }
  saveAdded(loadAdded().filter(d => d.id !== id))
  // A built library's extraction cache is the Studio's, and so is its index
  // when it is in the Studio's own folder; an index in a folder that was
  // chosen for it stays where it is.
  if (def?.build) fs.rmSync(builtLibraryDir(id), { recursive: true, force: true })
  // The labels the Studio kept for its files go with it.
  fs.rmSync(path.join(builtLibraryDir(id), 'labels.json'), { force: true })
  fs.rmSync(path.join(builtLibraryDir(id), 'labels.json.bak'), { force: true })
  forgetLabelsHook(id)
  void forgetIndexSettings(id)
}

export function publicLibrary(l: Library): PublicLibrary {
  return { id: l.id, label: l.label, primary: l.primary, writable: l.writable, pinned: l.pinned, source: !!l.sourceRoot || !!l.site, caps: l.caps, site: l.site ?? null, meaning: l.meaning ?? null, depth: l.depth ?? null }
}

/** What Settings shows of a build: its state and, while it runs, how far it
 *  has got (from libraryBuild.ts, which keeps that in memory). */
export interface PublicBuild extends Omit<LocalBuild, 'cacheRoot'> { progress: BuildProgress | null }
export interface BuildProgress { phase: string; done: number; total: number; current: string | null }
let progressOf: (id: string) => BuildProgress | null = () => null
export function setBuildProgressSource(fn: (id: string) => BuildProgress | null): void { progressOf = fn }

/** A library as Settings shows it: with its index and source roots, so
 *  whoever manages the libraries can see what each one points at. Only the
 *  Settings routes send this; the client config every viewer loads keeps
 *  to publicLibrary, without paths. */
export function settingsLibrary(l: Library): PublicLibrary & { indexRoot: string; sourceRoot: string | null; build?: PublicBuild } {
  const b = l.build
  return {
    ...publicLibrary(l), indexRoot: l.indexRoot, sourceRoot: l.sourceRoot,
    ...(b ? { build: { indexDir: b.indexDir, managed: b.managed, state: b.state, error: b.error, builtAt: b.builtAt, files: b.files, dirs: b.dirs, progress: progressOf(l.id) } } : {}),
  }
}

/** Tests replace the stored set and the probe memory between cases. */
export function resetLibrariesForTests(opts: { keepKnownCaps?: boolean } = {}): void {
  added = null
  probeCache = new Map()
  probing.clear()
  // A restart: the record on disk is read again; without keepKnownCaps it
  // is gone too, as for a new Studio.
  known = null
  if (!opts.keepKnownCaps) fs.rmSync(CAPS_FILE, { force: true })
}
