import { spawn, ChildProcess } from 'node:child_process'
import type { FastifyInstance } from 'fastify'
import { PORT, PW_CLI } from './config.js'
import { effectiveSettings } from './settings.js'
import { gatewayKey } from './chat/gateway.js'

/**
 * Optional self-registration of the /v1 RAG surface as a platform AI
 * provider: spawns `pw endpoints run --openai` around a tiny forwarder to
 * this server, so the corpus-grounded models (studio-agent, studio-rag)
 * appear in the platform chat and provider catalog. Requires an
 * authenticated pw CLI on the host. Platform calls arrive without a
 * bearer, so the forwarder injects the deployment credential on requests
 * that lack one (key passed via env, never written to disk); that is the
 * same trust decision as the "allow deployment credential" setting,
 * scoped to this registration.
 */

// Serves the endpoint's assigned $PORT and pipes everything to the app.
const FORWARDER = `
const http = require('http');
const target = 'http://127.0.0.1:' + process.env.STUDIO_TARGET_PORT;
http.createServer((q, s) => {
  const headers = { ...q.headers };
  if (!headers.authorization && process.env.STUDIO_INJECT_KEY) {
    headers.authorization = 'Bearer ' + process.env.STUDIO_INJECT_KEY;
    headers['x-studio-injected-key'] = '1';
  }
  const p = http.request(target + q.url, { method: q.method, headers }, r => {
    s.writeHead(r.statusCode, r.headers);
    r.pipe(s, { end: false });
    r.on('end', () => setTimeout(() => s.end(), 400));
  });
  q.pipe(p);
  p.on('error', () => { s.statusCode = 502; s.end(); });
}).listen(process.env.PORT, '127.0.0.1');
`

export interface RagEndpointStatus {
  state: 'stopped' | 'starting' | 'running' | 'error'
  name: string | null
  url: string | null
  startedAt: string | null
  detail: string | null
}

let child: ChildProcess | null = null
const status: RagEndpointStatus = { state: 'stopped', name: null, url: null, startedAt: null, detail: null }
export function endpointName(): string {
  const eff = effectiveSettings()
  if (eff.ragEndpointName) return eff.ragEndpointName
  const app = eff.appName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'studio'
  return `${app}-rag`.slice(0, 40)
}

export function ragEndpointStatus(): RagEndpointStatus {
  return { ...status }
}

export function startRagEndpoint(): RagEndpointStatus {
  if (child) return ragEndpointStatus()
  const key = gatewayKey()
  if (!key) {
    status.state = 'error'
    status.detail = 'No deployment gateway credential; the registered provider would have nothing to authenticate with.'
    return ragEndpointStatus()
  }
  const name = endpointName()
  // A container deployment has no pw CLI inside it unless the binary was
  // bound in, and a spawn that cannot find its command emits an 'error'
  // event: unhandled, that took the whole server down with it, so one
  // click on a button ended the session.
  const cli = PW_CLI
  const proc = spawn(cli, ['endpoints', 'run', '--openai', '--name', name, '-o', 'text', '--', 'node', '-e', FORWARDER], {
    env: { ...process.env, STUDIO_TARGET_PORT: String(PORT), STUDIO_INJECT_KEY: key },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  child = proc
  status.state = 'starting'
  status.name = name
  status.url = null
  status.startedAt = new Date().toISOString()
  status.detail = null
  let tail = ''
  const onData = (buf: Buffer) => {
    tail = (tail + buf.toString('utf8')).slice(-2000)
    const m = tail.match(/https?:\/\/\S+/)
    if (m && status.state === 'starting') {
      status.state = 'running'
      status.url = m[0].replace(/[).,]$/, '')
    }
    status.detail = tail.split('\n').filter(Boolean).slice(-3).join(' | ').slice(0, 400)
  }
  proc.stdout?.on('data', onData)
  proc.stderr?.on('data', onData)
  proc.on('error', err => {
    if (child === proc) child = null
    status.state = 'error'
    status.url = null
    status.detail = (err as NodeJS.ErrnoException).code === 'ENOENT'
      ? `The pw CLI is not available to this deployment (looked for "${cli}"), so the model cannot be registered on the platform from here. `
        + 'Bind the CLI into the container, or set PW_CLI to its path.'
      : `Could not start the endpoint agent: ${err.message}`
  })
  proc.on('exit', code => {
    if (child === proc) child = null
    status.state = code === 0 ? 'stopped' : 'error'
    if (code !== 0) status.detail = `exited with code ${code}: ${status.detail ?? ''}`.slice(0, 400)
    status.url = null
  })
  return ragEndpointStatus()
}

export function stopRagEndpoint(): RagEndpointStatus {
  if (child) {
    const proc = child
    child = null
    proc.kill('SIGTERM')
    setTimeout(() => proc.kill('SIGKILL'), 5000).unref()
  }
  status.state = 'stopped'
  status.url = null
  return ragEndpointStatus()
}

export async function ragEndpointRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/rag/endpoint', async () => ragEndpointStatus())
  app.post('/api/rag/endpoint/start', async () => startRagEndpoint())
  app.post('/api/rag/endpoint/stop', async () => stopRagEndpoint())
}

/** Auto-start at boot when the setting asks for it. */
export function maybeAutoStart(log: (msg: string) => void): void {
  if (effectiveSettings().ragEndpointAutoStart) {
    log('rag endpoint auto-start enabled; registering')
    startRagEndpoint()
  }
}
