import { execFile } from 'node:child_process'
import { randomBytes, timingSafeEqual } from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import type { FastifyInstance } from 'fastify'
import { GUFI_BIN, HOST, PORT, gufiEnv } from './config.js'
import type { Library } from './libraries.js'
import { isDirect, remoteRun, shellFor } from './remoteExec.js'
import { gufiTool, REMOTE_PREFIX, remoteTarget } from './userLibraries.js'
import { noteGufi } from './gufiTrace.js'

/**
 * A query across several libraries as one gufi_vt statement, GUFI's own
 * way of querying many indexes at once: a gufi_vt virtual table per
 * library, unioned, with the overall order and limit in the outer SELECT.
 *
 * A library on this host is a plain gufi_vt location. A library on a
 * user's cluster is a remote one, whose `remote_cmd` is the Studio's
 * wrapper (gufiVtRemote.ts): gufi_vt hands it the gufi_query command line,
 * the wrapper splits it without a shell and posts it to this server over
 * loopback, and the server runs it on the cluster through the user's
 * existing session (remoteExec.ts), so no remote shell parses the SQL and
 * the user's key never appears in a process's arguments. Each location is
 * checked first and one that cannot answer is left out and reported, since
 * gufi_vt fails the whole statement when a location exits nonzero with no
 * rows. gufi_vt runs the locations one after another.
 */

/** A query's stages, as gufi_query takes them. */
export interface Plan {
  stages: { I?: string; E: string; K?: string; J?: string; G?: string }
  columns: string[]
  subtree?: string
  /** Raw SQL: per-directory rows with no aggregation and no known order. */
  raw?: boolean
}
export interface VtOrder { col: number; desc: boolean; limit: number }
export interface VtResult { rows: { id: string; row: string[] }[]; failed: { library: string; error: string }[]; elapsedMs: number }

const WRAPPER = fileURLToPath(new URL('./gufiVtRemote.js', import.meta.url))
const SECRET = randomBytes(32).toString('hex')
const TOKEN_TTL_MS = 10 * 60_000
const THREADS = 4
/** A remote location gufi_vt asks through the wrapper: the session, and
 *  the index roots (one per library; several when libraries on one host
 *  are stacked) it may run gufi_query on. */
interface Location { site: string; key: string; gufiBin: string; index: string; indexes?: string[]; label: string; error?: string; ran?: { argv: string[]; ms: number }; at: number }
const tokens = new Map<string, Location>()

/**
 * A path as a gufi_vt argument, quoted, or null when it cannot be one.
 * gufi_vt strips a value's outer quotes and does not undo doubled ones
 * (set_refstr in gufi_vt.c), and runs gufi_query and remote_cmd with
 * execvp, never through a shell (popen_argv.c). So spaces and other
 * characters need no escaping: a path is single-quoted, or double-quoted
 * when it holds a single quote. One that holds both quotes, starts or ends
 * with one, or holds a NUL or a line break cannot be passed. The macOS
 * app's own paths ("ACTIVATE Studio.app", "Application Support") have
 * spaces in them.
 */
export function vtPath(p: string): string | null {
  if (!p || /[\0\n\r]/.test(p) || /^['"]|['"]$/.test(p)) return null
  if (!p.includes("'")) return `'${p}'`
  if (!p.includes('"')) return `"${p}"`
  return null
}

/** A remote location's index as gufi_vt also writes it, unquoted, at the
 *  end of the one command line it hands remote_cmd; the wrapper splits that
 *  line on spaces and checkRemoteArgs joins the index back. So it may have
 *  single spaces, but no other whitespace, and no word of it may start with
 *  a quote (the split would read a quoted word). */
export const vtRemoteIndex = (p: string): boolean =>
  vtPath(p) !== null && !/\s\s|[\t\v\f]|\s$/.test(p) && p.split(' ').every(w => w !== '' && !/^['"]/.test(w))

/** A SQL stage as gufi_vt can take it, or null. gufi_vt's stages are
 *  double-quoted values (E="...") read verbatim, so the stage cannot hold a
 *  double quote. Inside a single-quoted literal one is written as
 *  '||char(34)||' instead, which gives the same string: an FTS5 phrase
 *  query, MATCH '"a" AND "b"', becomes MATCH (''||char(34)||'a'||...).
 *  A double quote anywhere else (a quoted identifier, a comment) cannot be
 *  rewritten, and the stage stays with the per-library merge. */
export function vtStage(sql: string): string | null {
  let out = ''
  for (let i = 0; i < sql.length; i++) {
    const c = sql[i]
    if (c === '"') return null
    if (c === '-' && sql[i + 1] === '-') return sql.indexOf('"', i) < 0 ? out + sql.slice(i) : null
    if (c === '/' && sql[i + 1] === '*') {
      const end = sql.indexOf('*/', i + 2)
      const comment = end < 0 ? sql.slice(i) : sql.slice(i, end + 2)
      if (comment.includes('"')) return null
      out += comment
      i += comment.length - 1
      continue
    }
    if (c !== "'") { out += c; continue }
    // A single-quoted literal, '' being a quote inside it.
    let j = i + 1
    let body = ''
    for (; j < sql.length; j++) {
      if (sql[j] === "'") {
        if (sql[j + 1] === "'") { body += "''"; j++; continue }
        break
      }
      body += sql[j]
    }
    if (j >= sql.length) return null // unterminated: leave it to gufi_query's own error
    out += body.includes('"') ? `('${body.split('"').join("'||char(34)||'")}')` : `'${body}'`
    i = j
  }
  return out
}

const fitsVt = (plan: Plan) => Object.values(plan.stages).every(s => !s || vtStage(s) !== null)

/** Where the wrapper reaches this server. */
function selfUrl(): string {
  const h = HOST === '0.0.0.0' || HOST === '::' || !HOST ? '127.0.0.1' : HOST.includes(':') ? `[${HOST}]` : HOST
  return `http://${h}:${PORT}/internal/gufi-vt`
}

let available: Promise<boolean> | null = null
let forced: boolean | null = null
/** Whether this host's gufi_sqlite3 has gufi_vt with remote locations. */
export function gufiVtAvailable(): Promise<boolean> {
  if (forced !== null) return Promise.resolve(forced)
  available ??= new Promise(resolve => {
    const sqlite = path.join(GUFI_BIN, 'gufi_sqlite3')
    // macOS has no /bin/true, only /usr/bin/true; most Linux systems have both.
    const nothing = ['/usr/bin/true', '/bin/true'].find(p => fs.existsSync(p))
    if (!fs.existsSync(sqlite) || !nothing || !vtPath(vtCommand.node) || !vtPath(vtCommand.wrapper)) return resolve(false)
    // A remote location whose command prints nothing and exits 0 answers
    // with no rows; a build without remote_cmd refuses the table.
    const probe = `CREATE VIRTUAL TABLE temp.p USING gufi_vt(index='/', E="SELECT name FROM vrpentries;", remote_cmd='${nothing}'); SELECT count(*) FROM temp.p;`
    execFile(sqlite, [':memory:', probe], { timeout: 15_000, env: gufiEnv() },
      (err, out) => resolve(!err && String(out).trim() === '0'))
  })
  return available
}

/** Whether a library's plan can run as a gufi_vt location. */
export function vtEligible(plan: Plan): boolean { return fitsVt(plan) }

/** Whether a library's paths can be passed to gufi_vt; where one cannot,
 *  Query merges the libraries itself instead of leaving that one out. */
export function vtLocatable(lib: Library): boolean {
  if (!lib.indexRoot.startsWith(REMOTE_PREFIX)) return vtPath(path.resolve(lib.indexRoot)) !== null
  const t = remoteTarget(lib.indexRoot)
  return !t || (vtRemoteIndex(t.indexRoot) && (t.gufiBin === '' || vtPath(t.gufiBin) !== null))
}

/** One gufi_vt table: its index, or `indexes` when libraries on one host
 *  share it (gufi_vt takes index= more than once, and hands a remote
 *  command every root, in order, at the end of its line). */
interface VtLocation { id: string; index: string; indexes?: string[]; plan: Plan; remote?: { token: string; site: string; gufiBin: string } }

/** The program gufi_vt runs for a remote location: this node and the
 *  wrapper beside this module. */
let vtCommand = { node: process.execPath, wrapper: WRAPPER }

/** A remote location's command as the Studio runs it: the wrapper. */
const viaStudio = (r: NonNullable<VtLocation['remote']>) => [vtCommand.node, vtCommand.wrapper, r.token]
/** The same location as a person runs it from a terminal: gufi_vt's usual
 *  remote form, over ssh or pw ssh, with the host's GUFI on the PATH there;
 *  where the login runs only gufi_query (or GUFI is on its PATH), with
 *  nothing before gufi_query, since such a login refuses env. */
export const viaShell = (r: NonNullable<VtLocation['remote']>) =>
  [...shellFor(r.site), ...(r.gufiBin && !isDirect(r.site) ? ['env', `PATH=${r.gufiBin}:$PATH`] : [])]

/** The script for a set of locations: one table each, then the union. */
export function vtScript(locs: VtLocation[], order: VtOrder | null, remoteCmd = viaStudio): string {
  const val = (k: string, v?: string) => {
    if (!v) return ''
    const stage = vtStage(v)
    if (stage === null) throw new Error(`its ${k} stage has a double quote outside a string, which gufi_vt cannot take`)
    return `, ${k}="${stage}"`
  }
  const q = (v: string) => {
    const quoted = vtPath(v)
    if (!quoted) throw new Error(`${v} cannot be passed to gufi_vt`)
    return quoted
  }
  const tables = locs.map((l, i) => {
    const s = l.plan.stages
    const [cmd, ...rargs] = l.remote ? remoteCmd(l.remote) : []
    const remote = cmd ? `, remote_cmd=${q(cmd)}${rargs.map(a => `, remote_arg=${q(a)}`).join('')}` : ''
    const index = (l.indexes ?? [l.index]).map(ix => `index=${q(ix)}`).join(', ')
    return `CREATE VIRTUAL TABLE temp.loc_${i} USING gufi_vt(${index}, threads=${THREADS}${val('I', s.I)}${val('E', s.E)}${val('K', s.K)}${val('J', s.J)}${val('G', s.G)}${remote});`
  })
  const union = locs.map((l, i) => `SELECT '${l.id}', * FROM temp.loc_${i}`).join(' UNION ALL ')
  const tail = order ? ` ORDER BY ${order.col + 2} ${order.desc ? 'DESC' : 'ASC'} LIMIT ${order.limit}` : ''
  return `${tables.join('\n')}\n${union}${tail};`
}

/**
 * Runs each library's plan as one gufi_vt statement. Rows come back tagged
 * with their library's id, paths relative to that library.
 */
export async function runAcross(items: { lib: Library; plan: Plan }[], order: VtOrder | null): Promise<VtResult> {
  const t0 = Date.now()
  const failed: VtResult['failed'] = []
  type Member = { id: string; label: string; root: string; index: string }
  const locs: (VtLocation & { label: string; members: Member[] })[] = []
  // Libraries on another system, grouped: those on one host, reached the
  // same way and asked the same plan, share a location, so the host gets
  // one gufi_query (and one check) for all of them.
  const groups = new Map<string, { site: string; key: string; gufiBin: string; plan: Plan; members: Member[] }>()
  await Promise.all(items.map(async ({ lib: l, plan }) => {
    const sub = (plan.subtree ?? '').replace(/^\/+|\/+$/g, '')
    try {
      if (sub.split('/').includes('..')) throw new Error('scope escapes the index')
      if (l.indexRoot.startsWith(REMOTE_PREFIX)) {
        const t = remoteTarget(l.indexRoot)
        if (!t) throw new Error('its owner has no key to act with')
        const index = sub ? path.posix.join(t.indexRoot, sub) : t.indexRoot
        if (!vtRemoteIndex(index) || (t.gufiBin !== '' && !vtPath(t.gufiBin))) throw new Error('its index path cannot be passed to gufi_vt')
        // Raw SQL has no known shape to map rows back by, so it is not
        // stacked; STUDIO_STACK_REMOTE=0 stacks nothing.
        const k = plan.raw || process.env.STUDIO_STACK_REMOTE === '0' ? `lib:${l.id}` : JSON.stringify([t.site, t.key.slice(-16), t.gufiBin, plan.stages, plan.columns])
        const g = groups.get(k) ?? { site: t.site, key: t.key, gufiBin: t.gufiBin, plan, members: [] }
        g.members.push({ id: l.id, label: l.label, root: t.indexRoot, index })
        groups.set(k, g)
      } else {
        const root = path.resolve(l.indexRoot)
        const index = sub ? path.resolve(root, sub) : root
        if (index !== root && !index.startsWith(root + '/')) throw new Error('scope escapes the index')
        if (!vtPath(index)) throw new Error('its index path cannot be passed to gufi_vt')
        if (!fs.existsSync(path.join(index, 'db.db'))) throw new Error('index not built yet')
        locs.push({ id: l.id, label: l.label, index, plan, members: [{ id: l.id, label: l.label, root, index }] })
      }
    } catch (e) { failed.push({ library: l.label, error: String((e as Error).message ?? e).slice(0, 300) }) }
  }))
  let stacks = 0
  await Promise.all([...groups.values()].map(async g => {
    const answering = await checkRemote(g, failed)
    if (!answering.length) return
    const token = randomBytes(16).toString('hex')
    const indexes = answering.map(m => m.index)
    const label = answering.map(m => m.label).join(', ')
    tokens.set(token, { site: g.site, key: g.key, gufiBin: g.gufiBin, index: indexes[0], indexes, label, at: Date.now() })
    const id = answering.length === 1 ? answering[0].id : `stack${stacks++}`
    locs.push({ id, label, index: indexes[0], ...(indexes.length > 1 ? { indexes } : {}), plan: g.plan, members: answering, remote: { token, site: g.site, gufiBin: g.gufiBin } })
  }))
  // The union follows the libraries' order, not the order the checks ended in.
  const at = (id: string) => items.findIndex(i => i.lib.id === id)
  locs.sort((a, b) => at(a.members[0].id) - at(b.members[0].id))
  if (!locs.length) return { rows: [], failed, elapsedMs: Date.now() - t0 }
  const script = vtScript(locs, order)
  const sqlite = path.join(GUFI_BIN, 'gufi_sqlite3')
  const t1 = Date.now()
  let out: string
  try {
    out = await new Promise<string>((resolve, reject) => {
      execFile(sqlite, ['-d', 'x', ':memory:', script], {
        timeout: 300_000, maxBuffer: 64 * 1024 * 1024,
        env: gufiEnv({ STUDIO_GUFI_VT_URL: selfUrl(), STUDIO_GUFI_VT_SECRET: SECRET }),
      }, (err, so, se) => (err ? reject(new Error(String(se || err.message).trim().split('\n').slice(-2).join(' ').slice(0, 300))) : resolve(String(so))))
    })
  } finally {
    // Shown as a person would run it: gufi_vt's own remote form for the
    // statement, then what each cluster ran.
    let shown = script
    try { shown = vtScript(locs, order, viaShell) } catch { /* a site word gufi_vt cannot quote: show what ran */ }
    noteGufi([sqlite, '-d', 'x', ':memory:'], { engine: 'gufi_vt', ms: Date.now() - t1, script: shown })
    for (const l of locs) {
      if (!l.remote) continue
      const loc = tokens.get(l.remote.token)
      if (loc?.ran) noteGufi(loc.ran.argv, { site: loc.site, ms: loc.ran.ms })
      if (loc?.error) for (const m of l.members) failed.push({ library: m.label, error: loc.error })
      tokens.delete(l.remote.token)
    }
  }
  const byId = new Map(locs.map(l => [l.id, l]))
  const rows = out.split('\n').filter(Boolean).map(line => {
    const cells = line.split('\x1e')
    const loc = byId.get(cells[0])
    const row = cells.slice(1)
    if (!loc) return { id: cells[0], row }
    // A row goes to the library whose root its path is under; a stacked
    // location's grouped totals have no path, and are summed across
    // libraries afterwards anyway (query.ts), so they go to its first.
    const roots = [...loc.members].sort((a, b) => b.root.length - a.root.length)
    const member = loc.members.length === 1 ? loc.members[0]
      : roots.find(m => row.some(c => c === m.root || c.startsWith(m.root + '/'))) ?? loc.members[0]
    const prefix = member.root + '/'
    return { id: member.id, row: row.map(c => (c.startsWith(prefix) ? c.slice(prefix.length) : c)) }
  })
  return { rows, failed, elapsedMs: Date.now() - t0 }
}

/** The members of a remote group whose index answers, checked with
 *  gufi_query alone (a host may run nothing else): each root's top
 *  directory, one row each, in one remote command for the whole group.
 *  gufi_query exits 0 on a missing or unreadable index and says why on
 *  stderr; when fewer rows come back than roots were asked, each is asked
 *  on its own to find which, and those are reported. */
async function checkRemote<M extends { id: string; label: string; index: string }>(g: { site: string; key: string; gufiBin: string; members: M[] }, failed: VtResult['failed']): Promise<M[]> {
  const ask = (indexes: string[]) => remoteRun(g.site, g.key, [gufiTool(g.gufiBin), '-n', '1', '-d', '|', '--min-level', '0', '--max-level', '0', '-S', 'SELECT 1;', ...indexes], 60_000)
  const lines = (stdout: string) => stdout.split('\n').filter(l => l.trim()).length
  const why = (r: { code: number; stderr: string }) => r.stderr.trim().split('\n').filter(Boolean).pop() || `the index did not answer (exit ${r.code})`
  try {
    if (g.members.length > 1) {
      const r = await ask(g.members.map(m => m.index))
      if (lines(r.stdout) >= g.members.length) return g.members
    }
    const each = await Promise.all(g.members.map(async m => {
      try {
        const r = await ask([m.index])
        if (lines(r.stdout)) return m
        failed.push({ library: m.label, error: why(r).slice(0, 300) })
      } catch (e) { failed.push({ library: m.label, error: String((e as Error).message ?? e).slice(0, 300) }) }
      return null
    }))
    return each.filter((m): m is NonNullable<typeof m> => !!m)
  } catch (e) {
    for (const m of g.members) failed.push({ library: m.label, error: String((e as Error).message ?? e).slice(0, 300) })
    return []
  }
}

/** The gufi_query arguments gufi_vt sends, and nothing else: the stage SQL,
 *  its fixed flags, at most THREADS threads, then the location's index or
 *  indexes. gufi_vt writes them unquoted, so an index with spaces arrives as
 *  several words; they are joined back and must spell the token's indexes,
 *  in order, which go to gufi_query as one argument each. */
export function checkRemoteArgs(args: unknown, index: string | string[]): string[] | null {
  const roots = Array.isArray(index) ? index : [index]
  if (!Array.isArray(args) || args.some(a => typeof a !== 'string') || args[0] !== 'gufi_query') return null
  const a = args as string[]
  let i = 1
  for (; i < a.length; i++) {
    if (a[i] === '--print-tlv' || a[i] === '-x') continue
    if (a[i] === '--threads') { const n = Number(a[++i]); if (!(n >= 1 && n <= THREADS)) return null; continue }
    if (/^-[IEKJG]$/.test(a[i]) && i + 1 < a.length) { i++; continue }
    break
  }
  if (i >= a.length || a.slice(i).join(' ') !== roots.join(' ')) return null
  return [...a.slice(1, i), ...roots]
}

/** The wrapper's way in. The per-process secret admits it; the path is
 *  outside /api, so sign-in does not apply. */
export async function gufiVtRoutes(app: FastifyInstance): Promise<void> {
  app.post('/internal/gufi-vt', async (req, reply) => {
    const given = Buffer.from(String(req.headers['x-studio-secret'] ?? ''))
    const want = Buffer.from(SECRET)
    if (given.length !== want.length || !timingSafeEqual(given, want)) return reply.status(403).send('forbidden')
    const { token, args } = (req.body ?? {}) as { token?: string; args?: unknown }
    const loc = typeof token === 'string' ? tokens.get(token) : undefined
    if (!loc || Date.now() - loc.at > TOKEN_TTL_MS) return reply.status(404).send('unknown location')
    const rest = checkRemoteArgs(args, loc.indexes ?? loc.index)
    if (!rest) return reply.status(400).send('not a gufi_query on this location')
    const argv = [gufiTool(loc.gufiBin), ...rest]
    const t0 = Date.now()
    const r = await remoteRun(loc.site, loc.key, argv, 300_000)
      .catch((e: Error) => ({ code: -1, raw: Buffer.alloc(0), stderr: e.message }))
    loc.ran = { argv, ms: Date.now() - t0 }
    if (r.code !== 0 && !r.raw.length) loc.error = (r.stderr.trim().split('\n').pop() || `exit ${r.code}`).slice(0, 300)
    reply.header('Content-Type', 'application/octet-stream')
    if (r.stderr) reply.header('X-Stderr', Buffer.from(r.stderr.slice(-2000)).toString('base64'))
    return reply.send(r.raw)
  })
}

/** Tests: the secret the wrapper presents, a location to answer for, and a reset. */
export function gufiVtSecretForTests(): string { return SECRET }
export function addLocationForTests(token: string, loc: Omit<Location, 'at'>): void { tokens.set(token, { ...loc, at: Date.now() }) }
export function resetGufiVtForTests(): void { available = null; forced = null; tokens.clear() }
/** Tests: turn gufi_vt off (or on) regardless of the probe; null restores it. */
export function forceGufiVtForTests(v: boolean | null): void { forced = v }
/** For tests: the node and wrapper paths gufi_vt runs for a remote location,
 *  as the macOS app has them (with spaces); null restores this process's. */
export function setVtCommandForTests(node: string | null, wrapper?: string): void {
  vtCommand = node ? { node, wrapper: wrapper ?? WRAPPER } : { node: process.execPath, wrapper: WRAPPER }
  available = null
}
