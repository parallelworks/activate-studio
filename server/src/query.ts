import { AsyncLocalStorage } from 'node:async_hooks'
import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, gufiAvailable, gufiEnv } from './config.js'
import { KbError } from './kb.js'
import { tagMaps } from './tags.js'
import { scopeLibraries } from './libraryScope.js'
import { REMOTE_PREFIX, remoteIndexRoot, remoteQuery } from './userLibraries.js'
import { effectiveSettings } from './settings.js'
import { type GufiCommand, noteGufi, traced } from './gufiTrace.js'
import { type Plan, type VtOrder, gufiVtAvailable, runAcross, vtEligible } from './gufiVt.js'

const DELIM = '\x1e'
const MAX_ROWS = 2000

export interface QueryResult {
  columns: string[]
  rows: string[][]
  elapsedMs: number
  truncated: boolean
  /** Across several libraries: the library each row came from, by id. */
  rowLibraries?: string[]
  /** Across several libraries: the ones that could not answer, and why. */
  failed?: { library: string; error: string }[]
  /** Across several libraries: one gufi_vt statement, or each library on
   *  its own with the rows merged by the Studio. */
  engine?: 'gufi_vt' | 'merge'
  /** The GUFI commands that produced the rows, as a shell takes them. */
  gufi?: GufiCommand[]
}

/** The index a query runs against: the knowledge base unless a library
 *  was named. Set around a whole query with onIndex, so the canned
 *  queries, the builder, and raw SQL need not pass it along. */
const rootCtx = new AsyncLocalStorage<string>()
const indexRoot = (): string => rootCtx.getStore() ?? GUFI_INDEX
export function onIndex<T>(root: string, fn: () => Promise<T>): Promise<T> { return rootCtx.run(root, fn) }

/** gufi_query over the index in scope, below `subtree`; a user's library on
 *  a cluster runs there (userLibraries.ts), with at most 4 threads. */
async function run(args: string[], subtree?: string, timeoutMs = 60_000): Promise<{ stdout: string; stderr: string }> {
  const root = indexRoot()
  if (root.startsWith(REMOTE_PREFIX)) {
    const capped = args.map((a, i) => (args[i - 1] === '-n' ? String(Math.min(Number(a) || 4, 4)) : a))
    return { stdout: await remoteQuery(root, capped, cleanSubtree(subtree)), stderr: '' }
  }
  const argv = [path.join(GUFI_BIN, 'gufi_query'), ...args, scopePath(subtree)]
  const t0 = Date.now()
  return new Promise((resolve, reject) => {
    execFile(argv[0], argv.slice(1), { timeout: timeoutMs, maxBuffer: 64 * 1024 * 1024, env: gufiEnv() }, (err, so, se) => {
      noteGufi(argv, { ms: Date.now() - t0 })
      if (err && !so) reject(new Error(se?.slice(0, 500) || err.message))
      else resolve({ stdout: so, stderr: se ?? '' })
    })
  })
}

/** The local path of the index in scope, for prefixes in SQL; a remote
 *  index's own path on its cluster. */
function rootPath(): string {
  const root = indexRoot()
  return root.startsWith(REMOTE_PREFIX) ? remoteIndexRoot(root) : path.resolve(root)
}

/** gufi_query prints paths rooted at the index tree; make them relative. */
function stripIndexPrefix(cell: string): string {
  const idx = rootPath() + '/'
  return cell.startsWith(idx) ? cell.slice(idx.length) : cell
}

function cleanSubtree(subtree?: string): string | undefined {
  const cleaned = (subtree ?? '').replace(/^\/+|\/+$/g, '')
  if (!cleaned) return undefined
  if (cleaned.split('/').includes('..')) throw new KbError(400, 'scope escapes the index')
  return cleaned
}

function scopePath(subtree?: string): string {
  const root = indexRoot()
  const cleaned = cleanSubtree(subtree)
  if (!cleaned) return root
  const abs = path.resolve(root, cleaned)
  if (!abs.startsWith(path.resolve(root))) throw new KbError(400, 'scope escapes the index')
  return abs
}

function parse(stdout: string, columns: string[]): QueryResult['rows'] {
  return stdout.split('\n').filter(Boolean)
    .map(l => l.split(DELIM).map(stripIndexPrefix))
    .filter(r => r.length >= columns.length)
}

/** Staged aggregate: per-directory -E inserts into a per-thread table, -J
 *  merges into the global aggregate, -G applies the final global order. */
function aggPlan(opts: {
  schema: string
  perDir: string
  final: string
  columns: string[]
  subtree?: string
}): Plan {
  return {
    stages: {
      I: `CREATE TABLE t ${opts.schema};`,
      E: opts.perDir,
      K: `CREATE TABLE agg ${opts.schema};`,
      J: 'INSERT INTO agg SELECT * FROM t;',
      G: opts.final,
    },
    columns: opts.columns,
    subtree: opts.subtree,
  }
}

/** Runs a plan on the index in scope. */
async function runPlan(plan: Plan): Promise<QueryResult> {
  if (plan.raw) return rawRun(plan)
  const t0 = Date.now()
  const s = plan.stages
  const { stdout } = await run([
    '-n', '8', '-d', DELIM, '-a', '1',
    '-I', s.I!, '-K', s.K!, '-E', s.E, '-J', s.J!, '-G', s.G!,
  ], plan.subtree)
  const rows = parse(stdout, plan.columns)
  return {
    columns: plan.columns,
    rows: rows.slice(0, MAX_ROWS),
    elapsedMs: Date.now() - t0,
    truncated: rows.length > MAX_ROWS,
  }
}

const PATH_EXPR = `rpath(sname, sroll)||'/'||vrpentries.name`

const CANNED: Record<string, (p: { n: number; days: number; subtree?: string }) => Plan> = {
  largest: p => aggPlan({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries ORDER BY size DESC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY size DESC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  newest: p => aggPlan({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries ORDER BY mtime DESC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime DESC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  oldest: p => aggPlan({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries ORDER BY mtime ASC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime ASC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  recent: p => aggPlan({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries WHERE mtime > strftime('%s','now') - ${p.days} * 86400 ORDER BY mtime DESC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime DESC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  by_extension: p => aggPlan({
    schema: '(ext TEXT, cnt INT64, bytes INT64)',
    perDir: `INSERT INTO t SELECT lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '(none)' END), count(*), sum(size) FROM vrpentries GROUP BY 1;`,
    final: `SELECT ext, sum(cnt), sum(bytes) FROM agg GROUP BY ext ORDER BY sum(bytes) DESC LIMIT ${p.n};`,
    columns: ['extension', 'files', 'bytes'],
    subtree: p.subtree,
  }),
  big_directories: p => aggPlan({
    schema: '(path TEXT, files INT64, bytes INT64)',
    perDir: `INSERT INTO t SELECT rpath(sname, sroll), totfiles, totsize FROM vrsummary;`,
    final: `SELECT path, files, bytes FROM agg ORDER BY bytes DESC LIMIT ${p.n};`,
    columns: ['directory', 'files', 'bytes'],
    subtree: p.subtree,
  }),
}

/** Run a canned corpus query by id; used by the API and by the chat tool. */
export async function runCanned(id: string, p: { n: number; days: number; subtree?: string }): Promise<QueryResult> {
  const fn = CANNED[id]
  if (!fn) throw new KbError(400, `unknown canned query: ${id}`)
  return runPlan(fn(p))
}

export const CANNED_IDS = Object.keys(CANNED)

/* ---- structured builder: compile a form payload into a staged query ---- */

export interface BuilderFilter { field: string; op: string; value: string }
export interface BuilderPayload {
  source: 'files' | 'directories'
  filters: BuilderFilter[]
  groupBy: 'none' | 'extension' | 'directory'
  sort: string
  dir: 'asc' | 'desc'
  limit: number
  subtree?: string
}

function esc(v: string): string { return v.replace(/'/g, "''") }

const EXT_EXPR = `lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '(none)' END)`

const FILE_FIELDS: Record<string, string> = {
  name: 'vrpentries.name',
  path: PATH_EXPR,
  extension: EXT_EXPR,
  size: 'size',
  modified: 'mtime',
  text: 'text',
  label: 'label',
}

function filterSql(f: BuilderFilter, labeledDirs?: Map<string, string[]>): string {
  const field = FILE_FIELDS[f.field]
  if (!field) throw new KbError(400, `unknown field: ${f.field}`)
  let v = String(f.value ?? '')
  if (f.field === 'extension') v = v.replace(/^\./, '').toLowerCase()
  if (f.field === 'label') {
    if (f.op !== 'eq' && f.op !== 'contains') throw new KbError(400, 'label supports equals and contains')
    const pat = f.op === 'eq' ? `%,${esc(v.toLowerCase())},%` : `%${esc(v.toLowerCase())}%`
    const own = `EXISTS (SELECT 1 FROM xattrs_pwd xt WHERE xt.inode = vrpentries.inode AND CAST(xt.name AS TEXT) = 'user.studio.tags' AND (','||lower(CAST(xt.value AS TEXT))||',') LIKE '${pat}')`
    // Directory labels inherit down whole subtrees, and a per-directory db
    // cannot see its ancestors, so inheritance compiles to path prefixes of
    // the labeled directories (known app-side via the tag maps). In WHERE
    // context rpath() yields the ABSOLUTE index path (output rows are
    // stripped afterwards), so the prefix must carry the index root.
    const want = v.toLowerCase()
    const idxRoot = rootPath()
    const dirs = [...(labeledDirs ?? new Map<string, string[]>())]
      .filter(([rel, tags]) => rel !== '' && tags.some(t =>
        f.op === 'eq' ? t.toLowerCase() === want : t.toLowerCase().includes(want)))
      .map(([rel]) => rel)
    const prefixes = dirs.map(rel =>
      `(${PATH_EXPR}) LIKE '${esc(`${idxRoot}/${rel}`).replace(/([%_])/g, '\\$1')}/%' ESCAPE '\\'`)
    return `(${[own, ...prefixes].join(' OR ')})`
  }
  if (f.field === 'text') {
    if (f.op !== 'match') throw new KbError(400, 'text supports only match')
    const terms = v.split(/\s+/).map(t => t.replace(/["'*]/g, '')).filter(Boolean).slice(0, 8)
    if (!terms.length) throw new KbError(400, 'empty text match')
    return `wordf MATCH '${esc(terms.map(t => `"${t}"`).join(' AND '))}'`
  }
  const isNum = f.field === 'size' || f.field === 'modified'
  const val = f.field === 'modified'
    ? `strftime('%s', '${esc(v)}')`
    : isNum ? String(Number(v) || 0) : `'${esc(v)}'`
  switch (f.op) {
    case 'contains': return `${field} LIKE '%${esc(v.replace(/[%_]/g, ''))}%'`
    case 'not_contains': return `${field} NOT LIKE '%${esc(v.replace(/[%_]/g, ''))}%'`
    case 'eq': return `${field} = ${val}`
    case 'gt': return `${field} > ${val}`
    case 'lt': return `${field} < ${val}`
    default: throw new KbError(400, `unknown operator: ${f.op}`)
  }
}

async function builderPlan(p: BuilderPayload): Promise<Plan> {
  const limit = Math.min(Math.max(Number(p.limit) || 50, 1), MAX_ROWS)
  const usesText = p.filters.some(f => f.field === 'text')
  const labeledDirs = p.filters.some(f => f.field === 'label')
    ? (await tagMaps()).dirTags
    : undefined
  const where = p.filters.length
    ? 'WHERE ' + p.filters.map(f => filterSql(f, labeledDirs)).join(' AND ')
    : ''
  if (p.source === 'directories') {
    const allowed = new Set(['name', 'size', 'modified'])
    if (p.groupBy !== 'none' || p.filters.some(f => !allowed.has(f.field))) {
      throw new KbError(400, 'directory source supports name, size, and modified filters without grouping')
    }
    const dirWhere = where
      .replace(/vrpentries\.name/g, 'vrsummary.name')
      .replace(/\bsize\b/g, 'totsize')
    return aggPlan({
      schema: '(path TEXT, files INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT rpath(sname, sroll), totfiles, totsize FROM vrsummary ${dirWhere};`,
      final: `SELECT path, files, bytes FROM agg ORDER BY ${p.sort === 'files' ? 'files' : 'bytes'} ${p.dir === 'asc' ? 'ASC' : 'DESC'} LIMIT ${limit};`,
      columns: ['directory', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  const from = usesText
    ? 'FROM vrpentries INNER JOIN words ON inode=CAST(tinode AS TEXT)'
    : 'FROM vrpentries'
  if (p.groupBy === 'extension') {
    return aggPlan({
      schema: '(ext TEXT, cnt INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT ${EXT_EXPR}, count(*), sum(size) ${from} ${where} GROUP BY 1;`,
      final: `SELECT ext, sum(cnt), sum(bytes) FROM agg GROUP BY ext ORDER BY ${p.sort === 'files' ? 'sum(cnt)' : 'sum(bytes)'} ${p.dir === 'asc' ? 'ASC' : 'DESC'} LIMIT ${limit};`,
      columns: ['extension', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  if (p.groupBy === 'directory') {
    return aggPlan({
      schema: '(path TEXT, cnt INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT rpath(sname, sroll), count(*), sum(size) ${from} ${where} GROUP BY 1;`,
      final: `SELECT path, cnt, bytes FROM agg ORDER BY ${p.sort === 'files' ? 'cnt' : 'bytes'} ${p.dir === 'asc' ? 'ASC' : 'DESC'} LIMIT ${limit};`,
      columns: ['directory', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  const sortCol = p.sort === 'name' ? 'path' : p.sort === 'modified' ? 'mtime' : 'size'
  return aggPlan({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime ${from} ${where} ORDER BY ${sortCol} ${p.dir === 'asc' ? 'ASC' : 'DESC'} LIMIT ${limit};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY ${sortCol === 'mtime' ? 'mtime' : sortCol} ${p.dir === 'asc' ? 'ASC' : 'DESC'} LIMIT ${limit};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  })
}

/* ---- saved queries, Superset style ---- */

const SAVED_FILE = path.join(INDEX_BASE, 'saved-queries.json')

interface SavedQuery { id: string; name: string; payload: unknown; createdAt: string }

/** Starter examples written on first run so the saved-query rail is never
 *  empty; users edit or delete them like any saved query. */
const STARTER_QUERIES: SavedQuery[] = [
  { id: 'starter-large', name: 'Files over 10 MB', createdAt: '', payload: { builder: { source: 'files', filters: [{ field: 'size', op: 'gt', value: '10485760' }], groupBy: 'none', sort: 'size', dir: 'desc', limit: 200, subtree: '' } } },
  { id: 'starter-pdfs', name: 'Largest PDFs', createdAt: '', payload: { builder: { source: 'files', filters: [{ field: 'extension', op: 'eq', value: 'pdf' }], groupBy: 'none', sort: 'size', dir: 'desc', limit: 100, subtree: '' } } },
  { id: 'starter-recent', name: 'Changed in the last 7 days', createdAt: '', payload: { canned: 'recent', n: 200, days: 7, subtree: '' } },
  { id: 'starter-dirs', name: 'Biggest directories', createdAt: '', payload: { canned: 'big_directories', n: 25, days: 7, subtree: '' } },
]

function loadSaved(): SavedQuery[] {
  if (!fs.existsSync(SAVED_FILE)) {
    const seeded = STARTER_QUERIES.map(s => ({ ...s, createdAt: new Date().toISOString() }))
    try { fs.writeFileSync(SAVED_FILE, JSON.stringify(seeded, null, 1)) } catch { /* read-only index */ }
    return seeded
  }
  try { return JSON.parse(fs.readFileSync(SAVED_FILE, 'utf8')) } catch { return [] }
}

/** Raw per-directory SELECT with guardrails: one statement, read-only verbs. */
function rawPlan(sql: string, subtree?: string): Plan {
  const cleaned = sql.trim().replace(/;\s*$/, '')
  if (!/^select\s/i.test(cleaned)) throw new KbError(400, 'only SELECT statements are allowed')
  if (/;|\b(attach|detach|pragma|insert|update|delete|drop|create|alter|replace|vacuum|reindex)\b/i.test(cleaned)) {
    throw new KbError(400, 'statement contains a disallowed keyword')
  }
  return { stages: { E: cleaned + ';' }, columns: [], subtree, raw: true }
}

async function rawRun(plan: Plan): Promise<QueryResult> {
  const t0 = Date.now()
  const { stdout, stderr } = await run(['-n', '8', '-d', DELIM, '-E', plan.stages.E], plan.subtree)
  const lines = stdout.split('\n').filter(Boolean).map(l => l.split(DELIM).map(stripIndexPrefix))
  if (lines.length === 0 && /Error/i.test(stderr)) {
    const first = stderr.split('\n').find(l => /Error/i.test(l)) ?? 'query failed'
    throw new KbError(400, first.replace(/: \/[^\s"]+db\.db/g, '').slice(0, 300))
  }
  const width = lines.length ? Math.max(...lines.map(r => r.length)) : 1
  return {
    columns: rawColumns(width),
    rows: lines.slice(0, MAX_ROWS),
    elapsedMs: Date.now() - t0,
    truncated: lines.length > MAX_ROWS,
  }
}

const rawColumns = (width: number) => Array.from({ length: width }, (_, i) => `col${i + 1}`)

export interface QueryRequest {
  canned?: string; sql?: string; builder?: BuilderPayload
  n?: number; days?: number; subtree?: string
  /** A library id, or "all" for every library the viewer can read. */
  library?: string
}

/** One query's plan for the index in scope, whichever form it takes. */
async function planFor(body: QueryRequest): Promise<Plan> {
  const n = Math.min(Math.max(Number(body.n) || 25, 1), MAX_ROWS)
  const days = Math.min(Math.max(Number(body.days) || 7, 1), 3650)
  if (body.builder) return builderPlan(body.builder)
  if (body.canned) {
    const fn = CANNED[body.canned]
    if (!fn) throw new KbError(400, `unknown canned query: ${body.canned}`)
    return fn({ n, days, subtree: body.subtree })
  }
  if (body.sql) return rawPlan(String(body.sql), body.subtree)
  throw new KbError(400, 'builder, canned, or sql required')
}

const execQuery = async (body: QueryRequest): Promise<QueryResult> => runPlan(await planFor(body))

/**
 * A query on the named library, or on every library the viewer can read.
 * Across several, the rows carry a leading library column and a library
 * that cannot answer is reported beside the rows, not fatal. Where this
 * host's GUFI has gufi_vt and the query fits it, the libraries answer as
 * one gufi_vt statement (gufiVt.ts); otherwise each library answers on its
 * own and the rows merge here. Either way a ranked query's answer is the
 * overall top rows, re-ranked here, and a grouped one is summed here.
 */
export async function queryScope(body: QueryRequest, viewerId: string | null): Promise<QueryResult> {
  const libs = scopeLibraries(body.library, viewerId, effectiveSettings().kbLabel)
  const local = (root: string) => !root.startsWith(REMOTE_PREFIX)
  if (libs.length === 1) {
    if (local(libs[0].indexRoot) && !gufiAvailable()) throw new KbError(503, 'index not built yet; run indexer/setup_gufi.sh and indexer/reindex.sh')
    return onIndex(libs[0].indexRoot, () => execQuery(body))
  }
  const t0 = Date.now()
  const order = mergeOrder(body)
  // Each library's plan is built against its own index: a label filter
  // carries that index's paths.
  const plans = await Promise.all(libs.map(async lib => ({ lib, plan: await onIndex(lib.indexRoot, () => planFor(body)) })))
  let tagged: { id: string; row: string[] }[] | null = null
  let failed: QueryResult['failed'] = []
  let engine: QueryResult['engine'] = 'merge'
  if (plans.every(p => vtEligible(p.plan)) && await gufiVtAvailable()) {
    // The outer ORDER BY is exact for numbers and timestamps; names, and
    // grouped totals, are ordered here.
    const pushed: VtOrder | null = order && order.groupBy === undefined && order.numeric ? { col: order.col, desc: order.desc, limit: order.limit } : null
    try {
      const r = await runAcross(plans, pushed)
      tagged = r.rows; failed = r.failed; engine = 'gufi_vt'
    } catch (e) {
      vtFallbacks++
      console.warn(`gufi_vt query fell back to the per-library merge: ${(e as Error).message}`)
    }
  }
  if (!tagged) {
    const parts = await Promise.all(plans.map(({ lib, plan }) =>
      (local(lib.indexRoot) && !gufiAvailable() ? Promise.reject(new Error('index not built yet')) : onIndex(lib.indexRoot, () => runPlan(plan)))
        .then(r => ({ lib, r }), (e: Error) => ({ lib, err: String(e.message ?? e).slice(0, 300) }))))
    tagged = parts.flatMap(p => ('r' in p ? p.r.rows.map(row => ({ id: p.lib.id, row })) : []))
    failed = parts.flatMap(p => ('err' in p ? [{ library: p.lib.label, error: p.err }] : []))
  }
  const label = new Map(libs.map(l => [l.id, l.label]))
  const width = Math.max(1, ...tagged.map(t => t.row.length))
  const columns = ['library', ...(plans[0].plan.raw ? rawColumns(width) : plans[0].plan.columns)]
  const merged = mergeRows(tagged, order)
  return {
    columns,
    rows: merged.slice(0, MAX_ROWS).map(t => [t.id === 'all' ? 'all' : label.get(t.id) ?? t.id, ...t.row]),
    rowLibraries: merged.slice(0, MAX_ROWS).map(t => t.id),
    elapsedMs: Date.now() - t0, truncated: merged.length > MAX_ROWS,
    failed, engine,
  }
}

/** gufi_vt statements that failed and were answered by the merge instead. */
let vtFallbacks = 0
export const vtFallbackCount = () => vtFallbacks

/** Rows from several libraries as one answer: grouped totals summed by key,
 *  then the overall order and limit where the query has one. */
function mergeRows(tagged: { id: string; row: string[] }[], order: ReturnType<typeof mergeOrder>): { id: string; row: string[] }[] {
  let out = tagged
  if (order?.groupBy !== undefined) {
    const sums = new Map<string, { id: string; row: string[] }>()
    for (const t of out) {
      const k = t.row[order.groupBy]
      const cur = sums.get(k)
      if (!cur) sums.set(k, { id: 'all', row: [...t.row] })
      else for (const c of order.sum) cur.row[c] = String((Number(cur.row[c]) || 0) + (Number(t.row[c]) || 0))
    }
    out = [...sums.values()]
  }
  if (order) {
    const c = order.col
    out = [...out].sort((a, b) => {
      const x = order.numeric ? (Number(a.row[c]) || 0) - (Number(b.row[c]) || 0) : String(a.row[c]).localeCompare(String(b.row[c]))
      return order.desc ? -x : x
    }).slice(0, order.limit)
  }
  return out
}

/**
 * How rows from several libraries merge into one ranked answer: the column
 * (in the query's own columns) to sort on, its direction, and the limit;
 * for a grouped total, the key column and the columns to add up. Null for
 * raw SQL, whose order the Studio cannot know.
 */
function mergeOrder(body: QueryRequest): { col: number; numeric: boolean; desc: boolean; limit: number; groupBy?: number; sum: number[] } | null {
  const n = Math.min(Math.max(Number(body.n) || 25, 1), MAX_ROWS)
  switch (body.canned) {
    case 'largest': return { col: 1, numeric: true, desc: true, limit: n, sum: [] }
    case 'newest': case 'recent': return { col: 2, numeric: false, desc: true, limit: n, sum: [] }
    case 'oldest': return { col: 2, numeric: false, desc: false, limit: n, sum: [] }
    case 'big_directories': return { col: 2, numeric: true, desc: true, limit: n, sum: [] }
    case 'by_extension': return { col: 2, numeric: true, desc: true, limit: n, groupBy: 0, sum: [1, 2] }
  }
  const b = body.builder
  if (!b) return null
  const limit = Math.min(Math.max(Number(b.limit) || 50, 1), MAX_ROWS)
  const desc = b.dir !== 'asc'
  if (b.source === 'directories') return { col: b.sort === 'files' ? 1 : 2, numeric: true, desc, limit, sum: [] }
  if (b.groupBy === 'extension') return { col: b.sort === 'files' ? 1 : 2, numeric: true, desc, limit, groupBy: 0, sum: [1, 2] }
  if (b.groupBy === 'directory') return { col: b.sort === 'files' ? 1 : 2, numeric: true, desc, limit, sum: [] }
  if (b.sort === 'name') return { col: 0, numeric: false, desc, limit, sum: [] }
  if (b.sort === 'modified') return { col: 2, numeric: false, desc, limit, sum: [] }
  return { col: 1, numeric: true, desc, limit, sum: [] }
}

export async function queryRoutes(app: FastifyInstance): Promise<void> {
  app.post('/api/query', async req => {
    const { result, commands } = await traced(() => queryScope((req.body ?? {}) as QueryRequest, req.user?.id ?? null))
    return { ...result, gufi: commands }
  })

  app.get('/api/query/saved', async () => loadSaved())

  app.post('/api/query/saved', async req => {
    const body = req.body as { name?: string; payload?: unknown }
    const name = String(body.name ?? '').trim().slice(0, 80)
    if (!name || body.payload == null) throw new KbError(400, 'name and payload required')
    const saved = loadSaved()
    const entry: SavedQuery = {
      id: `q${Date.now().toString(36)}${Math.floor(Math.random() * 1e4)}`,
      name,
      payload: body.payload,
      createdAt: new Date().toISOString(),
    }
    saved.unshift(entry)
    await fsp.writeFile(SAVED_FILE, JSON.stringify(saved.slice(0, 200), null, 1))
    return entry
  })

  app.delete('/api/query/saved/:id', async req => {
    const id = (req.params as { id: string }).id
    const saved = loadSaved().filter(s => s.id !== id)
    await fsp.writeFile(SAVED_FILE, JSON.stringify(saved, null, 1))
    return { deleted: id }
  })
}
