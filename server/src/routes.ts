import type { FastifyInstance } from 'fastify'
import { execFile } from 'node:child_process'
import path from 'node:path'
import fs, { createReadStream } from 'node:fs'
import fsp from 'node:fs/promises'
import { EXCLUDE_DIRS, GUFI_INDEX, INDEX_BASE, KB_ROOT, PROJECT_ROOT, gufiAvailable } from './config.js'
import { extractPath, listDir, readFileContent, resolveKb, KbError } from './kb.js'
import { CONVERTIBLE, mimeFor, officeToPdf, pdfPageCount, pdfPagePng, previewPdfFor, removePdfPreview } from './preview.js'
import { blendHits, corpusStats, searchFts, searchNames, searchVector } from './gufi.js'
import { invalidateContext } from './chat/context.js'
import { incrementalIndexDir, indexRootDb, indexStatus, reindexForFile, sweep } from './indexing.js'
import { annotateHits, readTagsBatch } from './tags.js'
import { convertToDynamicForm, dumpYaml, getAllDeps, getStepLabel, workflowHasUserInputs } from '@parallelworks/workflow-parser'
import { removeModelCache } from './model.js'
import { effectiveSettings } from './settings.js'
import { authEnabled, authHeaderName } from './auth.js'

/** A brand image may be given as a URL instead of a path on the resource,
 *  which is easier for a deployment whose branding lives on a web server
 *  and needs no file staged next to the corpus. */
function isUrl(v: string | undefined): v is string {
  return !!v && /^https?:\/\//i.test(v)
}

function faviconFile(): string | null {
  for (const p of [process.env.APP_FAVICON, process.env.APP_ICON]) {
    if (p && !isUrl(p) && fs.existsSync(p)) return p
  }
  return null
}

/** The URL the browser should load for a brand image: an external URL as
 *  given, our own endpoint when a file is staged, or nothing. */
function brandUrl(kind: 'icon' | 'favicon'): string | null {
  const configured = kind === 'icon' ? process.env.APP_ICON : (process.env.APP_FAVICON ?? process.env.APP_ICON)
  if (isUrl(configured)) return configured
  if (kind === 'icon') return configured && fs.existsSync(configured) ? '/api/brand-icon' : null
  return faviconFile() ? '/api/favicon' : null
}

export async function kbRoutes(app: FastifyInstance): Promise<void> {
  app.setErrorHandler((err: unknown, _req, reply) => {
    if (err instanceof KbError) return reply.status(err.status).send({ error: err.message })
    app.log.error(err)
    const message = err instanceof Error ? err.message : String(err)
    return reply.status(500).send({ error: message })
  })

  app.get('/healthz', async () => ({ ok: true, gufi: gufiAvailable() }))

  // Identity introspection: whether THIS request carried a valid platform
  // token, and what it said. The footer shows it so a viewer can confirm
  // the platform is forwarding X-PW-User-Token and verification passes.
  app.get('/api/whoami', async req => {
    const u = req.user
    return {
      authEnabled: authEnabled(),
      header: authHeaderName(),
      verified: !!u,
      user: u ? { id: u.id, username: u.username, name: u.name } : null,
      token: u
        ? {
            issuer: (u.claims.iss as string) ?? null,
            audience: u.claims.aud ?? null,
            issuedAt: typeof u.claims.iat === 'number' ? new Date(u.claims.iat * 1000).toISOString() : null,
            expiresAt: typeof u.claims.exp === 'number' ? new Date(u.claims.exp * 1000).toISOString() : null,
          }
        : null,
      // The viewer's own token payload, so showing all of it is fine.
      claims: u ? u.claims : null,
    }
  })

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
      iconUrl: brandUrl('icon'),
      faviconUrl: brandUrl('favicon'),
      kbLabel: eff.kbLabel,
      theme: eff.theme,
      accent: eff.accent,
      surface: eff.surface,
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

  // Deployment favicon: APP_FAVICON, falling back to the brand icon.
  app.get('/api/favicon', async (_req, reply) => {
    const icon = faviconFile()
    if (!icon) throw new KbError(404, 'no favicon configured')
    reply.header('Content-Type', mimeFor(icon))
    reply.header('Cache-Control', 'public, max-age=3600')
    return reply.send(createReadStream(icon))
  })

  app.get('/api/kb/tree', async req => {
    const { path: rel = '' } = req.query as { path?: string }
    const entries = await listDir(rel)
    // Own labels straight from the filesystem xattrs (one getfattr per
    // listing), so a just-applied label shows without waiting on the index.
    const tagged = await readTagsBatch(entries.map(e => resolveKb(e.path))).catch(() => new Map<string, string[]>())
    for (const e of entries) {
      const t = tagged.get(resolveKb(e.path))
      if (t?.length) (e as typeof e & { tags?: string[] }).tags = t
    }
    return { path: rel, entries }
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
        await removeModelCache(cleaned)
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
  // Recursive directory delete: the tree's context menu offers it with an
  // explicit confirm. Removes the subtree, its caches, and its index slice;
  // the next sweep reconciles whatever this misses.
  app.delete('/api/kb/dir', async req => {
    const { path: rel = '' } = req.query as { path?: string }
    const cleaned = rel.replace(/^\/+|\/+$/g, '')
    if (!cleaned) throw new KbError(400, 'refusing to delete the knowledge base root')
    for (const part of cleaned.split('/')) {
      if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'path not deletable')
    }
    const abs = resolveKb(cleaned)
    const st = await fsp.stat(abs).catch(() => null)
    if (!st) throw new KbError(404, 'not found')
    if (!st.isDirectory()) throw new KbError(400, 'not a directory; use the file delete')
    await fsp.rm(abs, { recursive: true })
    for (const cache of ['extract', 'pdf-preview', 'pdf-pages', 'model-cache']) {
      await fsp.rm(path.join(INDEX_BASE, cache, cleaned), { recursive: true, force: true }).catch(() => {})
    }
    await fsp.rm(path.join(GUFI_INDEX, cleaned), { recursive: true, force: true }).catch(() => {})
    invalidateContext()
    return { deleted: cleaned }
  })

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
    await removeModelCache(rel)
    const { ms } = await reindexForFile(rel)
    return { deleted: rel, indexMs: ms }
  })

  app.get('/api/kb/stats', async () => corpusStats())

  app.get('/api/search', async req => {
    const { q = '', limit = '50' } = req.query as { q?: string; limit?: string }
    if (!q.trim()) return { hits: [] }
    if (!gufiAvailable()) return { hits: [], error: 'index not built yet' }
    const n = Math.min(Number(limit) || 50, 1000)
    const [fts, names, vec] = await Promise.all([
      searchFts(q, n),
      searchNames(q, Math.min(Math.max(10, Math.floor(n / 5)), 50)),
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
    const jobs = (wf.yaml?.jobs ?? {}) as Record<string, { steps?: unknown[]; needs?: string[] }>
    // The platform's own parser names steps and resolves dependencies, so
    // this view matches what ACTIVATE shows and follows it as the workflow
    // schema evolves, rather than reimplementing either.
    const nodes = Object.entries(jobs).map(([id, def]) => ({
      id,
      steps: (def?.steps ?? []).map((s, i) => getStepLabel(s as Parameters<typeof getStepLabel>[0], i)),
      dependsOn: [...getAllDeps(id, jobs)],
    }))
    const edges: { from: string; to: string }[] = []
    for (const [job, def] of Object.entries(jobs)) {
      for (const dep of def?.needs ?? []) edges.push({ from: dep, to: job })
    }
    // The input form the platform would render for this workflow, so a
    // preview can show what a run actually asks for. The converter takes
    // the inputs subtree, not the whole document.
    let form: unknown = null
    const rawInputs = wf.yaml?.on?.execute?.inputs
    try {
      form = rawInputs && workflowHasUserInputs(wf.yaml) ? convertToDynamicForm(rawInputs) : null
    } catch { /* an older or hand-written schema the converter does not accept */ }
    return {
      name: wf.name, displayName: wf.displayName, nodes, edges, yaml: wf.yaml,
      form,
      yamlText: dumpYaml(wf.yaml ?? {}),
    }
  })

  // PDF preview as page images: the browser PDF plugin is unavailable in
  // the platform's sandboxed session iframe, so the client shows PNGs.
  app.get('/api/kb/pdf-info', async req => {
    const { path: rel = '' } = req.query as { path?: string }
    const abs = resolveKb(rel)
    const pdf = await previewPdfFor(abs, rel)
    return { pages: await pdfPageCount(pdf) }
  })

  app.get('/api/kb/pdf-page', async (req, reply) => {
    const { path: rel = '', page = '1' } = req.query as { path?: string; page?: string }
    const n = Math.max(1, Math.floor(Number(page) || 1))
    const abs = resolveKb(rel)
    const png = await pdfPagePng(abs, rel, n)
    reply.header('Content-Type', 'image/png')
    reply.header('Cache-Control', 'public, max-age=300')
    return reply.send(createReadStream(png))
  })
}
