import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, KB_ROOT, PROJECT_ROOT, PYTHON_BIN, SEMANTIC_MAP_DIR, gufiEnv, indexerEnv } from './config.js'
import { indexSettings, type IndexDepth } from './indexSettings.js'
import { KbError } from './kb.js'
import { builtLibraryDir, getLibrary, lacks, listLibraries, onLibraryRemoved, PRIMARY_ID, type Library } from './libraries.js'
import { countRemoteVectors, dequantize, fetchRemoteVectors, remoteMapFiles, remoteNeighbors, type VectorSource } from './remoteMap.js'
import { siteName } from './remoteExec.js'
import { onRemoteIndexBuilt, onUserLibraryRemoved, remoteMapHome, remoteTarget } from './userLibraries.js'

/**
 * The semantic map of a library, on the Stats page: the files that have
 * embeddings, placed by what they say, in clusters. Each library on this
 * computer with vectors has a map of its own: the main folder's in
 * SEMANTIC_MAP_DIR, another library's in its own folder under INDEX_BASE
 * (builtLibraryDir/semantic-map), removed with the library. A library on
 * another system keeps its vectors there: a sample of them is fetched with
 * one gufi_query (remoteMap.ts) and its map is made here from those, in
 * its owner's folder (remoteMapHome), and made again when a check finds
 * its vectors changed there (at most hourly while it is looked at, and
 * after a build there), or on request.
 *
 * It is built in the background, never while someone waits: by
 * indexer/semantic_map.py after a meaning phase that embedded something,
 * and once on first use when there is none yet. The script reads the
 * vectors from the index directly and writes, into the map's folder, an
 * overview (every cluster, and a sample of at most 3,000 files spread
 * across them) and each cluster's full member list. The routes only read
 * those files, so a page load never recomputes the map, and the last map is
 * served while a new one is made.
 *
 * Without numpy (a clone that has not fetched the embedding packages) the
 * server draws a smaller map itself from at most FALLBACK_FILES files, and
 * says so.
 */

/** A cluster: `label` is its distinctive words (or, without text, its
 *  folders); `folder` its folder name, kept for hover; `terms` the words in
 *  its label. Maps made before these were added have the label only. */
export interface MapCluster { c: number; label: string; count: number; x: number; y: number; z: number; folder?: string; terms?: string[] }
export interface MapPoint { p: string; x: number; y: number; z: number; c?: number }
/** Two clusters near each other in the full embedding space: `sim` is the
 *  cosine of their mean vectors. Each cluster has its two nearest. */
export interface MapLink { a: number; b: number; sim: number }
/** A file near another by meaning, with its place on the map where it has one. */
export interface MapNeighbor { p: string; sim: number; x?: number; y?: number; z?: number; c?: number }
/** The library a map is of, as the page needs it to say why there is none
 *  and to offer the depth control: `kind` is main (the main folder), built
 *  (a folder on this computer the Studio indexes), connected (a GUFI index
 *  on this computer used as it is) or remote (an index on another system). */
export interface MapLibrary {
  id: string; label: string; kind: 'main' | 'built' | 'connected' | 'remote'; depth: IndexDepth | null; vectors: boolean
  /** For a library on another system: the system, as its connection names it. */
  host?: string
}
/** How far the making of a remote library's map has got: counting its files
 *  with vectors there, fetching a sample of them, or drawing the map. */
export interface MapProgress { phase: 'counting' | 'fetching' | 'drawing'; files?: number; of?: number }
/** A remote library's map: where it was fetched from, when, and how much;
 *  `models`, the models its vectors there were made with (gvecmodel), most
 *  files first: the map is of the first, and the files of any other are
 *  left out of it. */
export interface RemoteMapInfo { host: string; fetchedAt: string | null; checkedAt: string | null; bytes?: number; fetchMs?: number; models?: { model: string; files: number }[] }
export interface SemanticMapOverview {
  available: boolean
  /** Why there is no map: its depth stops before search by meaning (off),
   *  the index has no embeddings yet (none), the last build of the map
   *  failed (or, for a library on another system, its vectors could not be
   *  fetched), an index used as it is has no vectors the Studio can read
   *  (connected), or a library on another system has none there
   *  (remote-none). */
  reason?: 'off' | 'none' | 'failed' | 'connected' | 'remote-none'
  library?: MapLibrary
  /** A new map is being made; the one returned stays until it is ready. */
  building: boolean
  progress?: MapProgress | null
  error?: string | null
  builtAt?: string
  model?: string
  /** Files with embeddings, and how many of them the overview carries. */
  files?: number
  shown?: number
  /** Where `files` is a sample: how many files have embeddings there. */
  of?: number
  remote?: RemoteMapInfo
  clusters?: MapCluster[]
  links?: MapLink[]
  points?: MapPoint[]
  /** Drawn by the server from a sample, without numpy. */
  fallback?: boolean
}

const SCRIPT = path.join(PROJECT_ROOT, 'indexer', 'semantic_map.py')
/** The map the server draws itself when numpy is missing: this many files at most. */
const FALLBACK_FILES = 4000
/** A cluster's points per request, at most. */
export const CLUSTER_PAGE_MAX = 5000

/** Where a library's map is made from and written to. `key` names it
 *  among the maps kept: the library's id, or for a library on another
 *  system its root, which names its owner too. `remote` is set for one on
 *  another system, with, when it is built, the vectors fetched from it. */
interface MapTarget {
  id: string; key: string; index: string; out: string
  remote?: { root: string; home: string; vectors?: string; model?: string; of?: number }
}
/** The folder a library's map is kept in. */
export function semanticMapDir(id: string): string {
  return id === PRIMARY_ID ? SEMANTIC_MAP_DIR : path.join(builtLibraryDir(id), 'semantic-map')
}

interface MapState {
  running: boolean
  /** Another rebuild was asked for while one ran. */
  again: boolean
  timer: NodeJS.Timeout | null
  dueAt: number
  /** A first build was started for a missing map, in this process. */
  firstTried: boolean
  lastEnd: number
  error: string | null
  /** The last build found no vectors it could read, and made no map. */
  empty: boolean
  overviewCache: { mtimeMs: number; data: SemanticMapOverview } | null
  clusterCache: Map<number, { mtimeMs: number; data: { c: number; label: string; count: number; points: MapPoint[] } }>
  /** A library on another system: what its next refresh is for, whether
   *  it fetches whatever the check finds, and how far it has got. */
  target: MapTarget | null
  force: boolean
  progress: MapProgress | null
}
const states = new Map<string, MapState>()
function stateOf(key: string): MapState {
  let st = states.get(key)
  if (!st) {
    st = { running: false, again: false, timer: null, dueAt: 0, firstTried: false, lastEnd: 0, error: null, empty: false, overviewCache: null, clusterCache: new Map(),
      target: null, force: false, progress: null }
    states.set(key, st)
  }
  return st
}

/** A library removed: its map goes with it, from disk and from memory. */
onLibraryRemoved(id => {
  const st = states.get(id)
  if (st?.timer) clearTimeout(st.timer)
  states.delete(id)
  forgetNeighbors(id)
  fs.rmSync(semanticMapDir(id), { recursive: true, force: true })
})
/** One on another system: its folder goes with it (userLibraries.ts). */
onUserLibraryRemoved(root => {
  const st = states.get(root)
  if (st?.timer) clearTimeout(st.timer)
  states.delete(root)
  forgetNeighbors(root)
})
/** A build there that finished: a check whether its vectors changed. */
onRemoteIndexBuilt(root => {
  const id = root.slice(root.lastIndexOf(':') + 1)
  scheduleRemoteMap(remoteMapTarget(id, root), false, 5_000)
})

const remoteMapTarget = (id: string, root: string): MapTarget => {
  const home = remoteMapHome(root)
  return { id, key: root, index: root, out: path.join(home, 'semantic-map'), remote: { root, home } }
}

/** The library a map would be of, and what it can be made from. */
function libraryOf(id: string, viewer: string | null | undefined): { lib: MapLibrary; target: MapTarget; meaningOff: boolean } {
  if (id === PRIMARY_ID) {
    const meaningOff = indexSettings(PRIMARY_ID).meaning === 'off'
    return {
      lib: { id, label: path.basename(KB_ROOT) || 'main folder', kind: 'main', depth: indexSettings(PRIMARY_ID).depth, vectors: !meaningOff },
      target: { id, key: id, index: GUFI_INDEX, out: SEMANTIC_MAP_DIR }, meaningOff,
    }
  }
  const l: Library = getLibrary(id, undefined, viewer)
  const kind: MapLibrary['kind'] = l.site ? 'remote' : l.build ? 'built' : 'connected'
  // A library the Studio builds, here or on another system, has a depth.
  const meaningOff = kind === 'built' || (kind === 'remote' && l.depth) ? (l.meaning ?? 'off') === 'off' : false
  return {
    // Vectors not found yet are looked for unless a count found none (lacks).
    lib: { id, label: l.label, kind, depth: l.depth ?? null, vectors: !lacks(l, 'vectors'), ...(l.site ? { host: siteName(l.site) } : {}) },
    target: kind === 'remote' ? remoteMapTarget(id, l.indexRoot) : { id, key: id, index: l.indexRoot, out: semanticMapDir(id) },
    meaningOff,
  }
}

function readJson<T>(file: string, cache: { mtimeMs: number; data: T } | null | undefined): { mtimeMs: number; data: T } | null {
  let st: fs.Stats
  try { st = fs.statSync(file) } catch { return null }
  if (cache && cache.mtimeMs === st.mtimeMs) return cache
  try { return { mtimeMs: st.mtimeMs, data: JSON.parse(fs.readFileSync(file, 'utf8')) as T } } catch { return null }
}

/** The overview of a library's map (the main folder's by default), as the
 *  Stats page draws it first. `kick` false (the picker's list) starts no
 *  fetch from another system. */
export function semanticMapOverview(id: string = PRIMARY_ID, viewer?: string | null, opts: { kick?: boolean } = {}): SemanticMapOverview {
  const { lib, target, meaningOff } = libraryOf(id, viewer)
  const st = stateOf(target.key)
  if (meaningOff) return { available: false, reason: 'off', building: false, library: lib }
  if (target.remote) return remoteOverview(lib, target, st, opts.kick !== false)
  const hit = readJson<SemanticMapOverview>(path.join(target.out, 'overview.json'), st.overviewCache)
  if (hit?.data.available) {
    st.overviewCache = hit
    return { ...hit.data, building: st.running, library: lib }
  }
  // An index used as it is, with no vectors the Studio can read (none, or
  // only in tables of another schema): nothing to make.
  if (lib.kind === 'connected' && (!lib.vectors || st.empty)) return { available: false, reason: 'connected', building: false, library: lib }
  // None yet, say after an update from a version that made it on request, or
  // for a library whose meaning came before maps were made per library:
  // made once, in the background, when there is an index to make it from.
  if (!st.firstTried && !st.running && fs.existsSync(path.join(target.index, 'db.db'))) {
    st.firstTried = true
    scheduleSemanticMapRebuild(0, id)
  }
  return { available: false, reason: st.error ? 'failed' : 'none', building: st.running || !!st.timer, error: st.error, library: lib }
}

/** Every library's map at a glance, for the page's picker and its default:
 *  which have one, of how many files, and why the others have none. */
export function semanticMapList(libraries: Library[], viewer?: string | null): { id: string; label: string; available: boolean; files: number; reason?: SemanticMapOverview['reason']; building: boolean }[] {
  return libraries.map(l => {
    try {
      const o = semanticMapOverview(l.id, viewer, { kick: false })
      return { id: l.id, label: l.label, available: o.available, files: o.files ?? 0, ...(o.reason ? { reason: o.reason } : {}), building: o.building }
    } catch {
      return { id: l.id, label: l.label, available: false, files: 0, reason: 'none' as const, building: false }
    }
  })
}

/** One cluster's files, a page at a time, in the random order the map
 *  stored them, so any first page is a fair sample of the cluster. */
export function semanticMapCluster(c: number, offset = 0, limit = 2000, id: string = PRIMARY_ID, viewer?: string | null): { c: number; label: string; count: number; offset: number; points: MapPoint[] } {
  if (!Number.isInteger(c) || c < 0) throw new KbError(400, 'cluster: a number from 0')
  const { target } = libraryOf(id, viewer)
  const st = stateOf(target.key)
  const hit = readJson(path.join(target.out, `cluster-${c}.json`), st.clusterCache.get(c))
  if (!hit) throw new KbError(404, 'There is no such cluster in the map.')
  st.clusterCache.delete(c)
  st.clusterCache.set(c, hit)
  // A few clusters are kept; the oldest goes.
  while (st.clusterCache.size > 4) st.clusterCache.delete(st.clusterCache.keys().next().value!)
  const start = Math.max(0, Math.floor(offset) || 0)
  const n = Math.min(CLUSTER_PAGE_MAX, Math.max(1, Math.floor(limit) || 2000))
  return { c: hit.data.c, label: hit.data.label, count: hit.data.count, offset: start, points: hit.data.points.slice(start, start + n) }
}

/**
 * Asks for a new map of a library (the main folder's by default) in
 * `delayMs`. Calls close together make one build, the earliest time asked
 * for wins, and a call while a build runs makes one more after it, so the
 * map catches up with a sync that ran meanwhile.
 */
export function scheduleSemanticMapRebuild(delayMs: number, id: string = PRIMARY_ID): void {
  const st = stateOf(id)
  if (st.running) { st.again = true; return }
  delayMs = Math.max(0, delayMs) * delayScale
  const due = Date.now() + delayMs
  if (st.timer && st.dueAt <= due) return
  if (st.timer) clearTimeout(st.timer)
  st.dueAt = due
  st.timer = setTimeout(() => { st.timer = null; void rebuild(id) }, delayMs)
  st.timer.unref?.()
}

/** Tests replace the build, and see how often it runs, and for which
 *  library; `delayScale` 0 runs a rebuild asked for later at once. */
type Builder = (t: MapTarget) => Promise<void>
let builder: Builder = buildWithPython
let delayScale = 1
/** How long a remote library's map goes before a check whether its vectors
 *  changed there, while it is looked at. */
const REMOTE_CHECK_MS = 60 * 60_000
let remoteCheckMs = REMOTE_CHECK_MS
export function setSemanticMapBuilderForTests(fn: Builder | null, opts: { delayScale?: number; remoteCheckMs?: number } = {}): void {
  builder = fn ?? buildWithPython
  delayScale = opts.delayScale ?? 1
  remoteCheckMs = opts.remoteCheckMs ?? REMOTE_CHECK_MS
}
export function resetSemanticMapForTests(): void {
  for (const st of states.values()) if (st.timer) clearTimeout(st.timer)
  states.clear()
  neighborCaches.clear()
}

async function rebuild(id: string): Promise<void> {
  const st = stateOf(id)
  if (st.running) { st.again = true; return }
  let target: MapTarget
  try {
    const t = libraryOf(id, undefined)
    if (t.meaningOff || t.target.remote) return
    target = t.target
  } catch { return } // removed meanwhile
  st.running = true
  try {
    await builder(target)
    st.error = null
    st.empty = !fs.existsSync(path.join(target.out, 'overview.json'))
  } catch (e) {
    st.error = String((e as Error).message ?? e).slice(0, 400)
    console.warn(`semantic map (${id}): ${st.error}`)
  } finally {
    st.running = false
    st.lastEnd = Date.now()
    if (st.again) { st.again = false; scheduleSemanticMapRebuild(60_000, id) }
  }
}

function buildWithPython(t: MapTarget): Promise<void> {
  const from = t.remote?.vectors
    ? ['--vectors', t.remote.vectors, '--model', t.remote.model ?? '', ...(t.remote.of ? ['--of', String(t.remote.of)] : [])]
    : ['--index', t.index]
  return new Promise((resolve, reject) => {
    execFile(PYTHON_BIN, [SCRIPT, ...from, '--out', t.out],
      { timeout: 30 * 60_000, maxBuffer: 4 * 1024 * 1024, env: indexerEnv({ PYTHONDONTWRITEBYTECODE: '1' }) },
      (err, stdout, stderr) => {
        if (!err) {
          try { console.log(`semantic map (${t.id}): ${JSON.stringify(JSON.parse(stdout.trim().split('\n').pop() ?? '{}'))}`) } catch { /* the summary is for the log */ }
          return resolve()
        }
        if ((err as { code?: unknown }).code === 3) return buildWithoutNumpy(t).then(resolve, reject)
        reject(new Error(String(stderr || err.message).trim().split('\n').slice(-3).join(' ')))
      })
  })
}

// ---- a library on another system: its vectors fetched, the map made here ----

/** In a remote library's folder (remoteMapHome): the files fetched, a JSON
 *  line each (remoteMap.ts's rows), and what the last fetch and check found. */
const VECTORS_FILE = 'vectors.jsonl'
const RECORD_FILE = 'state.json'
interface RemoteRecord {
  /** The counts the last check found (remoteMap.ts's ModelCount, per model):
   *  the next check fetches again only when they differ. */
  fingerprint?: string
  models?: { model: string; files: number }[]
  model?: string
  source?: VectorSource
  files?: number
  of?: number
  bytes?: number
  fetchMs?: number
  fetchedAt?: string
  checkedAt?: number
  /** The last check found no vectors there, or too few for a map (`of`). */
  empty?: boolean
}
function readRecord(home: string): RemoteRecord {
  try { return JSON.parse(fs.readFileSync(path.join(home, RECORD_FILE), 'utf8')) as RemoteRecord } catch { return {} }
}
function writeRecord(home: string, rec: RemoteRecord): void {
  fs.mkdirSync(home, { recursive: true })
  fs.writeFileSync(path.join(home, `${RECORD_FILE}.tmp`), JSON.stringify(rec))
  fs.renameSync(path.join(home, `${RECORD_FILE}.tmp`), path.join(home, RECORD_FILE))
}

function remoteOverview(lib: MapLibrary, t: MapTarget, st: MapState, kick: boolean): SemanticMapOverview {
  // Not built yet: its own build says how far it has got.
  try { remoteTarget(t.remote!.root) } catch { return { available: false, reason: 'none', building: false, library: lib } }
  const rec = readRecord(t.remote!.home)
  const busy = st.running || !!st.timer
  const remote: RemoteMapInfo = {
    host: lib.host ?? '', fetchedAt: rec.fetchedAt ?? null, checkedAt: rec.checkedAt ? new Date(rec.checkedAt).toISOString() : null,
    ...(rec.bytes !== undefined ? { bytes: rec.bytes, fetchMs: rec.fetchMs } : {}),
    ...(rec.models ? { models: rec.models } : {}),
  }
  const due = Date.now() - (rec.checkedAt ?? 0) >= remoteCheckMs
  const hit = readJson<SemanticMapOverview>(path.join(t.out, 'overview.json'), st.overviewCache)
  if (hit?.data.available) {
    st.overviewCache = hit
    // Looked at: a check, at most hourly, whether its vectors changed there.
    if (kick && due && !busy) scheduleRemoteMap(t, false)
    return { ...hit.data, building: st.running || !!st.timer, progress: st.progress, error: st.error, library: lib, remote }
  }
  if (kick && !busy && (!st.firstTried || (rec.empty && due))) {
    st.firstTried = true
    scheduleRemoteMap(t, true)
  }
  const now = st.running || !!st.timer
  const reason = now ? 'none' : st.error ? 'failed' : rec.empty ? 'remote-none' : 'none'
  return { available: false, reason, building: now, progress: st.progress, error: st.error, library: lib, remote, ...(rec.empty && rec.of ? { of: rec.of } : {}) }
}

/** Asks for a remote library's map to be checked (and fetched and made
 *  again if its vectors changed there), or with `force` fetched and made
 *  again regardless, in `delayMs`. One runs at a time per library; one
 *  asked for meanwhile runs after it. */
function scheduleRemoteMap(t: MapTarget, force: boolean, delayMs = 0): void {
  const st = stateOf(t.key)
  st.target = t
  st.force ||= force
  if (st.running) { st.again = true; return }
  if (st.timer) return
  st.timer = setTimeout(() => { st.timer = null; void refreshRemote(t.key) }, delayMs * delayScale)
  st.timer.unref?.()
}

async function refreshRemote(key: string): Promise<void> {
  const st = stateOf(key)
  const t = st.target
  if (!t?.remote || st.running) return
  const force = st.force
  st.force = false
  st.running = true
  const { root, home } = t.remote
  try {
    st.progress = { phase: 'counting' }
    const { models } = await countRemoteVectors(root)
    const was = readRecord(home)
    const checkedAt = Date.now()
    const fingerprint = JSON.stringify(models)
    const found = models.map(m => ({ model: m.model, files: m.files }))
    const best = models[0]
    if (!best) {
      // No vectors there (any longer): no map.
      await fsp.rm(t.out, { recursive: true, force: true })
      await fsp.rm(path.join(home, VECTORS_FILE), { force: true })
      writeRecord(home, { fingerprint, models: found, checkedAt, empty: true })
      st.error = null
      return
    }
    if (!force && was.fingerprint === fingerprint && fs.existsSync(path.join(t.out, 'overview.json'))) {
      writeRecord(home, { ...was, models: found, checkedAt })
      st.error = null
      return
    }
    const cap = remoteMapFiles()
    st.progress = { phase: 'fetching', files: Math.min(cap, best.files), of: best.files }
    const got = await fetchRemoteVectors(root, best, cap)
    fs.mkdirSync(home, { recursive: true })
    const vectors = path.join(home, VECTORS_FILE)
    await fsp.writeFile(`${vectors}.tmp`, got.files.map(f => JSON.stringify({ p: f.p, r: f.r, v: f.v, ...(f.t ? { t: f.t } : {}) })).join('\n') + '\n')
    await fsp.rename(`${vectors}.tmp`, vectors)
    st.progress = { phase: 'drawing', files: got.files.length, of: best.files }
    await builder({ ...t, remote: { ...t.remote, vectors, model: best.model, of: best.files } })
    if (!fs.existsSync(path.join(t.out, 'overview.json'))) {
      // Too few files to place (fewer than three): none, with how many.
      writeRecord(home, { fingerprint, models: found, model: best.model, of: best.files, checkedAt, empty: true })
      st.error = null
      return
    }
    writeRecord(home, {
      fingerprint, models: found, model: best.model, source: best.source, files: got.files.length, of: best.files,
      bytes: got.bytes, fetchMs: got.ms, fetchedAt: new Date().toISOString(), checkedAt,
    })
    console.log(`semantic map (${t.id}): ${got.files.length} of ${best.files} files fetched, ${got.bytes} bytes in ${got.ms} ms`)
    st.error = null
  } catch (e) {
    st.error = String((e as Error).message ?? e).slice(0, 400)
    console.warn(`semantic map (${t.id}): ${st.error}`)
  } finally {
    st.running = false
    st.progress = null
    st.lastEnd = Date.now()
    if (st.again) { st.again = false; scheduleRemoteMap(t, false) }
  }
}

/** A library's map made again now (the Refresh on the map, and its retry):
 *  for one on another system, its vectors fetched again. */
export function refreshSemanticMap(id: string, viewer?: string | null): SemanticMapOverview {
  const { target, meaningOff } = libraryOf(id, viewer)
  if (meaningOff) throw new KbError(409, 'This library has no search by meaning, so there is no map to make.')
  const st = stateOf(target.key)
  st.error = null
  if (target.remote) {
    remoteTarget(target.remote.root) // not built yet: 409
    st.firstTried = true
    scheduleRemoteMap(target, true)
  } else {
    scheduleSemanticMapRebuild(0, id)
  }
  return semanticMapOverview(id, viewer)
}

// ---- without numpy: a smaller map, drawn here ----

const SEP = '\x1f'

function listVectorDbs(root: string): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    let names: fs.Dirent[] = []
    try { names = fs.readdirSync(dir, { withFileTypes: true }) } catch { return }
    for (const e of names) {
      if (e.name.startsWith('.')) continue
      const p = path.join(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.name === 'db.db') out.push(p)
    }
  }
  walk(root)
  return out
}

function sqliteScript(lines: string[]): Promise<string> {
  return new Promise(resolve => {
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: 600_000, maxBuffer: 64 * 1024 * 1024, env: gufiEnv() },
      (_err, so) => resolve(so ?? ''))
    child.on('error', () => resolve(''))
    child.stdin?.write(lines.join('\n'))
    child.stdin?.end()
  })
}

/** Mean vectors of at most `cap` files, from folders taken in a random
 *  order, a batch of folders at a time so no one read is large. */
async function sampleFileVectors(root: string, cap: number): Promise<Map<string, number[]>> {
  const esc = (s: string) => `'${s.replace(/'/g, "''")}'`
  const dbs = listVectorDbs(root)
  for (let i = dbs.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [dbs[i], dbs[j]] = [dbs[j], dbs[i]] }
  const files = new Map<string, { sum: number[]; n: number }>()
  for (let s = 0; s < dbs.length && files.size < cap; s += 50) {
    const batch = dbs.slice(s, s + 50)
    const probe = await sqliteScript(batch.flatMap(db => [`ATTACH ${esc(db)} AS c;`,
      `SELECT 'HASVEC' || ${esc(SEP)} || ${esc(db)} FROM c.sqlite_master WHERE name = 'gvec';`, 'DETACH c;']))
    const withVec = probe.split('\n').filter(l => l.startsWith(`HASVEC${SEP}`)).map(l => l.split(SEP)[1]).filter(Boolean)
    if (!withVec.length) continue
    const out = await sqliteScript(withVec.flatMap(db => [`ATTACH ${esc(db)} AS d;`,
      `SELECT 'V' || ${esc(SEP)} || ${esc(path.relative(root, path.dirname(db)))} || ${esc(SEP)} || d.gchunks.fname || ${esc(SEP)} || vec_to_json(d.gvec.fp384) FROM d.gvec JOIN d.gchunks ON d.gvec.cid = d.gchunks.rowid;`,
      'DETACH d;']))
    for (const line of out.split('\n')) {
      if (!line.startsWith(`V${SEP}`)) continue
      const [, dir, fname, json] = line.split(SEP)
      let vec: number[]
      try { vec = JSON.parse(json) } catch { continue }
      const rel = dir ? `${dir}/${fname}` : fname
      const cur = files.get(rel)
      if (!cur) { if (files.size < cap) files.set(rel, { sum: vec.slice(), n: 1 }) } else { for (let i = 0; i < vec.length; i++) cur.sum[i] += vec[i]; cur.n++ }
    }
  }
  return new Map([...files].map(([rel, { sum, n }]) => [rel, sum.map(v => v / n)]))
}

/** The first `k` principal components by power iteration with deflation. */
function pca(vectors: number[][], k: number): number[][] {
  const n = vectors.length, d = vectors[0].length
  const mean = new Array(d).fill(0)
  for (const v of vectors) for (let i = 0; i < d; i++) mean[i] += v[i] / n
  const X = vectors.map(v => v.map((x, i) => x - mean[i]))
  const comps: number[][] = []
  for (let c = 0; c < k; c++) {
    let w = new Array(d).fill(0).map((_, i) => ((i * 7 + c * 13) % 11) / 11 - 0.5)
    for (let it = 0; it < 40; it++) {
      const scores = X.map(row => row.reduce((s, x, i) => s + x * w[i], 0))
      const next = new Array(d).fill(0)
      for (let r = 0; r < n; r++) for (let i = 0; i < d; i++) next[i] += scores[r] * X[r][i]
      for (const prev of comps) { const dot = next.reduce((s, x, i) => s + x * prev[i], 0); for (let i = 0; i < d; i++) next[i] -= dot * prev[i] }
      const norm = Math.sqrt(next.reduce((s, x) => s + x * x, 0)) || 1
      w = next.map(x => x / norm)
    }
    comps.push(w)
  }
  return X.map(row => comps.map(w => row.reduce((s, x, i) => s + x * w[i], 0)))
}

function kmeans(points: number[][], k: number): number[] {
  const cents = points.filter((_, i) => i % Math.max(1, Math.floor(points.length / k)) === 0).slice(0, k).map(p => p.slice())
  const assign = new Array(points.length).fill(0)
  for (let it = 0; it < 30; it++) {
    for (let i = 0; i < points.length; i++) {
      let best = 0, bd = Infinity
      for (let c = 0; c < cents.length; c++) {
        const d = points[i].reduce((s, x, j) => s + (x - cents[c][j]) ** 2, 0)
        if (d < bd) { bd = d; best = c }
      }
      assign[i] = best
    }
    for (let c = 0; c < cents.length; c++) {
      const members = points.filter((_, i) => assign[i] === c)
      if (members.length) cents[c] = cents[c].map((_, j) => members.reduce((s, p) => s + p[j], 0) / members.length)
    }
  }
  return assign
}

/** At most `cap` of the vectors fetched from a library on another system. */
async function fetchedFileVectors(file: string, cap: number): Promise<Map<string, number[]>> {
  const out = new Map<string, number[]>()
  for (const line of (await fsp.readFile(file, 'utf8')).split('\n')) {
    if (out.size >= cap) break
    try {
      const row = JSON.parse(line) as { p: string; r?: number; v: string }
      out.set(row.p, [...dequantize(row.v, row.r || 1)])
    } catch { /* a line cut short */ }
  }
  return out
}

async function buildWithoutNumpy(t: MapTarget): Promise<void> {
  const vecs = t.remote?.vectors ? await fetchedFileVectors(t.remote.vectors, FALLBACK_FILES) : await sampleFileVectors(t.index, FALLBACK_FILES)
  if (vecs.size < 3) return
  const rels = [...vecs.keys()]
  const proj = pca(rels.map(r => vecs.get(r)!), 3)
  const unit = (j: number) => {
    const col = proj.map(p => p[j]), lo = Math.min(...col), hi = Math.max(...col), span = hi - lo || 1
    return col.map(v => Math.round(((v - lo) / span) * 1e4) / 1e4)
  }
  const [xs, ys, zs] = [unit(0), unit(1), unit(2)]
  const k = Math.max(4, Math.min(14, Math.round(Math.sqrt(rels.length / 2))))
  const raw = kmeans(rels.map((_, i) => [xs[i], ys[i], zs[i]]), k)
  const sizes = new Map<number, number>()
  for (const c of raw) sizes.set(c, (sizes.get(c) ?? 0) + 1)
  const order = [...sizes.entries()].sort((a, b) => b[1] - a[1]).map(([c]) => c)
  const assign = raw.map(c => order.indexOf(c))
  const clusters: MapCluster[] = order.map((_, c) => {
    const m = rels.map((_, i) => i).filter(i => assign[i] === c)
    const tally = new Map<string, number>()
    for (const i of m) { const top = rels[i].includes('/') ? rels[i].split('/')[0] : '(root)'; tally.set(top, (tally.get(top) ?? 0) + 1) }
    const label = [...tally.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? ''
    const avg = (a: number[]) => Math.round((m.reduce((s, i) => s + a[i], 0) / m.length) * 1e4) / 1e4
    return { c, label, count: m.length, x: avg(xs), y: avg(ys), z: avg(zs) }
  })
  const points: MapPoint[] = rels.map((p, i) => ({ p, x: xs[i], y: ys[i], z: zs[i], c: assign[i] }))
  const tmp = `${t.out}.tmp-${process.pid}`
  await fsp.rm(tmp, { recursive: true, force: true })
  await fsp.mkdir(tmp, { recursive: true })
  for (const cl of clusters) {
    await fsp.writeFile(path.join(tmp, `cluster-${cl.c}.json`), JSON.stringify({ c: cl.c, label: cl.label, count: cl.count,
      points: points.filter(p => p.c === cl.c).map(({ p, x, y, z }) => ({ p, x, y, z })) }))
  }
  const overview: Omit<SemanticMapOverview, 'building'> = {
    available: true, builtAt: new Date().toISOString(), files: rels.length, shown: rels.length, clusters, points, fallback: true,
    ...(t.remote?.of ? { of: Math.max(t.remote.of, rels.length), model: t.remote.model } : {}),
  }
  await fsp.writeFile(path.join(tmp, 'overview.json'), JSON.stringify(overview))
  const old = `${t.out}.old`
  await fsp.rm(old, { recursive: true, force: true })
  if (fs.existsSync(t.out)) await fsp.rename(t.out, old)
  await fsp.mkdir(path.dirname(t.out), { recursive: true })
  await fsp.rename(tmp, t.out)
  await fsp.rm(old, { recursive: true, force: true })
}

/** The map earlier versions cached beside the index, made on request. */
fs.rm(path.join(INDEX_BASE, 'semantic-map.json'), { force: true }, () => {})

// ---- a file's nearest files by meaning ----

/** A map's own time: answers made for one map are kept until the next. */
const mapStamp = (out: string) => { try { return fs.statSync(path.join(out, 'overview.json')).mtimeMs } catch { return 0 } }

/** What one library's lookups keep: each index db with vectors and the
 *  model they come from (for five minutes, or until the map is made again),
 *  where each file of the map sits, and the answers given. */
interface NeighborCache {
  vectorDbs: { stamp: number; at: number; dbs: Map<string, string> } | null
  places: { stamp: number; places: Map<string, { x: number; y: number; z: number; c: number }> | null } | null
  answers: Map<string, Promise<{ file: string; neighbors: MapNeighbor[]; reason?: string }>>
}
const neighborCaches = new Map<string, NeighborCache>()
function neighborCacheOf(id: string): NeighborCache {
  let c = neighborCaches.get(id)
  if (!c) { c = { vectorDbs: null, places: null, answers: new Map() }; neighborCaches.set(id, c) }
  return c
}
function forgetNeighbors(id: string): void { neighborCaches.delete(id) }

async function vectorDbModels(id: string, root: string): Promise<Map<string, string>> {
  const cache = neighborCacheOf(id)
  const stamp = mapStamp(semanticMapDir(id))
  if (cache.vectorDbs && cache.vectorDbs.stamp === stamp && Date.now() - cache.vectorDbs.at < 5 * 60_000) return cache.vectorDbs.dbs
  const esc = (s: string) => `'${s.replace(/'/g, "''")}'`
  const all = listVectorDbs(root)
  const found = new Map<string, Set<string>>()
  for (let s = 0; s < all.length; s += 200) {
    const out = await sqliteScript(all.slice(s, s + 200).flatMap(db => [`ATTACH ${esc(db)} AS c;`,
      `SELECT 'HAS' || ${esc(SEP)} || ${esc(db)} || ${esc(SEP)} || name FROM c.sqlite_master WHERE name IN ('gvec', 'gvecmodel');`, 'DETACH c;']))
    for (const l of out.split('\n')) {
      const [tag, db, name] = l.split(SEP)
      if (tag === 'HAS' && db && name) found.set(db, (found.get(db) ?? new Set()).add(name.trim()))
    }
  }
  const dbs = new Map<string, string>()
  const withModel = [...found].filter(([, n]) => n.has('gvec') && n.has('gvecmodel')).map(([db]) => db)
  for (let s = 0; s < withModel.length; s += 200) {
    const out = await sqliteScript(withModel.slice(s, s + 200).flatMap(db => [`ATTACH ${esc(db)} AS c;`,
      `SELECT 'MODEL' || ${esc(SEP)} || ${esc(db)} || ${esc(SEP)} || model FROM c.gvecmodel LIMIT 1;`, 'DETACH c;']))
    for (const l of out.split('\n')) {
      const [tag, db, model] = l.split(SEP)
      if (tag === 'MODEL' && db && model) dbs.set(db, model.trim())
    }
  }
  // A directory embedded before models were recorded holds the first model's vectors.
  for (const [db, names] of found) if (names.has('gvec') && !dbs.has(db)) dbs.set(db, 'minilm384')
  cache.vectorDbs = { stamp, at: Date.now(), dbs }
  return dbs
}

/** Where each file of a library's map sits, from the clusters' files, made
 *  once per map; a map of more than PLACES_MAX files keeps none, and its
 *  neighbors come without a place unless they are on screen already. */
const PLACES_MAX = 300_000
function mapPlaces(key: string, out: string): Map<string, { x: number; y: number; z: number; c: number }> | null {
  const cache = neighborCacheOf(key)
  const stamp = mapStamp(out)
  if (cache.places?.stamp === stamp) return cache.places.places
  const overview = readJson<SemanticMapOverview>(path.join(out, 'overview.json'), stateOf(key).overviewCache)?.data
  let places: Map<string, { x: number; y: number; z: number; c: number }> | null = null
  if (overview?.available && (overview.files ?? 0) <= PLACES_MAX) {
    places = new Map()
    for (const cl of overview.clusters ?? []) {
      try {
        const data = JSON.parse(fs.readFileSync(path.join(out, `cluster-${cl.c}.json`), 'utf8')) as { points: MapPoint[] }
        for (const p of data.points) places.set(p.p, { x: p.x, y: p.y, z: p.z, c: cl.c })
      } catch { /* a cluster file missing: its files have no place */ }
    }
  }
  cache.places = { stamp, places }
  return places
}

/** Float32 little-endian bytes of a vector, as an SQL blob literal. */
function vectorBlob(v: number[]): string {
  const buf = Buffer.alloc(v.length * 4)
  v.forEach((x, i) => buf.writeFloatLE(x, i * 4))
  return `X'${buf.toString('hex')}'`
}

/**
 * The `k` files nearest one file of a library (the main folder by default)
 * by meaning: the mean of the file's own passage vectors is the query, and
 * every directory of that library whose vectors come from the same model
 * answers with its nearest passages, as a search by meaning does.
 * Similarity is the cosine of the vectors (they are unit length). For a
 * library on another system the query is the file's vector as fetched for
 * the map, and the search runs there, over its whole index, as one
 * gufi_query. An empty list, with a reason, where the folder or the file
 * has no vectors. Answers are kept until the map is made again.
 */
export function semanticNeighbors(file: string, k = 5, id: string = PRIMARY_ID, viewer?: string | null): Promise<{ file: string; neighbors: MapNeighbor[]; reason?: string }> {
  const rel = path.posix.normalize(String(file ?? '').replace(/\\/g, '/')).replace(/^\.\//, '')
  if (!rel || rel === '.' || rel.startsWith('/') || rel === '..' || rel.startsWith('../')) throw new KbError(400, 'file: a path in the library')
  const n = Math.min(20, Math.max(1, Math.floor(k) || 5))
  const { target, meaningOff } = libraryOf(id, viewer)
  if (meaningOff) return Promise.resolve({ file: rel, neighbors: [], reason: 'off' })
  const cache = neighborCacheOf(target.key)
  const key = `${mapStamp(target.out)}\0${rel}\0${n}`
  const hit = cache.answers.get(key)
  if (hit) return hit
  const value = target.remote ? findRemoteNeighbors(target, rel, n) : findNeighbors(id, target.index, rel, n)
  cache.answers.set(key, value)
  if (cache.answers.size > 300) cache.answers.delete(cache.answers.keys().next().value!)
  value.catch(() => cache.answers.delete(key))
  return value
}

async function findNeighbors(id: string, root: string, rel: string, k: number): Promise<{ file: string; neighbors: MapNeighbor[]; reason?: string }> {
  const esc = (s: string) => `'${s.replace(/'/g, "''")}'`
  const dir = path.posix.dirname(rel), name = path.posix.basename(rel)
  const own = path.join(root, dir === '.' ? '' : dir, 'db.db')
  const dbs = await vectorDbModels(id, root)
  const model = dbs.get(own)
  if (!model) return { file: rel, neighbors: [], reason: 'none' }
  const out = await sqliteScript([`ATTACH ${esc(own)} AS s;`,
    `SELECT 'Q' || ${esc(SEP)} || vec_to_json(v.fp384) FROM s.gvec v JOIN s.gchunks g ON g.cid = v.cid WHERE g.fname = ${esc(name)};`, 'DETACH s;'])
  const vecs: number[][] = []
  for (const l of out.split('\n')) {
    if (!l.startsWith(`Q${SEP}`)) continue
    try { const v = JSON.parse(l.slice(2)) as number[]; if (Array.isArray(v) && v.length) vecs.push(v) } catch { /* skipped */ }
  }
  if (!vecs.length) return { file: rel, neighbors: [], reason: 'none' }
  const mean = vecs[0].map((_, i) => vecs.reduce((s, v) => s + v[i], 0) / vecs.length)
  const norm = Math.hypot(...mean) || 1
  const query = mean.map(x => x / norm)
  const script = [`CREATE TABLE temp.q AS SELECT ${vectorBlob(query)} AS qe;`]
  for (const [db, m] of dbs) {
    if (m !== model) continue
    const relDir = path.relative(root, path.dirname(db))
    script.push(`ATTACH ${esc(db)} AS d;`,
      `SELECT 'N' || ${esc(SEP)} || ${esc(relDir)} || ${esc(SEP)} || g.fname || ${esc(SEP)} || n.distance FROM ` +
      `(SELECT cid, distance FROM d.gvec WHERE fp384 MATCH (SELECT qe FROM temp.q) ORDER BY distance ASC LIMIT ${k + 4}) n JOIN d.gchunks g ON g.cid = n.cid;`,
      'DETACH d;')
  }
  const best = new Map<string, number>()
  for (const l of (await sqliteScript(script)).split('\n')) {
    const [tag, d, fname, dist] = l.split(SEP)
    if (tag !== 'N' || !fname) continue
    const p = d ? `${d}/${fname}` : fname
    const v = Number(dist)
    if (p === rel || !Number.isFinite(v)) continue
    if (!best.has(p) || v < best.get(p)!) best.set(p, v)
  }
  const places = mapPlaces(id, semanticMapDir(id))
  const neighbors = [...best].sort((a, b) => a[1] - b[1]).slice(0, k).map(([p, d]): MapNeighbor => {
    const sim = Math.round(Math.max(-1, Math.min(1, 1 - (d * d) / 2)) * 1000) / 1000
    return { p, sim, ...(places?.get(p) ?? {}) }
  })
  return { file: rel, neighbors }
}

/** A fetched file's row, found by its path at the start of its line. */
async function fetchedRow(file: string, rel: string): Promise<{ v: string } | null> {
  let text: string
  try { text = await fsp.readFile(file, 'utf8') } catch { return null }
  const head = `{"p":${JSON.stringify(rel)},`
  const at = text.startsWith(head) ? 0 : text.indexOf(`\n${head}`) + 1
  if (at === 0 && !text.startsWith(head)) return null
  const end = text.indexOf('\n', at)
  try { return JSON.parse(text.slice(at, end < 0 ? undefined : end)) as { v: string } } catch { return null }
}

async function findRemoteNeighbors(t: MapTarget, rel: string, k: number): Promise<{ file: string; neighbors: MapNeighbor[]; reason?: string }> {
  const rec = readRecord(t.remote!.home)
  const row = rec.model ? await fetchedRow(path.join(t.remote!.home, VECTORS_FILE), rel) : null
  if (!rec.model || !row) return { file: rel, neighbors: [], reason: 'none' }
  const found = await remoteNeighbors(t.remote!.root, { model: rec.model, source: rec.source ?? 'studio' }, dequantize(row.v), k)
  const places = mapPlaces(t.key, t.out)
  const neighbors = found.filter(n => n.p !== rel).slice(0, k).map(({ p, d }): MapNeighbor => {
    const sim = Math.round(Math.max(-1, Math.min(1, 1 - (d * d) / 2)) * 1000) / 1000
    return { p, sim, ...(places?.get(p) ?? {}) }
  })
  return { file: rel, neighbors }
}

/** For tests: forget the kept answers. */
export function resetNeighborsForTests(): void { neighborCaches.clear() }

const viewerOf = (req: unknown) => (req as { user?: { id?: string } }).user?.id ?? null

export async function semanticMapRoutes(app: FastifyInstance): Promise<void> {
  // The main folder's map, at the address it has always had.
  app.get('/api/kb/semantic-map', async req => semanticMapOverview(PRIMARY_ID, viewerOf(req)))
  app.get('/api/kb/semantic-map/cluster/:c', async req => {
    const { c } = req.params as { c: string }
    const { offset, limit } = req.query as { offset?: string; limit?: string }
    return semanticMapCluster(Number(c), Number(offset ?? 0), Number(limit ?? 2000))
  })
  // The files nearest one file by meaning, for the lines the map draws from it.
  app.get('/api/kb/semantic-map/neighbors', async req => {
    const { file, k } = req.query as { file?: string; k?: string }
    return semanticNeighbors(file ?? '', Number(k ?? 5), PRIMARY_ID, viewerOf(req))
  })
  // Every library's map at a glance, for the picker and its default.
  app.get('/api/semantic-maps', async req => {
    const viewer = viewerOf(req)
    return { maps: semanticMapList(listLibraries(undefined, viewer), viewer) }
  })
  // Any library's map, by its id ("kb" is the main folder).
  app.get('/api/libraries/:id/semantic-map', async req => {
    const { id } = req.params as { id: string }
    return semanticMapOverview(id, viewerOf(req))
  })
  app.get('/api/libraries/:id/semantic-map/cluster/:c', async req => {
    const { id, c } = req.params as { id: string; c: string }
    const { offset, limit } = req.query as { offset?: string; limit?: string }
    return semanticMapCluster(Number(c), Number(offset ?? 0), Number(limit ?? 2000), id, viewerOf(req))
  })
  app.get('/api/libraries/:id/semantic-map/neighbors', async req => {
    const { id } = req.params as { id: string }
    const { file, k } = req.query as { file?: string; k?: string }
    return semanticNeighbors(file ?? '', Number(k ?? 5), id, viewerOf(req))
  })
  // Made again now: for a library on another system, its vectors fetched
  // again whether or not they changed there.
  app.post('/api/libraries/:id/semantic-map/refresh', async req => {
    const { id } = req.params as { id: string }
    return refreshSemanticMap(id, viewerOf(req))
  })
  app.post('/api/kb/semantic-map/refresh', async req => refreshSemanticMap(PRIMARY_ID, viewerOf(req)))
}

/** For tests: the map drawn here, as without numpy. */
export { buildWithoutNumpy as buildWithoutNumpyForTests }
