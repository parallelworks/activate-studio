import type { FastifyInstance } from 'fastify'
import { execFile } from 'node:child_process'
import path from 'node:path'
import fs, { createReadStream } from 'node:fs'
import fsp from 'node:fs/promises'
import { EXCLUDE_DIRS, KB_ROOT, PROJECT_ROOT, gufiAvailable } from './config.js'
import { extractPath, listDir, readFileContent, resolveKb, KbError } from './kb.js'
import { CONVERTIBLE, mimeFor, officeToPdf, removePdfPreview } from './preview.js'
import { blendHits, corpusStats, searchFts, searchNames, searchVector } from './gufi.js'
import { invalidateContext } from './chat/context.js'
import { incrementalIndexDir, indexRootDb, indexStatus, reindexForFile, sweep } from './indexing.js'
import { annotateHits } from './tags.js'
import { effectiveSettings } from './settings.js'

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
  app.get('/api/config', async req => {
    const eff = effectiveSettings()
    // A platform-verified identity (JWT) wins over the static env identity.
    const user = req.user
      ? { id: req.user.id, username: req.user.username, name: req.user.name }
      : {
          id: process.env.APP_USER_ID ?? 'user',
          username: process.env.APP_USERNAME ?? 'user',
          name: process.env.APP_USER_NAME || undefined,
        }
    return {
      appName: eff.appName,
      iconUrl: process.env.APP_ICON && fs.existsSync(process.env.APP_ICON) ? '/api/brand-icon' : null,
      kbLabel: eff.kbLabel,
      theme: eff.theme,
      suggestedPrompts: eff.suggestedPrompts,
      user,
    }
  })

  // Help content: markdown from docs/HELP.md, overridable per deployment
  // via HELP_FILE, with {appName} and {kbLabel} substituted.
  app.get('/api/help', async (_req, reply) => {
    const file = process.env.HELP_FILE ?? path.join(PROJECT_ROOT, 'docs', 'HELP.md')
    let md = ''
    try { md = await fsp.readFile(file, 'utf8') } catch { md = 'No help content configured.' }
    const eff = effectiveSettings()
    md = md
      .replaceAll('{appName}', eff.appName)
      .replaceAll('{kbLabel}', eff.kbLabel)
    return reply.type('text/markdown; charset=utf-8').send(md)
  })

  // Deployment brand icon (APP_ICON env points at an image file).
  app.get('/api/brand-icon', async (_req, reply) => {
    const icon = process.env.APP_ICON
    if (!icon || !fs.existsSync(icon)) throw new KbError(404, 'no brand icon configured')
    reply.header('Content-Type', mimeFor(icon))
    reply.header('Cache-Control', 'public, max-age=3600')
    return reply.send(createReadStream(icon))
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
    reply.header('Content-Type', mimeFor(abs))
    reply.header('Content-Disposition', `attachment; filename="${path.basename(abs)}"`)
    return reply.send(createReadStream(abs))
  })

  // Inline serving of the actual file (images in <img>, PDFs in <iframe>).
  app.get('/api/kb/raw', async (req, reply) => {
    const { path: rel = '' } = req.query as { path?: string }
    const abs = resolveKb(rel)
    reply.header('Content-Type', mimeFor(abs))
    reply.header('Content-Disposition', 'inline')
    return reply.send(createReadStream(abs))
  })

  // Office documents rendered as PDF for the Original preview tab.
  app.get('/api/kb/pdf', async (req, reply) => {
    const { path: rel = '' } = req.query as { path?: string }
    const abs = resolveKb(rel)
    if (!CONVERTIBLE.has(path.extname(abs).toLowerCase())) {
      throw new KbError(415, 'no PDF preview for this file type')
    }
    const pdf = await officeToPdf(abs, rel)
    reply.header('Content-Type', 'application/pdf')
    reply.header('Content-Disposition', 'inline')
    return reply.send(createReadStream(pdf))
  })

  // Bulk removal: unlink every file first, then re-index each touched
  // subtree once, so deleting fifty files costs a handful of reindexes.
  app.post('/api/kb/delete', async req => {
    const body = req.body as { paths?: string[] }
    const paths = (body.paths ?? []).slice(0, 500)
    if (!paths.length) throw new KbError(400, 'paths required')
    const deleted: string[] = []
    const skipped: { path: string; reason: string }[] = []
    const roots = new Set<string>()
    let rootDb = false
    for (const rel of paths) {
      const cleaned = rel.replace(/^\/+|\/+$/g, '')
      try {
        for (const part of cleaned.split('/')) {
          if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'protected path')
        }
        const abs = resolveKb(cleaned)
        const st = await fsp.stat(abs)
        if (!st.isFile()) throw new KbError(400, 'directories are not deletable here')
        await fsp.rm(abs)
        await fsp.rm(extractPath(cleaned), { force: true }).catch(() => {})
        await removePdfPreview(cleaned)
        deleted.push(cleaned)
        if (cleaned.includes('/')) roots.add(cleaned.split('/')[0])
        else rootDb = true
      } catch (err) {
        skipped.push({ path: cleaned, reason: err instanceof KbError ? err.message : 'not found' })
      }
    }
    let indexMs = 0
    for (const root of roots) indexMs += (await incrementalIndexDir(root)).ms
    if (rootDb) indexMs += (await indexRootDb()).ms
    return { deleted, skipped, indexMs }
  })

  // Remove a file from the knowledge base and from the index.
  app.delete('/api/kb/file', async req => {
    const { path: rel = '' } = req.query as { path?: string }
    if (!rel) throw new KbError(400, 'path required')
    for (const part of rel.split('/')) {
      if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'path not deletable')
    }
    const abs = resolveKb(rel)
    const st = await fsp.stat(abs).catch(() => null)
    if (!st) throw new KbError(404, 'not found')
    if (!st.isFile()) throw new KbError(400, 'only files can be deleted')
    await fsp.rm(abs)
    await fsp.rm(extractPath(rel), { force: true }).catch(() => {})
    await removePdfPreview(rel)
    const { ms } = await reindexForFile(rel)
    return { deleted: rel, indexMs: ms }
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
    const { tags } = req.query as { tags?: string }
    const filter = tags ? tags.split(',').filter(Boolean) : undefined
    return { hits: await annotateHits(blendHits(fts, names, vec, n), filter) }
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
