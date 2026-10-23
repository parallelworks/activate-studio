import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { authEnabled } from './auth.js'
import { EXCLUDE_DIRS, GUFI_BIN, INDEX_BASE, KB_ROOT, PROJECT_ROOT, PYTHON_BIN, gufiEnv, indexerEnv } from './config.js'
import { sofficeFound } from './extractors.js'
import { invalidateDbList, treeStats } from './gufi.js'
import { getIndexJob, startIndexJob, type IndexJob } from './indexing.js'
import { builtLibrarySettings, indexSettings, isMeaningLevel, meaningArgs, saveIndexSettings, threadsFor, type IndexSettings } from './indexSettings.js'
import { KbError } from './kb.js'
import {
  addBuiltLibrary, addedDef, builtLibraryDir, checkNewId, forgetProbe, getLibrary, libraryIdFor, listLibraries, setBuildProgressSource,
  settingsLibrary, updateBuild, type LibraryDef,
} from './libraries.js'
import { runPreExtract } from './preextract.js'
import { effectiveSettings } from './settings.js'
import { scanLatest } from './dirScan.js'

/**
 * Libraries the Studio builds of a folder on this computer, beside the
 * main folder: Settings, Libraries, Add a library, "This computer", "Build
 * an index of a folder". The Studio indexes the folder with GUFI as it
 * indexes the main folder (gufi_dir2index, then text extraction and, where
 * the model is installed, embeddings), into an index folder of the user's
 * choosing or its own (INDEX_BASE/libraries/<id>), and keeps the index in
 * step with the folder: on the sweep interval it looks for folders changed
 * since the last pass and indexes those again.
 *
 * Such a library is read only in the Studio for now. Writing to it (uploads,
 * new folders, renames, deletes) would need the write routes, the
 * extraction and preview caches, the label overlay and the incremental
 * indexer, all of which assume the main folder today, to take a library's
 * own folder, index and caches; that is a change across the indexing code,
 * left for when it can be made in one piece. Files added to the folder
 * outside the Studio are picked up by the sync.
 *
 * Building writes files and folders on this computer as the account the
 * Studio runs as, so it is offered only on a Studio with sign-in off.
 */

const EMBED_MODEL = path.join(INDEX_BASE, 'models', 'minilm384.gguf')
const MANIFEST = '.studio-index.json'

const SINGLE_USER = 'Libraries are built from folders on this computer only on a Studio with sign-in off.'

/** Whether this Studio offers building a library from a folder here. */
export function localBuildOffered(): boolean {
  return !authEnabled()
}

function gufiTools(): boolean {
  return fs.existsSync(path.join(GUFI_BIN, 'gufi_dir2index')) && fs.existsSync(path.join(GUFI_BIN, 'gufi_query'))
}

/** A path as typed: ~ is the home folder, and a relative path is taken from it. */
function expand(p: string): string {
  const home = os.homedir()
  if (p === '~') return home
  if (p.startsWith('~/')) return path.join(home, p.slice(2))
  return path.resolve(home, p)
}

const within = (child: string, parent: string) => child === parent || child.startsWith(parent.endsWith(path.sep) ? parent : parent + path.sep)
const realOr = (p: string) => { try { return fs.realpathSync(p) } catch { return path.resolve(p) } }
/** The nearest folder that exists at or above a path. */
function existingAncestor(p: string): string {
  let a = p
  while (!fs.existsSync(a) && path.dirname(a) !== a) a = path.dirname(a)
  return a
}
function writable(p: string): boolean { try { fs.accessSync(p, fs.constants.W_OK); return true } catch { return false } }
function readable(p: string): boolean { try { fs.accessSync(p, fs.constants.R_OK | fs.constants.X_OK); return true } catch { return false } }
const isDir = (p: string) => { try { return fs.statSync(p).isDirectory() } catch { return false } }

export interface Step { name: 'gufi' | 'source' | 'index' | 'extract'; ok: boolean; detail: string; warn?: boolean }
export interface LocalCheck {
  ok: boolean
  steps: Step[]
  /** Folders that do not exist yet and are made when the build starts. */
  create: string[]
  sourceRoot: string
  indexDir: string
  /** The index goes in the Studio's own folder. */
  managed: boolean
  id: string
}

export interface BuildInput { sourceRoot?: unknown; indexRoot?: unknown; label?: unknown; id?: unknown; create?: unknown; meaning?: unknown }

/**
 * The check before a build, step by step: GUFI is here; the source folder
 * is readable, or can be made; the index folder is new, empty, or one the
 * Studio wrote for this folder, outside it and outside the Studio's own
 * index; and what extraction covers.
 */
export function checkLocalBuild(a: BuildInput): LocalCheck {
  if (!localBuildOffered()) throw new KbError(403, SINGLE_USER)
  const typed = String(a.sourceRoot ?? '').trim()
  if (!typed) throw new KbError(400, 'source folder: the folder to index')
  if (/[\0-\x1f]/.test(typed)) throw new KbError(400, 'source folder: no control characters')
  const label = String(a.label ?? '').trim()
  const id = String(a.id ?? '').trim().toLowerCase() || libraryIdFor(label || undefined, typed)
  const steps: Step[] = []
  const create: string[] = []

  steps.push(gufiTools()
    ? { name: 'gufi', ok: true, detail: `GUFI is in ${GUFI_BIN}.` }
    : { name: 'gufi', ok: false, detail: `GUFI is not installed here: there is no gufi_dir2index in ${GUFI_BIN}.` })

  const source = expand(typed)
  const src = realOr(source)
  const base = realOr(INDEX_BASE)
  if (src === path.parse(src).root) steps.push({ name: 'source', ok: false, detail: 'Indexing the top of the disk is not supported; choose a folder.' })
  else if (src === realOr(KB_ROOT)) steps.push({ name: 'source', ok: false, detail: `${source} is the main folder, which is indexed already.` })
  else if (within(src, base) || within(base, src)) steps.push({ name: 'source', ok: false, detail: `${source} holds the Studio's own data (${INDEX_BASE}), or is inside it; choose another folder.` })
  else if (fs.existsSync(source)) {
    steps.push(!isDir(source) ? { name: 'source', ok: false, detail: `${source} is a file, not a folder.` }
      : readable(source) ? { name: 'source', ok: true, detail: `${source} is readable.` }
      : { name: 'source', ok: false, detail: `${source} is there, but the Studio cannot read it.` })
  } else {
    const at = existingAncestor(source)
    if (isDir(at) && writable(at)) { create.push(source); steps.push({ name: 'source', ok: true, detail: `${source} does not exist yet; it is created, empty, when the build starts.` }) }
    else steps.push({ name: 'source', ok: false, detail: `${source} does not exist, and it cannot be made: ${at} is not writable.` })
  }

  const given = String(a.indexRoot ?? '').trim()
  const managed = !given
  const indexDir = managed ? path.join(builtLibraryDir(id), 'index') : expand(given)
  const idx = realOr(indexDir)
  if (!managed && /[\0-\x1f]/.test(given)) steps.push({ name: 'index', ok: false, detail: 'The index folder has control characters in its name.' })
  else if (within(idx, src)) steps.push({ name: 'index', ok: false, detail: `The index folder ${indexDir} is inside the folder being indexed. Choose an index folder outside it.` })
  else if (within(src, idx)) steps.push({ name: 'index', ok: false, detail: `${source} is inside the index folder ${indexDir}. Choose an index folder outside it.` })
  else if (!managed && within(idx, path.join(base, 'gufi'))) steps.push({ name: 'index', ok: false, detail: `${indexDir} is inside the main folder's index. Choose another folder.` })
  else if (fs.existsSync(indexDir)) {
    let entries: string[] = []
    try { entries = fs.readdirSync(indexDir) } catch { /* unreadable: said below */ }
    let owner = ''
    try { owner = String((JSON.parse(fs.readFileSync(path.join(indexDir, MANIFEST), 'utf8')) as { source?: string }).source ?? '') } catch { /* none */ }
    if (!isDir(indexDir)) steps.push({ name: 'index', ok: false, detail: `${indexDir} is a file, not a folder.` })
    else if (!writable(indexDir)) steps.push({ name: 'index', ok: false, detail: `${indexDir} is not writable.` })
    else if (owner && owner !== src) steps.push({ name: 'index', ok: false, detail: `${indexDir} holds the index of ${owner}. Choose another index folder.` })
    else if (owner) steps.push({ name: 'index', ok: true, detail: `${indexDir} holds an index the Studio built of this folder; the new one replaces it when complete.` })
    else if (entries.length) steps.push({ name: 'index', ok: false, detail: `${indexDir} already holds files the Studio did not write. Choose an empty or new folder for the index.` })
    else steps.push({ name: 'index', ok: true, detail: `The index is written in ${indexDir}.` })
  } else {
    const at = existingAncestor(indexDir)
    if (!(isDir(at) && writable(at))) steps.push({ name: 'index', ok: false, detail: `${indexDir} does not exist, and it cannot be made: ${at} is not writable.` })
    else if (managed) steps.push({ name: 'index', ok: true, detail: 'The index is written in the Studio\'s own data folder.' })
    else { create.push(indexDir); steps.push({ name: 'index', ok: true, detail: `${indexDir} does not exist yet; it is created when the build starts.` }) }
  }

  steps.push({ name: 'extract', ok: true, detail: `Text is extracted from documents and images, so search covers what the files say${fs.existsSync(EMBED_MODEL) ? ', and by meaning' : ''}.` })
  return { ok: steps.every(s => s.ok), steps, create, sourceRoot: source, indexDir, managed, id }
}

// ---- building and keeping in step ----

/** One index job per library at a time; a sync waits for a build. */
const running = new Map<string, number>()
const locks = new Map<string, Promise<unknown>>()
function withLibraryLock<T>(id: string, fn: () => Promise<T>): Promise<T> {
  const next = (locks.get(id) ?? Promise.resolve()).then(fn, fn)
  locks.set(id, next.catch(() => {}))
  return next
}

function jobOf(id: string): IndexJob | null {
  const j = running.get(id)
  const job = j != null ? getIndexJob(j) : null
  return job && (job.state === 'queued' || job.state === 'running') ? job : null
}
setBuildProgressSource(id => {
  const p = jobOf(id)?.progress
  return p ? { phase: p.phase, done: p.done, total: p.total, current: p.current } : null
})

function run(cmd: string, args: string[], timeoutMs = 2 * 60 * 60_000): Promise<string> {
  // With AI features off, or descriptions off, images are indexed by OCR and name only.
  const eff = effectiveSettings()
  const vm = eff.aiEnabled && indexSettings().imageDescriptions ? eff.visionModel : ''
  const extra: Record<string, string> = vm ? { ADE_VISION_MODEL: vm } : {}
  const env = path.resolve(path.dirname(cmd)) === path.resolve(GUFI_BIN) ? gufiEnv(extra) : indexerEnv(extra)
  if (!vm) delete env.ADE_VISION_MODEL
  return new Promise((resolve, reject) => {
    execFile(cmd, args, { timeout: timeoutMs, maxBuffer: 32 * 1024 * 1024, env }, (err, so, se) => {
      if (err) return reject(new Error(`${path.basename(cmd)}: ${String(se || err.message).trim().split('\n').slice(-3).join(' ').slice(0, 300)}`))
      resolve(so)
    })
  })
}

function skipFile(id: string): string {
  const f = path.join(builtLibraryDir(id), 'skip-dirs.txt')
  fs.mkdirSync(path.dirname(f), { recursive: true })
  fs.writeFileSync(f, [...EXCLUDE_DIRS, '.claude', '.github'].join('\n') + '\n')
  return f
}

/** db.db files under a tree, counted for progress, a few thousand at most per look. */
function countDbs(root: string, cap = 200_000): number {
  let n = 0
  const queue = [root]
  while (queue.length && n < cap) {
    const dir = queue.pop()!
    let entries: fs.Dirent[]
    try { entries = fs.readdirSync(dir, { withFileTypes: true }) } catch { continue }
    for (const e of entries) {
      if (e.name === 'db.db') n++
      else if (e.isDirectory()) queue.push(path.join(dir, e.name))
    }
  }
  return n
}

/** Each folder's latest change (the folder and the files directly in it),
 *  keyed by its path in the source, '' for the top; as the main folder's
 *  sweep records them. */
async function scanDirs(root: string): Promise<Record<string, number>> {
  return Object.fromEntries(await scanLatest(root, EXCLUDE_DIRS))
}

const stateFile = (id: string) => path.join(builtLibraryDir(id), 'sync-state.json')
function loadState(id: string): Record<string, number> | null {
  try { return JSON.parse(fs.readFileSync(stateFile(id), 'utf8')) as Record<string, number> } catch { return null }
}
async function saveState(id: string, state: Record<string, number>): Promise<void> {
  await fsp.mkdir(builtLibraryDir(id), { recursive: true })
  await fsp.writeFile(stateFile(id), JSON.stringify(state))
}

type Progress = (phase: string, done?: number, current?: string | null) => void

type Scope = { subdir?: string; noRecurse?: boolean }

/**
 * The steps that extract and embed one part of a library (a subtree, the
 * top folder's own files, or everything), under the indexing settings the
 * main folder uses (Settings, General, Indexing): its thread count and which
 * readers run; and the library's own level of search by meaning, off unless
 * it was turned on. Embeddings keep a cache of their own in the library's
 * folder, so a rebuild or a sync embeds only passages it has not seen.
 * Exported for tests.
 */
export function enrichSteps(def: LibraryDef, scope: Scope, s: IndexSettings = builtLibrarySettings(def.id), threads = threadsFor()): {
  documents: Parameters<typeof runPreExtract>[0] | null
  text: string[]
  meaning: string[] | null
} {
  const b = def.build!
  const extra = [...(scope.subdir ? ['--subdir', scope.subdir] : []), ...(scope.noRecurse ? ['--no-recurse'] : [])]
  const text = [path.join(PROJECT_ROOT, 'indexer', 'enrich.py'), '--kb-root', def.sourceRoot!, '--index', def.indexRoot,
    '--extract-cache', b.cacheRoot, '--jobs', String(threads), '--pdf-reader', s.pdfReader, ...extra]
  if (!s.ocr) text.push('--no-ocr')
  if (!s.documents) text.push('--no-documents')
  const soffice = s.legacyOffice ? sofficeFound() : null
  if (soffice) text.push('--soffice', soffice)
  const level = meaningArgs(s.meaning, s.meaningSpeed)
  const meaning = level && fs.existsSync(EMBED_MODEL)
    ? [path.join(PROJECT_ROOT, 'indexer', 'embed.py'), '--index', def.indexRoot, '--model', EMBED_MODEL,
        '--gufi-sqlite3', path.join(GUFI_BIN, 'gufi_sqlite3'), '--jobs', String(threads),
        '--cache', path.join(builtLibraryDir(def.id), 'embed-cache.db'), ...extra, ...(scope.subdir || scope.noRecurse ? [] : ['--prune']), ...level]
    : null
  return {
    documents: s.documents ? { kbRoot: def.sourceRoot!, cacheRoot: b.cacheRoot, ...scope, jobs: threads, ocr: s.ocr } : null,
    text,
    meaning,
  }
}

async function enrich(def: LibraryDef, scope: Scope, progress: Progress): Promise<void> {
  const steps = enrichSteps(def, scope)
  progress('Extracting text')
  if (steps.documents) await runPreExtract(steps.documents, m => console.warn(m))
  await run(PYTHON_BIN, steps.text)
  if (steps.meaning) {
    progress('Embedding text')
    await run(PYTHON_BIN, steps.meaning)
  }
}

/** Counts, the manifest, and the caches that remember the old tree. */
async function settle(def: LibraryDef): Promise<{ files: number; dirs: number; builtAt: string }> {
  forgetProbe(def.indexRoot)
  invalidateDbList()
  const st = await treeStats(def.indexRoot, '').catch(() => null)
  const builtAt = new Date().toISOString()
  const files = st?.files ?? 0, dirs = st?.dirs ?? 0
  await fsp.writeFile(path.join(def.build!.indexDir, MANIFEST), JSON.stringify({
    version: 1, source: def.sourceRoot, index: def.indexRoot, indexDir: def.build!.indexDir, builtAt, gufiBin: GUFI_BIN, directories: dirs, files,
  }, null, 2))
  return { files, dirs, builtAt }
}

/** The whole folder, written beside the index there and swapped in when
 *  complete, so search keeps answering while it runs. */
async function fullBuild(id: string, job: IndexJob): Promise<{ ms: number }> {
  const t0 = Date.now()
  const def = addedDef(id)
  if (!def?.build || !def.sourceRoot) throw new Error('no such library')
  const b = def.build
  const src = def.sourceRoot, tree = def.indexRoot
  const progress: Progress = (phase, done = 0, current = null) => { job.progress = { phase, done, total: 0, current, failed: [] } }
  updateBuild(id, { state: fs.existsSync(path.join(tree, 'db.db')) ? 'updating' : 'building', error: null })
  try {
    if (!isDir(src)) throw new Error(`${src} is not a folder any more`)
    // Taken first, so a change made during the build is still new to the sync.
    const snapshot = await scanDirs(src)
    progress('Indexing folders')
    await fsp.mkdir(b.indexDir, { recursive: true })
    await fsp.mkdir(b.cacheRoot, { recursive: true })
    const stage = path.join(b.indexDir, '.build')
    await fsp.rm(stage, { recursive: true, force: true })
    const counter = setInterval(() => { const n = countDbs(stage); progress('Indexing folders', n, `${n.toLocaleString('en-US')} folders`) }, 1000)
    try {
      await run(path.join(GUFI_BIN, 'gufi_dir2index'), ['-x', '-n', String(threadsFor()), '--skip-file', skipFile(id), src, stage])
    } finally { clearInterval(counter) }
    const built = path.join(stage, path.basename(src))
    if (!fs.existsSync(path.join(built, 'db.db'))) throw new Error(`gufi_dir2index wrote no index at ${built}`)
    const old = path.join(b.indexDir, '.old')
    await fsp.rm(old, { recursive: true, force: true })
    if (fs.existsSync(tree)) await fsp.rename(tree, old)
    await fsp.rename(built, tree)
    await fsp.rm(old, { recursive: true, force: true })
    await fsp.rm(stage, { recursive: true, force: true })
    await enrich(def, {}, progress)
    progress('Counting')
    const counts = await settle(def)
    await saveState(id, snapshot)
    updateBuild(id, { state: 'ready', error: null, ...counts })
    return { ms: Date.now() - t0 }
  } catch (e) {
    const message = String((e as Error).message ?? e).slice(0, 400)
    updateBuild(id, { state: fs.existsSync(path.join(tree, 'db.db')) ? 'ready' : 'failed', error: message })
    throw e
  }
}

/** One subtree of a library indexed again into a staging folder and swapped in. */
async function indexSubtree(def: LibraryDef, rel: string): Promise<void> {
  const b = def.build!
  const src = path.join(def.sourceRoot!, rel)
  const stage = path.join(b.indexDir, `.inc-${Date.now()}-${Math.floor(Math.random() * 1e6)}`)
  await fsp.mkdir(stage, { recursive: true })
  try {
    await run(path.join(GUFI_BIN, 'gufi_dir2index'), ['-x', '-n', String(threadsFor()), '--skip-file', skipFile(def.id), src, stage])
    const target = path.join(def.indexRoot, rel)
    await fsp.mkdir(path.dirname(target), { recursive: true })
    await fsp.rm(target, { recursive: true, force: true })
    await fsp.rename(path.join(stage, path.basename(src)), target)
  } finally {
    await fsp.rm(stage, { recursive: true, force: true })
  }
  await enrich(def, { subdir: rel }, () => {})
}

/** The top folder's own files: its db.db alone. */
async function indexTop(def: LibraryDef): Promise<void> {
  const b = def.build!
  const stage = path.join(b.indexDir, `.inc-top-${Date.now()}`)
  await fsp.mkdir(stage, { recursive: true })
  try {
    await run(path.join(GUFI_BIN, 'gufi_dir2index'), ['-x', '-n', String(Math.min(4, threadsFor())), '--max-level', '0', '--skip-file', skipFile(def.id), def.sourceRoot!, stage])
    await fsp.rename(path.join(stage, path.basename(def.sourceRoot!), 'db.db'), path.join(def.indexRoot, 'db.db'))
  } finally {
    await fsp.rm(stage, { recursive: true, force: true })
  }
  await enrich(def, { noRecurse: true }, () => {})
}

/**
 * Brings a ready library's index up to its folder: folders whose content
 * changed since the last pass are indexed again (the topmost of each
 * changed branch), the top folder's own files when they changed, and the
 * index of a folder that is gone is removed. Returns what changed.
 */
export async function syncLibrary(id: string): Promise<string[]> {
  return withLibraryLock(id, async () => {
    const def = addedDef(id)
    if (!def?.build || def.build.state !== 'ready' || !def.sourceRoot) return []
    if (!isDir(def.sourceRoot)) { updateBuild(id, { error: `${def.sourceRoot} is not a folder any more` }); return [] }
    const before = loadState(id)
    const scan = await scanDirs(def.sourceRoot)
    if (!before) { await saveState(id, scan); return [] }
    const top = (before[''] ?? 0) < (scan[''] ?? 0)
    let changed = Object.keys(scan).filter(rel => rel && (before[rel] ?? 0) < scan[rel]).sort()
    changed = changed.filter(rel => !changed.some(o => o !== rel && rel.startsWith(`${o}/`)))
    const removed = Object.keys(before).filter(rel => rel && !(rel in scan) && !Object.keys(scan).some(s => rel.startsWith(`${s}/`) && changed.includes(s)))
    if (!top && !changed.length && !removed.length) return []
    const done: string[] = []
    const failed: string[] = []
    const next = { ...scan }
    for (const rel of removed) { await fsp.rm(path.join(def.indexRoot, rel), { recursive: true, force: true }).catch(() => {}); done.push(`${rel} (removed)`) }
    for (const rel of changed) {
      try { await indexSubtree(def, rel); done.push(rel) } catch (e) {
        failed.push(`${rel}: ${(e as Error).message}`)
        for (const k of Object.keys(next)) if (k === rel || k.startsWith(`${rel}/`)) { if (before[k] !== undefined) next[k] = before[k]; else delete next[k] }
      }
    }
    if (top) {
      try { await indexTop(def); done.push('(top folder)') } catch (e) { failed.push(`(top folder): ${(e as Error).message}`); if (before[''] !== undefined) next[''] = before['']; else delete next[''] }
    }
    const counts = await settle(def)
    await saveState(id, next)
    updateBuild(id, { error: failed.length ? failed.join('; ').slice(0, 400) : null, ...counts })
    return done
  })
}

/** Starts a full build of a library as an index job; one already running is returned. */
export function startBuild(id: string): IndexJob {
  const was = jobOf(id)
  if (was) return was
  const job = startIndexJob(`library:${id}`, j => withLibraryLock(id, () => fullBuild(id, j)))
  running.set(id, job.id)
  return job
}

/** Adds a library built of a folder on this computer and starts its build. */
export async function addLocalBuild(a: BuildInput): Promise<{ id: string; job: IndexJob }> {
  const c = checkLocalBuild(a)
  if (!c.ok) throw new KbError(400, `The check did not pass. ${c.steps.find(s => !s.ok)?.detail ?? ''}`.trim())
  if (c.create.length && a.create !== true) throw new KbError(409, `These folders do not exist yet: ${c.create.join(', ')}. Confirm to create them.`)
  checkNewId(c.id)
  if (a.meaning !== undefined && !isMeaningLevel(a.meaning)) throw new KbError(400, 'meaning: off, documents or full')
  for (const dir of c.create) await fsp.mkdir(dir, { recursive: true })
  const src = realOr(c.sourceRoot)
  const label = String(a.label ?? '').trim() || path.basename(src)
  addBuiltLibrary({
    id: c.id, label, indexRoot: path.join(c.indexDir, path.basename(src)), sourceRoot: src,
    build: { indexDir: c.indexDir, cacheRoot: path.join(builtLibraryDir(c.id), 'extract'), managed: c.managed, state: 'building', error: null, builtAt: null, files: null, dirs: null },
  })
  if (a.meaning !== undefined) await saveIndexSettings(c.id, { meaning: a.meaning })
  return { id: c.id, job: startBuild(c.id) }
}

const builtIds = () => listLibraries().filter(l => l.build).map(l => l.id)

/**
 * Keeps built libraries in step with their folders on the sweep interval
 * (Settings, General; 0 pauses it), one library at a time, and finishes a
 * build a restart interrupted.
 */
export function startLibrarySync(log: (msg: string) => void): void {
  for (const id of builtIds()) {
    const s = addedDef(id)?.build?.state
    if (s === 'building' || s === 'updating') { log(`library ${id}: the build was interrupted; building it again`); startBuild(id) }
  }
  const tick = async (): Promise<void> => {
    const sec = effectiveSettings().sweepIntervalSec
    if (sec > 0) {
      for (const id of builtIds()) {
        if (jobOf(id)) continue
        const changed = await syncLibrary(id).catch(e => { log(`library ${id}: sync failed: ${(e as Error).message}`); return [] })
        if (changed.length) log(`library ${id}: indexed again ${changed.join(', ')}`)
      }
    }
    setTimeout(() => { void tick() }, Math.max(sec, 60) * 1000).unref()
  }
  setTimeout(() => { void tick() }, Math.max(effectiveSettings().sweepIntervalSec, 60) * 1000).unref()
}

export async function libraryBuildRoutes(app: FastifyInstance): Promise<void> {
  app.post('/api/libraries/build/check', async req => {
    const c = checkLocalBuild((req.body ?? {}) as BuildInput)
    return { ok: c.ok, steps: c.steps, create: c.create, sourceRoot: c.sourceRoot, indexDir: c.managed ? null : c.indexDir }
  })
  app.post('/api/libraries/build', async req => {
    const { id, job } = await addLocalBuild((req.body ?? {}) as BuildInput)
    return { library: settingsLibrary(getLibrary(id)), job }
  })
  app.post('/api/libraries/:id/rebuild', async req => {
    if (!localBuildOffered()) throw new KbError(403, SINGLE_USER)
    const id = String((req.params as { id: string }).id)
    if (!addedDef(id)?.build) throw new KbError(404, `${id} is not a library the Studio built`)
    const job = startBuild(id)
    return { library: settingsLibrary(getLibrary(id)), job }
  })
}
