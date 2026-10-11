import { AsyncLocalStorage } from 'node:async_hooks'
import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, gufiAvailable } from './config.js'
import { KbError } from './kb.js'
import { tagMaps } from './tags.js'
import { scopeLibraries } from './libraryScope.js'
import { REMOTE_PREFIX, remoteIndexRoot, remoteQuery } from './userLibraries.js'
import { effectiveSettings } from './settings.js'

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
  return new Promise((resolve, reject) => {
    execFile(path.join(GUFI_BIN, 'gufi_query'), [...args, scopePath(subtree)], { timeout: timeoutMs, maxBuffer: 64 * 1024 * 1024 },
      (err, so, se) => (err && !so ? reject(new Error(se?.slice(0, 500) || err.message)) : resolve({ stdout: so, stderr: se ?? '' })))
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
async function aggQuery(opts: {
  schema: string
  perDir: string
  final: string
  columns: string[]
  subtree?: string
}): Promise<QueryResult> {
  const t0 = Date.now()
  const { stdout } = await run([
    '-n', '8', '-d', DELIM, '-a', '1',
    '-I', `CREATE TABLE t ${opts.schema};`,
    '-K', `CREATE TABLE agg ${opts.schema};`,
    '-E', opts.perDir,
    '-J', 'INSERT INTO agg SELECT * FROM t;',
    '-G', opts.final,
  ], opts.subtree)
  const rows = parse(stdout, opts.columns)
  return {
    columns: opts.columns,
    rows: rows.slice(0, MAX_ROWS),
    elapsedMs: Date.now() - t0,
    truncated: rows.length > MAX_ROWS,
  }
}

const PATH_EXPR = `rpath(sname, sroll)||'/'||vrpentries.name`

const CANNED: Record<string, (p: { n: number; days: number; subtree?: string }) => Promise<QueryResult>> = {
  largest: p => aggQuery({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries ORDER BY size DESC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY size DESC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  newest: p => aggQuery({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries ORDER BY mtime DESC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime DESC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  oldest: p => aggQuery({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries ORDER BY mtime ASC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime ASC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  recent: p => aggQuery({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries WHERE mtime > strftime('%s','now') - ${p.days} * 86400 ORDER BY mtime DESC LIMIT ${p.n};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY mtime DESC LIMIT ${p.n};`,
    columns: ['path', 'bytes', 'modified'],
    subtree: p.subtree,
  }),
  by_extension: p => aggQuery({
    schema: '(ext TEXT, cnt INT64, bytes INT64)',
    perDir: `INSERT INTO t SELECT lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '(none)' END), count(*), sum(size) FROM vrpentries GROUP BY 1;`,
    final: `SELECT ext, sum(cnt), sum(bytes) FROM agg GROUP BY ext ORDER BY sum(bytes) DESC LIMIT ${p.n};`,
    columns: ['extension', 'files', 'bytes'],
    subtree: p.subtree,
  }),
  big_directories: p => aggQuery({
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
  return fn(p)
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

async function builderQuery(p: BuilderPayload): Promise<QueryResult> {
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
    return aggQuery({
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
    return aggQuery({
      schema: '(ext TEXT, cnt INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT ${EXT_EXPR}, count(*), sum(size) ${from} ${where} GROUP BY 1;`,
      final: `SELECT ext, sum(cnt), sum(bytes) FROM agg GROUP BY ext ORDER BY ${p.sort === 'files' ? 'sum(cnt)' : 'sum(bytes)'} ${p.dir === 'asc' ? 'ASC' : 'DESC'} LIMIT ${limit};`,
      columns: ['extension', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  if (p.groupBy === 'directory') {
    return aggQuery({
      schema: '(path TEXT, cnt INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT rpath(sname, sroll), count(*), sum(size) ${from} ${where} GROUP BY 1;`,
      final: `SELECT path, cnt, bytes FROM agg ORDER BY ${p.sort === 'files' ? 'cnt' : 'bytes'} ${p.dir === 'asc' ? 'ASC' : 'DESC'} LIMIT ${limit};`,
      columns: ['directory', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  const sortCol = p.sort === 'name' ? 'path' : p.sort === 'modified' ? 'mtime' : 'size'
  return aggQuery({
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
async function rawQuery(sql: string, subtree?: string): Promise<QueryResult> {
  const cleaned = sql.trim().replace(/;\s*$/, '')
  if (!/^select\s/i.test(cleaned)) throw new KbError(400, 'only SELECT statements are allowed')
  if (/;|\b(attach|detach|pragma|insert|update|delete|drop|create|alter|replace|vacuum|reindex)\b/i.test(cleaned)) {
    throw new KbError(400, 'statement contains a disallowed keyword')
  }
  const t0 = Date.now()
  const { stdout, stderr } = await run(['-n', '8', '-d', DELIM, '-E', cleaned + ';'], subtree)
  const lines = stdout.split('\n').filter(Boolean).map(l => l.split(DELIM).map(stripIndexPrefix))
  if (lines.length === 0 && /Error/i.test(stderr)) {
    const first = stderr.split('\n').find(l => /Error/i.test(l)) ?? 'query failed'
    throw new KbError(400, first.replace(/: \/[^\s"]+db\.db/g, '').slice(0, 300))
  }
  const width = lines.length ? Math.max(...lines.map(r => r.length)) : 1
  const columns = Array.from({ length: width }, (_, i) => `col${i + 1}`)
  return {
    columns,
    rows: lines.slice(0, MAX_ROWS),
    elapsedMs: Date.now() - t0,
    truncated: lines.length > MAX_ROWS,
  }
}

export interface QueryRequest {
  canned?: string; sql?: string; builder?: BuilderPayload
  n?: number; days?: number; subtree?: string
  /** A library id, or "all" for every library the viewer can read. */
  library?: string
}

/** One query on one index, whichever form it takes. */
function execQuery(body: QueryRequest): Promise<QueryResult> {
  const n = Math.min(Math.max(Number(body.n) || 25, 1), MAX_ROWS)
  const days = Math.min(Math.max(Number(body.days) || 7, 1), 3650)
  if (body.builder) return builderQuery(body.builder)
  if (body.canned) {
    const fn = CANNED[body.canned]
    if (!fn) throw new KbError(400, `unknown canned query: ${body.canned}`)
    return fn({ n, days, subtree: body.subtree })
  }
  if (body.sql) return rawQuery(String(body.sql), body.subtree)
  throw new KbError(400, 'builder, canned, or sql required')
}

/**
 * A query on the named library, or on every library the viewer can read.
 * Across several, each library answers the query on its own (its own top
 * rows for a ranked query) and the rows carry a leading library column; a
 * library that cannot answer is reported beside the rows, not fatal.
 */
export async function queryScope(body: QueryRequest, viewerId: string | null): Promise<QueryResult> {
  const libs = scopeLibraries(body.library, viewerId, effectiveSettings().kbLabel)
  const local = (root: string) => !root.startsWith(REMOTE_PREFIX)
  if (libs.length === 1) {
    if (local(libs[0].indexRoot) && !gufiAvailable()) throw new KbError(503, 'index not built yet; run indexer/setup_gufi.sh and indexer/reindex.sh')
    return onIndex(libs[0].indexRoot, () => execQuery(body))
  }
  const t0 = Date.now()
  const parts = await Promise.all(libs.map(l =>
    (local(l.indexRoot) && !gufiAvailable() ? Promise.reject(new Error('index not built yet')) : onIndex(l.indexRoot, () => execQuery(body)))
      .then(r => ({ l, r }), (e: Error) => ({ l, err: String(e.message ?? e).slice(0, 300) }))))
  const ok = parts.filter((p): p is { l: typeof libs[number]; r: QueryResult } => 'r' in p)
  const width = Math.max(1, ...ok.map(p => p.r.columns.length))
  const columns = ['library', ...(ok.find(p => p.r.columns.length === width)?.r.columns ?? Array.from({ length: width }, (_, i) => `col${i + 1}`))]
  let tagged = ok.flatMap(p => p.r.rows.map(row => ({ id: p.l.id, row: [p.l.label, ...row] })))
  // A ranked query's answer across libraries is the overall top rows, not
  // each library's top rows stacked; where the order is known, re-rank.
  const order = mergeOrder(body)
  if (order?.groupBy !== undefined) {
    const sums = new Map<string, { id: string; row: string[] }>()
    for (const t of tagged) {
      const k = t.row[order.groupBy + 1]
      const cur = sums.get(k)
      if (!cur) sums.set(k, { id: 'all', row: ['all', ...t.row.slice(1)] })
      else for (const c of order.sum) cur.row[c + 1] = String((Number(cur.row[c + 1]) || 0) + (Number(t.row[c + 1]) || 0))
    }
    tagged = [...sums.values()]
  }
  if (order) {
    const c = order.col + 1
    tagged.sort((a, b) => {
      const x = order.numeric ? (Number(a.row[c]) || 0) - (Number(b.row[c]) || 0) : String(a.row[c]).localeCompare(String(b.row[c]))
      return order.desc ? -x : x
    })
    tagged = tagged.slice(0, order.limit)
  }
  const rows = tagged.map(t => t.row); const rowLibraries = tagged.map(t => t.id)
  return {
    columns, rows: rows.slice(0, MAX_ROWS), rowLibraries: rowLibraries.slice(0, MAX_ROWS),
    elapsedMs: Date.now() - t0, truncated: rows.length > MAX_ROWS || ok.some(p => p.r.truncated),
    failed: parts.filter((p): p is { l: typeof libs[number]; err: string } => 'err' in p).map(p => ({ library: p.l.label, error: p.err })),
  }
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
  app.post('/api/query', async req => queryScope((req.body ?? {}) as QueryRequest, req.user?.id ?? null))

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
