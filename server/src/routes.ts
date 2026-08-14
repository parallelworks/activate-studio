import type { FastifyInstance } from 'fastify'
import { execFile } from 'node:child_process'
import path from 'node:path'
import { createReadStream } from 'node:fs'
import { KB_ROOT, PROJECT_ROOT, gufiAvailable } from './config.js'
import { listDir, readFileContent, resolveKb, KbError } from './kb.js'
import { blendHits, corpusStats, searchFts, searchNames, searchVector } from './gufi.js'
import { invalidateContext } from './chat/context.js'
import { incrementalIndexDir, indexStatus, sweep } from './indexing.js'

export async function kbRoutes(app: FastifyInstance): Promise<void> {
  app.setErrorHandler((err: unknown, _req, reply) => {
    if (err instanceof KbError) return reply.status(err.status).send({ error: err.message })
    app.log.error(err)
    const message = err instanceof Error ? err.message : String(err)
    return reply.status(500).send({ error: message })
  })

  app.get('/healthz', async () => ({ ok: true, gufi: gufiAvailable() }))

  // Deployment-specific presentation values live in the environment (or the
  // gitignored .env the deploy script sources), never in the repo.
  app.get('/api/config', async () => {
    let suggestedPrompts: string[] = []
    try { suggestedPrompts = JSON.parse(process.env.SUGGESTED_PROMPTS ?? '[]') } catch { /* ignore bad JSON */ }
    return {
      kbLabel: process.env.KB_LABEL ?? path.basename(KB_ROOT),
      suggestedPrompts,
      user: {
        id: process.env.APP_USER_ID ?? 'user',
        username: process.env.APP_USERNAME ?? 'user',
        name: process.env.APP_USER_NAME || undefined,
      },
    }
  })

  app.get('/api/kb/tree', async req => {
    const { path: rel = '' } = req.query as { path?: string }
    return { path: rel, entries: await listDir(rel) }
  })

  app.get('/api/kb/file', async req => {
    const { path: rel = '' } = req.query as { path?: string }
    return readFileContent(rel)
  })

  app.get('/api/kb/download', async (req, reply) => {
    const { path: rel = '' } = req.query as { path?: string }
    const abs = resolveKb(rel)
    reply.header('Content-Disposition', `attachment; filename="${path.basename(abs)}"`)
    return reply.send(createReadStream(abs))
  })

  app.get('/api/kb/stats', async () => corpusStats())

  app.get('/api/search', async req => {
    const { q = '', limit = '20' } = req.query as { q?: string; limit?: string }
    if (!q.trim()) return { hits: [] }
    if (!gufiAvailable()) return { hits: [], error: 'index not built yet' }
    const n = Math.min(Number(limit) || 20, 50)
    const [fts, names, vec] = await Promise.all([
      searchFts(q, n),
      searchNames(q, 10),
      searchVector(q, Math.min(n, 10)).catch(() => []),
    ])
    return { hits: blendHits(fts, names, vec, n) }
  })

  app.get('/api/index/status', async () => indexStatus())

  app.post('/api/index/dir', async req => {
    const { path: rel = '' } = req.body as { path?: string }
    if (!rel) return { error: 'path required' }
    const { ms } = await incrementalIndexDir(rel)
    return { indexed: rel, ms }
  })

  app.post('/api/index/sweep', async () => {
    const changed = await sweep(m => app.log.info(m))
    return { changed }
  })

  app.post('/api/reindex', async (_req, reply) => {
    const script = path.join(PROJECT_ROOT, 'indexer', 'reindex.sh')
    execFile('bash', [script], { timeout: 15 * 60_000 }, err => {
      if (err) app.log.error({ err }, 'reindex failed')
      else invalidateContext()
    })
    return reply.send({ started: true })
  })

  app.get('/api/workflows', async () => {
    const out = await new Promise<string>((resolve, reject) => {
      execFile('pw', ['workflows', 'ls'], { timeout: 20_000 }, (e, so, se) => e ? reject(new Error(se || e.message)) : resolve(so))
    })
    const rows = out.trim().split('\n').slice(1).map(l => {
      const m = l.trim().match(/^(\S+)\s+(\S+)$/)
      return m ? { name: m[1], type: m[2] } : null
    }).filter(Boolean)
    return { workflows: rows }
  })

  app.get('/api/workflows/:name', async req => {
    const name = (req.params as { name: string }).name.replace(/[^A-Za-z0-9_.-]/g, '')
    const out = await new Promise<string>((resolve, reject) => {
      execFile('pw', ['workflows', 'get', name, '-o', 'json'], { timeout: 20_000 }, (e, so, se) => e ? reject(new Error(se || e.message)) : resolve(so))
    })
    const wf = JSON.parse(out)
    const jobs = wf.yaml?.jobs ?? {}
    const nodes = Object.entries<any>(jobs).map(([id, def]) => ({
      id,
      steps: (def?.steps ?? []).map((s: any) => s?.name ?? 'step'),
    }))
    const edges: { from: string; to: string }[] = []
    for (const [job, def] of Object.entries<any>(jobs)) {
      for (const dep of def?.needs ?? []) edges.push({ from: dep, to: job })
    }
    return { name: wf.name, displayName: wf.displayName, nodes, edges, yaml: wf.yaml }
  })
}
