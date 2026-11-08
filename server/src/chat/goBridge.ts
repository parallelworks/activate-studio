import crypto from 'node:crypto'
import type { FastifyInstance } from 'fastify'
import { executeTool } from './tools.js'

/**
 * TRANSITIONAL: the chat tools for the Go server in front. The Go server
 * runs POST /api/chat/stream, the tool catalog, the system prompt and the
 * calls held for approval (internal/chat); the tools not ported yet still
 * run here, in tools.ts, and the platform values the loop reads are still
 * access.ts's. Each op is one of the calls the Node route made in-process,
 * with the same inputs. It goes away with the tools' port and the platform
 * track's merge; nothing else belongs here.
 *
 * Only a request carrying STUDIO_BRIDGE_SECRET is answered, and the Go
 * server refuses /internal/go/ from clients.
 */

const HEADER = 'x-studio-bridge-secret'

function allowed(given: unknown): boolean {
  const secret = process.env.STUDIO_BRIDGE_SECRET
  if (!secret || typeof given !== 'string') return false
  const a = Buffer.from(given), b = Buffer.from(secret)
  return a.length === b.length && crypto.timingSafeEqual(a, b)
}

type Body = Record<string, any>

async function op(name: string, b: Body, req: any): Promise<unknown> {
  switch (name) {
    // The platform values the loop reads from the pw CLI's context, which
    // Node still owns (access.ts): the gateway, the CLI credential, whether
    // a platform answers for this viewer, and the pw CLI chosen.
    // A tool not ported to Go yet.
    case 'execute':
      return executeTool(String(b.name), String(b.args ?? ''), b.ctx ?? {})
  }
  throw new Error(`unknown op ${name}`)
}

export async function goChatBridgeRoutes(app: FastifyInstance): Promise<void> {
  app.post('/internal/go/chat/:op', async (req, reply) => {
    if (!allowed(req.headers[HEADER])) return reply.status(404).send({ error: 'not found' })
    try {
      return await op(String((req.params as { op: string }).op), (req.body ?? {}) as Body, req)
    } catch (e) {
      return reply.status(500).send({ error: String((e as Error).message ?? e) })
    }
  })
}
