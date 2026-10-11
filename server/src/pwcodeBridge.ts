import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import http from 'node:http'
import { Duplex } from 'node:stream'
import { GATEWAY_BASE, PW_CLI } from './config.js'
import { daemonRequest, DaemonError, type DaemonClient } from './pwcode.js'

/**
 * A pw code daemon on another machine, reached the way `pw code attach`
 * reaches it: `pw ssh <resource> pw code daemon proxy --ensure` bridges the
 * ssh session's stdio to that machine's daemon socket, starting the daemon
 * when none runs, and the daemon's own HTTP API is spoken over the pipe.
 * This needs nothing from the platform beyond `pw ssh`, so it works where
 * the platform's agent routes are not served, and it reaches the whole
 * API, goals included.
 *
 * One keep-alive connection per viewer and machine carries every request,
 * one at a time, so only the first pays for the ssh setup (about 5 s, then
 * about 0.25 s a request, measured on a customer cluster). The viewer's
 * platform key authenticates `pw ssh`; on the machine the daemon runs as
 * that user, with their own pw login for models. Over the bridge the
 * Studio has the owner's authority on that daemon (no remote permission
 * ceiling applies), so the Studio sets each session's permission mode.
 */

const IDLE_MS = 10 * 60_000

type Spawn = (resource: string, key: string) => ChildProcessWithoutNullStreams
let spawnBridge: Spawn = (resource, key) => {
  let host = ''
  try { host = new URL(GATEWAY_BASE).hostname } catch { /* the CLI's own context then */ }
  return spawn(PW_CLI, ['ssh', ...(host ? ['--platform-host', host] : []), resource, 'pw code daemon proxy --ensure'],
    { stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, PW_API_KEY: key } })
}
/** Test seam: what runs in place of `pw ssh … daemon proxy`. */
export function setBridgeSpawn(fn: Spawn): void { spawnBridge = fn }

interface Bridge { agent: http.Agent; last: number; stderr: string }
const bridges = new Map<string, Bridge>()

/** The pipe as a socket the HTTP client can use: it writes into ssh's
 *  stdin and reads ssh's stdout. */
function openPipe(b: Bridge, resource: string, key: string): Duplex {
  const child = spawnBridge(resource, key)
  b.stderr = ''
  const sock: Duplex = new Duplex({
    read() {},
    write(chunk, _enc, cb) { child.stdin.write(chunk, cb) },
    final(cb) { child.stdin.end(); cb() },
    destroy(err, cb) { child.kill(); cb(err) },
  })
  // The HTTP client tunes a network socket with these; a pipe has none.
  Object.assign(sock, { setKeepAlive: () => sock, setNoDelay: () => sock, setTimeout: () => sock, ref: () => sock, unref: () => sock })
  child.stdout.on('data', d => sock.push(d))
  child.stdout.on('end', () => sock.push(null))
  child.stderr.on('data', d => { b.stderr = (b.stderr + String(d)).slice(-2000) })
  child.on('error', e => sock.destroy(e))
  child.on('exit', () => { if (!sock.destroyed) sock.destroy() })
  return sock
}

/** A client for the user's daemon on `resource`, authenticated with `key`. */
export function remoteDaemon(resource: string, key: string): DaemonClient {
  const id = `${key.slice(-16)}|${resource}`
  const bridge = (): Bridge => {
    let b = bridges.get(id)
    if (!b) {
      const agent = new http.Agent({ keepAlive: true, maxSockets: 1 })
      const nb: Bridge = { agent, last: Date.now(), stderr: '' }
      ;(agent as unknown as { createConnection: () => Duplex }).createConnection = () => openPipe(nb, resource, key)
      bridges.set(id, nb)
      b = nb
    }
    return b
  }
  return {
    site: resource,
    call: async <T>(method: string, route: string, body?: unknown, timeoutMs = 60_000): Promise<T> => {
      const b = bridge()
      b.last = Date.now()
      try {
        return await daemonRequest<T>({ agent: b.agent, host: 'daemon' }, method, route, body, timeoutMs)
      } catch (e) {
        // An answer from the daemon (a 4xx) leaves the connection good.
        // Anything else may have left the pipe mid-response, so the next
        // request starts a fresh one, and the error says what ssh said.
        if (e instanceof DaemonError && e.status) throw e
        b.agent.destroy(); bridges.delete(id)
        const said = b.stderr.trim().split('\n').filter(Boolean).pop()
        throw new DaemonError(`${resource}: ${said || (e as Error).message}`)
      }
    },
  }
}

const sweeper = setInterval(() => {
  for (const [id, b] of bridges) if (Date.now() - b.last > IDLE_MS) { b.agent.destroy(); bridges.delete(id) }
}, 60_000)
sweeper.unref?.()

/** Tests and shutdown: close every bridge. */
export function closeBridges(): void { for (const b of bridges.values()) b.agent.destroy(); bridges.clear() }
