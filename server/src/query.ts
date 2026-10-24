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
import { REMOTE_PREFIX, remoteIndexRoot, remoteQuery, remoteTarget } from './userLibraries.js'
import { effectiveSettings } from './settings.js'
import { type GufiCommand, noteGufi, shellQuote, traced } from './gufiTrace.js'
import { siteName } from './remoteExec.js'
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

/** A cluster's share of threads: at most 4. */
const capThreads = (args: string[]): string[] => args.map((a, i) => (args[i - 1] === '-n' ? String(Math.min(Number(a) || 4, 4)) : a))

/** gufi_query over the index in scope, below `subtree`; a user's library on
 *  a cluster runs there (userLibraries.ts), with at most 4 threads. */
async function run(args: string[], subtree?: string, timeoutMs = 60_000): Promise<{ stdout: string; stderr: string }> {
  const root = indexRoot()
  if (root.startsWith(REMOTE_PREFIX)) {
    return { stdout: await remoteQuery(root, capThreads(args), cleanSubtree(subtree)), stderr: '' }
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

/** gufi_query's arguments for a plan, ahead of the index path: raw SQL is
 *  the -E stage alone; the rest are staged aggregates. */
function planArgs(plan: Plan): string[] {
  const s = plan.stages
  if (plan.raw) return ['-n', '8', '-d', DELIM, '-E', s.E]
  return ['-n', '8', '-d', DELIM, '-a', '1', '-I', s.I!, '-K', s.K!, '-E', s.E, '-J', s.J!, '-G', s.G!]
}

/** Runs a plan on the index in scope. */
async function runPlan(plan: Plan): Promise<QueryResult> {
  if (plan.raw) return rawRun(plan)
  const t0 = Date.now()
  const { stdout } = await run(planArgs(plan), plan.subtree)
  const rows = parse(stdout, plan.columns)
  return {
    columns: plan.columns,
    rows: rows.slice(0, MAX_ROWS),
    elapsedMs: Date.now() - t0,
    truncated: rows.length > MAX_ROWS,
  }
}

const PATH_EXPR = `rpath(sname, sroll)||'/'||vrpentries.name`

const nowSeconds = () => Math.floor(Date.now() / 1000)

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
  // The cutoff is computed here: GUFI's own strftime(format, seconds) takes
  // the place of SQLite's, so strftime('%s','now') would read 'now' as 0.
  recent: p => aggPlan({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime FROM vrpentries WHERE mtime > ${nowSeconds() - p.days * 86400} ORDER BY mtime DESC LIMIT ${p.n};`,
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
  /** Whether every condition must hold (all, the default) or any one. */
  match?: 'all' | 'any'
  groupBy: 'none' | 'extension' | 'directory' | 'owner'
  sort: string
  dir: 'asc' | 'desc'
  limit: number
  subtree?: string
}

function esc(v: string): string { return v.replace(/'/g, "''") }

const EXT_EXPR = `lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '(none)' END)`
/** GUFI's own function: the user name for a uid on the host the index is
 *  queried on, or the uid where it has none. */
const OWNER_EXPR = 'uidtouser(uid)'

const FILE_FIELDS: Record<string, string> = {
  name: 'vrpentries.name',
  path: PATH_EXPR,
  extension: EXT_EXPR,
  owner: OWNER_EXPR,
  size: 'size',
  modified: 'mtime',
  text: 'text',
  label: 'label',
}

/** The operators each field takes. */
const FIELD_OPS: Record<string, string[]> = {
  name: ['contains', 'not_contains', 'eq', 'gt', 'lt'],
  path: ['contains', 'not_contains', 'eq'],
  extension: ['contains', 'not_contains', 'eq'],
  owner: ['contains', 'not_contains', 'eq'],
  size: ['eq', 'gt', 'lt'],
  modified: ['eq', 'gt', 'lt'],
  text: ['match'],
  label: ['eq', 'contains'],
}
const OP_WORDS: Record<string, string> = { contains: 'contains', not_contains: 'does not contain', eq: 'equals', gt: 'greater than', lt: 'less than', match: 'matches' }

const SIZE_UNITS: Record<string, number> = { '': 1, b: 1, k: 1024, kb: 1024, kib: 1024, m: 1024 ** 2, mb: 1024 ** 2, mib: 1024 ** 2, g: 1024 ** 3, gb: 1024 ** 3, gib: 1024 ** 3, t: 1024 ** 4, tb: 1024 ** 4, tib: 1024 ** 4 }

/** A size in bytes: a number, or one with a unit (10 MB, 1.5G, 500k), in
 *  powers of 1024. */
export function parseSize(v: string): number {
  const m = /^\s*(\d+(?:\.\d+)?)\s*([a-z]*)\s*$/i.exec(v)
  const unit = m ? SIZE_UNITS[m[2].toLowerCase()] : undefined
  if (!m || unit === undefined) throw new KbError(400, `size takes a number of bytes or a size such as 10 MB, not "${v}"`)
  return Math.round(Number(m[1]) * unit)
}

/**
 * The span of time a modified value names, in seconds since 1970 (UTC, as
 * the results show times): a day (2026-01-01), a minute (2026-01-01 09:30),
 * a second (2026-01-01 09:30:15), or a moment some time ago (7 days, 12
 * hours, 2 weeks), whose span is that moment alone. On means within the
 * span, after means at or after its end, before means before its start.
 * Worked out here, not in SQL: GUFI's own strftime(format, seconds) takes
 * the place of SQLite's, so strftime('%s', '2026-01-01') reads the date
 * as the number 2026.
 */
export function parseWhen(v: string, now = nowSeconds()): { start: number; end: number } {
  const s = v.trim()
  const ago = /^(\d+)\s*(h|hours?|d|days?|w|weeks?)(\s+ago)?$/i.exec(s)
  if (ago) {
    const unit = ago[2][0].toLowerCase()
    const t = now - Number(ago[1]) * (unit === 'h' ? 3600 : unit === 'd' ? 86400 : 7 * 86400)
    return { start: t, end: t }
  }
  const m = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?$/.exec(s)
  if (m) {
    const [y, mo, d, h, mi, se] = m.slice(1).map(x => (x === undefined ? undefined : Number(x)))
    const start = Date.UTC(y!, mo! - 1, d!, h ?? 0, mi ?? 0, se ?? 0) / 1000
    const back = new Date(start * 1000)
    if (Number.isFinite(start) && back.getUTCFullYear() === y && back.getUTCMonth() === mo! - 1 && back.getUTCDate() === d) {
      return { start, end: start + (se !== undefined ? 1 : mi !== undefined ? 60 : 86400) }
    }
  }
  throw new KbError(400, `modified takes a date such as 2026-01-01, or a time ago such as 7 days, not "${v}"`)
}

/** Where the index in scope sits, as rpath() writes it in a directory's
 *  WHERE clause: an absolute path, which output rows have stripped. */
function rootPrefix(): string { return rootPath() + '/' }

/** The same fields for directories, from GUFI's summary of each one. */
const DIR_FIELDS: Record<string, string> = {
  name: 'vrsummary.name',
  owner: OWNER_EXPR,
  size: 'totsize',
  modified: 'mtime',
}

function filterSql(f: BuilderFilter, labeledDirs?: Map<string, string[]>, fields = FILE_FIELDS): string {
  const field = fields[f.field]
  if (!field) throw new KbError(400, `unknown field: ${f.field}`)
  if (!FIELD_OPS[f.field].includes(f.op)) {
    const words = FIELD_OPS[f.field].map(o => OP_WORDS[o])
    const list = words.length > 1 ? `${words.slice(0, -1).join(', ')} or ${words[words.length - 1]}` : words[0]
    throw new KbError(400, `${f.field} takes ${list}; "${OP_WORDS[f.op] ?? f.op}" does not apply to it`)
  }
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
    const terms = v.split(/\s+/).map(t => t.replace(/["'*]/g, '')).filter(Boolean).slice(0, 8)
    if (!terms.length) throw new KbError(400, 'empty text match')
    // A subquery, not a join, so a text condition can stand beside others
    // joined by OR, and a file with several rows of text appears once.
    return `vrpentries.inode IN (SELECT CAST(tinode AS TEXT) FROM words WHERE wordf MATCH '${esc(terms.map(t => `"${t}"`).join(' AND '))}')`
  }
  if (f.field === 'size') {
    const n = parseSize(v)
    return `${field} ${f.op === 'eq' ? '=' : f.op === 'gt' ? '>' : '<'} ${n}`
  }
  if (f.field === 'modified') {
    const { start, end } = parseWhen(v)
    if (f.op === 'gt') return `${field} >= ${end}`
    if (f.op === 'lt') return `${field} < ${start}`
    return end > start ? `(${field} >= ${start} AND ${field} < ${end})` : `${field} = ${start}`
  }
  const like = (expr: string) => `${expr} LIKE '%${esc(v.replace(/[%_]/g, ''))}%'`
  if (f.field === 'path') {
    // rpath() is absolute where a directory runs the WHERE clause: compare
    // equality to the whole path, and match within the part below the index.
    const rel = `substr(${field}, ${[...rootPrefix()].length + 1})`
    if (f.op === 'eq') return `${field} = '${esc(rootPrefix() + v.replace(/^\/+/, ''))}'`
    return f.op === 'contains' ? like(rel) : `NOT ${like(rel)}`
  }
  switch (f.op) {
    case 'contains': return like(field)
    case 'not_contains': return `NOT ${like(field)}`
    case 'eq': return `${field} = '${esc(v)}'`
    case 'gt': return `${field} > '${esc(v)}'`
    case 'lt': return `${field} < '${esc(v)}'`
    default: throw new KbError(400, `unknown operator: ${f.op}`)
  }
}

/** The WHERE clause for a set of conditions: every one, or any one. */
function whereSql(filters: BuilderFilter[], match: BuilderPayload['match'], labeledDirs?: Map<string, string[]>, fields = FILE_FIELDS): string {
  if (!filters.length) return ''
  const parts = filters.map(f => `(${filterSql(f, labeledDirs, fields)})`)
  return 'WHERE ' + parts.join(match === 'any' ? ' OR ' : ' AND ')
}

async function builderPlan(p: BuilderPayload): Promise<Plan> {
  const limit = Math.min(Math.max(Number(p.limit) || 50, 1), MAX_ROWS)
  const filters = Array.isArray(p.filters) ? p.filters : []
  const labeledDirs = filters.some(f => f.field === 'label')
    ? (await tagMaps()).dirTags
    : undefined
  const dir = p.dir === 'asc' ? 'ASC' : 'DESC'
  if (p.source === 'directories') {
    if (p.groupBy !== 'none' || filters.some(f => !(f.field in DIR_FIELDS))) {
      throw new KbError(400, 'directories take name, owner, size and modified conditions, without grouping')
    }
    const dirWhere = whereSql(filters, p.match, undefined, DIR_FIELDS)
    return aggPlan({
      schema: '(path TEXT, files INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT rpath(sname, sroll), totfiles, totsize FROM vrsummary ${dirWhere};`,
      final: `SELECT path, files, bytes FROM agg ORDER BY ${p.sort === 'files' ? 'files' : 'bytes'} ${dir} LIMIT ${limit};`,
      columns: ['directory', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  const where = whereSql(filters, p.match, labeledDirs)
  const from = 'FROM vrpentries'
  const grouped = p.groupBy === 'extension' || p.groupBy === 'owner'
  if (grouped) {
    const key = p.groupBy === 'owner' ? OWNER_EXPR : EXT_EXPR
    return aggPlan({
      schema: '(k TEXT, cnt INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT ${key}, count(*), sum(size) ${from} ${where} GROUP BY 1;`,
      final: `SELECT k, sum(cnt), sum(bytes) FROM agg GROUP BY k ORDER BY ${p.sort === 'files' ? 'sum(cnt)' : 'sum(bytes)'} ${dir} LIMIT ${limit};`,
      columns: [p.groupBy === 'owner' ? 'owner' : 'extension', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  if (p.groupBy === 'directory') {
    return aggPlan({
      schema: '(path TEXT, cnt INT64, bytes INT64)',
      perDir: `INSERT INTO t SELECT rpath(sname, sroll), count(*), sum(size) ${from} ${where} GROUP BY 1;`,
      final: `SELECT path, cnt, bytes FROM agg ORDER BY ${p.sort === 'files' ? 'cnt' : 'bytes'} ${dir} LIMIT ${limit};`,
      columns: ['directory', 'files', 'bytes'],
      subtree: p.subtree,
    })
  }
  // Each directory's own top rows, then the top rows of all of them: the
  // overall top N, as -G orders the merged table.
  const sortCol = p.sort === 'name' ? 'path' : p.sort === 'modified' ? 'mtime' : 'size'
  return aggPlan({
    schema: '(path TEXT, size INT64, mtime INT64)',
    perDir: `INSERT INTO t SELECT ${PATH_EXPR}, size, mtime ${from} ${where} ORDER BY ${sortCol === 'path' ? '1' : sortCol} ${dir} LIMIT ${limit};`,
    final: `SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY ${sortCol} ${dir} LIMIT ${limit};`,
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
  const { stdout, stderr } = await run(planArgs(plan), plan.subtree)
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

/** What a query runs, without running it. */
export interface QuerySql {
  /** gufi_query's SQL stages in the order it runs them: -I and -K create a
   *  table for each thread and the result's, -E runs in every directory,
   *  -J merges each thread's rows, -G orders the result. Raw SQL is -E
   *  alone. Across several libraries, the first library's. */
  stages: { flag: '-I' | '-K' | '-E' | '-J' | '-G'; sql: string }[]
  /** The gufi_query command for each library in scope, as a shell takes it
   *  and as a run of the query records it; a library on a cluster's runs
   *  there. Across several libraries the Studio merges their rows. */
  commands: GufiCommand[]
}

/** The command run() runs for a plan on the index in scope. */
function planCommand(plan: Plan): GufiCommand {
  const root = indexRoot()
  if (root.startsWith(REMOTE_PREFIX)) {
    const t = remoteTarget(root)
    if (!t) throw new KbError(404, 'library not found')
    const sub = cleanSubtree(plan.subtree)
    const argv = [`${t.gufiBin}/gufi_query`, ...capThreads(planArgs(plan)), sub ? path.posix.join(t.indexRoot, sub) : t.indexRoot]
    return { engine: 'gufi_query', site: siteName(t.site), command: argv.map(shellQuote).join(' ') }
  }
  const argv = [path.join(GUFI_BIN, 'gufi_query'), ...planArgs(plan), scopePath(plan.subtree)]
  return { engine: 'gufi_query', command: argv.map(shellQuote).join(' ') }
}

/** The SQL a query runs and its commands, for any form the query takes
 *  (canned, builder, raw SQL), on the library named or every library the
 *  viewer can read. Nothing runs, so it answers without GUFI installed. */
export async function querySql(body: QueryRequest, viewerId: string | null): Promise<QuerySql> {
  const libs = scopeLibraries(body.library, viewerId, effectiveSettings().kbLabel)
  const plans = await Promise.all(libs.map(lib => onIndex(lib.indexRoot, async () => {
    const plan = await planFor(body)
    return { plan, command: planCommand(plan) }
  })))
  if (!plans.length) throw new KbError(404, 'no library to query')
  const s = plans[0].plan.stages
  const order = plans[0].plan.raw ? (['E'] as const) : (['I', 'K', 'E', 'J', 'G'] as const)
  return {
    stages: order.flatMap(k => (s[k] ? [{ flag: `-${k}` as const, sql: s[k]! }] : [])),
    commands: plans.map(p => p.command),
  }
}

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
  if (b.groupBy === 'extension' || b.groupBy === 'owner') return { col: b.sort === 'files' ? 1 : 2, numeric: true, desc, limit, groupBy: 0, sum: [1, 2] }
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

  // A query's SQL and commands without running it: the Query page shows a
  // saved query's this way.
  app.post('/api/query/sql', async req => querySql((req.body ?? {}) as QueryRequest, req.user?.id ?? null))

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
