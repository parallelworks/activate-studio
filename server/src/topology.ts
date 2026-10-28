import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, gufiAvailable } from './config.js'
import { queryAggregate } from './gufi.js'
import { gufiSource } from './gufiTrace.js'
import { KbError } from './kb.js'
import { getLibrary, PRIMARY_ID, type Library } from './libraries.js'
import { semanticMapRoutes } from './semanticMap.js'
import { REMOTE_PREFIX, remoteIndexRoot } from './userLibraries.js'

/**
 * The corpus topology of a library: how its bytes and files spread over its
 * folders, from its GUFI index, for the treemap on the Stats page. One
 * aggregate gufi_query, run where the index is (here, or on the library's
 * own system over ssh, as gufi_query alone, which a host that runs nothing
 * else accepts): each directory's totals are added to its ancestor
 * TOPOLOGY_LEVELS below the folder asked for, so the answer is a few rows
 * per folder shown, however deep or large the tree, and the totals at every
 * level are whole. A folder at the last level is asked for again, with
 * `dir`, to see below it.
 */

export interface TopologyRow { dir: string; files: number; bytes: number }
export interface Topology {
  available: boolean
  reason?: 'none'
  /** Paths from the library's top, under `root`. */
  dirs?: TopologyRow[]
  /** The folder asked for ('' for the top). */
  root?: string
  /** How many levels below `root` the rows reach; a folder at that depth
   *  holds the totals of everything below it. */
  levels?: number
  ms?: number
}

export const TOPOLOGY_LEVELS = 6
/** Rows at most: past this a shallower answer is made from the same rows. */
export const TOPOLOGY_ROWS_MAX = 20_000
/** Answers are kept this long: a library on this computer, and one elsewhere. */
const TTL_LOCAL_MS = 2 * 60_000
const TTL_REMOTE_MS = 10 * 60_000

/** The per-directory step: the directory's path below the folder walked
 *  (`rootLen` characters long as gufi_query prints it), cut to its first
 *  `levels` parts, with its file count and bytes. A recursive CTE splits
 *  the path, since SQLite has no function that does. */
export function topologyStages(rootLen: number, levels: number): string[] {
  const head = `CASE WHEN instr(rest, '/') > 0 THEN substr(rest, 1, instr(rest, '/') - 1) ELSE rest END`
  const tail = `CASE WHEN instr(rest, '/') > 0 THEN substr(rest, instr(rest, '/') + 1) ELSE '' END`
  const E = 'INSERT INTO t WITH RECURSIVE ' +
    `d(p, f, b) AS (SELECT ltrim(substr(rpath(sname, sroll), ${rootLen + 1}), '/'), totfiles, totsize FROM vrsummary), ` +
    `s(i, rest, pre, f, b) AS (SELECT 0, p, '', f, b FROM d UNION ALL ` +
    `SELECT i + 1, ${tail}, pre || CASE WHEN pre = '' THEN '' ELSE '/' END || ${head}, f, b FROM s WHERE i < ${levels} AND rest <> '') ` +
    `SELECT pre, f, b FROM s WHERE i = ${levels} OR rest = '';`
  return [
    '-I', 'CREATE TABLE t (p TEXT, f INT64, b INT64);',
    '-K', 'CREATE TABLE agg (p TEXT, f INT64, b INT64);',
    '-E', E,
    '-J', 'INSERT INTO agg SELECT p, sum(f), sum(b) FROM t GROUP BY p;',
    '-G', 'SELECT p, sum(f), sum(b) FROM agg GROUP BY p;',
  ]
}

/** Rows cut to fewer levels, their totals added together. */
export function shallower(rows: TopologyRow[], levels: number): TopologyRow[] {
  const sum = new Map<string, TopologyRow>()
  for (const r of rows) {
    const dir = r.dir.split('/').filter(Boolean).slice(0, levels).join('/')
    const cur = sum.get(dir) ?? { dir, files: 0, bytes: 0 }
    cur.files += r.files
    cur.bytes += r.bytes
    sum.set(dir, cur)
  }
  return [...sum.values()]
}

function cleanDir(dir: unknown): string {
  const parts = String(dir ?? '').split('/').filter(p => p && p !== '.')
  if (parts.some(p => p === '..' || p.includes('\0'))) throw new KbError(400, 'dir: a folder of the library')
  return parts.join('/')
}

const cache = new Map<string, { at: number; value: Topology }>()

/** The topology of a library (the main folder by default) below `dir`. */
export async function topology(lib: Library | null = null, dir = '', opts: { refresh?: boolean } = {}): Promise<Topology> {
  const root = lib?.indexRoot ?? GUFI_INDEX
  const sub = cleanDir(dir)
  const remote = root.startsWith(REMOTE_PREFIX)
  // The main folder needs its index; another library on this computer, GUFI
  // and its own index; one elsewhere is asked there.
  if (!lib && !gufiAvailable()) return { available: false, reason: 'none' }
  if (lib && !remote && (!fs.existsSync(path.join(GUFI_BIN, 'gufi_query')) || !lib.caps.index)) return { available: false, reason: 'none' }
  const key = `${lib?.id ?? PRIMARY_ID}\0${root}\0${sub}`
  const hit = cache.get(key)
  if (hit && !opts.refresh && Date.now() - hit.at < (remote ? TTL_REMOTE_MS : TTL_LOCAL_MS)) return hit.value
  // The folder as gufi_query is given it, so its paths can be cut below it.
  const walked = remote ? (sub ? path.posix.join(remoteIndexRoot(root), sub) : remoteIndexRoot(root)) : (sub ? path.join(root, sub) : root)
  const t0 = Date.now()
  const rows = await gufiSource('stats', () => queryAggregate(topologyStages([...walked].length, TOPOLOGY_LEVELS), { indexRoot: root, subdir: sub || undefined }))
  let dirs: TopologyRow[] = rows
    .map(r => ({ dir: r[0] ?? '', files: Number(r[1]) || 0, bytes: Number(r[2]) || 0 }))
    .filter(r => r.files > 0 || r.bytes > 0 || r.dir !== '')
  let levels = TOPOLOGY_LEVELS
  while (dirs.length > TOPOLOGY_ROWS_MAX && levels > 1) { levels--; dirs = shallower(dirs, levels) }
  // Paths from the library's top, as the treemap places them.
  if (sub) dirs = dirs.map(r => ({ ...r, dir: r.dir ? `${sub}/${r.dir}` : sub }))
  const value: Topology = { available: true, dirs, root: sub, levels, ms: Date.now() - t0 }
  cache.set(key, { at: Date.now(), value })
  if (cache.size > 200) cache.delete(cache.keys().next().value!)
  return value
}

/** For tests: forget the kept answers. */
export function resetTopologyForTests(): void { cache.clear() }

const viewerOf = (req: unknown) => (req as { user?: { id?: string } }).user?.id ?? null

export async function topologyRoutes(app: FastifyInstance): Promise<void> {
  // The main folder's, at the address it has always had.
  app.get('/api/kb/topology', async req => {
    const q = req.query as { dir?: string; refresh?: string }
    return topology(null, q.dir ?? '', { refresh: q.refresh === '1' })
  })
  // Any library's ("kb" is the main folder), here or on another system.
  app.get('/api/libraries/:id/topology', async req => {
    const { id } = req.params as { id: string }
    const q = req.query as { dir?: string; refresh?: string }
    const lib = id === PRIMARY_ID ? null : getLibrary(id, undefined, viewerOf(req))
    return topology(lib, q.dir ?? '', { refresh: q.refresh === '1' })
  })
  await semanticMapRoutes(app)
}
