import crypto from 'node:crypto'
import type { FastifyInstance } from 'fastify'
import { invalidateContext } from './chat/context.js'
import { invalidateDbList } from './gufi.js'
import { incrementalIndexDir, indexRootDb } from './indexing.js'
import { invalidateTagMaps } from './tags.js'

/**
 * TRANSITIONAL: the bridge the Go server in front uses while Node still
 * owns the index queue and the caches built from the index. Its file
 * routes (server-go/internal/kb) change files and labels, then ask here
 * for the index job that follows (with its run time, which they answer
 * with) and for those caches to be dropped. It goes away once the Go index
 * package takes the queue and the release runs without Node; nothing else
 * belongs here.
 *
 * Only a request carrying STUDIO_BRIDGE_SECRET (set by the Go server when
 * it starts Node) is answered; without the variable the routes do not
 * exist. The Go server refuses /internal/go/ from clients.
 */

const HEADER = 'x-studio-bridge'

function allowed(given: unknown): boolean {
  const secret = process.env.STUDIO_BRIDGE_SECRET
  if (!secret || typeof given !== 'string') return false
  const a = Buffer.from(given), b = Buffer.from(secret)
  return a.length === b.length && crypto.timingSafeEqual(a, b)
}

export async function goBridgeRoutes(app: FastifyInstance): Promise<void> {
  app.post('/internal/go/index', async (req, reply) => {
    if (!allowed(req.headers[HEADER])) return reply.status(404).send({ error: 'not found' })
    const { op, path: rel = '' } = (req.body ?? {}) as { op?: string; path?: string }
    try {
      const { ms } = op === 'root' ? await indexRootDb() : await incrementalIndexDir(String(rel))
      return { ok: true, ms }
    } catch (e) {
      return { ok: false, error: String((e as Error).message ?? e) }
    }
  })

  app.post('/internal/go/changed', async (req, reply) => {
    if (!allowed(req.headers[HEADER])) return reply.status(404).send({ error: 'not found' })
    const { what = [] } = (req.body ?? {}) as { what?: string[] }
    for (const w of what) {
      if (w === 'dbList') invalidateDbList()
      else if (w === 'context') invalidateContext()
      else if (w === 'tagMaps') invalidateTagMaps()
    }
    return { ok: true }
  })
}
