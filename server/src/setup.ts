import { createHash } from 'node:crypto'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { authEnabled } from './auth.js'
import { EXCLUDE_DIRS, GUFI_BIN, GUFI_INDEX, HOST, INDEX_BASE, KB_ROOT, PORT } from './config.js'
import { corpusStats, invalidateDbList } from './gufi.js'
import { getIndexJob, incrementalIndexDir, indexRootDb, saveSweepState, startIndexJob, sweepSnapshot, type IndexJob, type IndexProgress } from './indexing.js'
import { KbError } from './kb.js'
import { listLibraries, publicLibrary } from './libraries.js'
import { canRestart, launcherConfigFile, readLauncherSettings, requestRestart, saveLauncherSetting } from './lifecycle.js'
import { brandUrl } from './routes.js'
import { effectiveSettings, loadSettings, updateSettings } from './settings.js'

/**
 * First-run setup for a single-user install (the macOS app): the Studio's
 * icon, which folder is the primary library, a build of its whole index,
 * the port it listens on, and a record that setup is done. Everything but
 * the status refuses where users sign in; a hosted Studio is configured by
 * its deployment, and listing the server's folders is for the person whose
 * computer it is.
 */

const EMBED_MODEL = path.join(INDEX_BASE, 'models', 'minilm384.gguf')
// The folder the caches under INDEX_BASE were built from.
const ROOT_MARK = path.join(INDEX_BASE, 'indexed-root')

let setupJobId: number | null = null

function requireSingleUser(): void {
  if (authEnabled()) throw new KbError(403, 'setup is for a single-user install; this Studio signs users in')
}

/** A path as typed: ~ is the home folder, and a relative path is taken from it. */
export function expandPath(p: string): string {
  const home = os.homedir()
  if (p === '~') return home
  if (p.startsWith('~/')) return path.join(home, p.slice(2))
  return path.resolve(home, p)
}

function writable(dir: string): boolean {
  try { fs.accessSync(dir, fs.constants.W_OK); return true } catch { return false }
}

function isDir(p: string): boolean {
  try { return fs.statSync(p).isDirectory() } catch { return false }
}

function realOr(p: string): string {
  try { return fs.realpathSync(p) } catch { return path.resolve(p) }
}

const within = (parent: string, child: string) => child === parent || child.startsWith(parent + path.sep)

function gufiFound(): boolean {
  return fs.existsSync(path.join(GUFI_BIN, 'gufi_dir2index')) && fs.existsSync(path.join(GUFI_BIN, 'gufi_query'))
}

function runningJob(): IndexJob | null {
  const job = setupJobId === null ? null : getIndexJob(setupJobId)
  return job && (job.state === 'queued' || job.state === 'running') ? job : null
}

/** Directories directly under the primary folder that indexing covers. */
function topLevelDirs(): string[] {
  let entries: fs.Dirent[] = []
  try { entries = fs.readdirSync(KB_ROOT, { withFileTypes: true }) } catch { return [] }
  return entries
    .filter(e => e.isDirectory() && !e.name.startsWith('.') && !EXCLUDE_DIRS.has(e.name))
    .map(e => e.name)
    .sort()
}

/**
 * Caches under INDEX_BASE are keyed by the path inside the folder, so after
 * a switch to another folder an entry could stand in for a same-named file
 * there. A build for a different folder than last time starts them over.
 */
async function forgetOtherFolder(): Promise<void> {
  let was = ''
  try { was = (await fsp.readFile(ROOT_MARK, 'utf8')).trim() } catch { /* first build */ }
  if (was && was !== KB_ROOT) {
    for (const d of ['extract', 'pdf-preview', 'model-cache']) {
      await fsp.rm(path.join(INDEX_BASE, d), { recursive: true, force: true })
    }
    await fsp.rm(path.join(INDEX_BASE, 'gufi', path.basename(was)), { recursive: true, force: true })
    await fsp.rm(GUFI_INDEX, { recursive: true, force: true })
  }
  await fsp.mkdir(INDEX_BASE, { recursive: true })
  await fsp.writeFile(ROOT_MARK, `${KB_ROOT}\n`)
}

const message = (e: unknown) => String((e as Error)?.message ?? e).slice(0, 300)

/** Whether a port is free where the Studio listens. */
export function portFree(port: number, host = HOST): Promise<boolean> {
  return new Promise(resolve => {
    const probe = net.createServer()
    probe.once('error', () => resolve(false))
    probe.listen({ port, host, exclusive: true }, () => probe.close(() => resolve(true)))
  })
}

/** The port saved for the next start, when it is not the one in use. */
function nextPort(): number | null {
  const saved = Number(readLauncherSettings().PORT)
  return Number.isInteger(saved) && saved > 0 && saved !== PORT ? saved : null
}

// ---- the Studio's icon ----

const BRANDING = path.join(INDEX_BASE, 'branding')
export const ICON_MAX_BYTES = 1024 * 1024

/** The image type by its first bytes, not its name: png, jpg, webp, or an
 *  SVG document. */
export function iconType(body: Buffer): 'png' | 'jpg' | 'webp' | 'svg' | null {
  if (body.length >= 8 && body.readUInt32BE(0) === 0x89504e47 && body.readUInt32BE(4) === 0x0d0a1a0a) return 'png'
  if (body.length >= 3 && body[0] === 0xff && body[1] === 0xd8 && body[2] === 0xff) return 'jpg'
  if (body.length >= 12 && body.toString('latin1', 0, 4) === 'RIFF' && body.toString('latin1', 8, 12) === 'WEBP') return 'webp'
  if (body.includes(0)) return null
  const text = body.toString('utf8').replace(/^\uFEFF/, '').trimStart()
  return text.startsWith('<') && /<svg[\s>]/i.test(text) ? 'svg' : null
}

/**
 * Why an SVG cannot be a brand icon, or null when it can. The icon is shown
 * in <img>, where nothing in it runs, and served with a policy that blocks
 * scripts besides; anything active or loaded from elsewhere is refused
 * here too, so the file kept is a plain drawing.
 */
export function svgProblem(svg: string): string | null {
  const checks: [RegExp, string][] = [
    [/<script\b/i, 'a script'],
    [/<foreignObject\b/i, 'a foreignObject'],
    [/<(?:iframe|embed|object)\b/i, 'embedded content'],
    [/<!(?:ENTITY|DOCTYPE)\b/i, 'a DOCTYPE or entity'],
    [/<\?xml-stylesheet\b/i, 'a stylesheet reference'],
    [/\son[a-z]+\s*=/i, 'an event handler'],
    [/javascript:/i, 'a javascript: link'],
    [/@import\b/i, 'a CSS import'],
    [/\bhref\s*=(?!\s*["']?\s*(?:#|data:image\/(?:png|jpe?g|gif|webp);))/i, 'a link to another file'],
    [/url\((?!\s*["']?\s*(?:#|data:image\/(?:png|jpe?g|gif|webp);))/i, 'a reference to another file'],
    [/<(?:set|animate)\b[^>]*attributeName\s*=\s*["']?(?:xlink:)?href/i, 'an animated link'],
  ]
  const hit = checks.find(([re]) => re.test(svg))
  return hit ? `This SVG contains ${hit[1]}. Export it as a plain SVG, or as a PNG.` : null
}

/** The icons set for the Studio, as the browser loads them: the light one,
 *  and the dark one only when one is set of its own. */
function brandingState() {
  const eff = effectiveSettings()
  return {
    light: eff.iconUrl ? brandUrl('icon') : null,
    dark: eff.iconUrlDark ? brandUrl('icon', true) : null,
    iconUrl: eff.iconUrl,
    iconUrlDark: eff.iconUrlDark,
  }
}

/** Icon files no setting names any more. */
async function dropUnusedIcons(): Promise<void> {
  const s = loadSettings()
  const keep = new Set([s.iconUrl, s.iconUrlDark].filter(Boolean))
  let names: string[] = []
  try { names = await fsp.readdir(BRANDING) } catch { return }
  for (const n of names) {
    const f = path.join(BRANDING, n)
    if (/^icon-[0-9a-f]{16}\.(png|jpg|webp|svg)$/.test(n) && !keep.has(f)) await fsp.rm(f, { force: true })
  }
}

const variantOf = (q: unknown): 'iconUrl' | 'iconUrlDark' =>
  String((q as { variant?: string } | null)?.variant ?? '') === 'dark' ? 'iconUrlDark' : 'iconUrl'

/** The whole primary folder: its own files, then each top-level directory,
 *  through the same passes (and lock) as any other indexing. */
async function buildWholeFolder(job: IndexJob): Promise<{ ms: number }> {
  const t0 = Date.now()
  await forgetOtherFolder()
  // Taken first, so a change made during the build is still new to the sweep.
  const snapshot = await sweepSnapshot()
  const dirs = topLevelDirs()
  const progress: IndexProgress = { phase: 'files', done: 0, total: dirs.length + 1, current: null, failed: [] }
  job.progress = progress
  try { await indexRootDb() } catch (e) {
    progress.failed.push(`(files at the top): ${message(e)}`)
    delete snapshot['.']
  }
  progress.done = 1
  progress.phase = 'directories'
  for (const d of dirs) {
    progress.current = d
    try { await incrementalIndexDir(d) } catch (e) {
      progress.failed.push(`${d}: ${message(e)}`)
      for (const k of Object.keys(snapshot)) if (k === d || k.startsWith(`${d}/`)) delete snapshot[k]
    }
    progress.done++
  }
  progress.phase = 'finishing'
  progress.current = null
  // Index trees for directories that are no longer there.
  const keep = new Set(dirs)
  let entries: fs.Dirent[] = []
  try { entries = await fsp.readdir(GUFI_INDEX, { withFileTypes: true }) } catch { /* nothing built */ }
  for (const e of entries) {
    if (e.isDirectory() && !keep.has(e.name)) await fsp.rm(path.join(GUFI_INDEX, e.name), { recursive: true, force: true })
  }
  invalidateDbList()
  await saveSweepState(snapshot)
  progress.phase = 'done'
  if (progress.failed.length === progress.total) throw new Error(`nothing could be indexed: ${progress.failed[0]}`)
  return { ms: Date.now() - t0 }
}

export async function setupRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/setup/status', async req => {
    // A hosted Studio never runs setup, and its paths stay on the server.
    if (authEnabled()) return { firstRun: false, standalone: false, canRestart: false }
    const eff = effectiveSettings()
    const indexed = fs.existsSync(path.join(GUFI_INDEX, 'db.db'))
    // Counting walks the index, so only when asked (the wizard), not on
    // every page load.
    let files: number | undefined
    if (indexed && (req.query as { files?: string }).files === '1') {
      files = (await corpusStats().catch(() => null))?.files
    }
    const found = gufiFound()
    const model = fs.existsSync(EMBED_MODEL)
    return {
      // A Studio the platform deploys is configured there, sign-in or not.
      firstRun: !eff.setupDone && !process.env.PW_PLATFORM_HOST,
      standalone: true,
      canRestart: canRestart() && !!launcherConfigFile(),
      launcher: !!launcherConfigFile(),
      // The port the Studio listens on; its MCP endpoint is on the same one.
      port: PORT,
      portNext: nextPort(),
      primary: {
        path: KB_ROOT,
        exists: isDir(KB_ROOT),
        writable: writable(KB_ROOT),
        indexed,
        ...(files !== undefined ? { files } : {}),
      },
      gufi: { found, bin: GUFI_BIN, semantic: found && model && fs.existsSync(path.join(GUFI_BIN, 'gufi_sqlite3')) },
      embedModel: { path: EMBED_MODEL, found: model },
      branding: brandingState(),
      libraries: listLibraries(eff.kbLabel, req.user?.id ?? null).map(publicLibrary),
      job: runningJob(),
    }
  })

  // Folders only, for picking the primary folder.
  app.get('/api/setup/browse', async req => {
    requireSingleUser()
    const raw = String((req.query as { path?: string }).path ?? '').trim()
    const abs = expandPath(raw || '~')
    let st: fs.Stats
    try { st = await fsp.stat(abs) } catch { throw new KbError(404, `no such folder: ${abs}`) }
    if (!st.isDirectory()) throw new KbError(400, `not a folder: ${abs}`)
    let entries: fs.Dirent[]
    try { entries = await fsp.readdir(abs, { withFileTypes: true }) } catch (e) {
      throw new KbError(403, `cannot read ${abs} (${(e as NodeJS.ErrnoException).code ?? 'error'})`)
    }
    const dirs: { name: string; path: string }[] = []
    for (const e of entries) {
      if (e.name.startsWith('.')) continue
      let dir = e.isDirectory()
      if (!dir && e.isSymbolicLink()) dir = isDir(path.join(abs, e.name))
      if (dir) dirs.push({ name: e.name, path: path.join(abs, e.name) })
    }
    dirs.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true }))
    const parent = path.dirname(abs)
    return { path: abs, parent: parent === abs ? null : parent, writable: writable(abs), dirs: dirs.slice(0, 2000) }
  })

  // KB_ROOT is read once at startup, so a new folder takes a restart: the
  // launcher's settings file carries it into the next start.
  app.post('/api/setup/primary-folder', async req => {
    requireSingleUser()
    const raw = String((req.body as { path?: string } | null)?.path ?? '').trim()
    if (!raw) throw new KbError(400, 'path required')
    const abs = expandPath(raw)
    if (!fs.existsSync(abs)) throw new KbError(400, `no such folder: ${abs}`)
    if (!isDir(abs)) throw new KbError(400, `not a folder: ${abs}`)
    if (!writable(abs)) throw new KbError(400, `${abs} is not writable`)
    const real = realOr(abs)
    const index = realOr(INDEX_BASE)
    if (within(real, index) || within(index, real)) {
      throw new KbError(400, `${abs} holds the Studio's own index (${INDEX_BASE}); choose a folder that does not contain it`)
    }
    if (real === realOr(KB_ROOT)) return { saved: true, restarting: false, note: 'This folder is already in use.' }
    if (runningJob()) throw new KbError(409, 'an index build is running; wait for it to finish')
    if (!launcherConfigFile()) {
      return { saved: false, restarting: false, note: `Set KB_ROOT=${abs} in the Studio's environment and restart it.` }
    }
    saveLauncherSetting('KB_ROOT', abs)
    const restarting = requestRestart()
    return restarting
      ? { saved: true, restarting: true }
      : { saved: true, restarting: false, note: 'Saved. Restart the Studio to open this folder.' }
  })

  // PORT is read once at startup too, so a new port is saved for the
  // launcher and takes a restart, which serves the Studio and its MCP
  // endpoint (/api/mcp) at the new address.
  app.post('/api/setup/port', async req => {
    requireSingleUser()
    const port = Number((req.body as { port?: unknown } | null)?.port)
    if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new KbError(400, 'the port must be a whole number from 1024 to 65535')
    if (port === PORT) {
      if (launcherConfigFile() && nextPort() !== null) saveLauncherSetting('PORT', String(PORT))
      return { saved: true, restarting: false, port, note: 'The Studio already uses this port.' }
    }
    if (!launcherConfigFile()) {
      return { saved: false, restarting: false, port, note: `Set PORT=${port} in the Studio's environment and restart it.` }
    }
    if (runningJob()) throw new KbError(409, 'an index build is running; wait for it to finish')
    if (!(await portFree(port))) throw new KbError(409, `port ${port} is in use by another program`)
    saveLauncherSetting('PORT', String(port))
    const restarting = requestRestart()
    return restarting
      ? { saved: true, restarting: true, port }
      : { saved: true, restarting: false, port, note: 'Saved. Restart the Studio to use this port.' }
  })

  // One job over the whole primary folder; its progress is read through
  // GET /api/index/job/:id like any other index job.
  app.post('/api/setup/index', async () => {
    requireSingleUser()
    const running = runningJob()
    if (running) return running
    if (!gufiFound()) throw new KbError(409, `GUFI is not installed: no gufi_dir2index in ${GUFI_BIN}`)
    if (!isDir(KB_ROOT)) throw new KbError(409, `the folder ${KB_ROOT} does not exist`)
    const job = startIndexJob('', buildWholeFolder)
    setupJobId = job.id
    return job
  })

  // The Studio's icon, uploaded from this computer: for the light theme, or
  // with ?variant=dark for the dark one. Stored under INDEX_BASE/branding
  // with its content's hash in the name, and set as iconUrl or iconUrlDark,
  // which /api/brand-icon and /api/favicon serve.
  app.post('/api/setup/icon', async req => {
    requireSingleUser()
    const field = variantOf(req.query)
    let body: Buffer
    try {
      const part = await req.file({ limits: { fileSize: ICON_MAX_BYTES, files: 1 } })
      if (!part) throw new KbError(400, 'no image in the request')
      body = await part.toBuffer()
      if (part.file.truncated) throw new KbError(413, 'The icon is over 1 MB.')
    } catch (e) {
      if (e instanceof KbError) throw e
      const code = (e as { code?: string }).code ?? ''
      if (code === 'FST_REQ_FILE_TOO_LARGE') throw new KbError(413, 'The icon is over 1 MB.')
      throw new KbError(400, 'send the icon as multipart/form-data')
    }
    const type = iconType(body)
    if (!type) throw new KbError(415, 'The icon must be a PNG, JPEG, SVG or WebP image.')
    if (type === 'svg') {
      const problem = svgProblem(body.toString('utf8'))
      if (problem) throw new KbError(415, problem)
    }
    await fsp.mkdir(BRANDING, { recursive: true })
    const file = path.join(BRANDING, `icon-${createHash('sha256').update(body).digest('hex').slice(0, 16)}.${type}`)
    await fsp.writeFile(file, body)
    await updateSettings({ [field]: file })
    await dropUnusedIcons()
    return brandingState()
  })

  // Back to no icon of the Studio's own (the dark one falls back to the light).
  app.delete('/api/setup/icon', async req => {
    requireSingleUser()
    await updateSettings({ [variantOf(req.query)]: '' })
    await dropUnusedIcons()
    return brandingState()
  })

  app.post('/api/setup/complete', async () => {
    requireSingleUser()
    await updateSettings({ setupDone: true })
    return { setupDone: true }
  })
}
