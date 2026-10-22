import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, gufiAvailable, gufiEnv } from './config.js'
import { REMOTE_PREFIX, remoteIndexRoot, remoteQuery, remoteTarget } from './userLibraries.js'
import { noteGufi } from './gufiTrace.js'
import { ONNX_MODEL_ID, embedQueries, onnxModelPresent } from './embedWorker.js'

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
  const t0 = Date.now()
  return new Promise((resolve, reject) => {
    execFile(path.join(GUFI_BIN, bin), args, { timeout: timeoutMs, maxBuffer: 64 * 1024 * 1024, env: gufiEnv() }, (err, stdout, stderr) => {
      if (bin === 'gufi_query') noteGufi([path.join(GUFI_BIN, bin), ...args], { ms: Date.now() - t0 })
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
 * The same expression with every term a bareword, for gufi_vt, which
 * carries a statement's SQL in double quotes and so cannot take fts5's
 * quoted terms. Null when a term is more than letters, digits, and
 * underscores; such a search runs per library instead.
 */
export function ftsExprBare(query: string): string | null {
  const bare = (t: string): string | null => {
    const s = t.replace(/"/g, '')
    if (!/^[\p{L}\p{N}_]+$/u.test(s)) return null
    return /^(AND|OR|NOT|NEAR)$/.test(s) ? s.toLowerCase() : s
  }
  const parts: string[] = []
  for (const g of parseSearchQuery(query).groups) {
    const pos: string[] = []
    for (const i of g.items) { const b = bare(i.text); if (b === null) return null; pos.push(b + (i.prefix ? '*' : '')) }
    if (!pos.length) continue
    let e = pos.length > 1 ? `(${pos.join(' AND ')})` : pos[0]
    for (const n of g.negatives) { const b = bare(n.text); if (b === null) return null; e = `(${e} NOT ${b})` }
    parts.push(e)
  }
  return parts.length ? (parts.length > 1 ? parts.join(' OR ') : parts[0]) : null
}

/** Full-text search's per-directory SQL, for an fts5 expression. */
export function ftsSql(expr: string, limit: number): string {
  return `SELECT rpath(sname, sroll)||'/'||vrpentries.name, vrpentries.name, size, mtime, ` +
    `replace(replace(snippet(words, 2, '[', ']', ' … ', 12), char(10), ' '), char(13), ' ') ` +
    `FROM vrpentries INNER JOIN words ON inode=CAST(tinode AS TEXT) ` +
    `WHERE wordf MATCH ${q(expr)} LIMIT ${limit};`
}

/** Name search's per-directory SQL, or null when the query names nothing. */
export function namesSql(queryText: string, limit: number): string | null {
  const where = nameWhere(parseSearchQuery(queryText))
  return where === '0' ? null : `SELECT rpath(sname, sroll)||'/'||name, name, size, mtime FROM vrpentries WHERE ${where} LIMIT ${limit};`
}

/** A full-text row as a hit, its path already relative to the library. */
export const ftsHit = (r: string[], rel: string): SearchHit => ({ path: rel, name: r[1], size: Number(r[2]) || 0, mtime: Number(r[3]) || 0, snippet: r[4] ?? '', score: 1, mode: 'fts' })
/** A name row as a hit, its path already relative to the library. */
export const nameHit = (r: string[], rel: string): SearchHit => ({ path: rel, name: r[1], size: Number(r[2]) || 0, mtime: Number(r[3]) || 0, snippet: '', score: 0.5, mode: 'name' })

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

export async function queryPerDir(sql: string, opts: { threads?: number; subdir?: string; indexRoot?: string } = {}): Promise<string[][]> {
  const root = opts.indexRoot ?? GUFI_INDEX
  // A user's library on a cluster: the same query, run there as that user.
  if (root.startsWith(REMOTE_PREFIX)) return parseRows(await remoteQuery(root, ['-n', String(Math.min(opts.threads ?? 4, 4)), '-d', DELIM, '-E', sql], opts.subdir), 1)
  const target = opts.subdir ? path.join(root, opts.subdir) : root
  const { stdout } = await run('gufi_query', ['-n', String(opts.threads ?? 8), '-d', DELIM, '-E', sql, target])
  return parseRows(stdout, 1)
}

/** A query in GUFI's aggregate form (-I, -K, -E, -J, -G stages): the merge
 *  happens where the index is, and only the final rows come back. */
async function queryAggregate(stages: string[], opts: { indexRoot?: string; subdir?: string } = {}): Promise<string[][]> {
  const root = opts.indexRoot ?? GUFI_INDEX
  if (root.startsWith(REMOTE_PREFIX)) return parseRows(await remoteQuery(root, ['-n', '4', '-d', DELIM, '-a', '1', ...stages], opts.subdir), 1)
  const { stdout } = await run('gufi_query', ['-n', '8', '-d', DELIM, '-a', '1', ...stages, opts.subdir ? path.join(root, opts.subdir) : root])
  return parseRows(stdout, 1)
}

/** Full-text search over enriched fts5 `words` tables, joined to entries by inode. */
export async function searchFts(queryText: string, limit = 20, indexRoot?: string): Promise<SearchHit[]> {
  const expr = ftsExpr(queryText)
  if (!expr) return []
  const rows = await queryPerDir(ftsSql(expr, limit), { indexRoot })
  return rows.slice(0, limit * 3).map(r => ftsHit(r, toKbRel(r[0], indexRoot)))
}

/** Filename substring search over entries metadata (no enrichment needed). */
export async function searchNames(queryText: string, limit = 20, indexRoot?: string): Promise<SearchHit[]> {
  const sql = namesSql(queryText, limit)
  if (!sql) return []
  const rows = await queryPerDir(sql, { indexRoot })
  return rows.slice(0, limit).map(r => nameHit(r, toKbRel(r[0], indexRoot)))
}

/** gufi_query prints paths rooted at the index tree; strip the index prefix. */
function toKbRel(p: string, indexRoot: string = GUFI_INDEX): string {
  if (indexRoot.startsWith(REMOTE_PREFIX)) {
    const idx = remoteIndexRoot(indexRoot)
    return p.startsWith(idx + '/') ? p.slice(idx.length + 1) : p === idx ? '' : p.replace(/^\.\//, '')
  }
  const idx = path.resolve(indexRoot)
  const abs = path.resolve(p)
  if (abs.startsWith(idx + path.sep)) return abs.slice(idx.length + 1)
  if (abs === idx) return ''
  return p.replace(/^\.\//, '')
}

const EMBED_MODEL = path.join(INDEX_BASE, 'models', 'minilm384.gguf')

// Cached per index root: a second library walks its own tree.
const dbListCache = new Map<string, { dbs: string[]; at: number }>()
interface VectorDb { db: string; model: string }
const vecDbCache = new Map<string, { dbs: VectorDb[]; at: number }>()
export function invalidateDbList(): void { dbListCache.clear(); vecDbCache.clear() }
function listIndexDbs(indexRoot: string = GUFI_INDEX): string[] {
  const hit = dbListCache.get(indexRoot)
  if (hit && Date.now() - hit.at < 60_000) return hit.dbs
  const dbs: string[] = []
  const walk = (dir: string) => {
    let entries: fs.Dirent[]
    try { entries = fs.readdirSync(dir, { withFileTypes: true }) } catch { return }
    if (entries.some(e => e.name === 'db.db')) dbs.push(path.join(dir, 'db.db'))
    for (const e of entries) if (e.isDirectory()) walk(path.join(dir, e.name))
  }
  walk(indexRoot)
  dbListCache.set(indexRoot, { dbs, at: Date.now() })
  return dbs
}

export function vectorsAvailable(): boolean {
  return (fs.existsSync(EMBED_MODEL) || onnxModelPresent()) && gufiAvailable()
}

/** The model a directory's vectors come from when its db names none. */
const LEGACY_MODEL = 'minilm384'

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
function sqliteScript(lines: string[]): Promise<string> {
  return new Promise(resolve => {
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: 60_000, maxBuffer: 32 * 1024 * 1024, env: gufiEnv() },
      (_err, so) => resolve(so ?? ''))
    child.stdin?.write(lines.join('\n'))
    child.stdin?.end()
  })
}

/** Each db with a vector table, and the model its vectors come from (its
 *  gvecmodel table, written by embed.py; minilm384 where there is none). A
 *  table is read only once sqlite_master shows it is there, since a missing
 *  one would stop the whole script. */
async function listVectorDbs(indexRoot: string = GUFI_INDEX): Promise<VectorDb[]> {
  const hit = vecDbCache.get(indexRoot)
  if (hit && Date.now() - hit.at < 60_000) return hit.dbs
  const all = listIndexDbs(indexRoot)
  if (!all.length) return []
  const names = await sqliteScript(all.flatMap(db => [
    `ATTACH ${q(db)} AS c;`,
    `SELECT 'HASVEC' || ${q(DELIM)} || ${q(db)} || ${q(DELIM)} || name FROM c.sqlite_master WHERE type IN ('table','view') AND name IN ('gvec', 'gvecmodel');`,
    'DETACH c;',
  ]))
  const found = new Map<string, Set<string>>()
  for (const l of names.split('\n')) {
    const [tag, db, name] = l.split(DELIM)
    if (tag !== 'HASVEC' || !db || !name) continue
    found.set(db, (found.get(db) ?? new Set()).add(name))
  }
  const withModel = [...found].filter(([, n]) => n.has('gvec') && n.has('gvecmodel')).map(([db]) => db)
  const models = new Map<string, string>()
  if (withModel.length) {
    const out = await sqliteScript(withModel.flatMap(db => [
      `ATTACH ${q(db)} AS c;`,
      `SELECT 'VMODEL' || ${q(DELIM)} || ${q(db)} || ${q(DELIM)} || model FROM c.gvecmodel LIMIT 1;`,
      'DETACH c;',
    ]))
    for (const l of out.split('\n')) {
      const [tag, db, model] = l.split(DELIM)
      if (tag === 'VMODEL' && db && model) models.set(db, model.trim())
    }
  }
  const dbs = [...found].filter(([, n]) => n.has('gvec')).map(([db]) => ({ db, model: models.get(db) ?? LEGACY_MODEL }))
  vecDbCache.set(indexRoot, { dbs, at: Date.now() })
  return dbs
}

/** The models an index's vectors come from, with how many directories each. */
export async function vectorModels(indexRoot: string = GUFI_INDEX): Promise<Record<string, number>> {
  if (indexRoot.startsWith(REMOTE_PREFIX) || !gufiAvailable()) return {}
  const counts: Record<string, number> = {}
  for (const v of await listVectorDbs(indexRoot)) counts[v.model] = (counts[v.model] ?? 0) + 1
  return counts
}

export async function searchVector(queryText: string, limit = 10, indexRoot: string = GUFI_INDEX): Promise<SearchHit[]> {
  if (!vectorsAvailable()) return []
  const idx = path.resolve(indexRoot)
  const vecDbs = await listVectorDbs(indexRoot)
  if (!vecDbs.length) return []
  // The query is embedded once per model the index holds: the GGUF one by
  // sqlite-lembed in the script, the ONNX one by the query worker, whose
  // vector goes in as a blob. Directories whose model is unavailable here
  // are left out rather than searched with the wrong vector.
  const script: string[] = []
  const tableFor = new Map<string, string>()
  if (vecDbs.some(v => v.model === LEGACY_MODEL) && fs.existsSync(EMBED_MODEL)) {
    script.push(
      `INSERT INTO temp.lembed_models(name, model) SELECT 'minilm384', lembed_model_from_file(${q(EMBED_MODEL)});`,
      `CREATE TABLE temp.qv AS SELECT lembed('minilm384', ${q(queryText.slice(0, 500))}) AS qe;`,
    )
    tableFor.set(LEGACY_MODEL, 'temp.qv')
  }
  if (vecDbs.some(v => v.model === ONNX_MODEL_ID)) {
    const vec = await embedQueries([queryText.slice(0, 500)]).then(v => v[0], e => {
      console.warn(`search by meaning: ${(e as Error).message}`)
      return null
    })
    if (vec && vec.length === 384 * 4) {
      script.push(`CREATE TABLE temp.qo AS SELECT X'${vec.toString('hex')}' AS qe;`)
      tableFor.set(ONNX_MODEL_ID, 'temp.qo')
    }
  }
  const searched = vecDbs.filter(v => tableFor.has(v.model))
  if (!searched.length) return []
  for (const { db, model } of searched) {
    const relDir = path.relative(idx, path.dirname(db))
    script.push(`ATTACH ${q(db)} AS d;`)
    script.push(
      `SELECT 'VROW' || ${q(DELIM)} || ${q(relDir)} || ${q(DELIM)} || g.fname || ${q(DELIM)} || k.distance || ${q(DELIM)} || ` +
      `replace(replace(substr(g.ctext, 1, 200), char(10), ' '), char(13), ' ') ` +
      `FROM (SELECT cid, distance FROM d.gvec WHERE fp384 MATCH (SELECT qe FROM ${tableFor.get(model)}) ORDER BY distance ASC LIMIT 3) k ` +
      `JOIN d.gchunks g ON g.cid = k.cid;`,
    )
    script.push('DETACH d;')
  }
  const { stdout } = await new Promise<{ stdout: string }>((resolve, reject) => {
    const child = execFile(path.join(GUFI_BIN, 'gufi_sqlite3'), [], { timeout: 60_000, maxBuffer: 32 * 1024 * 1024, env: gufiEnv() },
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

// A library on a cluster changes only when it is rebuilt, and counting it
// over the remote session takes seconds, so its figures are kept until the
// next build, on disk as well, so a restart does not count it again. The
// owner's key is still checked on every call.
const REMOTE_STATS_FILE = path.join(INDEX_BASE, 'remote-stats.json')
const remoteStats = new Map<string, { builtAt: string; stats: CorpusStats }>(
  (() => { try { return Object.entries(JSON.parse(fs.readFileSync(REMOTE_STATS_FILE, 'utf8'))) } catch { return [] } })() as [string, { builtAt: string; stats: CorpusStats }][])
function keepRemoteStats(): void {
  try { fs.writeFileSync(REMOTE_STATS_FILE, JSON.stringify(Object.fromEntries(remoteStats))) } catch { /* the in-memory copy still serves */ }
}

export async function corpusStats(indexRoot?: string): Promise<CorpusStats> {
  if (indexRoot?.startsWith(REMOTE_PREFIX)) {
    const builtAt = remoteTarget(indexRoot)?.builtAt ?? null
    const hit = remoteStats.get(indexRoot)
    if (builtAt && hit?.builtAt === builtAt) return hit.stats
    const stats = await countCorpus(indexRoot)
    if (builtAt) { remoteStats.set(indexRoot, { builtAt, stats }); keepRemoteStats() }
    return stats
  }
  if (!gufiAvailable()) return { available: false }
  return countCorpus(indexRoot)
}

/**
 * Totals and the file-type breakdown in one pass, aggregated where the index
 * is. Each directory adds its counts by extension and its summary to a
 * per-thread table, and only the merged rows come back: a few dozen, where
 * the two per-directory queries this replaces returned one row per
 * directory, which over the remote session to a large cluster library was
 * most of the wait. Summary rows are keyed with a leading \x01, which no
 * extension has.
 */
async function countCorpus(indexRoot?: string): Promise<CorpusStats> {
  const ext = `lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '' END)`
  const rows = await queryAggregate([
    '-I', 'CREATE TABLE t (k TEXT, n INT64, b INT64);',
    '-K', 'CREATE TABLE agg (k TEXT, n INT64, b INT64);',
    '-E', `INSERT INTO t SELECT ${ext}, count(*), sum(size) FROM vrpentries GROUP BY 1; ` +
      `INSERT INTO t SELECT char(1)||'summary', sum(totfiles), sum(totsize) FROM vrsummary; ` +
      `INSERT INTO t SELECT char(1)||'dirs', count(*), 0 FROM vrsummary;`,
    '-J', 'INSERT INTO agg SELECT * FROM t;',
    '-G', 'SELECT k, sum(n), sum(b) FROM agg GROUP BY k;',
  ], { indexRoot })
  let files = 0, totalBytes = 0, dirs = 0
  const byExt: { ext: string; count: number; bytes: number }[] = []
  for (const [k, n, b] of rows) {
    if (k === '\x01summary') { files = Number(n) || 0; totalBytes = Number(b) || 0 }
    else if (k === '\x01dirs') dirs = Number(n) || 0
    else byExt.push({ ext: k || '(none)', count: Number(n) || 0, bytes: Number(b) || 0 })
  }
  byExt.sort((a, b) => b.count - a.count)
  return { available: true, files, dirs, totalBytes, byExt: byExt.slice(0, 25) }
}

export interface TreeStats {
  files: number
  dirs: number
  bytes: number
  /** The latest modification of anything below, in seconds. */
  mtime: number
  /** By extension, largest first. */
  byExt: { ext: string; count: number; bytes: number }[]
  /** Labels applied below (the Studio's xattr), with how many entries carry each. */
  labels: { label: string; count: number }[]
}

/**
 * One folder of a library, described from its index: files, folders, size,
 * the latest change, the extensions taking the most space, and the labels
 * applied below it. Aggregated where the index is, in two passes so an
 * index without xattr tables still answers the first; `subdir` is a path
 * inside the index, checked by the caller.
 */
export async function treeStats(indexRoot: string, subdir: string, tagXattr = 'user.studio.tags'): Promise<TreeStats> {
  const ext = `lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '' END)`
  const opts = { indexRoot, subdir: subdir || undefined }
  const [rows, tagRows] = await Promise.all([
    queryAggregate([
      '-I', 'CREATE TABLE t (k TEXT, n INT64, b INT64);',
      '-K', 'CREATE TABLE agg (k TEXT, n INT64, b INT64);',
      '-E', `INSERT INTO t SELECT ${ext}, count(*), sum(size) FROM vrpentries GROUP BY 1; ` +
        `INSERT INTO t SELECT char(1)||'summary', sum(totfiles), sum(totsize) FROM vrsummary; ` +
        `INSERT INTO t SELECT char(1)||'dirs', count(*), 0 FROM vrsummary; ` +
        `INSERT INTO t SELECT char(1)||'mtime', max(max(maxmtime), max(mtime)), 0 FROM vrsummary;`,
      '-J', 'INSERT INTO agg SELECT * FROM t;',
      '-G', `SELECT k, CASE WHEN k = char(1)||'mtime' THEN max(n) ELSE sum(n) END, sum(b) FROM agg GROUP BY k;`,
    ], opts),
    queryAggregate([
      '-I', 'CREATE TABLE t (k TEXT, n INT64);',
      '-K', 'CREATE TABLE agg (k TEXT, n INT64);',
      '-E', `INSERT INTO t SELECT CAST(x.value AS TEXT), count(*) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode WHERE CAST(x.name AS TEXT) = ${q(tagXattr)} GROUP BY 1; ` +
        `INSERT INTO t SELECT CAST(x.value AS TEXT), count(*) FROM vrsummary s JOIN xattrs_pwd x ON s.inode = x.inode WHERE CAST(x.name AS TEXT) = ${q(tagXattr)} GROUP BY 1;`,
      '-J', 'INSERT INTO agg SELECT * FROM t;',
      '-G', 'SELECT k, sum(n) FROM agg GROUP BY k;',
    ], opts).catch(() => [] as string[][]),
  ])
  let files = 0, bytes = 0, dirs = 0, mtime = 0
  const byExt: TreeStats['byExt'] = []
  for (const [k, n, b] of rows) {
    if (k === '\x01summary') { files = Number(n) || 0; bytes = Number(b) || 0 }
    else if (k === '\x01dirs') dirs = Number(n) || 0
    else if (k === '\x01mtime') mtime = Number(n) || 0
    else byExt.push({ ext: k || '(none)', count: Number(n) || 0, bytes: Number(b) || 0 })
  }
  byExt.sort((a, b) => b.bytes - a.bytes || b.count - a.count)
  const labels = new Map<string, number>()
  for (const [v, n] of tagRows) for (const t of String(v ?? '').split(',').map(x => x.trim()).filter(Boolean)) labels.set(t, (labels.get(t) ?? 0) + (Number(n) || 0))
  return { files, dirs, bytes, mtime, byExt: byExt.slice(0, 8), labels: [...labels].map(([label, count]) => ({ label, count })).sort((a, b) => b.count - a.count).slice(0, 12) }
}

/** One file's entry in its folder's index: size, modified time, and its
 *  labels (the Studio's xattr), or null when the index has no such entry. */
export async function entryStats(indexRoot: string, dir: string, name: string, tagXattr = 'user.studio.tags'): Promise<{ size: number; mtime: number; labels: string[] } | null> {
  const sql = `SELECT v.size, v.mtime, (SELECT CAST(x.value AS TEXT) FROM xattrs_pwd x WHERE x.inode = v.inode AND CAST(x.name AS TEXT) = ${q(tagXattr)}) FROM vrpentries v WHERE v.name = ${q(name)} LIMIT 1;`
  const args = ['-n', '1', '-d', DELIM, '--min-level', '0', '--max-level', '0', '-E', sql]
  const out = indexRoot.startsWith(REMOTE_PREFIX)
    ? await remoteQuery(indexRoot, args, dir || undefined)
    : (await run('gufi_query', [...args, dir ? path.join(indexRoot, dir) : indexRoot])).stdout
  const row = parseRows(out, 2)[0]
  if (!row) return null
  return { size: Number(row[0]) || 0, mtime: Number(row[1]) || 0, labels: String(row[2] ?? '').split(',').map(t => t.trim()).filter(Boolean) }
}
