import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, PROJECT_ROOT, PYTHON_BIN, SEMANTIC_MAP_DIR, gufiEnv, indexerEnv } from './config.js'
import { indexSettings } from './indexSettings.js'
import { KbError } from './kb.js'

/**
 * The semantic map of the main folder, on the Stats page: the files that
 * have embeddings, placed by what they say, in clusters.
 *
 * It is built in the background, never while someone waits: by
 * indexer/semantic_map.py after a meaning phase that embedded something,
 * and once on first use when there is none yet. The script reads the
 * vectors from the index directly and writes, into SEMANTIC_MAP_DIR, an
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
export interface SemanticMapOverview {
  available: boolean
  /** Why there is no map: search by meaning is off for the main folder, the
   *  index has no embeddings yet, or the last build of the map failed. */
  reason?: 'off' | 'none' | 'failed'
  /** A new map is being made; the one returned stays until it is ready. */
  building: boolean
  error?: string | null
  builtAt?: string
  model?: string
  /** Files with embeddings, and how many of them the overview carries. */
  files?: number
  shown?: number
  clusters?: MapCluster[]
  links?: MapLink[]
  points?: MapPoint[]
  /** Drawn by the server from a sample, without numpy. */
  fallback?: boolean
}

const SCRIPT = path.join(PROJECT_ROOT, 'indexer', 'semantic_map.py')
const OVERVIEW = () => path.join(SEMANTIC_MAP_DIR, 'overview.json')
/** The map the server draws itself when numpy is missing: this many files at most. */
const FALLBACK_FILES = 4000
/** A cluster's points per request, at most. */
export const CLUSTER_PAGE_MAX = 5000

const state = {
  running: false,
  /** Another rebuild was asked for while one ran. */
  again: false,
  timer: null as NodeJS.Timeout | null,
  dueAt: 0,
  /** A first build was started for a missing map, in this process. */
  firstTried: false,
  lastEnd: 0,
  error: null as string | null,
}

let overviewCache: { mtimeMs: number; data: SemanticMapOverview } | null = null
const clusterCache = new Map<number, { mtimeMs: number; data: { c: number; label: string; count: number; points: MapPoint[] } }>()

function readJson<T>(file: string, cache: { mtimeMs: number; data: T } | null | undefined): { mtimeMs: number; data: T } | null {
  let st: fs.Stats
  try { st = fs.statSync(file) } catch { return null }
  if (cache && cache.mtimeMs === st.mtimeMs) return cache
  try { return { mtimeMs: st.mtimeMs, data: JSON.parse(fs.readFileSync(file, 'utf8')) as T } } catch { return null }
}

/** The overview, as the Stats page draws it first. */
export function semanticMapOverview(): SemanticMapOverview {
  if (indexSettings('kb').meaning === 'off') return { available: false, reason: 'off', building: false }
  const hit = readJson<SemanticMapOverview>(OVERVIEW(), overviewCache)
  if (hit?.data.available) {
    overviewCache = hit
    return { ...hit.data, building: state.running }
  }
  // None yet, say after an update from a version that made it on request:
  // made once, in the background, when there is an index to make it from.
  if (!state.firstTried && !state.running && fs.existsSync(path.join(GUFI_INDEX, 'db.db'))) {
    state.firstTried = true
    scheduleSemanticMapRebuild(0)
  }
  return { available: false, reason: state.error ? 'failed' : 'none', building: state.running || !!state.timer, error: state.error }
}

/** One cluster's files, a page at a time, in the random order the map
 *  stored them, so any first page is a fair sample of the cluster. */
export function semanticMapCluster(c: number, offset = 0, limit = 2000): { c: number; label: string; count: number; offset: number; points: MapPoint[] } {
  if (!Number.isInteger(c) || c < 0) throw new KbError(400, 'cluster: a number from 0')
  const hit = readJson(path.join(SEMANTIC_MAP_DIR, `cluster-${c}.json`), clusterCache.get(c))
  if (!hit) throw new KbError(404, 'There is no such cluster in the map.')
  clusterCache.delete(c)
  clusterCache.set(c, hit)
  // A few clusters are kept; the oldest goes.
  while (clusterCache.size > 4) clusterCache.delete(clusterCache.keys().next().value!)
  const start = Math.max(0, Math.floor(offset) || 0)
  const n = Math.min(CLUSTER_PAGE_MAX, Math.max(1, Math.floor(limit) || 2000))
  return { c: hit.data.c, label: hit.data.label, count: hit.data.count, offset: start, points: hit.data.points.slice(start, start + n) }
}

/**
 * Asks for a new map in `delayMs`. Calls close together make one build, the
 * earliest time asked for wins, and a call while a build runs makes one
 * more after it, so the map catches up with a sync that ran meanwhile.
 */
export function scheduleSemanticMapRebuild(delayMs: number): void {
  if (state.running) { state.again = true; return }
  const due = Date.now() + Math.max(0, delayMs)
  if (state.timer && state.dueAt <= due) return
  if (state.timer) clearTimeout(state.timer)
  state.dueAt = due
  state.timer = setTimeout(() => { state.timer = null; void rebuild() }, Math.max(0, delayMs))
  state.timer.unref?.()
}

/** Tests replace the build, and see how often it runs. */
let builder: () => Promise<void> = buildWithPython
export function setSemanticMapBuilderForTests(fn: (() => Promise<void>) | null): void { builder = fn ?? buildWithPython }
export function resetSemanticMapForTests(): void {
  if (state.timer) clearTimeout(state.timer)
  Object.assign(state, { running: false, again: false, timer: null, dueAt: 0, firstTried: false, lastEnd: 0, error: null })
  overviewCache = null
  clusterCache.clear()
}

async function rebuild(): Promise<void> {
  if (state.running) { state.again = true; return }
  if (indexSettings('kb').meaning === 'off') return
  state.running = true
  try {
    await builder()
    state.error = null
  } catch (e) {
    state.error = String((e as Error).message ?? e).slice(0, 400)
    console.warn(`semantic map: ${state.error}`)
  } finally {
    state.running = false
    state.lastEnd = Date.now()
    if (state.again) { state.again = false; scheduleSemanticMapRebuild(60_000) }
  }
}

function buildWithPython(): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(PYTHON_BIN, [SCRIPT, '--index', GUFI_INDEX, '--out', SEMANTIC_MAP_DIR],
      { timeout: 30 * 60_000, maxBuffer: 4 * 1024 * 1024, env: indexerEnv({ PYTHONDONTWRITEBYTECODE: '1' }) },
      (err, stdout, stderr) => {
        if (!err) {
          try { console.log(`semantic map: ${JSON.stringify(JSON.parse(stdout.trim().split('\n').pop() ?? '{}'))}`) } catch { /* the summary is for the log */ }
          return resolve()
        }
        if ((err as { code?: unknown }).code === 3) return buildWithoutNumpy().then(resolve, reject)
        reject(new Error(String(stderr || err.message).trim().split('\n').slice(-3).join(' ')))
      })
  })
}

// ---- without numpy: a smaller map, drawn here ----

const SEP = '\x1f'

function listVectorDbs(): string[] {
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
  walk(GUFI_INDEX)
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
async function sampleFileVectors(cap: number): Promise<Map<string, number[]>> {
  const esc = (s: string) => `'${s.replace(/'/g, "''")}'`
  const dbs = listVectorDbs()
  for (let i = dbs.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [dbs[i], dbs[j]] = [dbs[j], dbs[i]] }
  const files = new Map<string, { sum: number[]; n: number }>()
  for (let s = 0; s < dbs.length && files.size < cap; s += 50) {
    const batch = dbs.slice(s, s + 50)
    const probe = await sqliteScript(batch.flatMap(db => [`ATTACH ${esc(db)} AS c;`,
      `SELECT 'HASVEC' || ${esc(SEP)} || ${esc(db)} FROM c.sqlite_master WHERE name = 'gvec';`, 'DETACH c;']))
    const withVec = probe.split('\n').filter(l => l.startsWith(`HASVEC${SEP}`)).map(l => l.split(SEP)[1]).filter(Boolean)
    if (!withVec.length) continue
    const out = await sqliteScript(withVec.flatMap(db => [`ATTACH ${esc(db)} AS d;`,
      `SELECT 'V' || ${esc(SEP)} || ${esc(path.relative(GUFI_INDEX, path.dirname(db)))} || ${esc(SEP)} || d.gchunks.fname || ${esc(SEP)} || vec_to_json(d.gvec.fp384) FROM d.gvec JOIN d.gchunks ON d.gvec.cid = d.gchunks.rowid;`,
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

async function buildWithoutNumpy(): Promise<void> {
  const vecs = await sampleFileVectors(FALLBACK_FILES)
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
  const tmp = `${SEMANTIC_MAP_DIR}.tmp-${process.pid}`
  await fsp.rm(tmp, { recursive: true, force: true })
  await fsp.mkdir(tmp, { recursive: true })
  for (const cl of clusters) {
    await fsp.writeFile(path.join(tmp, `cluster-${cl.c}.json`), JSON.stringify({ c: cl.c, label: cl.label, count: cl.count,
      points: points.filter(p => p.c === cl.c).map(({ p, x, y, z }) => ({ p, x, y, z })) }))
  }
  const overview: Omit<SemanticMapOverview, 'building'> = {
    available: true, builtAt: new Date().toISOString(), files: rels.length, shown: rels.length, clusters, points, fallback: true,
  }
  await fsp.writeFile(path.join(tmp, 'overview.json'), JSON.stringify(overview))
  const old = `${SEMANTIC_MAP_DIR}.old`
  await fsp.rm(old, { recursive: true, force: true })
  if (fs.existsSync(SEMANTIC_MAP_DIR)) await fsp.rename(SEMANTIC_MAP_DIR, old)
  await fsp.rename(tmp, SEMANTIC_MAP_DIR)
  await fsp.rm(old, { recursive: true, force: true })
}

/** The map earlier versions cached beside the index, made on request. */
fs.rm(path.join(INDEX_BASE, 'semantic-map.json'), { force: true }, () => {})

// ---- a file's nearest files by meaning ----

/** The map's own time: answers made for one map are kept until the next. */
const mapStamp = () => { try { return fs.statSync(OVERVIEW()).mtimeMs } catch { return 0 } }

/** Each index db with vectors, and the model they come from, kept for five
 *  minutes or until the map is made again. */
let vectorDbCache: { stamp: number; at: number; dbs: Map<string, string> } | null = null
async function vectorDbModels(): Promise<Map<string, string>> {
  const stamp = mapStamp()
  if (vectorDbCache && vectorDbCache.stamp === stamp && Date.now() - vectorDbCache.at < 5 * 60_000) return vectorDbCache.dbs
  const esc = (s: string) => `'${s.replace(/'/g, "''")}'`
  const all = listVectorDbs()
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
  vectorDbCache = { stamp, at: Date.now(), dbs }
  return dbs
}

/** Where each file of the map sits, from the clusters' files, made once per
 *  map; a map of more than PLACES_MAX files keeps none, and its neighbors
 *  come without a place unless they are on screen already. */
const PLACES_MAX = 300_000
let placeCache: { stamp: number; places: Map<string, { x: number; y: number; z: number; c: number }> | null } | null = null
function mapPlaces(): Map<string, { x: number; y: number; z: number; c: number }> | null {
  const stamp = mapStamp()
  if (placeCache?.stamp === stamp) return placeCache.places
  const overview = readJson<SemanticMapOverview>(OVERVIEW(), overviewCache)?.data
  let places: Map<string, { x: number; y: number; z: number; c: number }> | null = null
  if (overview?.available && (overview.files ?? 0) <= PLACES_MAX) {
    places = new Map()
    for (const cl of overview.clusters ?? []) {
      try {
        const data = JSON.parse(fs.readFileSync(path.join(SEMANTIC_MAP_DIR, `cluster-${cl.c}.json`), 'utf8')) as { points: MapPoint[] }
        for (const p of data.points) places.set(p.p, { x: p.x, y: p.y, z: p.z, c: cl.c })
      } catch { /* a cluster file missing: its files have no place */ }
    }
  }
  placeCache = { stamp, places }
  return places
}

const neighborCache = new Map<string, Promise<{ file: string; neighbors: MapNeighbor[]; reason?: string }>>()

/** Float32 little-endian bytes of a vector, as an SQL blob literal. */
function vectorBlob(v: number[]): string {
  const buf = Buffer.alloc(v.length * 4)
  v.forEach((x, i) => buf.writeFloatLE(x, i * 4))
  return `X'${buf.toString('hex')}'`
}

/**
 * The `k` files nearest one file of the main folder by meaning: the mean of
 * the file's own passage vectors is the query, and every directory whose
 * vectors come from the same model answers with its nearest passages, as a
 * search by meaning does. Similarity is the cosine of the vectors (they are
 * unit length). An empty list, with a reason, where the folder or the file
 * has no vectors. Answers are kept until the map is made again.
 */
export function semanticNeighbors(file: string, k = 5): Promise<{ file: string; neighbors: MapNeighbor[]; reason?: string }> {
  const rel = path.posix.normalize(String(file ?? '').replace(/\\/g, '/')).replace(/^\.\//, '')
  if (!rel || rel === '.' || rel.startsWith('/') || rel === '..' || rel.startsWith('../')) throw new KbError(400, 'file: a path in the main folder')
  const n = Math.min(20, Math.max(1, Math.floor(k) || 5))
  if (indexSettings('kb').meaning === 'off') return Promise.resolve({ file: rel, neighbors: [], reason: 'off' })
  const key = `${mapStamp()}\0${rel}\0${n}`
  const hit = neighborCache.get(key)
  if (hit) return hit
  const value = findNeighbors(rel, n)
  neighborCache.set(key, value)
  if (neighborCache.size > 300) neighborCache.delete(neighborCache.keys().next().value!)
  value.catch(() => neighborCache.delete(key))
  return value
}

async function findNeighbors(rel: string, k: number): Promise<{ file: string; neighbors: MapNeighbor[]; reason?: string }> {
  const esc = (s: string) => `'${s.replace(/'/g, "''")}'`
  const dir = path.posix.dirname(rel), name = path.posix.basename(rel)
  const own = path.join(GUFI_INDEX, dir === '.' ? '' : dir, 'db.db')
  const dbs = await vectorDbModels()
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
    const relDir = path.relative(GUFI_INDEX, path.dirname(db))
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
  const places = mapPlaces()
  const neighbors = [...best].sort((a, b) => a[1] - b[1]).slice(0, k).map(([p, d]): MapNeighbor => {
    const sim = Math.round(Math.max(-1, Math.min(1, 1 - (d * d) / 2)) * 1000) / 1000
    return { p, sim, ...(places?.get(p) ?? {}) }
  })
  return { file: rel, neighbors }
}

/** For tests: forget the kept answers. */
export function resetNeighborsForTests(): void { neighborCache.clear(); vectorDbCache = null; placeCache = null }

export async function semanticMapRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/kb/semantic-map', async () => semanticMapOverview())
  app.get('/api/kb/semantic-map/cluster/:c', async req => {
    const { c } = req.params as { c: string }
    const { offset, limit } = req.query as { offset?: string; limit?: string }
    return semanticMapCluster(Number(c), Number(offset ?? 0), Number(limit ?? 2000))
  })
  // The files nearest one file by meaning, for the lines the map draws from it.
  app.get('/api/kb/semantic-map/neighbors', async req => {
    const { file, k } = req.query as { file?: string; k?: string }
    return semanticNeighbors(file ?? '', Number(k ?? 5))
  })
}
