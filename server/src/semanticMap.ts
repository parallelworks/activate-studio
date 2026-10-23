import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, PROJECT_ROOT, PYTHON_BIN, SEMANTIC_MAP_DIR, gufiAvailable, gufiEnv, indexerEnv } from './config.js'
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

export interface MapCluster { c: number; label: string; count: number; x: number; y: number; z: number }
export interface MapPoint { p: string; x: number; y: number; z: number; c?: number }
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
  // made once, in the background.
  if (!state.firstTried && !state.running && gufiAvailable()) {
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

export async function semanticMapRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/kb/semantic-map', async () => semanticMapOverview())
  app.get('/api/kb/semantic-map/cluster/:c', async req => {
    const { c } = req.params as { c: string }
    const { offset, limit } = req.query as { offset?: string; limit?: string }
    return semanticMapCluster(Number(c), Number(offset ?? 0), Number(limit ?? 2000))
  })
}
