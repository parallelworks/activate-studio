import { execFile, spawn } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { GUFI_BIN, GUFI_INDEX, INDEX_BASE, gufiAvailable, gufiEnv } from './config.js'
import { REMOTE_PREFIX, remoteIndexRoot, remoteQuery, remoteQueryMany, remoteTarget } from './userLibraries.js'
import { noteGufi } from './gufiTrace.js'
import { ONNX_MODEL_ID, embedQueries, isOnnxModel, onnxModelPresent } from './embedWorker.js'

const DELIM = '\x1e' // record separator; never appears in KB text

/** How a hit was found: its words (fts), its name, the path of a folder it
 *  is in, or its meaning (vector). */
export type HitMode = 'fts' | 'name' | 'path' | 'vector'

export interface SearchHit {
  path: string
  name: string
  size: number
  mtime: number
  snippet: string
  score: number
  /** The most exact way it was found: words, then name, then folder path,
   *  then meaning. */
  mode: HitMode
  /** Every way it was found, most exact first (searchScope's fused hits). */
  kinds?: HitMode[]
  tags?: string[]
  /** Order within its kind, lower first: a word match's bm25, a name
   *  match's 0 (the query is the name), 1 (the name starts with it) or 2. */
  rank?: number
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

const FTS_COLUMNS = `rpath(sname, sroll)||'/'||vrpentries.name, vrpentries.name, size, mtime, ` +
  `replace(replace(snippet(words, 2, '[', ']', ' … ', 12), char(10), ' '), char(13), ' ')`
const FTS_FROM = (expr: string) => `FROM vrpentries INNER JOIN words ON inode=CAST(tinode AS TEXT) WHERE wordf MATCH ${q(expr)}`

/** Full-text search's per-directory SQL, for an fts5 expression: the
 *  directory's best matches by bm25. */
export function ftsSql(expr: string, limit: number): string {
  return `SELECT ${FTS_COLUMNS} ${FTS_FROM(expr)} ORDER BY bm25(words) LIMIT ${limit};`
}

/** Name search's per-directory SQL, or null when the query names nothing:
 *  the directory's best name matches by nameRank. */
export function namesSql(queryText: string, limit: number): string | null {
  const parsed = parseSearchQuery(queryText)
  const where = nameWhere(parsed)
  return where === '0' ? null : `SELECT rpath(sname, sroll)||'/'||name, name, size, mtime FROM vrpentries WHERE ${where} ORDER BY ${nameRank(parsed)}, name LIMIT ${limit};`
}

/**
 * How well a name matches, as SQL, lower first: 0 when the name is the
 * query (its first group's words as typed, with or without an extension),
 * 1 when it starts with the query's first word, 2 when it only contains a
 * term. A name match has no frequency to rank by, so this and then the path
 * order them.
 */
export function nameRank(parsed: ParsedQuery): string {
  const first = parsed.groups.find(g => g.items.length)?.items ?? []
  const clean = (t: string) => t.replace(/[%_]/g, '')
  const whole = clean(first.map(i => i.text).join(' '))
  const lead = clean(first[0]?.text ?? '')
  if (!whole) return '2'
  return `CASE WHEN name LIKE ${q(whole)} OR name LIKE ${q(whole + '.%')} THEN 0 WHEN name LIKE ${q(lead + '%')} THEN 1 ELSE 2 END`
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
  return likeClause('name', term)
}

/** The same clause over any text column or expression; a folder path also
 *  starts a word after a slash. */
function likeClause(col: string, term: string): string {
  const t = term.replace(/[%_]/g, '')
  if (!t) return '0'
  if (t.length > 3) return `${col} LIKE ${q('%' + t + '%')}`
  const starts = ['', ' ', '-', '_', '.', '(', '[', ',', ...(col === 'name' ? [] : ['/'])]
  return '(' + starts.map(sep => `${col} LIKE ${q((sep ? '%' + sep : '') + t + '%')}`).join(' OR ') + ')'
}

function whereOver(col: string, parsed: ParsedQuery): string {
  const groups = parsed.groups
    .map(g => g.items.map(i => likeClause(col, i.text)).filter(c => c !== '0'))
    .filter(cs => cs.length)
    .map(cs => cs.length > 1 ? `(${cs.join(' AND ')})` : cs[0])
  return groups.length > 1 ? groups.join(' OR ') : (groups[0] ?? '0')
}

export function nameWhere(parsed: ParsedQuery): string {
  return whereOver('name', parsed)
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

/** Blend full-text, filename, and vector hits without letting one starve
 *  the rest. A file found more than one way keeps its place and is labelled
 *  by the most exact of them: its words, then its name, then meaning, so a
 *  name that matches is never shown as a match by meaning. */
export function blendHits(fts: SearchHit[], names: SearchHit[], vec: SearchHit[], limit: number): SearchHit[] {
  const vecTop = vec.slice(0, Math.min(6, Math.max(2, Math.floor(limit / 3))))
  const ftsTop = fts.slice(0, Math.max(4, limit - vecTop.length))
  const exact = new Map<string, SearchHit['mode']>()
  for (const h of [...vec, ...names, ...fts]) exact.set(h.path, h.mode)
  const seen = new Set<string>()
  return [...ftsTop, ...vecTop, ...names, ...fts, ...vec]
    .filter(h => !seen.has(h.path) && seen.add(h.path))
    .slice(0, limit)
    .map(h => (exact.get(h.path) === h.mode ? h : { ...h, mode: exact.get(h.path)! }))
}

/** The threads a gufi_query here runs with: one per processor core, up to
 *  16. A remote one gets its host's (remoteQuery). */
const LOCAL_THREADS = Math.min(Math.max(os.availableParallelism?.() ?? os.cpus().length, 1), 16)

export async function queryPerDir(sql: string, opts: { threads?: number; subdir?: string; indexRoot?: string } = {}): Promise<string[][]> {
  const root = opts.indexRoot ?? GUFI_INDEX
  // A user's library on a cluster: the same query, run there as that user.
  if (root.startsWith(REMOTE_PREFIX)) return parseRows(await remoteQuery(root, ['-n', String(opts.threads ?? LOCAL_THREADS), '-d', DELIM, '-E', sql], opts.subdir), 1)
  const target = opts.subdir ? path.join(root, opts.subdir) : root
  const { stdout } = await run('gufi_query', ['-n', String(opts.threads ?? LOCAL_THREADS), '-d', DELIM, '-E', sql, target])
  return parseRows(stdout, 1)
}

/** A query in GUFI's aggregate form (-I, -K, -E, -J, -G stages): the merge
 *  happens where the index is, and only the final rows come back. */
async function queryAggregate(stages: string[], opts: { indexRoot?: string; subdir?: string } = {}): Promise<string[][]> {
  const root = opts.indexRoot ?? GUFI_INDEX
  if (root.startsWith(REMOTE_PREFIX)) return parseRows(await remoteQuery(root, ['-n', String(LOCAL_THREADS), '-d', DELIM, '-a', '1', ...stages], opts.subdir), 1)
  const { stdout } = await run('gufi_query', ['-n', String(LOCAL_THREADS), '-d', DELIM, '-a', '1', ...stages, opts.subdir ? path.join(root, opts.subdir) : root])
  return parseRows(stdout, 1)
}

/**
 * Full-text and name matches of one library in one walk of its index: one
 * gufi_query whose per-directory step runs both lookups while that
 * directory's tables are open, where a separate query for each walked the
 * tree twice (and over ssh, ran two remote commands).
 *
 * Each kind keeps its best rows: a word match by bm25 over the directory's
 * fts5 `words` table, a name match by nameRank, then path. Each thread
 * holds at most its best `limit` of a kind, trimmed after every directory,
 * and the merge orders them, so the rows that come back are the best ranked
 * of the whole tree, not the first found, and only those come back: a
 * common word in a tree of a few hundred thousand directories otherwise
 * sent every directory's matches (13 MB for one name search, as measured)
 * to keep 10 of them, over ssh for a remote library. bm25 is computed
 * within each directory's own table, so scores from two directories
 * compare roughly.
 *
 * ftsLimit is per directory, and three times it overall, as before; 0 skips
 * the words, as for an index without them, whose missing table would fail
 * the step in every directory. nameLimit is per directory and overall.
 */
export async function searchWordsAndNames(queryText: string, opts: { ftsLimit: number; nameLimit: number }, indexRoot?: string): Promise<{ fts: SearchHit[]; names: SearchHit[] }> {
  const parsed = parseSearchQuery(queryText)
  const expr = opts.ftsLimit > 0 ? ftsExprFor(parsed) : ''
  const where = opts.nameLimit > 0 ? nameWhere(parsed) : '0'
  const words = !!expr, names = where !== '0'
  if (!words && !names) return { fts: [], names: [] }
  const ftsTotal = opts.ftsLimit * 3, nameTotal = opts.nameLimit
  const ftsCols = 'r REAL, p TEXT, n TEXT, s INT64, m INT64, x TEXT', nameCols = 'r REAL, p TEXT, n TEXT, s INT64, m INT64'
  // The best rows of a table, past which a thread's copy is trimmed.
  const trim = (t: string, keep: number) => `DELETE FROM ${t} WHERE rowid IN (SELECT rowid FROM ${t} ORDER BY r, p LIMIT -1 OFFSET ${keep});`
  const I: string[] = [], K: string[] = [], E: string[] = [], J: string[] = [], G: string[] = []
  // Names first: a directory whose words step fails still adds its names.
  if (names) {
    I.push(`CREATE TABLE tn (${nameCols});`); K.push(`CREATE TABLE an (${nameCols});`)
    E.push(`INSERT INTO tn SELECT ${nameRank(parsed)}, rpath(sname, sroll)||'/'||name, name, size, mtime FROM vrpentries WHERE ${where} ORDER BY 1, 2 LIMIT ${opts.nameLimit};`, trim('tn', nameTotal))
    J.push('INSERT INTO an SELECT * FROM tn;')
    G.push(`SELECT 'n', r, p, n, s, m, '' FROM (SELECT * FROM an ORDER BY r, p LIMIT ${nameTotal})`)
  }
  if (words) {
    I.push(`CREATE TABLE tf (${ftsCols});`); K.push(`CREATE TABLE af (${ftsCols});`)
    E.push(`INSERT INTO tf SELECT bm25(words), ${FTS_COLUMNS} ${FTS_FROM(expr)} ORDER BY 1, 2 LIMIT ${opts.ftsLimit};`, trim('tf', ftsTotal))
    J.push('INSERT INTO af SELECT * FROM tf;')
    G.push(`SELECT 'f', r, p, n, s, m, x FROM (SELECT * FROM af ORDER BY r, p LIMIT ${ftsTotal})`)
  }
  const rows = await queryAggregate(['-I', I.join(' '), '-K', K.join(' '), '-E', E.join(' '), '-J', J.join(' '), '-G', G.join(' UNION ALL ') + ';'], { indexRoot })
  const fts: SearchHit[] = [], nm: SearchHit[] = []
  for (const r of rows) {
    if (r.length < 7) continue
    const rank = Number(r[1])
    const row = r.slice(2)
    if (r[0] === 'f') fts.push({ ...ftsHit(row, toKbRel(row[0], indexRoot)), rank })
    else if (r[0] === 'n') nm.push({ ...nameHit(row, toKbRel(row[0], indexRoot)), rank })
  }
  const byRank = (a: SearchHit, b: SearchHit) => (a.rank ?? 0) - (b.rank ?? 0) || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0)
  return { fts: fts.sort(byRank).slice(0, ftsTotal), names: nm.sort(byRank).slice(0, nameTotal) }
}

/** Full-text search over enriched fts5 `words` tables, joined to entries by
 *  inode: up to three times `limit` matches, best by bm25 first. */
export async function searchFts(queryText: string, limit = 20, indexRoot?: string): Promise<SearchHit[]> {
  return (await searchWordsAndNames(queryText, { ftsLimit: limit, nameLimit: 0 }, indexRoot)).fts
}

/** Filename substring search over entries metadata (no enrichment needed),
 *  best match first (nameRank). */
export async function searchNames(queryText: string, limit = 20, indexRoot?: string): Promise<SearchHit[]> {
  return (await searchWordsAndNames(queryText, { ftsLimit: 0, nameLimit: limit }, indexRoot)).names
}

// ---- one walk for names, folder paths, words and meaning ----

/** What one walk looks for, and how many of each kind it keeps. 0 leaves a
 *  kind out. Per directory, a kind keeps at most its own limit, words at
 *  most `wordsPerDir` and meaning `vectorsPerDir`; overall, each its total.
 *  `studioText` and `studioVectors` say whether the index carries the
 *  Studio's own words and vector tables; `external`, a basename pattern for
 *  external databases in the shared text and vector schema, which are read
 *  wherever they are (null: not looked for). */
export interface WalkLimits {
  names: number; paths: number; words: number; wordsPerDir: number; vectors: number; vectorsPerDir: number
  studioText: boolean; studioVectors: boolean; external: string | null
}

/** The basename pattern for external databases in the shared text and
 *  vector schema (ext_bm25_by_file, ext_vectors_by_file): any file in an
 *  index directory ending in .db, which gufi_query matches against every
 *  file there but db.db. STUDIO_EXTERNAL_DB_PATTERN changes it; empty turns
 *  it off. */
export const EXTERNAL_DB_PATTERN = process.env.STUDIO_EXTERNAL_DB_PATTERN ?? '\\.db$'

/** A query embedding for the vectors of the models a LIKE pattern names
 *  (gvecmodel), as hex for a blob literal. */
export interface QueryVector { pattern: string; hex: string }

/** One row of a walk: its kind, its score within the kind (lower first:
 *  nameRank, folder depth, bm25 or distance), and the file. */
export interface WalkRow { kind: HitMode; r: number; path: string; name: string; size: number; mtime: number; snippet: string; root: string }

const CODE_KIND: Record<string, HitMode> = { n: 'name', p: 'path', f: 'fts', v: 'vector' }
/** Reciprocal rank fusion's constant: a hit's score is the sum over the
 *  kinds that found it of 1 / (60 + its rank in that kind). */
export const RRF_K = 60

/** A directory's path below the root it was reached from, in SQL: rpath()
 *  prints the index path given on the command line, so each root's own
 *  length is cut off. */
function relDirSql(roots: string[]): string {
  const rp = 'rpath(sname, sroll)'
  // SQLite's substr counts characters; so does a string's code points.
  const len = (r: string) => [...r].length
  if (roots.length === 1) return `substr(${rp}, ${len(roots[0]) + 2})`
  return `CASE ${roots.map(r => `WHEN substr(${rp}, 1, ${len(r)}) = ${q(r)} THEN substr(${rp}, ${len(r) + 2})`).join(' ')} ELSE ${rp} END`
}

/**
 * The one-pass pattern GUFI's authors recommend, for search: one gufi_query
 * walk whose per-directory step (-E) adds every kind of match while that
 * directory's tables are open, each into the same per-thread table (-I),
 * trimmed to its best as it goes, merged per thread (-J), and ranked once
 * at the end (-G), where reciprocal rank fusion combines the kinds into
 * one order. A tree walked once for all of them is the win, and the larger
 * the more directories it has.
 *
 * The statements run in a fixed order, names and folder paths first, then
 * words, then meaning: in a directory without a words table (or vectors),
 * the statement that needs it fails and those after it are skipped there,
 * while the rows before it stand. That lets one query serve an index whose
 * directories have text in some places and none in others, without writing
 * empty tables into every directory (20 KB for an empty fts5 table and 40 KB
 * for empty vector tables, per directory, as measured).
 *
 * The query's embedding goes in -I as a blob literal, one copy per thread,
 * so this needs nothing beyond what every GUFI here has (--global-db would
 * do the same on newer GUFI).
 *
 * External databases in the shared schema are read in the same walk, with
 * --external-copy: in each directory, every file matching lim.external is
 * attached and its best text (ext_bm25_by_file, by bm25) and, for a model
 * whose query vector is here, its nearest chunks (ext_vectors_by_file, by
 * cosine distance on the scale of vec0's distance for unit vectors) join
 * the same table, matched to the directory's entries by inode and mtime, so
 * a file changed since its text was extracted is not matched. That runs
 * before -E, which trims what it added first.
 *
 * Null when the query names nothing.
 */
export function onePassStages(queryText: string, lim: WalkLimits, roots: string[], vectors: QueryVector[] = []): string[] | null {
  const parsed = parseSearchQuery(queryText)
  const nWhere = lim.names > 0 ? nameWhere(parsed) : '0'
  const relDir = relDirSql(roots)
  const pWhere = lim.paths > 0 ? whereOver(relDir, parsed) : '0'
  const expr = lim.words > 0 ? ftsExprFor(parsed) : ''
  const vecs = lim.vectors > 0 ? vectors : []
  const studioWords = lim.studioText && !!expr
  const studioVecs = lim.studioVectors && vecs.length > 0
  const external = lim.external && (expr || vecs.length) ? lim.external : null
  if (nWhere === '0' && pWhere === '0' && !studioWords && !studioVecs && !external) return null
  const cols = 'k TEXT, r REAL, p TEXT, n TEXT, s INT64, m INT64, x TEXT'
  const rp = `rpath(sname, sroll)||'/'||name`
  // What a thread keeps of each kind; the final step keeps vectors' own limit.
  const held = `CASE k WHEN 'n' THEN ${lim.names} WHEN 'p' THEN ${lim.paths} WHEN 'f' THEN ${lim.words} ELSE ${lim.vectors * 3} END`
  // gufi_query compiles every statement again in every directory, which on a
  // tree of many directories costs more than the rows do, so there are few:
  // one trim, first, keeping each kind's best (it also bounds what an
  // external database and the previous directory added, whichever of their
  // steps failed), then one insert per kind of table read.
  const E: string[] = [`DELETE FROM t WHERE rowid IN (SELECT rowid FROM (SELECT rowid, k, row_number() OVER (PARTITION BY k ORDER BY r, p) AS rn FROM t) WHERE rn > ${held});`]
  const names = nWhere !== '0'
    ? `SELECT * FROM (SELECT 'n', ${nameRank(parsed)}, ${rp}, name, size, mtime, '' FROM vrpentries WHERE ${nWhere} ORDER BY 2, 3 LIMIT ${lim.names})` : ''
  // Files below a folder whose path matches; a name that matches is a name
  // match already. Nearer the matching folder first.
  const paths = pWhere !== '0'
    ? `SELECT * FROM (SELECT 'p', length(${relDir}), ${rp}, name, size, mtime, '' FROM vrpentries WHERE (${pWhere})${nWhere !== '0' ? ` AND NOT (${nWhere})` : ''} ORDER BY 2, 3 LIMIT ${lim.paths})` : ''
  if (names || paths) E.push(`INSERT INTO t ${[names, paths].filter(Boolean).join(' UNION ALL ')};`)
  if (studioWords) {
    E.push(`INSERT INTO t SELECT 'f', bm25(words), ${FTS_COLUMNS} ${FTS_FROM(expr)} ORDER BY 2, 3 LIMIT ${lim.wordsPerDir};`)
  }
  const ext: string[] = []
  if (external && expr) {
    ext.push(
      `INSERT INTO t SELECT 'f', bm25(ext_bm25_by_file), rpath(e.sname, e.sroll)||'/'||e.name, e.name, e.size, e.mtime, ` +
      `replace(replace(snippet(ext_bm25_by_file, -1, '[', ']', ' … ', 12), char(10), ' '), char(13), ' ') ` +
      `FROM ext_bm25_by_file JOIN vrpentries e ON CAST(binode AS INT64) = e.inode AND CAST(bmtime AS INT64) = e.mtime ` +
      `WHERE ext_bm25_by_file MATCH ${q(expr)} ORDER BY 2, 3 LIMIT ${lim.wordsPerDir};`)
  }
  if (external && vecs.length) {
    ext.push(
      `INSERT INTO t SELECT 'v', sqrt(2.0 * vec_distance_cosine(substr(x.embedding, 1, 1536), qv.qe)), rpath(e.sname, e.sroll)||'/'||e.name, e.name, e.size, e.mtime, ` +
      `replace(replace(substr(x.chunk_content, 1, 200), char(10), ' '), char(13), ' ') ` +
      `FROM ext_vectors_by_file x JOIN qv ON x.vector_model_name LIKE qv.pat ` +
      `JOIN vrpentries e ON CAST(x.vinode AS INT64) = e.inode AND CAST(x.vmtime AS INT64) = e.mtime ` +
      `WHERE length(x.embedding) >= 1536 ORDER BY 2, 3 LIMIT ${lim.vectorsPerDir};`)
  }
  if (studioVecs) {
    // The directory's own model picks the query vector; a directory whose
    // model has none here fails this step alone, as the last one.
    E.push(
      `INSERT INTO t SELECT 'v', k.distance, rpath(e.sname, e.sroll)||'/'||e.name, e.name, e.size, e.mtime, ` +
      `replace(replace(substr(g.ctext, 1, 200), char(10), ' '), char(13), ' ') ` +
      `FROM (SELECT cid, distance FROM gvec WHERE fp384 MATCH (SELECT qe FROM qv WHERE (SELECT model FROM gvecmodel LIMIT 1) LIKE pat LIMIT 1) ORDER BY distance LIMIT ${lim.vectorsPerDir}) k ` +
      `JOIN gchunks g ON g.cid = k.cid JOIN vrpentries e ON e.inode = g.tinode;`,
    )
  }
  const cap = `CASE k WHEN 'n' THEN ${lim.names} WHEN 'p' THEN ${lim.paths} WHEN 'f' THEN ${lim.words} ELSE ${lim.vectors} END`
  const I = [`CREATE TABLE t (${cols});`]
  if (vecs.length) I.push('CREATE TABLE qv (pat TEXT, qe BLOB);', `INSERT INTO qv VALUES ${vecs.map(v => `(${q(v.pattern)}, X'${v.hex}')`).join(', ')};`)
  if (!E.length) E.push('SELECT 1 WHERE 0;')
  return [
    '-I', I.join(' '),
    '-K', `CREATE TABLE agg (${cols});`,
    ...(ext.length ? ['--external-copy', external!, ext.join(' ')] : []),
    '-E', E.join(' '),
    '-J', 'INSERT INTO agg SELECT * FROM t;',
    // Each file once per kind (its best row: several chunks of one file can
    // match by meaning), ranked within its kind, kept to the kind's limit,
    // and fused: the order a search across these roots alone would show.
    '-G', `WITH best AS (SELECT k, min(r) AS r, p, n, s, m, x FROM agg GROUP BY k, p), ` +
      `ranked AS (SELECT *, row_number() OVER (PARTITION BY k ORDER BY r, p) AS rn FROM best), ` +
      `kept AS (SELECT * FROM ranked WHERE rn <= ${cap}), ` +
      `fused AS (SELECT p AS fp, sum(1.0 / (${RRF_K}.0 + rn)) AS score FROM kept GROUP BY p) ` +
      `SELECT k, r, p, n, s, m, x FROM kept JOIN fused ON p = fp ORDER BY score DESC, p, k;`,
  ]
}

/** The query embeddings a walk can use: the ONNX model's, for every
 *  truncation of it, when the model is here. A directory embedded with
 *  another model is left to searchVector. */
export async function queryVectors(queryText: string): Promise<QueryVector[]> {
  if (!onnxModelPresent()) return []
  const vec = await embedQueries([queryText.slice(0, 500)]).then(v => v[0], e => {
    console.warn(`search by meaning: ${(e as Error).message}`)
    return null
  })
  if (!vec || vec.length !== 384 * 4) return []
  const hex = vec.toString('hex')
  // The ONNX model is all-MiniLM-L6-v2 with mean pooling, quantized, so its
  // query vector also searches vectors an external database names by that
  // model (by cosine distance, which does not depend on their scale).
  return [{ pattern: ONNX_MODEL_ID, hex }, { pattern: `${ONNX_MODEL_ID}-t%`, hex }, { pattern: '%all-MiniLM-L6-v2', hex }]
}

/** gufi_query, keeping all of its output but only the end of its error
 *  output: a walk prints a line for every directory where a step failed (a
 *  directory without a words table, say), which on a large tree is more
 *  than a buffer should hold. */
function runQuery(args: string[], timeoutMs = 60_000): Promise<{ stdout: string; stderr: string }> {
  const bin = path.join(GUFI_BIN, 'gufi_query')
  const t0 = Date.now()
  return new Promise((resolve, reject) => {
    const child = spawn(bin, args, { env: gufiEnv(), stdio: ['ignore', 'pipe', 'pipe'] })
    const out: Buffer[] = []
    let err = ''
    child.stdout.on('data', (d: Buffer) => out.push(d))
    child.stderr.on('data', (d: Buffer) => { err = (err + d.toString()).slice(-16_384) })
    const timer = setTimeout(() => child.kill('SIGKILL'), timeoutMs)
    child.on('error', e => { clearTimeout(timer); reject(e) })
    child.on('close', (code, signal) => {
      clearTimeout(timer)
      noteGufi([bin, ...args], { ms: Date.now() - t0 })
      const stdout = Buffer.concat(out).toString()
      // An unknown option prints the usage on stdout, so it is told by its error.
      if ((code !== 0 || signal) && (!stdout || unknownOption(err))) return reject(new Error(`gufi_query failed: ${(err.trim().split('\n').pop() || signal || `exit ${code}`)}`))
      resolve({ stdout, stderr: err })
    })
  })
}

/** Whether a failure is gufi_query not knowing an option: an older GUFI. */
const unknownOption = (e: unknown) => /unrecognized option|invalid option|unknown option/i.test(String((e as Error)?.message ?? e))

/**
 * One walk over one or more index roots on one machine: local roots in one
 * gufi_query, or the roots of one remote site in one remote command. The
 * rows come back tagged with the root each path is under.
 */
export async function walkSearch(queryText: string, lim: WalkLimits, indexRoots: string[], vectors: QueryVector[] = []): Promise<WalkRow[]> {
  if (!indexRoots.length) return []
  const remote = indexRoots[0].startsWith(REMOTE_PREFIX)
  const printed = remote ? indexRoots.map(r => remoteIndexRoot(r)) : indexRoots.map(r => path.resolve(r))
  const stages = onePassStages(queryText, lim, printed, vectors)
  if (!stages) return []
  const exec = async (current: boolean) => {
    // An older GUFI is asked again without what it may not know:
    // --no-print-sql-on-err (which keeps a failed step's error to one short
    // line) and --external-copy.
    let s = stages
    if (!current) {
      const i = s.indexOf('--external-copy')
      if (i >= 0) s = [...s.slice(0, i), ...s.slice(i + 3)]
    }
    const args = ['-n', String(LOCAL_THREADS), '-d', DELIM, '-a', '1', ...(current ? ['--no-print-sql-on-err'] : []), ...s]
    return remote ? remoteQueryMany(indexRoots, args) : (await runQuery([...args, ...printed])).stdout
  }
  const stdout = await exec(true).catch(e => (unknownOption(e) ? exec(false) : Promise.reject(e)))
  // Longest root first, so a root inside another claims its own paths.
  const order = printed.map((p, i) => ({ p, root: indexRoots[i] })).sort((a, b) => b.p.length - a.p.length)
  const rows: WalkRow[] = []
  for (const r of parseRows(stdout, 7)) {
    const kind = CODE_KIND[r[0]]
    if (!kind) continue
    const at = order.find(o => r[2] === o.p || r[2].startsWith(o.p + '/'))
    if (!at) continue
    rows.push({ kind, r: Number(r[1]), path: r[2] === at.p ? '' : r[2].slice(at.p.length + 1), name: r[3], size: Number(r[4]) || 0, mtime: Number(r[5]) || 0, snippet: r[6] ?? '', root: at.root })
  }
  return rows
}

const KIND_ORDER: HitMode[] = ['fts', 'name', 'path', 'vector']

/**
 * Reciprocal rank fusion over the rows of one or more walks, each row's
 * path already unique per library: every kind is ranked on its own (by its
 * score, then path, so the order is the same on every run), kept to its
 * limit, and a file's score is the sum over the kinds that found it of
 * 1 / (60 + its rank). Ties go to the more exact kind, then the path. A hit
 * is labelled by the most exact way it was found, and carries all of them.
 */
export function fuseRows(rows: WalkRow[], caps: Partial<Record<HitMode, number>>, limit: number): SearchHit[] {
  const best = new Map<string, WalkRow>()
  for (const row of rows) {
    const key = `${row.kind}\0${row.path}`
    const was = best.get(key)
    if (!was || row.r < was.r) best.set(key, row)
  }
  const byKind = new Map<HitMode, WalkRow[]>()
  for (const row of best.values()) byKind.set(row.kind, [...(byKind.get(row.kind) ?? []), row])
  const files = new Map<string, { score: number; kinds: HitMode[]; rows: Partial<Record<HitMode, WalkRow>> }>()
  for (const kind of KIND_ORDER) {
    const list = (byKind.get(kind) ?? []).sort((a, b) => a.r - b.r || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0))
    list.slice(0, caps[kind] ?? list.length).forEach((row, i) => {
      const f = files.get(row.path) ?? { score: 0, kinds: [], rows: {} }
      f.score += 1 / (RRF_K + i + 1)
      f.kinds.push(kind)
      f.rows[kind] = row
      files.set(row.path, f)
    })
  }
  const prio = (m: HitMode) => KIND_ORDER.indexOf(m)
  return [...files].map(([p, f]) => {
    const mode = f.kinds[0]
    const main = f.rows[mode]!
    const sized = KIND_ORDER.map(k => f.rows[k]).find(r => r && (r.size || r.mtime)) ?? main
    const snippet = f.rows.fts?.snippet || f.rows.vector?.snippet || ''
    return { path: p, name: main.name, size: sized.size, mtime: sized.mtime, snippet, score: f.score, mode, kinds: f.kinds, rank: main.r }
  }).sort((a, b) => b.score - a.score || prio(a.mode) - prio(b.mode) || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0)).slice(0, limit)
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
export function invalidateDbList(): void { dbListCache.clear(); vecDbCache.clear(); countCache.clear() }

/**
 * Counts over a whole library or a folder of it (the details card, Stats),
 * kept until the index changes: a library on another system until its
 * recorded index time does, or an hour; one here until an index job ends
 * (invalidateDbList), or ten minutes. On a large index each count is a walk
 * of the tree (20 s for a million and a half files over ssh with 16
 * threads, as measured), so a card opened twice should not walk it twice.
 * Requests for the same count while it runs share it.
 */
const countCache = new Map<string, { stamp: string; at: number; value: Promise<unknown> }>()
const COUNT_MS = 10 * 60_000, REMOTE_COUNT_MS = 60 * 60_000
function keptCount(key: string, indexRoot: string | undefined): { id: string; stamp: string; value: Promise<unknown> | null } {
  const root = indexRoot ?? GUFI_INDEX
  const remote = root.startsWith(REMOTE_PREFIX)
  const stamp = remote ? (remoteTarget(root)?.builtAt ?? '') : ''
  const id = `${root}\0${key}`
  const hit = countCache.get(id)
  const fresh = hit && hit.stamp === stamp && Date.now() - hit.at < (remote && stamp ? REMOTE_COUNT_MS : COUNT_MS)
  return { id, stamp, value: fresh ? hit.value : null }
}
function cachedCount<T>(key: string, indexRoot: string | undefined, compute: () => Promise<T>): Promise<T> {
  const k = keptCount(key, indexRoot)
  if (k.value) return k.value as Promise<T>
  const value = compute()
  countCache.set(k.id, { stamp: k.stamp, at: Date.now(), value })
  if (countCache.size > 500) countCache.delete(countCache.keys().next().value!)
  value.catch(() => { if (countCache.get(k.id)?.value === value) countCache.delete(k.id) })
  return value
}
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
  const onnxModels = [...new Set(vecDbs.map(v => v.model).filter(isOnnxModel))]
  if (onnxModels.length) {
    const vec = await embedQueries([queryText.slice(0, 500)]).then(v => v[0], e => {
      console.warn(`search by meaning: ${(e as Error).message}`)
      return null
    })
    if (vec && vec.length === 384 * 4) {
      // A query is short, so one vector serves every truncation length.
      script.push(`CREATE TABLE temp.qo AS SELECT X'${vec.toString('hex')}' AS qe;`)
      for (const m of onnxModels) tableFor.set(m, 'temp.qo')
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
  return cachedCount('corpus', indexRoot, () => countCorpus(indexRoot))
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
export function treeStats(indexRoot: string, subdir: string, tagXattr = 'user.studio.tags'): Promise<TreeStats> {
  return cachedCount(`tree\0${subdir}\0${tagXattr}`, indexRoot, () => treeStatsNow(indexRoot, subdir, tagXattr))
}

/** treeStats when it has been made or is being made, else null. */
export function treeStatsKept(indexRoot: string, subdir: string, tagXattr = 'user.studio.tags'): Promise<TreeStats> | null {
  return keptCount(`tree\0${subdir}\0${tagXattr}`, indexRoot).value as Promise<TreeStats> | null
}

async function treeStatsNow(indexRoot: string, subdir: string, tagXattr: string): Promise<TreeStats> {
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
