import { AsyncLocalStorage } from 'node:async_hooks'
import { appendGufiLog, gufiLogOn } from './gufiLog.js'
import { type RemoteResult, remoteRun, shellFor, siteName } from './remoteExec.js'

/**
 * The GUFI commands a request ran, so Search, Query, and the chat's tool
 * blocks can show them. A route wraps its work in traced(); each place that
 * runs GUFI notes its argv with noteGufi(), with how it ended and its error
 * output. Outside a trace a note only goes to the GUFI command log, when
 * that is on (gufiLog.ts), under the source gufiSource() or the note names.
 */

export interface GufiCommand {
  /** gufi_query on one index, one gufi_vt statement over several, or a
   *  gufi_sqlite3 script over the index's databases. */
  engine: 'gufi_query' | 'gufi_vt' | 'gufi_sqlite3'
  /** The cluster or host it ran on, for a library there; absent on this host. */
  site?: string
  /** How a person reaches that site from a terminal: pw ssh, or ssh. */
  shell?: string
  /** As a shell would take it, ready to copy. */
  command: string
  ms?: number
  /** When it started. */
  at?: string
  /** Its exit status; absent when it did not exit (failed says why) or is not known. */
  exit?: number
  /** Why it has no exit status: the signal that ended it, a time limit,
   *  or the ssh session or program that failed. */
  failed?: string
  /** Its error output as kept, without the expected lines (below); absent
   *  where the Studio does not keep it. */
  stderr?: string
  /** Only the end of the error output was kept. */
  stderrCut?: boolean
  /** Lines of its error output that are expected: a statement that reads
   *  the text or vector tables fails, in one line, in each directory
   *  without them. Counted, with one of them as an example. */
  expected?: number
  expectedExample?: string
}

/** Where a command came from, as the log names it. */
export type GufiSource = 'search' | 'query' | 'tool' | 'stats' | 'library' | 'labels' | 'map' | 'history' | 'libraries' | 'other'

interface Trace { source: GufiSource; commands: GufiCommand[] | null }

const store = new AsyncLocalStorage<Trace>()
const MAX = 40
/** The error output a record keeps: its end, after the expected lines are taken out. */
const STDERR_KEEP = 8 * 1024

/** The work fn does, with the GUFI commands it ran. When it fails, the
 *  error carries them (commandsOf), so a route can show why. */
export async function traced<T>(source: GufiSource, fn: () => Promise<T>): Promise<{ result: T; commands: GufiCommand[] }> {
  const commands: GufiCommand[] = []
  try {
    const result = await store.run({ source, commands }, fn)
    return { result, commands }
  } catch (e) {
    // Not enumerable, so nothing that serializes the error repeats them.
    if (e && typeof e === 'object' && commands.length) Object.defineProperty(e, 'gufi', { value: commands, configurable: true })
    throw e
  }
}

/** The commands a traced() call ran before it failed. */
export const commandsOf = (e: unknown): GufiCommand[] | undefined => (e as { gufi?: GufiCommand[] } | null)?.gufi

/** Names the source of the commands fn runs, for the log, unless a trace
 *  already does: a chat tool that reads statistics stays a tool's. */
export function gufiSource<T>(source: GufiSource, fn: () => Promise<T>): Promise<T> {
  return store.getStore() ? fn() : store.run({ source, commands: null }, fn)
}

/** How a GUFI program ended, as its runner saw it. */
export interface GufiRun {
  /** Date.now() when it started. */
  t0?: number
  ms?: number
  /** The exit status; null or absent when a signal or a failure ended it. */
  code?: number | null
  signal?: string | null
  /** Why it did not run or finish, where there is no exit status. */
  error?: string
  /** Its error output as kept; undefined where it is not kept. */
  stderr?: string
  /** The runner kept only the end of the error output. */
  cut?: boolean
}

/** What execFile's callback says about how a program ended. */
export function execRun(err: (Error & { code?: unknown; signal?: unknown; killed?: boolean }) | null, stderr: unknown, t0: number): GufiRun {
  const code = !err ? 0 : typeof err.code === 'number' ? err.code : null
  const signal = err && typeof err.signal === 'string' ? err.signal : null
  const error = err && code === null && !signal ? String(err.message ?? err).split('\n')[0].slice(0, 300) : undefined
  return { t0, code, signal, ...(error ? { error } : {}), stderr: stderr == null ? undefined : String(stderr) }
}

/** A line of error output that is expected: a statement that reads the
 *  Studio's or an external database's text or vector tables, in a
 *  directory without them. --no-print-sql-on-err keeps each to one line. */
const EXPECTED = /no such table: (?:\w+\.)?(?:words|gvec|gchunks|gvecmodel|gvec_rowids|ext_bm25_by_file|ext_vectors_by_file)\b/

export function splitStderr(text: string): { other: string; expected: number; example?: string } {
  const other: string[] = []
  let expected = 0, example: string | undefined
  for (const line of text.split('\n')) {
    if (!line.trim()) continue
    if (EXPECTED.test(line)) { expected++; example ??= line.slice(0, 500) } else other.push(line)
  }
  return { other: other.join('\n'), expected, ...(example ? { example } : {}) }
}

/** Records a command; `site` is the site it ran on (remoteExec.ts), and
 *  `script` SQL on its standard input, shown as a here-document so a long
 *  statement reads as written. `show: false` keeps it out of what a view
 *  shows (a check around the command a view runs) and only logs it;
 *  `source` names it for the log outside a trace. */
export function noteGufi(argv: string[], o: GufiRun & { engine?: GufiCommand['engine']; site?: string; script?: string; show?: boolean; source?: GufiSource } = {}): void {
  const trace = store.getStore()
  const list = o.show === false ? null : trace?.commands
  const logging = gufiLogOn()
  if (!logging && (!list || list.length >= MAX)) return
  const line = argv.map(shellQuote).join(' ')
  const command = o.script !== undefined ? `${line} <<'SQL'\n${o.script}\nSQL` : line
  const ms = o.ms ?? (o.t0 !== undefined ? Date.now() - o.t0 : undefined)
  const rec: GufiCommand = { engine: o.engine ?? 'gufi_query', ...(o.site ? { site: siteName(o.site), ...reach(o.site) } : {}), command, ...(ms !== undefined ? { ms } : {}) }
  if (o.t0 !== undefined) rec.at = new Date(o.t0).toISOString()
  if (typeof o.code === 'number') rec.exit = o.code
  else if (o.signal || o.error) rec.failed = o.signal ? `ended by ${o.signal}` : String(o.error)
  if (o.stderr !== undefined) {
    // A runner that kept only the end may have cut its first line.
    const s = splitStderr(o.cut ? o.stderr.replace(/^[^\n]*\n/, '') : o.stderr)
    rec.stderr = s.other.length > STDERR_KEEP ? s.other.slice(-STDERR_KEEP).replace(/^[^\n]*\n/, '') : s.other
    if (o.cut || s.other.length > STDERR_KEEP) rec.stderrCut = true
    if (s.expected) { rec.expected = s.expected; rec.expectedExample = s.example }
  }
  if (list && list.length < MAX) list.push(rec)
  if (logging) appendGufiLog(rec, trace?.source ?? o.source ?? 'other')
}

/** How to reach a site from a terminal; nothing for one that does not parse. */
function reach(site: string): { shell?: string } {
  try { return { shell: shellFor(site).join(' ') } } catch { return {} }
}

/** A GUFI command on another system (remoteRun), noted as it ended, or
 *  as its session failed. */
export async function remoteGufi(site: string, key: string, argv: string[], o: { timeoutMs?: number; show?: boolean; source?: GufiSource } = {}): Promise<RemoteResult> {
  const t0 = Date.now()
  const note = { site, t0, show: o.show, source: o.source }
  try {
    const r = await remoteRun(site, key, argv, o.timeoutMs)
    noteGufi(argv, { ...note, code: r.code, stderr: r.stderr, cut: r.stderrCut })
    return r
  } catch (e) {
    noteGufi(argv, { ...note, error: String((e as Error).message ?? e) })
    throw e
  }
}

/** One argument as a POSIX shell reads it back unchanged. */
export function shellQuote(a: string): string {
  if (a === '\x1e') return "$'\\x1e'"
  if (/^[A-Za-z0-9_/.,:=@+%-]+$/.test(a)) return a
  return `'${a.replace(/'/g, `'\\''`)}'`
}
