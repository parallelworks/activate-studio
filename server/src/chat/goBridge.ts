import crypto from 'node:crypto'
import type { FastifyInstance } from 'fastify'
import { platformAvailable } from '../workflowsTab.js'
import { cliCredential, credentialContextName, ensureFreshAccess } from '../access.js'
import { GATEWAY_BASE, PW_API_KEY, PW_CLI, gufiAvailable } from '../config.js'
import { executeTool } from './tools.js'
import { buildContext } from '../ragProxy.js'

/**
 * TRANSITIONAL: the chat tools for the Go server in front. The Go server
 * runs POST /api/chat/stream, the tool catalog, the system prompt and the
 * calls held for approval (internal/chat), the RAG proxy (/v1) and
 * /api/mcp; the tools not ported yet and the RAG proxy's retrieval still
 * run here, in tools.ts and ragProxy.ts, and the platform values the loop
 * reads are still access.ts's. Each op is one of the calls the Node route
 * made in-process, with the same inputs. It goes away with the tools' and
 * library search's port and the platform track's merge; nothing else
 * belongs here.
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
    case 'access': {
      if (b.fresh) await ensureFreshAccess(Number(b.fresh) || undefined)
      return { gatewayBase: GATEWAY_BASE, deploymentKey: PW_API_KEY || cliCredential(), contextName: credentialContextName(), platform: platformAvailable(req.user?.id), cli: PW_CLI }
    }
    // A tool not ported to Go yet.
    case 'execute':
      return executeTool(String(b.name), String(b.args ?? ''), b.ctx ?? {})
    // The RAG proxy's retrieval (ragProxy.ts buildContext), which needs
    // library search: available is gufiAvailable(), context the blocks or
    // null when nothing matched.
    case 'ragContext': {
      if (!gufiAvailable()) return { available: false, context: null }
      const tags = Array.isArray(b.tags) ? b.tags.map(String) : []
      return { available: true, context: await buildContext(String(b.query ?? ''), Number(b.topK) || 1, tags) }
    }
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
