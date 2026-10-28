import { EXTERNAL_DB_PATTERN } from './gufi.js'
import { remoteGufi } from './gufiTrace.js'
import { KbError } from './kb.js'
import { remoteThreads, siteName } from './remoteExec.js'
import { gufiTool, remoteTarget } from './userLibraries.js'

/**
 * The vectors of a library on another system, brought here for its semantic
 * map: one aggregate gufi_query over the library's own connection, so a
 * host whose login runs nothing but gufi_query answers it too. Each file
 * comes back as one row: its path, the mean of its passage vectors (folded
 * in SQL with sqlite-vec's vec_add, normalized and quantized to int8, as
 * hex: 768 characters), that mean's length (so the mean itself, which the
 * map of a library here is made from, can be put back together), and, for
 * a part of them, the start of its text, for naming the clusters.
 *
 * Two walks: the first counts the files with vectors per model (a few rows
 * back, and the fingerprint a later check compares), the second fetches a
 * sample of the model most files use. The sample is the files whose
 * inode hashes lowest, so the same files come back every time and a file
 * stays in or out of the map from one refresh to the next.
 *
 * Vectors in the shared external schema (ext_vectors_by_file, for
 * all-MiniLM-L6-v2) are read the same way, with --external-copy, matched to
 * the directory's entries by inode and mtime as a search does.
 */

const DELIM = '\x1e'

/** Files a map of a library on another system is drawn from, at most:
 *  STUDIO_REMOTE_MAP_FILES (default 50,000). */
export function remoteMapFiles(): number {
  const n = Number(process.env.STUDIO_REMOTE_MAP_FILES)
  return Number.isInteger(n) && n >= 3 ? Math.min(n, 1_000_000) : 50_000
}

/** Files whose text comes along for naming the clusters, at most, and how
 *  much of it: the first passage. */
export const REMOTE_TEXT_FILES = 10_000
export const REMOTE_TEXT_CHARS = 1000

/** The hash a file is sampled by, in [0, HASH_RANGE): its inode, scaled
 *  through a multiplicative hash so neighbouring inodes spread out. */
export const HASH_RANGE = 2_147_483_648
const hashSql = (inode: string) => `((CAST(${inode} AS INTEGER) % 2147483647) * 1103515245 + 12345) % ${HASH_RANGE}`
/** The same hash here, for tests. */
export function fileHash(inode: number): number {
  return Number(((BigInt(inode) % 2147483647n) * 1103515245n + 12345n) % BigInt(HASH_RANGE))
}

/** Models of the shared external schema whose vectors are read. */
const EXT_MODELS = '%all-MiniLM-L6-v2'
/** The directory a step runs in, as gufi_query prints its paths; in a
 *  rolled-up directory, the row of the directory itself. */
const DIR = '(SELECT rpath(sname, sroll) FROM vrsummary WHERE isroot = 1)'
/** Text cleared of what would break a row: line ends and the delimiter. */
const clean = (sql: string) => `replace(replace(replace(${sql}, char(10), ' '), char(13), ' '), char(30), ' ')`
const lit = (s: string) => `'${s.replace(/'/g, "''")}'`
const ENTRY = 'JOIN vrpentries e ON CAST(x.vinode AS INT64) = e.inode AND CAST(x.vmtime AS INT64) = e.mtime'

/** Where a model's vectors are: the Studio's own tables, or the shared external schema. */
export type VectorSource = 'studio' | 'external'
export interface ModelCount { model: string; source: VectorSource; files: number; chunks: number; digest: number }

/** The counting walk: per model, its files and passages, and a digest of
 *  the passages (their ids and hashes) that changes when they do. */
export function countStages(external: string | null): string[] {
  const cols = 's TEXT, m TEXT, f INT64, c INT64, d REAL'
  const stages = [
    '-I', `CREATE TABLE t (${cols});`,
    '-K', `CREATE TABLE agg (${cols});`,
  ]
  if (external) {
    stages.push('--external-copy', external,
      `INSERT INTO t SELECT 'e', x.vector_model_name, count(DISTINCT e.inode), count(*), total(length(x.chunk_content)) FROM ext_vectors_by_file x ${ENTRY} ` +
      `WHERE x.vector_model_name LIKE ${lit(EXT_MODELS)} AND length(x.embedding) >= 1536 GROUP BY x.vector_model_name;`)
  }
  stages.push(
    // gvec_rowids, sqlite-vec's own table of the passages it holds, needs
    // no extension to read.
    '-E', `INSERT INTO t SELECT 's', (SELECT model FROM gvecmodel LIMIT 1), count(DISTINCT g.fname), count(*), total(g.cid * (unicode(g.h) % 89 + 1)) ` +
      'FROM gchunks g JOIN gvec_rowids r ON r.rowid = g.cid;',
    '-J', 'INSERT INTO agg SELECT * FROM t;',
    '-G', 'SELECT s, m, sum(f), sum(c), sum(d) FROM agg WHERE m IS NOT NULL GROUP BY s, m HAVING sum(f) > 0 ORDER BY 3 DESC, 2;',
  )
  return stages
}

/** The fetching walk: the files of one model whose hash is below `below`,
 *  each with its mean vector and its length, and the start of its text where
 *  its hash is below `textBelow`. */
export function fetchStages(m: Pick<ModelCount, 'model' | 'source'>, below: number, textBelow: number, external: string | null): string[] {
  const cols = 'h INT64, p TEXT, r REAL, v TEXT, x TEXT'
  const text = (h: string, x: string) => `CASE WHEN ${h} < ${textBelow} THEN ${clean(`substr(${x}, 1, ${REMOTE_TEXT_CHARS})`)} ELSE '' END`
  // The fold: each file's passages in order, added up one at a time (SQLite
  // has no aggregate over vectors), and the sum at its last passage kept.
  // Each row carries its file's passage count, so the last is found without
  // a join, which SQLite would answer by running the fold again per file.
  const fold = (key: string) =>
    `acc(${key}, rn, c, s, h, p, x) AS (SELECT ${key}, rn, c, e, h, p, x FROM ch WHERE rn = 1 UNION ALL ` +
    `SELECT ch.${key}, ch.rn, ch.c, vec_add(acc.s, ch.e), acc.h, acc.p, acc.x FROM acc JOIN ch ON ch.${key} = acc.${key} AND ch.rn = acc.rn + 1) ` +
    `SELECT h, p, round(vec_distance_l2(s, vec_sub(s, s)) / c, 5), hex(vec_quantize_int8(vec_normalize(s), 'unit')), ${text('h', 'x')} ` +
    'FROM acc WHERE rn = c;'
  const stages = [
    '-I', `CREATE TABLE t (${cols});`,
    '-K', `CREATE TABLE agg (${cols});`,
  ]
  if (m.source === 'external') {
    if (!external) throw new KbError(409, 'external databases are not read here (STUDIO_EXTERNAL_DB_PATTERN is empty)')
    stages.push('--external-copy', external,
      'INSERT INTO t WITH RECURSIVE ch AS (SELECT e.inode AS i, ' +
      `row_number() OVER (PARTITION BY e.inode ORDER BY x.page_number) AS rn, count(*) OVER (PARTITION BY e.inode) AS c, substr(x.embedding, 1, 1536) AS e, ${hashSql('e.inode')} AS h, ` +
      `rpath(e.sname, e.sroll)||'/'||e.name AS p, x.chunk_content AS x FROM ext_vectors_by_file x ${ENTRY} ` +
      `WHERE x.vector_model_name = ${lit(m.model)} AND length(x.embedding) >= 1536 AND ${hashSql('e.inode')} < ${below}), ${fold('i')}`,
      '-E', 'SELECT 1 WHERE 0;')
  } else {
    stages.push('-E',
      'INSERT INTO t WITH RECURSIVE ch AS (SELECT g.fname AS f, ' +
      `row_number() OVER (PARTITION BY g.fname ORDER BY g.seq, g.cid) AS rn, count(*) OVER (PARTITION BY g.fname) AS c, v.fp384 AS e, ${hashSql('g.tinode')} AS h, ` +
      `${DIR}||'/'||g.fname AS p, g.ctext AS x FROM gchunks g JOIN gvec v ON v.cid = g.cid ` +
      `WHERE ${hashSql('g.tinode')} < ${below} AND (SELECT model FROM gvecmodel LIMIT 1) = ${lit(m.model)}), ${fold('f')}`)
  }
  stages.push('-J', 'INSERT INTO agg SELECT * FROM t;', '-G', 'SELECT h, p, r, v, x FROM agg ORDER BY h, p;')
  return stages
}

/** The nearest-files walk: each directory's nearest passages to `query`
 *  (vec0's own search, or cosine distance for the external schema, put on
 *  vec0's scale for unit vectors), trimmed per thread as it goes, and each
 *  file's best at the end. */
export function neighborStages(m: Pick<ModelCount, 'model' | 'source'>, queryHex: string, k: number, external: string | null): string[] {
  const keep = 4 * (k + 4)
  const trim = `DELETE FROM t WHERE rowid IN (SELECT rowid FROM t ORDER BY d LIMIT -1 OFFSET ${keep});`
  const stages = [
    '-I', `CREATE TABLE q (qe BLOB); INSERT INTO q VALUES (X'${queryHex}'); CREATE TABLE t (p TEXT, d REAL);`,
    '-K', 'CREATE TABLE agg (p TEXT, d REAL);',
  ]
  if (m.source === 'external') {
    if (!external) throw new KbError(409, 'external databases are not read here (STUDIO_EXTERNAL_DB_PATTERN is empty)')
    stages.push('--external-copy', external,
      `INSERT INTO t SELECT rpath(e.sname, e.sroll)||'/'||e.name, sqrt(2.0 * vec_distance_cosine(substr(x.embedding, 1, 1536), (SELECT qe FROM q))) ` +
      `FROM ext_vectors_by_file x ${ENTRY} WHERE x.vector_model_name = ${lit(m.model)} AND length(x.embedding) >= 1536 ORDER BY 2 LIMIT ${k + 4};`,
      '-E', trim)
  } else {
    stages.push('-E', `${trim} INSERT INTO t SELECT ${DIR}||'/'||g.fname, n.distance FROM ` +
      `(SELECT cid, distance FROM gvec WHERE fp384 MATCH (SELECT qe FROM q) ORDER BY distance LIMIT ${k + 4}) n JOIN gchunks g ON g.cid = n.cid ` +
      `WHERE (SELECT model FROM gvecmodel LIMIT 1) = ${lit(m.model)};`)
  }
  stages.push('-J', 'INSERT INTO agg SELECT * FROM t;', '-G', `SELECT p, min(d) FROM agg GROUP BY p ORDER BY 2, 1 LIMIT ${k + 1};`)
  return stages
}

const UNKNOWN_OPTION = /unrecognized option|invalid option|unknown option/i
/** The GUFI there was built without sqlite-vec, which the fold and the
 *  nearest-neighbour search need. */
const NO_VEC = /no such (?:module: vec0|function: vec_)/

interface Ran { code: number; stdout: string; stderr: string; ms: number; bytes: number; site: string }

/** gufi_query over a remote library's index, as its searches run it, with
 *  --no-print-sql-on-err (so a directory without the tables a step reads
 *  costs one short line of error output); a GUFI from before it, or before
 *  --external-copy, is asked again without them. */
async function runRemote(root: string, stages: string[], timeoutMs: number): Promise<Ran> {
  const t = remoteTarget(root)
  if (!t) throw new KbError(400, 'not a library on another system')
  const threads = await remoteThreads(t.site, t.key)
  const run = async (s: string[], quiet: boolean): Promise<Ran> => {
    const cmd = [gufiTool(t.gufiBin), '-n', String(threads), '-d', DELIM, '-a', '1', ...(quiet ? ['--no-print-sql-on-err'] : []), ...s, t.indexRoot]
    const t0 = Date.now()
    const r = await remoteGufi(t.site, t.key, cmd, { timeoutMs, source: 'map' })
    return { code: r.code, stdout: r.stdout, stderr: r.stderr, ms: Date.now() - t0, bytes: r.raw.length, site: siteName(t.site) }
  }
  const r = await run(stages, true)
  if (r.code === 0 || !UNKNOWN_OPTION.test(r.stderr)) return r
  const i = stages.indexOf('--external-copy')
  return run(i >= 0 ? [...stages.slice(0, i), ...stages.slice(i + 3)] : stages, false)
}

function rows(stdout: string, fields: number): string[][] {
  return stdout.split('\n').filter(Boolean).map(l => l.split(DELIM)).filter(r => r.length >= fields)
}

/** A walk that answered nothing: an error when its GUFI cannot read vectors
 *  or the command failed outright, else null (there was nothing to find). */
function noRows(r: Ran, what: string): KbError | null {
  if (NO_VEC.test(r.stderr)) return new KbError(502, `gufi_query on ${r.site} cannot read vectors: its GUFI was built without sqlite-vec`)
  if (r.code !== 0 && UNKNOWN_OPTION.test(r.stderr)) return new KbError(502, `gufi_query on ${r.site} failed ${what}: ${r.stderr.trim().split('\n').pop()}`)
  return null
}

/** The files with vectors in a remote library's index, per model, most files first. */
export async function countRemoteVectors(root: string, timeoutMs = 10 * 60_000): Promise<{ models: ModelCount[]; ms: number }> {
  const r = await runRemote(root, countStages(EXTERNAL_DB_PATTERN || null), timeoutMs)
  const models = rows(r.stdout, 5).map(([s, model, f, c, d]): ModelCount => ({
    model, source: s === 'e' ? 'external' : 'studio', files: Number(f) || 0, chunks: Number(c) || 0, digest: Math.round(Number(d) || 0),
  })).filter(m => m.model && m.files > 0)
  if (!models.length) { const e = noRows(r, 'counting the vectors'); if (e) throw e }
  return { models, ms: r.ms }
}

export interface FetchedFile { h: number; p: string; r: number; v: string; t: string }
export interface Fetched { files: FetchedFile[]; bytes: number; ms: number }

/** Each file's distance from the root as gufi_query printed it. */
function relative(printed: string, root: string): string | null {
  const base = root.replace(/\/+$/, '')
  return printed.startsWith(`${base}/`) ? printed.slice(base.length + 1) : null
}

/**
 * A sample of at most `cap` of the `m.files` files of one model: every file
 * where there are no more, else the files whose hash is lowest. The walk
 * asks for a little more than the cap (the hash spreads files evenly, not
 * exactly), and the files past it are dropped here, lowest hash first.
 */
export async function fetchRemoteVectors(root: string, m: ModelCount, cap: number, timeoutMs = 20 * 60_000): Promise<Fetched> {
  const want = cap * 1.05 + 4 * Math.sqrt(cap) + 4
  const share = (n: number) => Math.min(HASH_RANGE, Math.ceil(HASH_RANGE * Math.min(1, n / Math.max(1, m.files))))
  const below = m.files <= cap ? HASH_RANGE : share(want)
  const textBelow = Math.min(below, share(REMOTE_TEXT_FILES * 1.05))
  const r = await runRemote(root, fetchStages(m, below, textBelow, EXTERNAL_DB_PATTERN || null), timeoutMs)
  const top = remoteTarget(root)!.indexRoot
  const seen = new Set<string>()
  const files: FetchedFile[] = []
  for (const [h, p, len, v, t] of rows(r.stdout, 5)) {
    const rel = relative(p, top)
    if (rel === null || seen.has(rel) || !/^[0-9A-F]{768}$/i.test(v)) continue
    seen.add(rel)
    files.push({ h: Number(h), p: rel, r: Number(len) || 0, v, t: t ?? '' })
  }
  if (!files.length) {
    const e = noRows(r, 'fetching the vectors')
    if (e) throw e
    throw new KbError(502, `gufi_query on ${r.site} returned no vectors${r.stderr.trim() ? `: ${r.stderr.trim().split('\n').pop()}` : ''}`)
  }
  files.sort((a, b) => a.h - b.h || (a.p < b.p ? -1 : a.p > b.p ? 1 : 0))
  return { files: files.slice(0, cap), bytes: r.bytes, ms: r.ms }
}

/** A file's vector put back from its row: the int8 values (sqlite-vec's
 *  'unit' quantization truncates (x + 1) * 127.5 - 128, so each value is
 *  read back at the middle of its step), normalized, times `scale`. */
export function dequantize(hex: string, scale = 1): Float32Array {
  const out = new Float32Array(hex.length / 2)
  let norm = 0
  for (let i = 0; i < out.length; i++) {
    const q = (Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16) << 24) >> 24
    const x = (q + Math.sign(q) * 0.5 + 128) / 127.5 - 1
    out[i] = x
    norm += x * x
  }
  const f = norm > 0 ? scale / Math.sqrt(norm) : 0
  for (let i = 0; i < out.length; i++) out[i] *= f
  return out
}

/** The files nearest a vector in a remote library's index, by the distance
 *  vec0 reports for unit vectors, nearest first, with paths below its top. */
export async function remoteNeighbors(root: string, m: Pick<ModelCount, 'model' | 'source'>, query: Float32Array, k: number, timeoutMs = 120_000): Promise<{ p: string; d: number }[]> {
  const buf = Buffer.alloc(query.length * 4)
  query.forEach((x, i) => buf.writeFloatLE(x, i * 4))
  const r = await runRemote(root, neighborStages(m, buf.toString('hex'), k, EXTERNAL_DB_PATTERN || null), timeoutMs)
  const top = remoteTarget(root)!.indexRoot
  const out = rows(r.stdout, 2).map(([p, d]) => ({ p: relative(p, top), d: Number(d) }))
    .filter((n): n is { p: string; d: number } => n.p !== null && Number.isFinite(n.d))
  if (!out.length) { const e = noRows(r, 'looking for the nearest files'); if (e) throw e }
  return out
}
