import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { GATEWAY_BASE, INDEX_BASE, PW_CLI } from './config.js'
import { KbError } from './kb.js'

/**
 * Commands on a remote host over one long-lived session, so a library tree
 * there expands in about a quarter of a second rather than paying the ssh
 * setup (about 5 seconds over `pw ssh`) on every click.
 *
 * Two transports carry the session. A cluster on the platform is reached
 * with `pw ssh <site>`, acting with the viewer's own platform key, as that
 * user. A host written `ssh://[user@]host[:port]` (sshSite) is reached with
 * plain ssh, using the keys, agent, and ~/.ssh/config of the account the
 * Studio runs as; the key argument is unused there.
 *
 * The remote end is a small loop in bash. Each request is one line: the
 * command's argv, every argument base64-encoded, so no SQL quote, dollar
 * sign, or space ever reaches a shell unprotected (the remote shell parsed
 * unescaped SQL when gufi_vt was spiked). The loop decodes the arguments
 * into an array and runs it without a shell, then answers with one line:
 * the exit status and the base64 of stdout and of stderr's tail. One
 * request runs at a time per session, so a host gets a few sessions
 * (POOL_MAX): a search's name, text and meaning queries, or a details
 * card's two counts, run side by side, and on a large index take about as
 * long as the slowest of them. Over plain ssh the sessions share one
 * connection (ControlMaster), so only the first pays for the login.
 *
 * Some hosts run nothing but gufi_query: no bash, a restricted shell, or
 * a forced command that only lets gufi_query through. There the loop
 * cannot start, so the site is marked direct (setDirect) and each command
 * is its own ssh call instead, over the same shared connection, written
 * as one command line with each argument quoted for a POSIX shell (sshd
 * hands it to the login shell, or a forced command reads it from
 * SSH_ORIGINAL_COMMAND). Only GUFI's query tools are sent to such a host;
 * anything else is refused here with a reason (NO_SHELL).
 */

const IDLE_MS = 10 * 60_000
/** Sessions to one host at most; STUDIO_REMOTE_SESSIONS changes it. */
const POOL_MAX = Math.min(Math.max(Number(process.env.STUDIO_REMOTE_SESSIONS) || 3, 1), 8)
const STDERR_TAIL = 4000

// Read a line of space-separated arguments, each "x" then its base64 (the
// "x" keeps an empty argument from vanishing in the split), run them as
// argv with stdin closed, and print "<status> o<stdout b64> e<stderr b64>".
// It runs on Linux and on macOS or BSD: base64 there has no -w (its output
// is unwrapped here with tr), and older macOS decodes with -D only.
// macOS also drops DYLD_* variables on the way into any system program,
// so a GUFI tool there, which finds its libraries through
// DYLD_LIBRARY_PATH, is started through env(1) with <prefix>/lib set, the
// prefix being the one its own bin directory is in.
const LOOP = [
  'set -f',
  'd=-d; [ "$(printf eA== | base64 -d 2>/dev/null)" = x ] || d=-D',
  'os=$(uname -s 2>/dev/null)',
  'while IFS= read -r line; do',
  '  args=(); for a in $line; do args+=("$(printf %s "${a#x}" | base64 $d)"); done',
  '  if [ "$os" = Darwin ]; then',
  '    case "${args[0]}" in /*/*/*) case "${args[0]##*/}" in gufi_*) args=(env "DYLD_LIBRARY_PATH=${args[0]%/*/*}/lib" "${args[@]}") ;; esac ;; esac',
  '  fi',
  '  o=$(mktemp); e=$(mktemp)',
  '  "${args[@]}" </dev/null >"$o" 2>"$e"; rc=$?',
  `  printf '%s o%s e%s\\n' "$rc" "$(base64 <"$o" | tr -d '\\n')" "$(tail -c ${STDERR_TAIL} "$e" | base64 | tr -d '\\n')"`,
  '  rm -f "$o" "$e"',
  'done',
].join('\n')

export interface RemoteResult { code: number; stdout: string; stderr: string; /** stdout as bytes, for binary output */ raw: Buffer }

// ---- plain ssh ----

const SSH_SCHEME = 'ssh://'
/** user@host, or an alias from ~/.ssh/config: nothing ssh would take as an
 *  option, and nothing a shell would interpret. */
export const SSH_DESTINATION = /^(?:[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}@)?[A-Za-z0-9_][A-Za-z0-9_.-]{0,252}$/

export interface SshHost { destination: string; port: number | null }

/** The site a host over plain ssh goes by: ssh://<destination>[:<port>]. */
export function sshSite(destination: string, port?: number | null): string {
  if (!SSH_DESTINATION.test(destination)) throw new KbError(400, 'invalid ssh destination')
  if (port != null && !(Number.isInteger(port) && port >= 1 && port <= 65535)) throw new KbError(400, 'invalid ssh port')
  return `${SSH_SCHEME}${destination}${port != null ? `:${port}` : ''}`
}

/** The host a site names when it is one over plain ssh; null for a cluster. */
export function parseSshSite(site: string): SshHost | null {
  if (!site.startsWith(SSH_SCHEME)) return null
  const m = /^(.*?)(?::(\d{1,5}))?$/.exec(site.slice(SSH_SCHEME.length))!
  const port = m[2] ? Number(m[2]) : null
  if (!SSH_DESTINATION.test(m[1]) || (port != null && !(port >= 1 && port <= 65535))) throw new KbError(400, 'invalid ssh site')
  return { destination: m[1], port }
}

/** How a site reads in a message: the destination for a host over ssh. */
export function siteName(site: string): string {
  return site.startsWith(SSH_SCHEME) ? site.slice(SSH_SCHEME.length) : site
}

/** How a person reaches a site from a terminal: ssh for a host over plain
 *  ssh, pw ssh for a cluster. For the GUFI commands panel. */
export function shellFor(site: string): string[] {
  const h = parseSshSite(site)
  return h ? ['ssh', ...(h.port != null ? ['-p', String(h.port)] : []), h.destination] : ['pw', 'ssh', site]
}

/** ssh's arguments for a command on a host: no prompts (keys and the agent
 *  only), a bounded connect, keepalives so a dropped link ends the session
 *  rather than hanging it, and a shared connection (controlArgs). */
export function sshArgv(site: string, command: string): string[] {
  const h = parseSshSite(site)
  if (!h) throw new KbError(400, `${site} is not an ssh site`)
  return ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', ...controlArgs(h),
    ...(h.port != null ? ['-p', String(h.port)] : []), h.destination, command]
}

/** ssh's connection sharing for a host: the first session to it logs in and
 *  keeps the connection for ten minutes after the last one closes, and the
 *  others ride on it. The socket is in a folder of the user's own
 *  (socketDir); the person's ~/.ssh/config is left as it is. The path is
 *  quoted, since a folder may have spaces in it and ssh refuses an unquoted
 *  one. None where no folder will do, or with STUDIO_SSH_SHARE=0. */
export function controlArgs(h: SshHost): string[] {
  if (process.env.STUDIO_SSH_SHARE === '0') return []
  const dir = socketDir()
  if (!dir) return []
  const sock = path.join(dir, crypto.createHash('sha1').update(`${h.destination}:${h.port ?? ''}`).digest('hex').slice(0, SOCKET_NAME))
  return ['-o', 'ControlMaster=auto', '-o', `ControlPath="${sock}"`, '-o', 'ControlPersist=600']
}

/** A socket's name: 12 hex characters of the destination (user and host)
 *  and port. ssh binds the socket at its path plus 17 characters, and
 *  macOS allows 104 bytes, so the whole path stays within 86. */
const SOCKET_NAME = 12
const SOCKET_PATH_MAX = 86

/** Where ssh's sockets may go, in order: $XDG_RUNTIME_DIR/activate-studio
 *  where that is set, /tmp/activate-studio-<uid>, and the data folder's
 *  ssh folder, which on macOS can be too long for a socket. */
export function socketDirs(): string[] {
  const xdg = process.env.XDG_RUNTIME_DIR
  return [
    ...(xdg && path.isAbsolute(xdg) && fs.existsSync(xdg) ? [path.join(xdg, 'activate-studio')] : []),
    `/tmp/activate-studio-${process.getuid?.() ?? 'x'}`,
    path.join(INDEX_BASE, 'ssh'),
  ]
}

/** The first of `dirs` that a socket fits in and that is the user's alone:
 *  made with mode 700 when missing, then checked with lstat each time, as
 *  a directory (not a link) owned by `uid` with mode 700. In /tmp another
 *  user could have made the folder first, or put a link there; such a
 *  folder is passed over. Null when none will do. */
export function socketDir(dirs = socketDirs(), uid = process.getuid?.()): string | null {
  if (uid === undefined) return null
  for (const dir of dirs) {
    if (Buffer.byteLength(dir) + 1 + SOCKET_NAME > SOCKET_PATH_MAX || /["%]/.test(dir)) continue
    try { fs.mkdirSync(dir, { recursive: true, mode: 0o700 }) } catch { /* checked below */ }
    try {
      const s = fs.lstatSync(dir)
      if (s.isDirectory() && s.uid === uid && (s.mode & 0o777) === 0o700) return dir
    } catch { /* not there */ }
  }
  return null
}

type Spawn = (site: string, key: string, command: string) => ChildProcessWithoutNullStreams
let spawnSsh: Spawn = (site, key, command) => {
  if (site.startsWith(SSH_SCHEME)) return spawn('ssh', sshArgv(site, command), { stdio: ['pipe', 'pipe', 'pipe'] })
  let host = ''
  try { host = new URL(GATEWAY_BASE).hostname } catch { /* the CLI's own context */ }
  // Without a key of its own the CLI uses its own sign-in.
  return spawn(PW_CLI, ['ssh', ...(host ? ['--platform-host', host] : []), site, command],
    { stdio: ['pipe', 'pipe', 'pipe'], env: key ? { ...process.env, PW_API_KEY: key } : process.env })
}
/** Test seam: what runs in place of `pw ssh <site> <command>` or
 *  `ssh ... <destination> <command>`. */
export function setRemoteSpawn(fn: Spawn): void { spawnSsh = fn }

/** The command that starts the loop, decoded by the remote login shell.
 *  Over plain ssh the host may be a macOS one whose base64 decodes with -D
 *  only; GNU base64 refuses -D at once, before reading its input. */
export function bootstrapCommand(site: string): string {
  const b64 = Buffer.from(LOOP).toString('base64')
  if (site.startsWith(SSH_SCHEME)) return `bash -c "$(echo ${b64} | base64 -D 2>/dev/null || echo ${b64} | base64 -d)"`
  return `bash -c "$(echo ${b64} | base64 -d)"`
}

// ---- hosts that run only gufi_query ----

/** Sites whose login runs nothing but gufi_query, learned when the loop
 *  could not start there or from a library's last connection test. */
const directSites = new Set<string>()
export function setDirect(site: string, on: boolean): void { if (on) directSites.add(site); else directSites.delete(site) }
export function isDirect(site: string): boolean { return directSites.has(site) }

/** What may run on such a host: GUFI's query tools, by name or by path. */
const QUERY_TOOL = /(?:^|\/)gufi_(?:query|sqlite3)$/

/** Why a command other than gufi_query cannot run on such a host. */
export function noShell(site: string): string {
  return `${siteName(site)} runs only gufi_query for this login (no shell), so this cannot run there.`
}

/** One argument as a POSIX shell reads it back: plain words as they are
 *  (so a forced command that checks for a leading "gufi_query " sees it),
 *  anything else in single quotes, with a quote inside written '\''. */
export function shellWord(a: string): string {
  if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(a)) return a
  return `'${a.replace(/'/g, `'\\''`)}'`
}

/** The command line a direct call sends: each argument a shell word. */
export function directCommand(argv: string[]): string { return argv.map(shellWord).join(' ') }

/** ssh's own notices on stderr that are not errors: OpenSSH's warnings
 *  (the post-quantum key exchange note starts "** "), a host key added to
 *  known_hosts, and blank lines. */
export function quietStderr(s: string): string {
  return s.split('\n').filter(l => l.trim() && !/^\*\* /.test(l) && !/^Warning: Permanently added /.test(l)).join('\n')
}

/** Runs argv as one ssh (or pw ssh) call with no session loop: for a host
 *  that runs only gufi_query, and for the first step of a connection test.
 *  Rejects when ssh itself fails (exit 255 over plain ssh); a command that
 *  ran and exited nonzero resolves with its code. */
export function remoteRunOnce(site: string, key: string, argv: string[], timeoutMs = 120_000): Promise<RemoteResult> {
  if (!argv.length) return Promise.reject(new KbError(400, 'no command'))
  return new Promise<RemoteResult>((resolve, reject) => {
    let child: ChildProcessWithoutNullStreams
    try { child = spawnSsh(site, key, directCommand(argv)) } catch (e) { reject(new KbError(502, `${siteName(site)}: ${(e as Error).message}`)); return }
    const out: Buffer[] = []
    let err = '', settled = false
    const settle = (fn: () => void) => { if (!settled) { settled = true; clearTimeout(timer); fn() } }
    const timer = setTimeout(() => settle(() => {
      child.kill()
      reject(new KbError(504, `${siteName(site)}: the command did not finish in ${Math.round(timeoutMs / 1000)} s`))
    }), timeoutMs)
    child.stdin.on('error', () => {})
    child.stdin.end()
    child.stdout.on('data', d => out.push(Buffer.from(d)))
    child.stderr.on('data', d => { err = (err + String(d)).slice(-STDERR_TAIL) })
    child.on('error', e => settle(() => reject(new KbError(502, `${siteName(site)}: ${e.message}`))))
    child.on('close', code => settle(() => {
      const stderr = quietStderr(err)
      if (code === 255 && site.startsWith(SSH_SCHEME)) {
        reject(new KbError(502, `${siteName(site)}: ${stderr.trim().split('\n').pop() || 'the ssh connection failed'}`))
        return
      }
      const raw = Buffer.concat(out)
      resolve({ code: code ?? -1, stdout: raw.toString('utf8'), raw, stderr })
    }))
  })
}

/** Whether a session that ended before its loop answered was refused by
 *  the host's login (no bash, a restricted shell, a forced command), as
 *  opposed to ssh failing: over plain ssh the remote command ran and
 *  exited (any status but ssh's 255); over pw ssh its message says so. */
function refusedLoop(site: string, code: number | null, stderr: string): boolean {
  if (site.startsWith(SSH_SCHEME)) return code !== null && code !== 255
  return /bash[:,]? .*(?:not found|No such file)|command not found|restricted|not allowed|not permitted|only gufi_query/i.test(stderr)
}

interface Pending { resolve: (r: RemoteResult) => void; reject: (e: Error) => void; timer: ReturnType<typeof setTimeout>; argv: string[]; timeoutMs: number }
interface Channel { child: ChildProcessWithoutNullStreams; queue: Pending[]; buf: string; last: number; stderr: string; dead: boolean; started: boolean; chain: Promise<unknown> }
/** The sessions to each host, by the key they act with and the site. */
const channels = new Map<string, Channel[]>()

function drop(id: string, ch: Channel): void {
  const pool = (channels.get(id) ?? []).filter(c => c !== ch)
  if (pool.length) channels.set(id, pool)
  else channels.delete(id)
}

function open(id: string, site: string, key: string): Channel {
  const child = spawnSsh(site, key, bootstrapCommand(site))
  const ch: Channel = { child, queue: [], buf: '', last: Date.now(), stderr: '', dead: false, started: false, chain: Promise.resolve() }
  const fail = (why: string, code: number | null = null) => {
    if (ch.dead) return
    ch.dead = true
    drop(id, ch)
    const said = quietStderr(ch.stderr).trim().split('\n').filter(Boolean).pop()
    const waiting = ch.queue.splice(0)
    // The login would not start the loop: the host runs only gufi_query.
    // Its query commands go again as direct calls; anything else cannot run.
    if (!ch.started && refusedLoop(site, code, ch.stderr)) {
      setDirect(site, true)
      for (const p of waiting) {
        clearTimeout(p.timer)
        if (QUERY_TOOL.test(p.argv[0])) remoteRunOnce(site, key, p.argv, p.timeoutMs).then(p.resolve, p.reject)
        else p.reject(new KbError(409, noShell(site)))
      }
      return
    }
    for (const p of waiting) { clearTimeout(p.timer); p.reject(new KbError(502, `${siteName(site)}: ${said || why}`)) }
  }
  child.stdout.on('data', d => {
    ch.buf += String(d)
    let nl: number
    while ((nl = ch.buf.indexOf('\n')) >= 0) {
      const line = ch.buf.slice(0, nl).trim(); ch.buf = ch.buf.slice(nl + 1)
      const m = /^(\d+) o(\S*) e(\S*)$/.exec(line)
      if (!m) continue // a login banner or motd before the loop starts
      ch.started = true
      const p = ch.queue.shift()
      if (!p) continue
      clearTimeout(p.timer)
      const raw = Buffer.from(m[2], 'base64')
      p.resolve({ code: Number(m[1]), stdout: raw.toString('utf8'), raw, stderr: Buffer.from(m[3] ?? '', 'base64').toString('utf8') })
    }
  })
  child.stderr.on('data', d => { ch.stderr = (ch.stderr + String(d)).slice(-2000) })
  // A write to a session that has just ended fails here; the exit below
  // rejects what was waiting on it.
  child.stdin.on('error', () => {})
  child.on('error', e => fail(e.message))
  // The reason a session ended is the last thing it wrote to stderr, which
  // can arrive after the exit: fail when the streams close, or shortly
  // after the exit should something else hold them open.
  child.on('close', code => fail(`the ssh session ended (${code})`, code))
  child.on('exit', code => { setTimeout(() => fail(`the ssh session ended (${code})`, code), 1000).unref?.() })
  return ch
}

/** Runs argv on the site as the key's user (or, over plain ssh, as the
 *  ssh login); rejects only when the session itself fails, not when the
 *  command exits nonzero. On a host that runs only gufi_query, a query
 *  tool runs as its own call and anything else is refused. */
export function remoteRun(site: string, key: string, argv: string[], timeoutMs = 120_000): Promise<RemoteResult> {
  if (!argv.length) return Promise.reject(new KbError(400, 'no command'))
  if (directSites.has(site)) {
    return QUERY_TOOL.test(argv[0]) ? remoteRunOnce(site, key, argv, timeoutMs) : Promise.reject(new KbError(409, noShell(site)))
  }
  const id = `${key.slice(-16)}|${site}`
  const c = pick(id, site, key)
  c.last = Date.now()
  return new Promise<RemoteResult>((resolve, reject) => {
    const timer = setTimeout(() => {
      // A command that hangs leaves the loop busy: drop the session, and
      // every request behind it with it.
      const i = c.queue.findIndex(p => p.timer === timer)
      if (i >= 0) c.queue.splice(i, 1)
      reject(new KbError(504, `${siteName(site)}: the command did not finish in ${Math.round(timeoutMs / 1000)} s`))
      c.child.kill()
    }, timeoutMs)
    c.queue.push({ resolve, reject, timer, argv, timeoutMs })
    c.child.stdin.write(argv.map(a => 'x' + Buffer.from(a).toString('base64')).join(' ') + '\n')
  })
}

/** A session for the next request: an idle one, else a new one while the
 *  host has fewer than POOL_MAX, else the one with the shortest queue. */
function pick(id: string, site: string, key: string): Channel {
  const pool = (channels.get(id) ?? []).filter(c => !c.dead)
  const idle = pool.find(c => !c.queue.length)
  if (idle) return idle
  if (pool.length < POOL_MAX) {
    const ch = open(id, site, key)
    channels.set(id, [...pool, ch])
    return ch
  }
  return pool.reduce((a, b) => (b.queue.length < a.queue.length ? b : a))
}

const sweeper = setInterval(() => {
  for (const [id, pool] of channels) {
    for (const c of pool) if (Date.now() - c.last > IDLE_MS && !c.queue.length) { c.child.kill(); drop(id, c) }
  }
}, 60_000)
sweeper.unref?.()

/** Tests and shutdown. */
export function closeRemoteChannels(): void {
  for (const pool of channels.values()) for (const c of pool) c.child.kill()
  channels.clear()
  cpus.clear()
  directSites.clear()
}

/** A host's processor count, asked once per site and key: nproc, which
 *  counts only the processors this login may use (and honours
 *  OMP_NUM_THREADS), else getconf, else sysctl on macOS. 4 when it cannot
 *  be read, asked again next time, and on a host that runs only gufi_query,
 *  where none of them may run. */
const cpus = new Map<string, Promise<number>>()
export function remoteCpus(site: string, key: string): Promise<number> {
  if (directSites.has(site)) return Promise.resolve(4)
  const id = `${key.slice(-16)}|${site}`
  let p = cpus.get(id)
  if (!p) {
    p = remoteRun(site, key, ['sh', '-c', 'nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null'], 30_000)
      .then(r => parseInt(r.stdout.trim().split('\n')[0], 10), () => NaN)
      .then(n => {
        if (Number.isFinite(n) && n > 0) return n
        cpus.delete(id)
        return 4
      })
    cpus.set(id, p)
  }
  return p
}

/** The threads a gufi_query on a host runs with: STUDIO_REMOTE_THREADS
 *  when set, else the host's processor count up to 16, so a large index
 *  is walked by as many threads as the host has without taking over a
 *  shared login node. */
export async function remoteThreads(site: string, key: string): Promise<number> {
  const set = Number(process.env.STUDIO_REMOTE_THREADS)
  if (Number.isInteger(set) && set >= 1) return Math.min(set, 256)
  return Math.min(await remoteCpus(site, key), 16)
}
