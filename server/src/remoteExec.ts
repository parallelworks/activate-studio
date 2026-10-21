import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { GATEWAY_BASE, PW_CLI } from './config.js'
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
 * request runs at a time per session; the queue is in this process.
 */

const IDLE_MS = 10 * 60_000
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
 *  only), a bounded connect, and keepalives so a dropped link ends the
 *  session rather than hanging it. */
export function sshArgv(site: string, command: string): string[] {
  const h = parseSshSite(site)
  if (!h) throw new KbError(400, `${site} is not an ssh site`)
  return ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', ...(h.port != null ? ['-p', String(h.port)] : []), h.destination, command]
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

interface Pending { resolve: (r: RemoteResult) => void; reject: (e: Error) => void; timer: ReturnType<typeof setTimeout> }
interface Channel { child: ChildProcessWithoutNullStreams; queue: Pending[]; buf: string; last: number; stderr: string; dead: boolean; chain: Promise<unknown> }
const channels = new Map<string, Channel>()

function open(id: string, site: string, key: string): Channel {
  const child = spawnSsh(site, key, bootstrapCommand(site))
  const ch: Channel = { child, queue: [], buf: '', last: Date.now(), stderr: '', dead: false, chain: Promise.resolve() }
  const fail = (why: string) => {
    if (ch.dead) return
    ch.dead = true
    channels.delete(id)
    const said = ch.stderr.trim().split('\n').filter(Boolean).pop()
    for (const p of ch.queue.splice(0)) { clearTimeout(p.timer); p.reject(new KbError(502, `${siteName(site)}: ${said || why}`)) }
  }
  child.stdout.on('data', d => {
    ch.buf += String(d)
    let nl: number
    while ((nl = ch.buf.indexOf('\n')) >= 0) {
      const line = ch.buf.slice(0, nl).trim(); ch.buf = ch.buf.slice(nl + 1)
      const m = /^(\d+) o(\S*) e(\S*)$/.exec(line)
      if (!m) continue // a login banner or motd before the loop starts
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
  child.on('close', code => fail(`the ssh session ended (${code})`))
  child.on('exit', code => { setTimeout(() => fail(`the ssh session ended (${code})`), 1000).unref?.() })
  return ch
}

/** Runs argv on the site as the key's user (or, over plain ssh, as the
 *  ssh login); rejects only when the session itself fails, not when the
 *  command exits nonzero. */
export function remoteRun(site: string, key: string, argv: string[], timeoutMs = 120_000): Promise<RemoteResult> {
  if (!argv.length) return Promise.reject(new KbError(400, 'no command'))
  const id = `${key.slice(-16)}|${site}`
  let ch = channels.get(id)
  if (!ch || ch.dead) { ch = open(id, site, key); channels.set(id, ch) }
  const c = ch
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
    c.queue.push({ resolve, reject, timer })
    c.child.stdin.write(argv.map(a => 'x' + Buffer.from(a).toString('base64')).join(' ') + '\n')
  })
}

const sweeper = setInterval(() => {
  for (const [id, c] of channels) if (Date.now() - c.last > IDLE_MS && !c.queue.length) { c.child.kill(); channels.delete(id) }
}, 60_000)
sweeper.unref?.()

/** Tests and shutdown. */
export function closeRemoteChannels(): void { for (const c of channels.values()) c.child.kill(); channels.clear() }
