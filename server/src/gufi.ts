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

/**
 * The search grammar, the subset of search-engine habits people arrive
 * with: "quoted words" match as an exact phrase, OR between alternatives,
 * -word or NOT word excludes, word* matches by prefix, and everything else
 * is a whole word that must be present (AND). The fts5 index uses the
 * default unicode61 tokenizer, so a bare term matches whole tokens only,
 * case-insensitively; partial matches happen only when asked for with *.
 */
export interface QueryItem { text: string; phrase: boolean; prefix: boolean }
export interface QueryGroup { items: QueryItem[]; negatives: QueryItem[] }
export interface ParsedQuery {
  /** OR-separated groups; within a group every item must match. */
  groups: QueryGroup[]
  hasOperators: boolean
  /** The whole query was one quoted phrase. */
  exact: boolean
}

export function parseSearchQuery(raw: string): ParsedQuery {
  const s = raw.trim()
  type Tok = { text: string; phrase: boolean; neg: boolean; prefix: boolean; op?: 'OR' | 'AND' }
  const toks: Tok[] = []
  let hasOperators = false
  let pendingNot = false
  const re = /(-|NOT\s+)?"([^"]*)"|(-)?(\S+)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(s))) {
    if (m[2] !== undefined) {
      const t = m[2].replace(/"/g, '').trim()
      if (t) { toks.push({ text: t, phrase: true, neg: !!m[1] || pendingNot, prefix: false }); hasOperators = true }
      pendingNot = false
      continue
    }
    let w = m[4]
    const neg = !!m[3]
    if (w === 'OR' || w === 'AND') { toks.push({ text: w, phrase: false, neg: false, prefix: false, op: w }); hasOperators = true; continue }
    if (w === 'NOT') { pendingNot = true; hasOperators = true; continue }
    const prefix = w.endsWith('*')
    w = w.replace(/["'*]/g, '')
    if (!w) continue
    if (neg || pendingNot || prefix) hasOperators = true
    toks.push({ text: w, phrase: false, neg: neg || pendingNot, prefix })
    pendingNot = false
  }
  const groups: QueryGroup[] = [{ items: [], negatives: [] }]
  for (const t of toks) {
    if (t.op === 'OR') { groups.push({ items: [], negatives: [] }); continue }
    if (t.op === 'AND') continue
    const item = { text: t.text, phrase: t.phrase, prefix: t.prefix }
    if (t.neg) groups[groups.length - 1].negatives.push(item)
    else groups[groups.length - 1].items.push(item)
  }
  for (const g of groups) {
    // Stopwords are dropped from bare terms only when something else is
    // left to search for; a phrase keeps every word it was given.
    const kept = g.items.filter(i => i.phrase || i.prefix || !STOPWORDS.has(i.text.toLowerCase()))
    g.items = (kept.length ? kept : g.items).slice(0, 8)
  }
  const exact = toks.length === 1 && toks[0].phrase && !toks[0].neg
  return { groups: groups.filter(g => g.items.length || g.negatives.length), hasOperators, exact }
}

const ftsQuote = (t: string) => `"${t.replace(/"/g, '')}"`

/** The fts5 MATCH expression for a parsed query, or '' when nothing positive remains. */
export function ftsExprFor(parsed: ParsedQuery): string {
  const parts: string[] = []
  for (const g of parsed.groups) {
    const pos = g.items.map(i => ftsQuote(i.text) + (i.prefix ? '*' : ''))
    if (!pos.length) continue
    let e = pos.length > 1 ? `(${pos.join(' AND ')})` : pos[0]
    for (const n of g.negatives) e = `(${e} NOT ${ftsQuote(n.text)})`
    parts.push(e)
  }
  return parts.length > 1 ? parts.join(' OR ') : (parts[0] ?? '')
}

function ftsExpr(query: string): string {
  return ftsExprFor(parseSearchQuery(query))
}

/**
 * A filename clause for one term. Filenames are matched by substring,
 * which is what people expect of a name search, except that a short
 * term is required to start a word: "tin" must not surface routine.md.
 */
export function nameClause(term: string): string {
  const t = term.replace(/[%_]/g, '')
  if (!t) return '0'
  if (t.length > 3) return `name LIKE ${q('%' + t + '%')}`
  const starts = ['', ' ', '-', '_', '.', '(', '[', ',']
  return '(' + starts.map(sep => `name LIKE ${q((sep ? '%' + sep : '') + t + '%')}`).join(' OR ') + ')'
}

export function nameWhere(parsed: ParsedQuery): string {
  const groups = parsed.groups
    .map(g => g.items.map(i => nameClause(i.text)).filter(c => c !== '0'))
    .filter(cs => cs.length)
    .map(cs => cs.length > 1 ? `(${cs.join(' AND ')})` : cs[0])
  return groups.length > 1 ? groups.join(' OR ') : (groups[0] ?? '0')
}

/**
 * Whether semantic (vector) hits belong in the blend. They do for a
 * question or a topic; they are noise for an identifier, an acronym, a
 * quoted phrase, or a query with operators, where the person is asking
 * for the literal thing and a "similar" document reads as a wrong answer.
 */
export function wantsSemantic(raw: string): boolean {
  const p = parseSearchQuery(raw)
  if (p.hasOperators || p.exact) return false
  const words = p.groups.flatMap(g => g.items).map(i => i.text)
  if (words.length === 1) {
    const w = words[0]
    if (w.length <= 3) return false
    if (/^[A-Z0-9][A-Z0-9-]{0,7}$/.test(w)) return false
    if (/\d/.test(w)) return false
  }
  return words.length > 0
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
  const where = nameWhere(parseSearchQuery(queryText))
  if (where === '0') return []
  const sql =
    `SELECT rpath(sname, sroll)||'/'||name, name, size, mtime ` +
    `FROM vrpentries WHERE ${where} LIMIT ${limit};`
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
let vecDbCache: { dbs: string[]; at: number } | null = null
export function invalidateDbList(): void { dbListCache = null; vecDbCache = null }
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
/**
 * Index dbs that actually carry a vector table. A directory with nothing
 * embeddable in it (an empty folder, images only, a corpus mid-ingest) has
 * no gvec, and gufi_sqlite3 abandons the whole script at the first missing
 * table, so attaching every db lets one empty directory silently disable
 * semantic search for the entire corpus. Reading sqlite_master is always
 * valid, so this pass is safe to run over everything.
 */
async function listVectorDbs(): Promise<string[]> {
  if (vecDbCache && Date.now() - vecDbCache.at < 60_000) return vecDbCache.dbs
  const all = listIndexDbs()
  if (!all.length) return []
  const script = all.flatMap(db => [
    `ATTACH ${q(db)} AS c;`,
    `SELECT 'HASVEC' || ${q(DELIM)} || ${q(db)} FROM c.sqlite_master WHERE type IN ('table','view') AND name = 'gvec';`,
    'DETACH c;',
  ])
  const { stdout } = await new Promise<{ stdout: string }>(resolve => {
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: 60_000, maxBuffer: 32 * 1024 * 1024 },
      (_err, so) => resolve({ stdout: so ?? '' }))
    child.stdin?.write(script.join('\n'))
    child.stdin?.end()
  })
  const dbs = stdout.split('\n')
    .filter(l => l.startsWith('HASVEC'))
    .map(l => l.split(DELIM)[1])
    .filter(Boolean)
  vecDbCache = { dbs, at: Date.now() }
  return dbs
}

export async function searchVector(queryText: string, limit = 10): Promise<SearchHit[]> {
  if (!vectorsAvailable()) return []
  const idx = path.resolve(GUFI_INDEX)
  const script: string[] = [
    `INSERT INTO temp.lembed_models(name, model) SELECT 'minilm384', lembed_model_from_file(${q(EMBED_MODEL)});`,
    `CREATE TABLE temp.qv AS SELECT lembed('minilm384', ${q(queryText.slice(0, 500))}) AS qe;`,
  ]
  const vecDbs = await listVectorDbs()
  if (!vecDbs.length) return []
  for (const db of vecDbs) {
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
