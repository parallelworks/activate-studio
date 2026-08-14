import type { FastifyInstance, FastifyRequest } from 'fastify'
import { createRemoteJWKSet, jwtVerify } from 'jose'

/**
 * Platform identity: when the app runs behind ACTIVATE, the platform forwards
 * a short-lived JWT in a header, verifiable against the platform JWKS
 * endpoint. Configure with:
 *   AUTH_JWKS_URL   JWKS endpoint (enables verification)
 *   AUTH_HEADER     header carrying the token (default x-pw-access-token;
 *                   "authorization" strips a Bearer prefix)
 *   AUTH_ISSUER     optional expected issuer
 *   AUTH_AUDIENCE   optional expected audience
 *   AUTH_REQUIRED   1 = reject API requests without a valid token
 * Standalone deployments leave AUTH_JWKS_URL unset and nothing changes.
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
const HEADER = (process.env.AUTH_HEADER ?? 'x-pw-access-token').toLowerCase()
const REQUIRED = process.env.AUTH_REQUIRED === '1'

const jwks = JWKS_URL ? createRemoteJWKSet(new URL(JWKS_URL)) : null

export function authEnabled(): boolean { return jwks !== null }

async function verify(req: FastifyRequest): Promise<VerifiedUser | null> {
  if (!jwks) return null
  let token = String(req.headers[HEADER] ?? '')
  if (token.toLowerCase().startsWith('bearer ')) token = token.slice(7)
  if (!token) return null
  const { payload } = await jwtVerify(token, jwks, {
    issuer: process.env.AUTH_ISSUER || undefined,
    audience: process.env.AUTH_AUDIENCE || undefined,
  })
  const username = String(payload.preferred_username ?? payload.username ?? payload.email ?? payload.sub ?? 'user')
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
