import { spawn, type ChildProcess } from 'node:child_process'
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
import { builtLibrarySettings, indexSettings, isIndexDepth, isMeaningLevel, meaningArgs, NEW_LIBRARY_DEPTH, saveIndexSettings, textArgs, threadsFor, type IndexDepth, type IndexSettings } from './indexSettings.js'
import { KbError } from './kb.js'
import {
  addBuiltLibrary, addedDef, builtLibraryDir, checkNewId, getLibrary, libraryIdFor, listLibraries, probeLibrary, setBuildProgressSource,
  settingsLibrary, staleProbe, updateBuild, type LibraryDef,
} from './libraries.js'
import { runPreExtract } from './preextract.js'
import { effectiveSettings } from './settings.js'
import { scanLatestDetail } from './dirScan.js'
import { deniedPaths, onlyDenied, recordDenied } from './omissions.js'
import { depthChange, depthRecordOf, readDepthRecord, writeDepthRecord, type DepthChange } from './depthState.js'
import { scheduleSemanticMapRebuild } from './semanticMap.js'
import { cleanIndexedText, markTextClean, textCleanNeeded } from './textCleanup.js'

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

export interface BuildInput { sourceRoot?: unknown; indexRoot?: unknown; label?: unknown; id?: unknown; create?: unknown; meaning?: unknown; depth?: unknown }

/**
 * The check before a build, step by step: GUFI is here; the source folder
 * is readable, or can be made; the index folder is new, empty, or one the
 * Studio wrote for this folder, outside it and outside the Studio's own
 * index; and what extraction covers.
 */
/** What a build at a depth reads, for the check. */
function extractDetail(depth: IndexDepth): string {
  if (depth === 'names') return 'Names, sizes and dates only: nothing is read from inside the files.'
  const meaning = depth.startsWith('meaning-') && fs.existsSync(EMBED_MODEL)
  return `Text is extracted from documents${depth === 'text' ? '' : ' and images'}, so search covers what the files say${meaning ? ', and by meaning' : ''}.`
}

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

  // A library not added yet is checked at the depth it would be given.
  const depth = isIndexDepth(a.depth) ? a.depth : addedDef(id) ? indexSettings(id).depth : NEW_LIBRARY_DEPTH
  steps.push({ name: 'extract', ok: true, detail: extractDetail(depth) })
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

/** Permission errors the programs of a build or sync printed, by library:
 *  the folders and files they could not read (omissions.ts). */
const denials = new Map<string, Set<string>>()
const denied = (id: string) => { let d = denials.get(id); if (!d) denials.set(id, d = new Set()); return d }

function run(cmd: string, args: string[], timeoutMs = 2 * 60 * 60_000, images = false, libraryId?: string, tolerateDenied = false): Promise<string> {
  // Images are described only where the depth reads them (Text + OCR and
  // up); with AI features off, or descriptions off, they are indexed by OCR
  // and name only.
  const eff = effectiveSettings()
  const vm = images && eff.aiEnabled && indexSettings().imageDescriptions ? eff.visionModel : ''
  const extra: Record<string, string> = vm ? { ADE_VISION_MODEL: vm } : {}
  const env = path.resolve(path.dirname(cmd)) === path.resolve(GUFI_BIN) ? gufiEnv(extra) : indexerEnv(extra)
  if (!vm) delete env.ADE_VISION_MODEL
  return new Promise((resolve, reject) => {
    // A process group of its own, so a stop or the time limit ends the
    // program and everything it started: enrich.py's worker processes, left
    // behind, held its output open and the build never ended. (execFile
    // drops the detached option, so this spawns.)
    const child = spawn(cmd, args, { env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] })
    const out: Buffer[] = [], err: Buffer[] = []
    let size = 0
    const take = (into: Buffer[]) => (b: Buffer) => { if (size < 32 * 1024 * 1024) { into.push(b); size += b.length } }
    child.stdout!.on('data', take(out))
    child.stderr!.on('data', take(err))
    const limit = setTimeout(() => killGroup(child, 'SIGTERM'), timeoutMs)
    limit.unref?.()
    children.add(child)
    const done = (e: Error | null, code: number | null, signal: NodeJS.Signals | null) => {
      clearTimeout(limit)
      children.delete(child)
      const so = Buffer.concat(out).toString('utf8'), se = Buffer.concat(err).toString('utf8')
      if (libraryId) for (const p of deniedPaths(se)) denied(libraryId).add(p)
      const failed = e ?? (code !== 0 ? new Error(signal ? `stopped (${signal})` : `exit ${code}`) : null)
      // A folder it may not read fails gufi_dir2index after it wrote the rest:
      // the build stands, and the folder is named (omissions.ts).
      if (failed && tolerateDenied && libraryId && !stopping && onlyDenied(se)) return resolve(so)
      if (failed) return reject(new Error(`${path.basename(cmd)}: ${String(se || failed.message).trim().split('\n').slice(-3).join(' ').slice(0, 300)}`))
      resolve(so)
    }
    child.on('error', e => done(e, null, null))
    child.on('close', (code, signal) => done(null, code, signal))
  })
}

/** Ends a program and every process it started (its process group), then
 *  kills what is left five seconds later. */
function killGroup(child: ChildProcess, sig: NodeJS.Signals): boolean {
  const pid = child.pid
  if (!pid) return false
  const send = (s: NodeJS.Signals) => { try { process.kill(-pid, s) } catch { try { child.kill(s) } catch { /* gone */ } } }
  send(sig)
  setTimeout(() => { if (child.exitCode === null && child.signalCode === null) send('SIGKILL') }, 5000).unref?.()
  return true
}

/** The programs library builds are running, so a stop can end them. */
const children = new Set<ChildProcess>()
/** Set once the server is stopping: a build cut off keeps its state, so the
 *  next start takes it up again. */
let stopping = false

/**
 * Ends the programs of every library build, as the server stops (SIGTERM,
 * which a shutdown of the computer sends). Their state stays 'building' or
 * 'updating', so the next start resumes them: a rebuild from the extraction
 * cache, which keeps every file read so far, and a change of depth with a
 * sync. Returns how many programs were stopped.
 */
export function stopLibraryJobs(): number {
  stopping = true
  for (const k of kicks.values()) clearTimeout(k)
  kicks.clear()
  let n = 0
  for (const c of children) if (killGroup(c, 'SIGTERM')) n++
  return n
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
async function scanDirs(root: string, libraryId?: string): Promise<Record<string, number>> {
  return (await scanDirsDetail(root, libraryId)).latest
}

/** The same, with the folders that could not be listed for want of
 *  permission, by their path below the root. */
async function scanDirsDetail(root: string, libraryId?: string): Promise<{ latest: Record<string, number>; deniedRel: string[] }> {
  const r = await scanLatestDetail(root, EXCLUDE_DIRS)
  if (libraryId) for (const rel of r.denied) denied(libraryId).add(rel ? path.join(root, rel) : root)
  return { latest: Object.fromEntries(r.latest), deniedRel: r.denied }
}

/** Records what this build or sync could not read, and starts the next
 *  count afresh: nothing denied clears the record. */
function settleDenied(id: string): void {
  const d = [...(denials.get(id) ?? [])]
  denials.delete(id)
  const folders = d.filter(p => isDir(p))
  recordDenied(id, folders, d.length - folders.length)
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

/** The depth the library was last built or synced at (depthState.ts). */
const depthFile = (id: string) => path.join(builtLibraryDir(id), 'depth-state.json')

/** The depth change the library's next sync applies, as for the main
 *  folder (indexing.ts): null when there is nothing to run; a depth that
 *  only went down, or a library with no record yet, is recorded here. */
async function pendingDepthChange(id: string, s: IndexSettings): Promise<DepthChange | null> {
  const now = depthRecordOf(s)
  const was = readDepthRecord(depthFile(id))
  const ch = was ? depthChange(was, now) : null
  if (!was || (ch && !ch.text && !ch.meaning)) await writeDepthRecord(depthFile(id), now)
  return ch && (ch.text || ch.meaning) ? ch : null
}

/** Applies a change of depth over the whole library, on its index in
 *  place: only the steps the new depth adds, with the library's caches. */
async function applyDepthChange(def: LibraryDef, ch: DepthChange, s: IndexSettings): Promise<void> {
  const steps = enrichSteps(def, {}, s)
  if (ch.text && steps.text) {
    if (steps.documents) await runPreExtract(steps.documents, m => console.warn(m))
    await run(PYTHON_BIN, steps.text, undefined, s.ocr, def.id)
  }
  if (ch.meaning && steps.meaning) await run(PYTHON_BIN, steps.meaning, undefined, false, def.id)
  await writeDepthRecord(depthFile(def.id), depthRecordOf(s))
}

type Scope = { subdir?: string; noRecurse?: boolean }

/** A library whose depth reaches search by meaning gets its semantic map
 *  made again once its embeddings changed: soon after a build or a new
 *  depth, later after a sync, so a run of syncs makes one map. */
function remapAfterMeaning(id: string, s: IndexSettings, delayMs: number): void {
  if (s.meaning !== 'off') scheduleSemanticMapRebuild(delayMs, id)
}

/**
 * The steps that extract and embed one part of a library (a subtree, the
 * top folder's own files, or everything), under the indexing settings the
 * main folder uses (Settings, General, Indexing): its thread count and how
 * documents are read; and the library's own depth, which says whether text
 * is read at all, whether OCR runs, and how much search by meaning covers.
 * Embeddings keep a cache of their own in the library's folder, so a
 * rebuild or a sync embeds only passages it has not seen. Exported for
 * tests.
 */
export function enrichSteps(def: LibraryDef, scope: Scope, s: IndexSettings = builtLibrarySettings(def.id), threads = threadsFor()): {
  documents: Parameters<typeof runPreExtract>[0] | null
  text: string[] | null
  meaning: string[] | null
} {
  const b = def.build!
  const extra = [...(scope.subdir ? ['--subdir', scope.subdir] : []), ...(scope.noRecurse ? ['--no-recurse'] : [])]
  const depth = textArgs(s.depth)
  if (!depth) return { documents: null, text: null, meaning: null }
  const text = [path.join(PROJECT_ROOT, 'indexer', 'enrich.py'), '--kb-root', def.sourceRoot!, '--index', def.indexRoot,
    '--extract-cache', b.cacheRoot, '--jobs', String(threads), '--pdf-reader', s.pdfReader, ...extra, ...depth]
  const soffice = s.legacyOffice ? sofficeFound() : null
  if (soffice) text.push('--soffice', soffice)
  const level = meaningArgs(s.meaning, s.meaningSpeed)
  const meaning = level && fs.existsSync(EMBED_MODEL)
    ? [path.join(PROJECT_ROOT, 'indexer', 'embed.py'), '--index', def.indexRoot, '--model', EMBED_MODEL,
        '--gufi-sqlite3', path.join(GUFI_BIN, 'gufi_sqlite3'), '--jobs', String(threads),
        '--cache', path.join(builtLibraryDir(def.id), 'embed-cache.db'), ...extra, ...(scope.subdir || scope.noRecurse ? [] : ['--prune']), ...level]
    : null
  return {
    documents: { kbRoot: def.sourceRoot!, cacheRoot: b.cacheRoot, ...scope, jobs: threads, ocr: s.ocr },
    text,
    meaning,
  }
}

/** `live` is a first build, on the index search already reads. */
async function enrich(def: LibraryDef, scope: Scope, progress: Progress, s = builtLibrarySettings(def.id), live = false): Promise<void> {
  const steps = enrichSteps(def, scope, s)
  if (!steps.text) return
  progress('Extracting text')
  if (steps.documents) await runPreExtract(steps.documents, m => console.warn(m))
  await run(PYTHON_BIN, steps.text, undefined, s.ocr, def.id)
  if (steps.meaning) {
    // Search by meaning comes last. Its words are found now, so search
    // reads them while it runs and says meaning is still being computed.
    if (live) { staleProbe(def.indexRoot); await probeLibrary(def.indexRoot).catch(() => {}) }
    progress('Computing search by meaning')
    await run(PYTHON_BIN, steps.meaning, undefined, false, def.id)
  }
}

/** Counts, the manifest, and the caches that remember the old tree. */
async function settle(def: LibraryDef): Promise<{ files: number; dirs: number; builtAt: string }> {
  // Probed again at once. Until it answers, the library keeps the
  // capabilities last found (libraries.ts), never "none".
  staleProbe(def.indexRoot)
  await probeLibrary(def.indexRoot).catch(() => {})
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
 *  complete, so search keeps answering while it runs. Where the library
 *  has an index already (a rebuild, or a build a restart interrupted), the
 *  new one gets its text before the swap, so the library's words stay
 *  searchable throughout; a first build is swapped in at once, so its
 *  names answer while the text is read. */
async function fullBuild(id: string, job: IndexJob): Promise<{ ms: number }> {
  const t0 = Date.now()
  const def = addedDef(id)
  if (!def?.build || !def.sourceRoot) throw new Error('no such library')
  const b = def.build
  const src = def.sourceRoot, tree = def.indexRoot
  const progress: Progress = (phase, done = 0, current = null) => { job.progress = { phase, done, total: 0, current, failed: [] } }
  // Read once, so the depth recorded is the one the build read at.
  const s = builtLibrarySettings(id)
  updateBuild(id, { state: fs.existsSync(path.join(tree, 'db.db')) ? 'updating' : 'building', error: null })
  try {
    if (!isDir(src)) throw new Error(`${src} is not a folder any more`)
    // Taken first, so a change made during the build is still new to the sync.
    const snapshot = await scanDirs(src, id)
    progress('Indexing folders')
    await fsp.mkdir(b.indexDir, { recursive: true })
    await fsp.mkdir(b.cacheRoot, { recursive: true })
    const stage = path.join(b.indexDir, '.build')
    await fsp.rm(stage, { recursive: true, force: true })
    const counter = setInterval(() => { const n = countDbs(stage); progress('Indexing folders', n, `${n.toLocaleString('en-US')} folders`) }, 1000)
    try {
      await run(path.join(GUFI_BIN, 'gufi_dir2index'), ['-x', '-n', String(threadsFor()), '--skip-file', skipFile(id), src, stage], undefined, false, id, true)
    } finally { clearInterval(counter) }
    const built = path.join(stage, path.basename(src))
    if (!fs.existsSync(path.join(built, 'db.db'))) throw new Error(`gufi_dir2index wrote no index at ${built}`)
    const replacing = fs.existsSync(path.join(tree, 'db.db'))
    if (replacing) await enrich({ ...def, indexRoot: built }, {}, progress, s)
    const old = path.join(b.indexDir, '.old')
    await fsp.rm(old, { recursive: true, force: true })
    if (fs.existsSync(tree)) await fsp.rename(tree, old)
    await fsp.rename(built, tree)
    await fsp.rm(old, { recursive: true, force: true })
    await fsp.rm(stage, { recursive: true, force: true })
    if (!replacing) await enrich(def, {}, progress, s, true)
    // Its words were written without the converter's provenance line.
    await markTextClean(tree)
    progress('Counting')
    const counts = await settle(def)
    settleDenied(id)
    await saveState(id, snapshot)
    await writeDepthRecord(depthFile(id), depthRecordOf(s))
    updateBuild(id, { state: 'ready', error: null, ...counts })
    remapAfterMeaning(id, s, 5_000)
    return { ms: Date.now() - t0 }
  } catch (e) {
    // Cut off by a stop: left 'building' or 'updating' for the next start.
    if (stopping) throw e
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
    await run(path.join(GUFI_BIN, 'gufi_dir2index'), ['-x', '-n', String(threadsFor()), '--skip-file', skipFile(def.id), src, stage], undefined, false, def.id, true)
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
    await run(path.join(GUFI_BIN, 'gufi_dir2index'), ['-x', '-n', String(Math.min(4, threadsFor())), '--max-level', '0', '--skip-file', skipFile(def.id), def.sourceRoot!, stage], undefined, false, def.id, true)
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
 * index of a folder that is gone is removed; then a change of depth since
 * the last build or sync is applied over the whole library. Returns what
 * changed.
 */
export async function syncLibrary(id: string): Promise<string[]> {
  return withLibraryLock(id, async () => {
    const def = addedDef(id)
    if (!def?.build || def.build.state !== 'ready' || !def.sourceRoot) return []
    if (!isDir(def.sourceRoot)) { updateBuild(id, { error: `${def.sourceRoot} is not a folder any more` }); return [] }
    const s = builtLibrarySettings(id)
    const depth = await pendingDepthChange(id, s)
    // An index from before enrich.py dropped the provenance line: cleaned in place once.
    const tidy = textCleanNeeded(def.indexRoot)
    const { latest: scan, deniedRel } = await scanDirsDetail(def.sourceRoot, id)
    const before = loadState(id) ?? scan
    // A folder the Studio may not read now (macOS privacy settings, say) is
    // kept as it was last indexed: not taken for removed, and not indexed again.
    for (const rel of deniedRel) for (const k of Object.keys(before)) if (k === rel || !rel || k.startsWith(`${rel}/`)) scan[k] = before[k]
    if (!loadState(id)) await saveState(id, scan)
    const top = (before[''] ?? 0) < (scan[''] ?? 0)
    let changed = Object.keys(scan).filter(rel => rel && (before[rel] ?? 0) < scan[rel]).sort()
    changed = changed.filter(rel => !changed.some(o => o !== rel && rel.startsWith(`${o}/`)))
    const removed = Object.keys(before).filter(rel => rel && !(rel in scan) && !Object.keys(scan).some(s => rel.startsWith(`${s}/`) && changed.includes(s)))
    if (!top && !changed.length && !removed.length && !depth && !tidy) { settleDenied(id); return [] }
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
    await saveState(id, next)
    if (depth) {
      // Shown as updating while it runs; a failure keeps the old record, so
      // the next sync tries again.
      updateBuild(id, { state: 'updating', pending: 'depth' })
      try { await applyDepthChange(def, depth, s); done.push(`(index depth: ${s.depth})`) } catch (e) { failed.push(`index depth: ${(e as Error).message}`) } finally {
        // Cut off by a stop, it stays 'updating', and the next start finishes it with a sync.
        if (!stopping) updateBuild(id, { state: 'ready', pending: null })
      }
      if (stopping) return done
    }
    if (tidy) {
      try {
        const r = await cleanIndexedText(def.indexRoot)
        if (r) { await markTextClean(def.indexRoot); if (r.rows) done.push('(indexed text tidied)') }
      } catch (e) { failed.push(`tidying the indexed text: ${(e as Error).message}`) }
    }
    const counts = await settle(def)
    settleDenied(id)
    updateBuild(id, { error: failed.length ? failed.join('; ').slice(0, 400) : null, ...counts })
    // A new depth that adds meaning maps soon; a sync's changes, later.
    if (done.length) remapAfterMeaning(id, s, depth?.meaning ? 5_000 : 10 * 60_000)
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
  if (a.depth !== undefined && !isIndexDepth(a.depth)) throw new KbError(400, 'depth: names, text, ocr, meaning-documents or meaning-full')
  for (const dir of c.create) await fsp.mkdir(dir, { recursive: true })
  const src = realOr(c.sourceRoot)
  const label = String(a.label ?? '').trim() || path.basename(src)
  addBuiltLibrary({
    id: c.id, label, indexRoot: path.join(c.indexDir, path.basename(src)), sourceRoot: src,
    build: { indexDir: c.indexDir, cacheRoot: path.join(builtLibraryDir(c.id), 'extract'), managed: c.managed, state: 'building', error: null, builtAt: null, files: null, dirs: null },
  })
  // The depth is stored with the library, the default included, so a later
  // change of the default leaves it as it was built.
  await saveIndexSettings(c.id, a.depth !== undefined ? { depth: a.depth } : a.meaning !== undefined ? { meaning: a.meaning } : { depth: NEW_LIBRARY_DEPTH })
  return { id: c.id, job: startBuild(c.id) }
}

const builtIds = () => listLibraries().filter(l => l.build).map(l => l.id)

let syncOn = false
const kicks = new Map<string, NodeJS.Timeout>()

/**
 * Asks for a library's sync soon, as a change of its depth does: after a
 * short wait, so a depth chosen in passing syncs once, and behind a build
 * that is running (the library's lock). Only where the background sync runs
 * and is not paused.
 */
export function requestLibrarySync(id: string, delayMs = 2000): void {
  if (!syncOn || effectiveSettings().sweepIntervalSec <= 0 || !addedDef(id)?.build) return
  const was = kicks.get(id)
  if (was) clearTimeout(was)
  const t = setTimeout(() => {
    kicks.delete(id)
    void syncLibrary(id).catch(e => console.warn(`library ${id}: sync failed: ${(e as Error).message}`))
  }, delayMs)
  t.unref?.()
  kicks.set(id, t)
}

/** The same for every library the Studio builds, as a change they all follow does (the speed of search by meaning). */
export function requestAllLibrarySyncs(): void {
  for (const id of builtIds()) requestLibrarySync(id)
}

/**
 * Keeps built libraries in step with their folders on the sweep interval
 * (Settings, General; 0 pauses it), one library at a time, and finishes a
 * build a restart interrupted.
 */
export function startLibrarySync(log: (msg: string) => void): void {
  syncOn = true
  stopping = false
  for (const id of builtIds()) {
    const b = addedDef(id)?.build
    const s = b?.state
    if (s === 'updating' && b?.pending === 'depth' && fs.existsSync(path.join(addedDef(id)!.indexRoot, 'db.db'))) {
      // A change of depth a stop cut off: its index is whole, and the depth
      // record was not written, so a sync applies the change again.
      updateBuild(id, { state: 'ready', pending: null })
      log(`library ${id}: applying its index depth was interrupted; finishing it`)
      void syncLibrary(id).catch(e => log(`library ${id}: sync failed: ${(e as Error).message}`))
    } else if (s === 'building' || s === 'updating') {
      log(`library ${id}: the build was interrupted; building it again from what was read so far`)
      startBuild(id)
    }
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
