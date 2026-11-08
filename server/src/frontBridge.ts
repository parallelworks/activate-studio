import { randomBytes, timingSafeEqual } from 'node:crypto'
import type { FastifyInstance, FastifyRequest } from 'fastify'
import { authEnabled } from './auth.js'
import { KbError } from './kb.js'
import { getSharedKeyStatus, getUserKeyStatus, hydrateFromCookie, personalKeysDisabled, resolveUserCred, SINGLE_OWNER, clearUserKey } from './credentials.js'

/**
 * The bridge between the Go server and this one while routes move to Go
 * (docs/GO-PORT.md, "The platform track"). A Go route that needs a value
 * still held here (the viewer's personal key, a conversation to append a
 * note to, the MCP tool list) asks for it under /api/__bridge/, with the
 * viewer's own identity headers, so auth.ts identifies the viewer exactly as
 * for any route; and this server asks the Go server for state it no longer
 * holds (the task board, the run registry) at STUDIO_FRONT_URL.
 *
 * Both directions carry STUDIO_BRIDGE_SECRET in x-studio-bridge-secret. The Go
 * server makes the secret and passes it to a Node server it starts; the Go
 * front refuses /api/__bridge/ from any client. Without the secret set,
 * none of this is registered and every module works as before.
 */

export const BRIDGE_HEADER = 'x-studio-bridge-secret'

export function bridgeSecret(): string { return process.env.STUDIO_BRIDGE_SECRET ?? '' }

/** Where the Go server answers this server's bridge calls, or ''. */
export function frontUrl(): string { return (process.env.STUDIO_FRONT_URL ?? '').replace(/\/$/, '') }

/** Whether the Go server owns the platform track's state and background
 *  work (runs, tasks, fleet, the RAG endpoint): set by a Go server that
 *  starts this one. Node then neither watches nor reattaches any of it. */
export function goOwnsPlatform(): boolean { return process.env.STUDIO_GO_PLATFORM === '1' && !!frontUrl() && !!bridgeSecret() }

function secretOk(given: unknown): boolean {
  const want = bridgeSecret()
  if (!want || typeof given !== 'string' || given.length !== want.length) return false
  return timingSafeEqual(Buffer.from(given), Buffer.from(want))
}

/** A call to the Go server's bridge; rejects on any failure. */
export async function callFront<T = unknown>(path: string, body: unknown): Promise<T> {
  const res = await fetch(`${frontUrl()}${path}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', [BRIDGE_HEADER]: bridgeSecret() },
    body: JSON.stringify(body ?? {}),
    signal: AbortSignal.timeout(15_000),
  })
  if (!res.ok) throw new Error(`the Go server answered ${res.status}`)
  return await res.json() as T
}

export async function frontBridgeRoutes(app: FastifyInstance): Promise<void> {
  if (!bridgeSecret()) return
  const guard = async (req: FastifyRequest) => {
    if (!secretOk(req.headers[BRIDGE_HEADER])) throw new KbError(404, 'not found')
  }

  // Who the request is, and the personal credential it carries: what
  // credentials.ts and auth.ts hold for the Go routes of the platform track.
  app.route({ method: 'GET', url: '/api/__bridge/viewer', onRequest: guard, handler: async (req, reply) => {
    const user = req.user ?? null
    if (user?.id) hydrateFromCookie(user.id, req.headers.cookie)
    const single = !authEnabled()
    const owner = user?.id ?? (single ? SINGLE_OWNER : null)
    const cred = resolveUserCred(user?.id)
    const shared = getSharedKeyStatus()
    reply.header('cache-control', 'no-store')
    return {
      authEnabled: authEnabled(),
      user: user ? { id: user.id ?? null, username: user.username ?? null, name: (user as { name?: string }).name ?? null } : null,
      cred: cred ? { key: cred.key, baseUrl: cred.baseUrl ?? null, endpoint: cred.endpoint ?? null } : null,
      keyStatus: owner && !personalKeysDisabled() ? getUserKeyStatus(owner) : null,
      sharedLast4: shared.last4 ?? null,
    }
  } })

  // The pw CLI's location or context changed in Go: read them again here,
  // so this server's pw commands and gateway follow.
  app.route({ method: 'POST', url: '/api/__bridge/access-changed', onRequest: guard, handler: async req => {
    const b = (req.body ?? {}) as { clearKey?: boolean }
    const { findPwCli } = await import('./config.js')
    const { reloadAccessChoice } = await import('./access.js')
    const { invalidateProviderProbes } = await import('./chat/gateway.js')
    findPwCli()
    reloadAccessChoice()
    if (b.clearKey) clearUserKey(SINGLE_OWNER)
    invalidateProviderProbes()
    return { ok: true }
  } })

  app.route({ method: 'POST', url: '/api/__bridge/provider-probes/invalidate', onRequest: guard, handler: async () => {
    const { invalidateProviderProbes } = await import('./chat/gateway.js')
    invalidateProviderProbes()
    return { ok: true }
  } })

  // A note in the conversation a finished run was launched from (runs.ts).
  app.route({ method: 'POST', url: '/api/__bridge/conversation-note', onRequest: guard, handler: async req => {
    const b = (req.body ?? {}) as { conversationId?: string; content?: string }
    const { appendConversationNote } = await import('./conversations.js')
    return { ok: appendConversationNote(String(b.conversationId ?? ''), String(b.content ?? '')) }
  } })

  // What a task agent's session pre-approves (mcp.ts taskToolNames).
  app.route({ method: 'GET', url: '/api/__bridge/task-tools', onRequest: guard, handler: async () => {
    const { taskToolNames } = await import('./mcp.js')
    return { tools: taskToolNames() }
  } })

  // The personas a task agent can adopt (extensions.ts).
  app.route({ method: 'GET', url: '/api/__bridge/personas', onRequest: guard, handler: async () => {
    const { personas } = await import('./extensions.js')
    return { personas: personas() }
  } })
}

/** A random secret for a Node server the caller starts. */
export function newBridgeSecret(): string { return randomBytes(24).toString('hex') }
