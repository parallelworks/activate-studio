import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE } from './config.js'
import { KbError } from './kb.js'

const DELIM = '\x1e'
const MAX_ROWS = 500

interface QueryResult {
  columns: string[]
  rows: string[][]
  elapsedMs: number
  truncated: boolean
}

function run(args: string[], timeoutMs = 60_000): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    execFile(path.join(GUFI_BIN, 'gufi_query'), args, { timeout: timeoutMs, maxBuffer: 64 * 1024 * 1024 },
      (err, so, se) => (err && !so ? reject(new Error(se?.slice(0, 500) || err.message)) : resolve({ stdout: so, stderr: se ?? '' })))
  })
}

/** gufi_query prints paths rooted at the index tree; make them KB-relative. */
function stripIndexPrefix(cell: string): string {
  const idx = path.resolve(GUFI_INDEX) + '/'
  return cell.startsWith(idx) ? cell.slice(idx.length) : cell
}

function scopePath(subtree?: string): string {
  const cleaned = (subtree ?? '').replace(/^\/+|\/+$/g, '')
  if (!cleaned) return GUFI_INDEX
  const abs = path.resolve(GUFI_INDEX, cleaned)
  if (!abs.startsWith(path.resolve(GUFI_INDEX))) throw new KbError(400, 'scope escapes the index')
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
    scopePath(opts.subtree),
  ])
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
}

function filterSql(f: BuilderFilter): string {
  const field = FILE_FIELDS[f.field]
  if (!field) throw new KbError(400, `unknown field: ${f.field}`)
  let v = String(f.value ?? '')
  if (f.field === 'extension') v = v.replace(/^\./, '').toLowerCase()
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
  const where = p.filters.length
    ? 'WHERE ' + p.filters.map(filterSql).join(' AND ')
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

function loadSaved(): SavedQuery[] {
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
  const { stdout, stderr } = await run(['-n', '8', '-d', DELIM, '-E', cleaned + ';', scopePath(subtree)])
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

export async function queryRoutes(app: FastifyInstance): Promise<void> {
  app.post('/api/query', async req => {
    const body = req.body as {
      canned?: string; sql?: string; builder?: BuilderPayload
      n?: number; days?: number; subtree?: string
    }
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
