import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, gufiAvailable } from './config.js'

const DELIM = '\x1e' // record separator; never appears in KB text

export interface SearchHit {
  path: string
  name: string
  size: number
  mtime: number
  snippet: string
  score: number
  mode: 'fts' | 'name' | 'vector'
  tags?: string[]
}

function run(bin: string, args: string[], timeoutMs = 60_000): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    execFile(path.join(GUFI_BIN, bin), args, { timeout: timeoutMs, maxBuffer: 64 * 1024 * 1024 }, (err, stdout, stderr) => {
      // gufi_query exits nonzero when some per-dir SQL fails (e.g. a table absent
      // in an unenriched directory); rows already emitted are still valid.
      if (err && !stdout) return reject(new Error(`${bin} failed: ${stderr || err.message}`))
      resolve({ stdout, stderr })
    })
  })
}

function parseRows(stdout: string, fields: number): string[][] {
  return stdout
    .split('\n')
    .filter(l => l.length > 0)
    .map(l => l.split(DELIM))
    .filter(r => r.length >= fields)
}

/** Escape a string literal for embedding in GUFI SQL. */
function q(s: string): string {
  return `'${s.replace(/'/g, "''")}'`
}

const STOPWORDS = new Set([
  'a', 'an', 'the', 'and', 'or', 'of', 'to', 'in', 'on', 'for', 'we', 'our', 'is',
  'are', 'was', 'be', 'can', 'how', 'what', 'when', 'with', 'up', 'after', 'do',
  'does', 'it', 'this', 'that', 'at', 'by', 'from', 'as', 'us', 'you',
])

/** Build an fts5 MATCH expression: meaningful terms quoted, AND-joined. */
function ftsExpr(query: string): string {
  const all = query.split(/\s+/).map(t => t.replace(/["'*]/g, '')).filter(Boolean)
  let terms = all.filter(t => !STOPWORDS.has(t.toLowerCase()))
  if (terms.length === 0) terms = all
  terms = terms.slice(0, 8)
  if (terms.length === 0) return ''
  return terms.map(t => `"${t}"`).join(' AND ')
}

/** Blend full-text, filename, and vector hits without letting one starve the rest. */
export function blendHits(fts: SearchHit[], names: SearchHit[], vec: SearchHit[], limit: number): SearchHit[] {
  const vecTop = vec.slice(0, Math.min(6, Math.max(2, Math.floor(limit / 3))))
  const ftsTop = fts.slice(0, Math.max(4, limit - vecTop.length))
  const seen = new Set<string>()
  return [...ftsTop, ...vecTop, ...names, ...fts, ...vec]
    .filter(h => !seen.has(h.path) && seen.add(h.path))
    .slice(0, limit)
}

export async function queryPerDir(sql: string, opts: { threads?: number; subdir?: string } = {}): Promise<string[][]> {
  const target = opts.subdir ? path.join(GUFI_INDEX, opts.subdir) : GUFI_INDEX
  const { stdout } = await run('gufi_query', ['-n', String(opts.threads ?? 8), '-d', DELIM, '-E', sql, target])
  return parseRows(stdout, 1)
}

/** Full-text search over enriched fts5 `words` tables, joined to entries by inode. */
export async function searchFts(queryText: string, limit = 20): Promise<SearchHit[]> {
  const expr = ftsExpr(queryText)
  if (!expr) return []
  const sql =
    `SELECT rpath(sname, sroll)||'/'||vrpentries.name, vrpentries.name, size, mtime, ` +
    `replace(replace(snippet(words, 2, '[', ']', ' … ', 12), char(10), ' '), char(13), ' ') ` +
    `FROM vrpentries INNER JOIN words ON inode=CAST(tinode AS TEXT) ` +
    `WHERE wordf MATCH ${q(expr)} LIMIT ${limit};`
  const rows = await queryPerDir(sql)
  return rows.slice(0, limit * 3).map(r => ({
    path: toKbRel(r[0]),
    name: r[1],
    size: Number(r[2]) || 0,
    mtime: Number(r[3]) || 0,
    snippet: r[4] ?? '',
    score: 1,
    mode: 'fts' as const,
  }))
}

/** Filename substring search over entries metadata (no enrichment needed). */
export async function searchNames(queryText: string, limit = 20): Promise<SearchHit[]> {
  const like = q('%' + queryText.replace(/[%_]/g, '') + '%')
  const sql =
    `SELECT rpath(sname, sroll)||'/'||name, name, size, mtime ` +
    `FROM vrpentries WHERE name LIKE ${like} LIMIT ${limit};`
  const rows = await queryPerDir(sql)
  return rows.slice(0, limit).map(r => ({
    path: toKbRel(r[0]),
    name: r[1],
    size: Number(r[2]) || 0,
    mtime: Number(r[3]) || 0,
    snippet: '',
    score: 0.5,
    mode: 'name' as const,
  }))
}

/** gufi_query prints paths rooted at the index tree; strip the index prefix. */
function toKbRel(p: string): string {
  const idx = path.resolve(GUFI_INDEX)
  const abs = path.resolve(p)
  if (abs.startsWith(idx + path.sep)) return abs.slice(idx.length + 1)
  if (abs === idx) return ''
  return p.replace(/^\.\//, '')
}

const EMBED_MODEL = path.join(INDEX_BASE, 'models', 'minilm384.gguf')

let dbListCache: { dbs: string[]; at: number } | null = null
export function invalidateDbList(): void { dbListCache = null }
function listIndexDbs(): string[] {
  if (dbListCache && Date.now() - dbListCache.at < 60_000) return dbListCache.dbs
  const dbs: string[] = []
  const walk = (dir: string) => {
    let entries: fs.Dirent[]
    try { entries = fs.readdirSync(dir, { withFileTypes: true }) } catch { return }
    if (entries.some(e => e.name === 'db.db')) dbs.push(path.join(dir, 'db.db'))
    for (const e of entries) if (e.isDirectory()) walk(path.join(dir, e.name))
  }
  walk(GUFI_INDEX)
  dbListCache = { dbs, at: Date.now() }
  return dbs
}

export function vectorsAvailable(): boolean {
  return fs.existsSync(EMBED_MODEL) && gufiAvailable()
}

/**
 * Semantic nearest-neighbor search over the per-directory vec0 tables.
 * One gufi_sqlite3 process: load the embedding model, embed the query once,
 * then ATTACH each directory db, take its top-k chunks, DETACH. Node merges.
 */
export async function searchVector(queryText: string, limit = 10): Promise<SearchHit[]> {
  if (!vectorsAvailable()) return []
  const idx = path.resolve(GUFI_INDEX)
  const script: string[] = [
    `INSERT INTO temp.lembed_models(name, model) SELECT 'minilm384', lembed_model_from_file(${q(EMBED_MODEL)});`,
    `CREATE TABLE temp.qv AS SELECT lembed('minilm384', ${q(queryText.slice(0, 500))}) AS qe;`,
  ]
  for (const db of listIndexDbs()) {
    const relDir = path.relative(idx, path.dirname(db))
    script.push(`ATTACH ${q(db)} AS d;`)
    script.push(
      `SELECT 'VROW' || ${q(DELIM)} || ${q(relDir)} || ${q(DELIM)} || g.fname || ${q(DELIM)} || k.distance || ${q(DELIM)} || ` +
      `replace(replace(substr(g.ctext, 1, 200), char(10), ' '), char(13), ' ') ` +
      `FROM (SELECT cid, distance FROM d.gvec WHERE fp384 MATCH (SELECT qe FROM temp.qv) ORDER BY distance ASC LIMIT 3) k ` +
      `JOIN d.gchunks g ON g.cid = k.cid;`,
    )
    script.push('DETACH d;')
  }
  const { stdout } = await new Promise<{ stdout: string }>((resolve, reject) => {
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: 60_000, maxBuffer: 32 * 1024 * 1024 },
      (err, so) => err && !so ? reject(new Error(`gufi_sqlite3: ${err.message}`)) : resolve({ stdout: so }))
    child.stdin?.write(script.join('\n'))
    child.stdin?.end()
  })
  const hits: SearchHit[] = []
  for (const line of stdout.split('\n')) {
    const parts = line.split(DELIM)
    if (parts[0] !== 'VROW' || parts.length < 5) continue
    const relDir = parts[1]
    const fname = parts[2]
    const dist = Number(parts[3])
    hits.push({
      path: relDir === '.' || relDir === '' ? fname : `${relDir}/${fname}`,
      name: fname,
      size: 0,
      mtime: 0,
      snippet: parts.slice(4).join(' '),
      score: Number.isFinite(dist) ? dist : 999,
      mode: 'vector',
    })
  }
  hits.sort((a, b) => a.score - b.score)
  const seen = new Set<string>()
  return hits.filter(h => !seen.has(h.path) && seen.add(h.path)).slice(0, limit)
}

export interface CorpusStats {
  available: boolean
  files?: number
  dirs?: number
  totalBytes?: number
  byExt?: { ext: string; count: number; bytes: number }[]
}

export async function corpusStats(): Promise<CorpusStats> {
  if (!gufiAvailable()) return { available: false }
  const sumSql = `SELECT totfiles, totsize FROM vrsummary;`
  const rows = await queryPerDir(sumSql)
  let files = 0, totalBytes = 0
  for (const r of rows) { files += Number(r[0]) || 0; totalBytes += Number(r[1]) || 0 }
  const extSql =
    `SELECT lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '' END), ` +
    `count(*), sum(size) FROM vrpentries GROUP BY 1;`
  const extRows = await queryPerDir(extSql)
  const agg = new Map<string, { count: number; bytes: number }>()
  for (const r of extRows) {
    const ext = r[0] || '(none)'
    const cur = agg.get(ext) ?? { count: 0, bytes: 0 }
    cur.count += Number(r[1]) || 0
    cur.bytes += Number(r[2]) || 0
    agg.set(ext, cur)
  }
  const byExt = [...agg.entries()]
    .map(([ext, v]) => ({ ext, ...v }))
    .sort((a, b) => b.count - a.count)
    .slice(0, 25)
  return { available: true, files, dirs: rows.length, totalBytes, byExt }
}
