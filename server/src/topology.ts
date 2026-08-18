import path from 'node:path'
import fs from 'node:fs'
import { execFile } from 'node:child_process'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, KB_ROOT } from './config.js'
import { queryPerDir } from './gufi.js'
import { gufiAvailable } from './config.js'

/**
 * Corpus topology and the semantic map, both served from the index.
 *
 * Topology is one aggregate query over the per-directory summaries. The
 * semantic map mean-pools the per-chunk MiniLM vectors into one vector per
 * file, projects to 2D with PCA, and clusters with k-means; the result is
 * cached beside the index and rebuilt when any directory db changes.
 */

const SEP = ''
const MAP_CACHE = path.join(INDEX_BASE, 'semantic-map.json')

function kbRel(p: string): string {
  const idx = path.resolve(GUFI_INDEX)
  let rel = path.resolve(p).startsWith(idx) ? path.resolve(p).slice(idx.length) : p
  rel = rel.replace(/^\/+/, '')
  return rel
}

export async function topology(): Promise<{ available: boolean; dirs?: { dir: string; files: number; bytes: number }[] }> {
  if (!gufiAvailable()) return { available: false }
  const rows = await queryPerDir(`SELECT rpath(sname, sroll), totfiles, totsize FROM vrsummary;`)
  const dirs = rows
    .map(r => ({ dir: kbRel(r[0] ?? ''), files: Number(r[1]) || 0, bytes: Number(r[2]) || 0 }))
    .filter(d => d.dir !== '')
  return { available: true, dirs }
}

function listDbs(): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    let names: fs.Dirent[] = []
    try { names = fs.readdirSync(dir, { withFileTypes: true }) } catch { return }
    for (const e of names) {
      const p = path.join(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (e.name === 'db.db') out.push(p)
    }
  }
  walk(GUFI_INDEX)
  return out
}

function latestDbStamp(dbs: string[]): number {
  let m = 0
  for (const db of dbs) {
    try { m = Math.max(m, fs.statSync(db).mtimeMs) } catch { /* raced */ }
  }
  return m
}

/** All (file, meanVector) pairs, one gufi_sqlite3 process over every db that
 *  carries vectors; dbs without gvec are skipped by checking sqlite_master
 *  inline so one bare directory cannot abort the script. */
async function collectFileVectors(dbs: string[]): Promise<Map<string, number[]>> {
  const chunks = new Map<string, { sum: number[]; n: number }>()
  const esc = (s: string) => `'${s.replace(/'/g, "''")}'`
  const runScript = (lines: string[]) => new Promise<string>(resolve => {
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: 600_000, maxBuffer: 512 * 1024 * 1024 },
      (_err, so) => resolve(so ?? ''))
    child.stdin?.write(lines.join('\n'))
    child.stdin?.end()
  })
  // Two passes: a statement referencing a missing table fails at parse and
  // aborts the whole script, so first learn which dbs carry vectors
  // (sqlite_master is always valid), then dump only those.
  const probe = dbs.flatMap(db => [
    `ATTACH ${esc(db)} AS c;`,
    `SELECT 'HASVEC' || ${esc(SEP)} || ${esc(db)} FROM c.sqlite_master WHERE type IN ('table','view') AND name = 'gvec';`,
    'DETACH c;',
  ])
  const withVec = (await runScript(probe)).split('\n')
    .filter(l => l.startsWith(`HASVEC${SEP}`))
    .map(l => l.split(SEP)[1])
    .filter(Boolean)
  const script: string[] = []
  for (const db of withVec) {
    const dirRel = kbRel(path.dirname(db))
    script.push(
      `ATTACH ${esc(db)} AS d;`,
      `SELECT 'V' || ${esc(SEP)} || ${esc(dirRel)} || ${esc(SEP)} || d.gchunks.fname || ${esc(SEP)} || vec_to_json(d.gvec.fp384) ` +
      `FROM d.gvec JOIN d.gchunks ON d.gvec.cid = d.gchunks.rowid;`,
      'DETACH d;',
    )
  }
  const stdout = script.length ? await runScript(script) : ''
  for (const line of stdout.split('\n')) {
    if (!line.startsWith(`V${SEP}`)) continue
    const parts = line.split(SEP)
    if (parts.length < 4) continue
    const rel = parts[1] ? `${parts[1]}/${parts[2]}` : parts[2]
    let vec: number[]
    try { vec = JSON.parse(parts[3]) } catch { continue }
    if (!Array.isArray(vec) || !vec.length) continue
    const cur = chunks.get(rel)
    if (!cur) chunks.set(rel, { sum: vec.slice(), n: 1 })
    else { for (let i = 0; i < vec.length; i++) cur.sum[i] += vec[i]; cur.n++ }
  }
  const files = new Map<string, number[]>()
  for (const [rel, { sum, n }] of chunks) files.set(rel, sum.map(v => v / n))
  return files
}

/** First two principal components by power iteration with deflation. */
function pca2(vectors: number[][]): [number[], number[]] {
  const n = vectors.length
  const d = vectors[0].length
  const mean = new Array(d).fill(0)
  for (const v of vectors) for (let i = 0; i < d; i++) mean[i] += v[i] / n
  const X = vectors.map(v => v.map((x, i) => x - mean[i]))
  const project = (comp: number[]) => X.map(row => row.reduce((s, x, i) => s + x * comp[i], 0))
  const power = (deflate?: number[]) => {
    let c = new Array(d).fill(0).map(() => 1 / Math.sqrt(d))
    for (let it = 0; it < 40; it++) {
      const scores = X.map(row => row.reduce((s, x, i) => s + x * c[i], 0))
      const next = new Array(d).fill(0)
      for (let r = 0; r < n; r++) for (let i = 0; i < d; i++) next[i] += scores[r] * X[r][i]
      if (deflate) {
        const dot = next.reduce((s, x, i) => s + x * deflate[i], 0)
        for (let i = 0; i < d; i++) next[i] -= dot * deflate[i]
      }
      const norm = Math.sqrt(next.reduce((s, x) => s + x * x, 0)) || 1
      c = next.map(x => x / norm)
    }
    return c
  }
  const c1 = power()
  const c2 = power(c1)
  return [project(c1), project(c2)]
}

function kmeans(points: { x: number; y: number }[], k: number): number[] {
  const cents = points.slice(0, k).map(p => ({ ...p }))
  const assign = new Array(points.length).fill(0)
  for (let it = 0; it < 30; it++) {
    for (let i = 0; i < points.length; i++) {
      let best = 0, bd = Infinity
      for (let c = 0; c < k; c++) {
        const dx = points[i].x - cents[c].x, dy = points[i].y - cents[c].y
        const dist = dx * dx + dy * dy
        if (dist < bd) { bd = dist; best = c }
      }
      assign[i] = best
    }
    const sums = cents.map(() => ({ x: 0, y: 0, n: 0 }))
    for (let i = 0; i < points.length; i++) {
      const s = sums[assign[i]]
      s.x += points[i].x; s.y += points[i].y; s.n++
    }
    for (let c = 0; c < k; c++) if (sums[c].n) { cents[c].x = sums[c].x / sums[c].n; cents[c].y = sums[c].y / sums[c].n }
  }
  return assign
}

interface SemanticMap {
  available: boolean
  builtAt?: string
  sourceStamp?: number
  files?: { path: string; x: number; y: number; c: number }[]
  clusters?: number
  clusterLabels?: string[]
}

export async function semanticMap(): Promise<SemanticMap> {
  if (!gufiAvailable()) return { available: false }
  const dbs = listDbs()
  const stamp = latestDbStamp(dbs)
  try {
    const cached = JSON.parse(fs.readFileSync(MAP_CACHE, 'utf8')) as SemanticMap
    if (cached.sourceStamp === stamp && cached.files?.length && cached.clusterLabels) return cached
  } catch { /* no cache yet */ }
  const vecs = await collectFileVectors(dbs)
  if (vecs.size < 3) return { available: false }
  const rels = [...vecs.keys()]
  const [xs, ys] = pca2(rels.map(r => vecs.get(r)!))
  // Normalize to a unit box so the client renders without knowing scales.
  const norm = (arr: number[]) => {
    const lo = Math.min(...arr), hi = Math.max(...arr)
    const span = hi - lo || 1
    return arr.map(v => (v - lo) / span)
  }
  const nx = norm(xs), ny = norm(ys)
  const pts = rels.map((_, i) => ({ x: nx[i], y: ny[i] }))
  const k = Math.max(4, Math.min(14, Math.round(Math.sqrt(rels.length / 2))))
  const assign = kmeans(pts, k)
  // A cluster's label is the directory most of its members live under,
  // in the corpus's own vocabulary. When several clusters collide on the
  // same label (a corpus dominated by one tree, like a proposals archive),
  // the colliding ones descend a path level until they differ, so
  // "proposals" times nine becomes the actual subdirectories.
  const labelAt = (c: number, depth: number): string => {
    const tally = new Map<string, number>()
    rels.forEach((rel, i) => {
      if (assign[i] !== c) return
      const parts = rel.split('/')
      const seg = parts.length > depth ? parts.slice(0, depth + 1).join('/') : (parts.length > 1 ? parts.slice(0, -1).join('/') : '(root)')
      tally.set(seg, (tally.get(seg) ?? 0) + 1)
    })
    const sorted = [...tally.entries()].sort((a, b) => b[1] - a[1])
    const total = sorted.reduce((s, [, n]) => s + n, 0)
    if (!sorted.length) return ''
    return sorted[0][1] / total >= 0.6 || sorted.length === 1
      ? sorted[0][0]
      : `${sorted[0][0]} + ${sorted[1][0].split('/').pop()}`
  }
  const clusterLabels: string[] = []
  for (let c = 0; c < k; c++) clusterLabels.push(labelAt(c, 0))
  for (let depth = 1; depth <= 3; depth++) {
    const counts = new Map<string, number>()
    for (const l of clusterLabels) counts.set(l, (counts.get(l) ?? 0) + 1)
    let collided = false
    for (let c = 0; c < k; c++) {
      if ((counts.get(clusterLabels[c]) ?? 0) > 1) { clusterLabels[c] = labelAt(c, depth); collided = true }
    }
    if (!collided) break
  }
  const out: SemanticMap = {
    available: true,
    builtAt: new Date().toISOString(),
    sourceStamp: stamp,
    clusters: k,
    clusterLabels,
    files: rels.map((rel, i) => ({ path: rel, x: Math.round(nx[i] * 1000) / 1000, y: Math.round(ny[i] * 1000) / 1000, c: assign[i] })),
  }
  try { fs.writeFileSync(MAP_CACHE, JSON.stringify(out)) } catch { /* cache is best effort */ }
  return out
}

export async function topologyRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/kb/topology', async () => topology())
  app.get('/api/kb/semantic-map', async () => semanticMap())
}
