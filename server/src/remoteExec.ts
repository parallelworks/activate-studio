import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { GATEWAY_BASE, PW_CLI } from './config.js'
import { KbError } from './kb.js'

/**
 * Commands on a user's cluster over one long-lived `pw ssh` session, so a
 * library tree on a cluster expands in about a quarter of a second rather
 * than paying the ssh setup (about 5 seconds) on every click.
 *
 * The remote end is a small loop in bash. Each request is one line: the
 * command's argv, every argument base64-encoded, so no SQL quote, dollar
 * sign, or space ever reaches a shell unprotected (the remote shell parsed
 * unescaped SQL when gufi_vt was spiked). The loop decodes the arguments
 * into an array and runs it without a shell, then answers with one line:
 * the exit status and the base64 of stdout and of stderr's tail. One
 * request runs at a time per session; the queue is in this process.
 *
 * The session acts with the viewer's own platform key, as that user.
 */

const IDLE_MS = 10 * 60_000
const STDERR_TAIL = 4000

// Read a line of space-separated arguments, each "x" then its base64 (the
// "x" keeps an empty argument from vanishing in the split), run them as
// argv with stdin closed, and print "<status> o<stdout b64> e<stderr b64>".
const LOOP = [
  'set -f',
  'while IFS= read -r line; do',
  '  args=(); for a in $line; do args+=("$(printf %s "${a#x}" | base64 -d)"); done',
  '  o=$(mktemp); e=$(mktemp)',
  '  "${args[@]}" </dev/null >"$o" 2>"$e"; rc=$?',
  `  printf '%s o%s e%s\\n' "$rc" "$(base64 -w0 <"$o")" "$(tail -c ${STDERR_TAIL} "$e" | base64 -w0)"`,
  '  rm -f "$o" "$e"',
  'done',
].join('\n')

export interface RemoteResult { code: number; stdout: string; stderr: string }

type Spawn = (site: string, key: string, command: string) => ChildProcessWithoutNullStreams
let spawnSsh: Spawn = (site, key, command) => {
  let host = ''
  try { host = new URL(GATEWAY_BASE).hostname } catch { /* the CLI's own context */ }
  return spawn(PW_CLI, ['ssh', ...(host ? ['--platform-host', host] : []), site, command],
    { stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, PW_API_KEY: key } })
}
/** Test seam: what runs in place of `pw ssh <site> <command>`. */
export function setRemoteSpawn(fn: Spawn): void { spawnSsh = fn }

interface Pending { resolve: (r: RemoteResult) => void; reject: (e: Error) => void; timer: ReturnType<typeof setTimeout> }
interface Channel { child: ChildProcessWithoutNullStreams; queue: Pending[]; buf: string; last: number; stderr: string; dead: boolean; chain: Promise<unknown> }
const channels = new Map<string, Channel>()

function open(id: string, site: string, key: string): Channel {
  // The loop travels base64-encoded too, decoded by the remote login shell.
  const command = `bash -c "$(echo ${Buffer.from(LOOP).toString('base64')} | base64 -d)"`
  const child = spawnSsh(site, key, command)
  const ch: Channel = { child, queue: [], buf: '', last: Date.now(), stderr: '', dead: false, chain: Promise.resolve() }
  const fail = (why: string) => {
    if (ch.dead) return
    ch.dead = true
    channels.delete(id)
    const said = ch.stderr.trim().split('\n').filter(Boolean).pop()
    for (const p of ch.queue.splice(0)) { clearTimeout(p.timer); p.reject(new KbError(502, `${site}: ${said || why}`)) }
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
      p.resolve({ code: Number(m[1]), stdout: Buffer.from(m[2], 'base64').toString('utf8'), stderr: Buffer.from(m[3] ?? '', 'base64').toString('utf8') })
    }
  })
  child.stderr.on('data', d => { ch.stderr = (ch.stderr + String(d)).slice(-2000) })
  child.on('error', e => fail(e.message))
  child.on('exit', code => fail(`the ssh session ended (${code})`))
  return ch
}

/** Runs argv on the site as the key's user; rejects only when the
 *  session itself fails, not when the command exits nonzero. */
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
      reject(new KbError(504, `${site}: the command did not finish in ${Math.round(timeoutMs / 1000)} s`))
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
