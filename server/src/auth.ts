import type { FastifyInstance, FastifyRequest } from 'fastify'
import { createRemoteJWKSet, jwtVerify } from 'jose'

/**
 * Platform identity: when the app runs behind ACTIVATE, the platform forwards
 * a short-lived JWT in a header, verifiable against the platform JWKS
 * endpoint. Configure with:
 *   AUTH_JWKS_URL   JWKS endpoint (enables verification). On ACTIVATE this
 *                   is GET /api/platform/keys on the platform host (the
 *                   jwks_uri from its OIDC discovery document).
 *   AUTH_HEADER     header carrying the token (default x-pw-user-token, the
 *                   header the ACTIVATE session proxy forwards;
 *                   "authorization" strips a Bearer prefix)
 *   AUTH_ISSUER     optional expected issuer (pw-session-proxy on ACTIVATE)
 *   AUTH_AUDIENCE   optional expected audience; the proxy stamps
 *                   ["session:<sessionId>", "session:<owner>/<sessionName>"]
 *   AUTH_REQUIRED   1 = reject API requests without a valid token
 * The ACTIVATE token is a short-lived (5 min) RS256 JWT with sub
 * user:<username>; it identifies the user and proves the platform forwarded
 * the request, and carries no credentials. Standalone deployments leave
 * AUTH_JWKS_URL unset and nothing changes.
 */

export interface VerifiedUser {
  id: string
  username: string
  name?: string
  claims: Record<string, unknown>
}

declare module 'fastify' {
  interface FastifyRequest {
    user?: VerifiedUser
  }
}

const JWKS_URL = process.env.AUTH_JWKS_URL ?? ''
const HEADER = (process.env.AUTH_HEADER ?? 'x-pw-user-token').toLowerCase()
const REQUIRED = process.env.AUTH_REQUIRED === '1'

const jwks = JWKS_URL ? createRemoteJWKSet(new URL(JWKS_URL)) : null

export function authEnabled(): boolean { return jwks !== null }
export function authHeaderName(): string { return HEADER }

async function verify(req: FastifyRequest): Promise<VerifiedUser | null> {
  if (!jwks) return null
  let token = String(req.headers[HEADER] ?? '')
  if (token.toLowerCase().startsWith('bearer ')) token = token.slice(7)
  if (!token) return null
  const { payload } = await jwtVerify(token, jwks, {
    issuer: process.env.AUTH_ISSUER || undefined,
    audience: process.env.AUTH_AUDIENCE || undefined,
  })
  // The platform proxy's sub is user:<username>; strip the prefix.
  const username = String(payload.preferred_username ?? payload.username ?? payload.email ?? payload.sub ?? 'user')
    .replace(/^user:/, '')
  return {
    id: String(payload.sub ?? username),
    username,
    name: typeof payload.name === 'string' ? payload.name : undefined,
    claims: payload as Record<string, unknown>,
  }
}

export async function authHook(app: FastifyInstance): Promise<void> {
  if (!authEnabled()) return
  app.addHook('onRequest', async (req, reply) => {
    if (req.url === '/healthz' || !req.url.startsWith('/api/')) return
    // A mission worker presents its mission token, not a platform JWT.
    // The token authorizes one mission's board and nothing else, and the
    // MCP layer checks it again before exposing any board tool.
    if (req.url.startsWith('/api/mcp')) {
      const auth = String(req.headers.authorization ?? '')
      const tok = auth.startsWith('Bearer ') ? auth.slice(7).trim() : ''
      if (tok) {
        const { taskByToken } = await import('./tasks.js')
        if (taskByToken(tok)) return
      }
    }
    try {
      const user = await verify(req)
      if (user) req.user = user
      else if (REQUIRED) return reply.status(401).send({ error: 'authentication required' })
    } catch (err) {
      req.log.warn({ err }, 'token verification failed')
      if (REQUIRED) return reply.status(401).send({ error: 'invalid token' })
    }
  })
}
