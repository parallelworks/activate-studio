import type { FastifyInstance } from 'fastify'
import { execFile } from 'node:child_process'
import path from 'node:path'
import fs, { createReadStream } from 'node:fs'
import fsp from 'node:fs/promises'
import { EXCLUDE_DIRS, GATEWAY_BASE, GUFI_INDEX, INDEX_BASE, KB_ROOT, PROJECT_ROOT, PW_CLI, gufiAvailable } from './config.js'
import { KbError, extractPath, listDir, moveCacheEntry, readFileContent, resolveKb } from './kb.js'
import { CONVERTIBLE, mimeFor, officeToPdf, OfficePreviewUnavailable, pdfPageCount, pdfPagePng, pdfPreviewPaths, previewPdfFor, removePdfPreview } from './preview.js'
import { blendHits, corpusStats, searchFts, searchNames, searchVector } from './gufi.js'
import { invalidateContext } from './chat/context.js'
import { extractorReport, getIndexJob, incrementalIndexDir, indexRootDb, indexStatus, reindexForFile, startIndexJob, sweep } from './indexing.js'
import { copyPaths, movePaths, reindexRoots, renamePath } from './move.js'
import { suggestLabels } from './labeling.js'
import { resolveUserCred } from './credentials.js'
import { listModels } from './chat/gateway.js'
import { getJob, startJob } from './jobs.js'
import { buildInfo } from './version.js'
import { annotateHits, readTagsBatch } from './tags.js'
import { gatewayKey } from './chat/gateway.js'
import { convertToDynamicForm, dumpYaml, getAllDeps, getStepLabel, workflowHasUserInputs } from '@parallelworks/workflow-parser'
import { modelCachePath, removeModelCache } from './model.js'
import { effectiveSettings } from './settings.js'
import { authEnabled, authHeaderName } from './auth.js'

/** A brand image may be given as a URL instead of a path on the resource,
 *  which is easier for a deployment whose branding lives on a web server
 *  and needs no file staged next to the corpus. */
function isUrl(v: string | undefined): v is string {
  return !!v && /^https?:\/\//i.test(v)
}

function faviconFile(): string | null {
  for (const p of [process.env.APP_FAVICON, effectiveSettings().iconUrl || undefined, process.env.APP_ICON]) {
    if (p && !isUrl(p) && fs.existsSync(p)) return p
  }
  return null
}

/**
 * Brand images are always served from our own endpoint, never linked
 * directly. A platform URL (a blob, say) needs a credential the browser
 * cannot send from this origin, so the server fetches it instead and hands
 * the bytes back; a staged file is streamed the same way. Cached briefly so
 * the page does not refetch it on every load.
 */
const remoteIcon = new Map<string, { body: Buffer; type: string; at: number }>()
const REMOTE_ICON_TTL = 10 * 60_000

async function fetchRemoteIcon(url: string): Promise<{ body: Buffer; type: string } | null> {
  const hit = remoteIcon.get(url)
  if (hit && Date.now() - hit.at < REMOTE_ICON_TTL) return hit
  const attempt = async (auth: boolean) => {
    const key = auth ? gatewayKey() : ''
    if (auth && !key) return null
    const res = await fetch(url, {
      headers: auth ? { Authorization: `Bearer ${key}` } : undefined,
      signal: AbortSignal.timeout(15_000),
    })
    if (!res.ok) return null
    return {
      body: Buffer.from(await res.arrayBuffer()),
      type: res.headers.get('content-type') ?? 'image/png',
    }
  }
  // Public images need no credential; a platform blob answers 401 without
  // one, so fall back to the deployment's.
  const got = await attempt(false).catch(() => null) ?? await attempt(true).catch(() => null)
  if (got) remoteIcon.set(url, { ...got, at: Date.now() })
  return got
}

/** The URL the browser should load for a brand image: our own endpoint
 *  whenever one is configured, or nothing. */
/** The image configured for a theme: the dark variant when one is set and
 *  dark is asked for, otherwise the ordinary one. A deployment whose mark is
 *  dark ink needs a second file to stay visible on a dark background. */
function configuredIcon(dark: boolean): string | undefined {
  const s = effectiveSettings()
  if (dark) return (s.iconUrlDark || process.env.APP_ICON_DARK) || s.iconUrl || process.env.APP_ICON
  return s.iconUrl || process.env.APP_ICON
}

function brandUrl(kind: 'icon' | 'favicon', dark = false): string | null {
  const configured = kind === 'favicon' && process.env.APP_FAVICON && !dark
    ? process.env.APP_FAVICON
    : configuredIcon(dark)
  const suffix = dark ? '?variant=dark' : ''
  if (isUrl(configured)) return (kind === 'icon' ? '/api/brand-icon' : '/api/favicon') + suffix
  if (!configured || !fs.existsSync(configured)) return null
  return (kind === 'icon' ? '/api/brand-icon' : '/api/favicon') + suffix
}

/** Sanitizing error handler for the WHOLE app: KbError text is deliberate
 *  and passes through; schema-validation messages are safe; anything else
 *  can carry exec detail (spawned binary paths, child stderr, command
 *  lines) and is returned as a generic message with a reference id that
 *  the server log holds the detail under. Register at the app level in
 *  main.ts: a Fastify error handler set inside one plugin does not cover
 *  routes registered by the others.
 */
export function sanitizedErrorHandler(app: FastifyInstance) {
  return (err: unknown, _req: unknown, reply: { status: (n: number) => { send: (b: unknown) => unknown } }) => {
    if (err instanceof KbError) return reply.status(err.status).send({ error: err.message })
    if ((err as { validation?: unknown })?.validation) {
      return reply.status(400).send({ error: err instanceof Error ? err.message : 'invalid request' })
    }
    const errorId = Math.random().toString(36).slice(2, 10)
    app.log.error({ err, errorId }, 'unhandled route error')
    return reply.status(500).send({ error: `internal error (ref ${errorId})` })
  }
}

export async function kbRoutes(app: FastifyInstance): Promise<void> {

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
      iconUrlDark: brandUrl('icon', true),
      faviconUrl: brandUrl('favicon'),
      faviconUrlDark: brandUrl('favicon', true),
      kbLabel: eff.kbLabel,
      theme: eff.theme,
      accent: eff.accent,
      surface: eff.surface,
      suggestedPrompts: eff.suggestedPrompts,
      bannerText: eff.bannerText,
      bannerColor: eff.bannerColor,
      bannerWhenEmbedded: eff.bannerWhenEmbedded,
      user,
    }
  })

  // Which build is serving. Deliberately its own route and free of auth
  // concerns: checking that a deploy landed should be one curl, and it
  // discloses nothing a user of the app cannot already see.
  app.get('/api/version', async () => buildInfo())

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
  app.get('/api/brand-icon', async (req, reply) => {
    const icon = configuredIcon(String((req.query as { variant?: string }).variant ?? '') === 'dark')
    if (isUrl(icon)) {
      const got = await fetchRemoteIcon(icon)
      if (!got) throw new KbError(502, 'could not load the configured brand image')
      reply.header('Content-Type', got.type)
      reply.header('Cache-Control', 'public, max-age=600')
      return reply.send(got.body)
    }
    if (!icon || !fs.existsSync(icon)) throw new KbError(404, 'no brand icon configured')
    reply.header('Content-Type', mimeFor(icon))
    reply.header('Cache-Control', 'public, max-age=3600')
    return reply.send(createReadStream(icon))
  })

  // Deployment favicon: APP_FAVICON, falling back to the brand icon.
  app.get('/api/favicon', async (req, reply) => {
    const wantDark = String((req.query as { variant?: string }).variant ?? '') === 'dark'
    const configured = (!wantDark && process.env.APP_FAVICON) || configuredIcon(wantDark)
    if (isUrl(configured)) {
      const got = await fetchRemoteIcon(configured)
      if (!got) throw new KbError(502, 'could not load the configured favicon')
      reply.header('Content-Type', got.type)
      reply.header('Cache-Control', 'public, max-age=600')
      return reply.send(got.body)
    }
    const icon = configured && fs.existsSync(configured) ? configured : faviconFile()
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
  // Corpus HTML rendered under a hard sandbox: inline scripts and styles
  // run, nothing loads over the network and the page has no origin
  // privileges, which is what makes model-authored interactive pages
  // (parametric viewers and the like) safe to display.
  app.get('/api/kb/html', async (req, reply) => {
    const { path: rel = '' } = req.query as { path?: string }
    const abs = resolveKb(rel)
    if (!/\.html?$/i.test(abs)) throw new KbError(415, 'not an html file')
    reply.header('Content-Type', 'text/html; charset=utf-8')
    reply.header('Content-Security-Policy', "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:")
    return reply.send(createReadStream(abs))
  })

  app.get('/api/kb/pdf', async (req, reply) => {
    const { path: rel = '' } = req.query as { path?: string }
    const abs = resolveKb(rel)
    if (!CONVERTIBLE.has(path.extname(abs).toLowerCase())) {
      throw new KbError(415, 'no PDF preview for this file type')
    }
    let pdf: string
    try {
      pdf = await officeToPdf(abs, rel)
    } catch (e) {
      // 415 rather than 500: the file is fine, this host cannot render it.
      // The viewer offers the extracted text instead.
      if (e instanceof OfficePreviewUnavailable) throw new KbError(415, e.message)
      throw e
    }
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

  // Propose labels for material that has none. Nothing is applied here;
  // the reviewer decides, and applying runs through the ordinary tag path.
  app.post('/api/kb/label-suggestions', async req => {
    const body = req.body as { dir?: string; limit?: number; includeLabelled?: boolean; model?: string }
    const eff = effectiveSettings()
    const cred = resolveUserCred(req.user?.id)
    let model = String(body.model ?? '') || eff.ragDefaultModel
    if (!model) {
      // No default set: use the first model the caller's credential can
      // reach, so labelling works without configuring anything.
      const wire = await listModels(cred?.key, cred?.baseUrl).catch(() => null) as { data?: { id: string }[]; models?: { id: string }[] } | null
      const first = (wire?.data ?? wire?.models ?? []).map(m => m?.id).filter(Boolean)[0]
      if (!first) throw new KbError(400, 'No model is available to this deployment for labelling. Add your model credential in Settings, Model access.')
      model = first
    }
    try {
      return await suggestLabels({
        dir: body.dir ?? '',
        limit: body.limit,
        includeLabelled: body.includeLabelled,
        model,
        key: cred?.key ?? null,
        baseUrl: cred?.baseUrl ?? null,
      })
    } catch (e) {
      const detail = String((e as Error).message ?? e)
      throw new KbError(502, cred
        ? `The model refused the request (${detail.slice(0, 160)}). Try a different model on the RAG endpoint settings page.`
        : `No personal model credential is loaded, and the deployment credential was refused (${detail.slice(0, 160)}). Add your key in Settings, Model access.`)
    }
  })

  // Every directory in the corpus, for choosing a move destination.
  app.get('/api/kb/dirs', async () => {
    const out: string[] = []
    const walk = async (rel: string, depth: number): Promise<void> => {
      if (depth > 8) return
      let entries
      try { entries = await fsp.readdir(resolveKb(rel), { withFileTypes: true }) } catch { return }
      for (const e of entries.sort((a, b) => a.name.localeCompare(b.name))) {
        if (!e.isDirectory() || e.name.startsWith('.') || EXCLUDE_DIRS.has(e.name)) continue
        const child = rel ? `${rel}/${e.name}` : e.name
        out.push(child)
        if (out.length > 2000) return
        await walk(child, depth + 1)
      }
    }
    await walk('', 0)
    return { dirs: out }
  })

  // Move files and directories inside the corpus, so the tree can be
  // reorganised without going back to a shell. Both ends are re-indexed:
  // material that moved is not new, but the index records where it lives.
  app.post('/api/kb/move', async req => {
    const body = req.body as { paths?: string[]; dest?: string; async?: boolean }
    if (body.async) {
      const paths = (body.paths ?? []).slice(0, 500)
      return startJob('move', paths.length, async h => {
        const r = await movePaths(paths, String(body.dest ?? ''), (done, current) => {
          h.progress({ phase: 'moving', done, current })
        })
        h.progress({ phase: 'indexing', done: paths.length, current: '' })
        r.indexMs = await reindexRoots(r.roots, r.rootDb)
        return r
      })
    }
    const paths = (body.paths ?? []).slice(0, 500)
    const r = await movePaths(paths, String(body.dest ?? ''))
    const indexMs = await reindexRoots(r.roots, r.rootDb)
    return { moved: r.moved, skipped: r.skipped, indexMs }
  })

  // Copy files and directories into another directory.
  app.post('/api/kb/copy', async req => {
    const body = req.body as { paths?: string[]; dest?: string; async?: boolean }
    const paths = (body.paths ?? []).slice(0, 500)
    if (body.async) {
      return startJob('move', paths.length, async h => {
        const r = await copyPaths(paths, String(body.dest ?? ''), (done, current) => h.progress({ phase: 'copying', done, current }))
        h.progress({ phase: 'indexing', done: paths.length, current: '' })
        r.indexMs = await reindexRoots(r.roots, r.rootDb)
        return r
      })
    }
    const r = await copyPaths(paths, String(body.dest ?? ''))
    const indexMs = await reindexRoots(r.roots, r.rootDb)
    return { copied: r.moved, skipped: r.skipped, indexMs }
  })

  // Rename a file or directory in place.
  app.post('/api/kb/rename', async req => {
    const body = req.body as { path?: string; name?: string }
    const r = await renamePath(String(body.path ?? ''), String(body.name ?? ''))
    const indexMs = await reindexRoots(r.roots, r.rootDb)
    return { renamed: r.moved[0] ?? null, indexMs }
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

  // What this host can extract text from, so a format that indexes by
  // filename alone is visible rather than a silent gap.
  app.get('/api/index/extractors', async () => ({ extractors: extractorReport() }))

  app.post('/api/index/dir', async req => {
    const { path: rel = '' } = req.body as { path?: string }
    if (!rel) return { error: 'path required' }
    const { ms } = await incrementalIndexDir(rel)
    return { indexed: rel, ms }
  })

  // Start an indexing pass and watch it, so a bulk upload can show the
  // phase it is in rather than holding a request open.
  app.post('/api/index/job', async req => {
    const { path: rel = '' } = req.body as { path?: string }
    return startIndexJob(String(rel).replace(/^\/+|\/+$/g, ''))
  })

  app.get('/api/jobs/:id', async (req, reply) => {
    const job = getJob(Number((req.params as { id: string }).id))
    if (!job) return reply.status(404).send({ error: 'no such job' })
    return job
  })

  app.get('/api/index/job/:id', async (req, reply) => {
    const job = getIndexJob(Number((req.params as { id: string }).id))
    if (!job) return reply.status(404).send({ error: 'no such job' })
    return job
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

  /**
   * Workflow listing and DAGs come from the platform REST API using the
   * deployment's own token, with the pw CLI as the fallback.
   *
   * The CLI was the only path, and it fails wherever the host has no
   * authenticated context, which is the normal state of a login node
   * nobody has run `pw auth` on. That surfaced as a 500 from the DAG
   * viewer naming neither the CLI nor the credential. The Studio already
   * holds a platform token for the model gateway and the platform serves
   * both the list and each workflow's yaml, so the token is the better
   * primary and the CLI now only covers a deployment whose credential is
   * the CLI's own login.
   */
  const platformApi = async (path: string, userId?: string): Promise<unknown | null> => {
    // The caller's own platform token first: a bring-your-own-key
    // deployment has no credential of its own, and its users do. A custom
    // provider key is never a platform token, so one carrying a baseUrl is
    // skipped rather than sent to the platform.
    const cred = userId ? resolveUserCred(userId) : null
    const key = (cred && !cred.baseUrl ? cred.key : null) || gatewayKey()
    if (!key) return null
    let host: string
    try { host = new URL(GATEWAY_BASE).origin } catch { return null }
    const res = await fetch(`${host}${path}`, {
      headers: { Authorization: `Bearer ${key}` },
      signal: AbortSignal.timeout(20_000),
    })
    if (!res.ok) return null
    return res.json()
  }

  app.get('/api/workflows', async req => {
    const viaApi = await platformApi('/api/workflows', req.user?.id).catch(() => null)
    if (viaApi) {
      const rows = (Array.isArray(viaApi) ? viaApi : (viaApi as { workflows?: unknown[] }).workflows ?? [])
        .map((w: any) => ({ name: String(w?.name ?? ''), type: String(w?.type ?? '') }))
        .filter((w: { name: string }) => w.name)
      return { workflows: rows }
    }
    const out = await new Promise<string>((resolve, reject) => {
      execFile(PW_CLI, ['workflows', 'ls'], { timeout: 20_000 }, (e, so, se) => e ? reject(new Error(se || e.message)) : resolve(so))
    }).catch(e => {
      throw new KbError(502, `Cannot list workflows: no platform credential available (this deployment has none, and you have not added your own under Settings, Model access), and the pw CLI on this host is not usable (${String(e.message).slice(0, 120)}). Authenticate the CLI here, or set PW_API_KEY.`)
    })
    const rows = out.trim().split('\n').slice(1).map(l => {
      const m = l.trim().match(/^(\S+)\s+(\S+)$/)
      return m ? { name: m[1], type: m[2] } : null
    }).filter(Boolean)
    return { workflows: rows }
  })

  app.get('/api/workflows/:name', async req => {
    const name = (req.params as { name: string }).name.replace(/[^A-Za-z0-9_.-]/g, '')
    const viaApi = await platformApi(`/api/workflows/${encodeURIComponent(name)}`, req.user?.id).catch(() => null)
    const wf: any = viaApi ?? JSON.parse(await new Promise<string>((resolve, reject) => {
      execFile(PW_CLI, ['workflows', 'get', name, '-o', 'json'], { timeout: 20_000 }, (e, so, se) => e ? reject(new Error(se || e.message)) : resolve(so))
    }).catch(e => {
      throw new KbError(502, `Cannot read workflow ${name}: no platform credential available (this deployment has none, and you have not added your own under Settings, Model access), and the pw CLI on this host is not usable (${String(e.message).slice(0, 120)}). Authenticate the CLI here, or set PW_API_KEY.`)
    }))
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
