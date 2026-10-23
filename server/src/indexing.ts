import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { createInterface } from 'node:readline'
import {
  EMBED_CACHE, EXCLUDE_DIRS, EXTRACT_CACHE, GUFI_BIN, GUFI_INDEX, INDEX_BASE, INDEX_DIR, KB_ROOT, PROJECT_ROOT, PYTHON_BIN, SEMANTIC_MAP_DIR,
  gufiEnv, indexerEnv,
} from './config.js'
import { invalidateDbList } from './gufi.js'
import { preextractCommand, downmarkNative } from './preextract.js'
import { invalidateContext } from './chat/context.js'
import { effectiveSettings } from './settings.js'
import { indexLog } from './indexLog.js'
import { indexSettings, meaningArgs, textArgs, threadsFor, type IndexSettings } from './indexSettings.js'
import { sofficeFound } from './extractors.js'
import { scheduleSemanticMapRebuild } from './semanticMap.js'
import { scanLatest } from './dirScan.js'
import { depthChange, depthRecordOf, readDepthRecord, writeDepthRecord, type DepthChange } from './depthState.js'

/**
 * Building the index, and every later change to it, as jobs that run one at
 * a time: a whole-folder build, one folder after an upload or an edit, and
 * the background sync. Each job goes through the same phases, each with
 * its own count:
 *
 *   names      gufi_dir2index: names, sizes and dates of every file and folder
 *   documents  downmark converts PDFs and Office documents (preextract.ts)
 *   text       enrich.py: images (OCR, descriptions), other readers, and the
 *              full-text tables
 *   meaning    embed.py: embeddings for search by meaning
 *
 * Every phase runs its work side by side on the library's thread count
 * (Settings, General, Indexing). A first build puts the names in place as soon as
 * they are read, so search by name works while the text and the
 * embeddings follow; a rebuild works on a copy and swaps it in at the end,
 * so search keeps answering from the old index meanwhile. Text and
 * embeddings are cached (by file date, and by the hash of the text), so a
 * rebuild, or a folder pointed at again, redoes only what changed.
 *
 * What each job does goes to the index log (indexLog.ts); its phases,
 * counts and times are in GET /api/index/status, which every view that
 * shows indexing reads.
 */

const SKIP_FILE = path.join(INDEX_BASE, 'skip-dirs.txt')
const SWEEP_STATE = path.join(INDEX_BASE, 'sweep-state.json')
// The depth the index was last built or synced at (depthState.ts).
const DEPTH_STATE = path.join(INDEX_BASE, 'index-depth.json')
const EMBED_MODEL = path.join(INDEX_BASE, 'models', 'minilm384.gguf')
const RUNS_FILE = path.join(INDEX_BASE, 'index-runs.json')
// The folder the index and caches under INDEX_DIR were built from.
const ROOT_MARK = path.join(INDEX_DIR, 'indexed-root')
const OLD_ROOT_MARK = path.join(INDEX_BASE, 'indexed-root')
// Indexes of folders used before, kept so pointing at one again updates it
// instead of building it from nothing.
const STASH = path.join(INDEX_DIR, 'folders')
const STASH_KEEP = 3

// ---- jobs ----

export type JobKind = 'build' | 'folder' | 'sweep' | 'delete' | 'move'
export type PhaseId = 'names' | 'documents' | 'text' | 'meaning'
export type PhaseState = 'pending' | 'running' | 'done' | 'skipped' | 'failed'

export interface IndexPhase {
  id: PhaseId
  label: string
  state: PhaseState
  done: number
  /** 0 while the phase is still counting its work. */
  total: number
  unit: string
  /** What the phase is doing now, when it has steps. */
  step: string | null
  ms: number | null
  note: string | null
}

export interface IndexJob {
  id: number
  kind: JobKind
  dir: string
  label: string
  state: 'queued' | 'running' | 'done' | 'error' | 'cancelled'
  startedAt: string
  runningAt: string | null
  finishedAt: string | null
  ms: number | null
  error: string | null
  threads: number
  phases: IndexPhase[]
  /** When the names were in place and searchable. */
  searchableAt: string | null
  /** Single files that could not be read, the first few of them. */
  warnings: string[]
  warningCount: number
  /** The summary older pages read: the phase now, its count, and failures. */
  progress?: IndexProgress
}

export interface IndexProgress {
  phase: string
  done: number
  total: number
  /** What is being worked on now. */
  current: string | null
  /** Folders that could not be indexed, with why. */
  failed: string[]
}

export class IndexCancelled extends Error {
  constructor() { super('stopped'); this.name = 'IndexCancelled' }
}

const PHASES: { id: PhaseId; label: string; unit: string }[] = [
  { id: 'names', label: 'Names and folders', unit: 'folders' },
  { id: 'documents', label: 'Documents (PDF, Word, PowerPoint, Excel)', unit: 'documents' },
  { id: 'text', label: 'Text, images and full-text search', unit: 'items' },
  { id: 'meaning', label: 'Search by meaning', unit: 'passages' },
]

interface JobRec {
  pub: IndexJob
  run: (ctx: JobCtx) => Promise<void>
  abort: AbortController
  done: Promise<{ ms: number }>
  resolve: (v: { ms: number }) => void
  reject: (e: Error) => void
  key: string | null
}

interface JobCtx {
  job: IndexJob
  signal: AbortSignal
  log: (msg: string) => void
  threads: number
  settings: IndexSettings
}

let jobSeq = 0
const jobs = new Map<number, JobRec>()
const queue: JobRec[] = []
let current: JobRec | null = null
let lastFinished: IndexJob | null = null

function newPhases(): IndexPhase[] {
  return PHASES.map(p => ({ ...p, state: 'pending', done: 0, total: 0, step: null, ms: null, note: null }))
}

function enqueue(kind: JobKind, dir: string, label: string, run: (ctx: JobCtx) => Promise<void>, key: string | null = null): JobRec {
  if (key) {
    const same = queue.find(r => r.key === key)
    if (same) return same
  }
  let resolve!: (v: { ms: number }) => void
  let reject!: (e: Error) => void
  const done = new Promise<{ ms: number }>((res, rej) => { resolve = res; reject = rej })
  // Nobody may be waiting; an unwatched failure is in the status and the log.
  done.catch(() => {})
  const rec: JobRec = {
    pub: {
      id: ++jobSeq, kind, dir, label, state: 'queued', startedAt: new Date().toISOString(), runningAt: null, finishedAt: null,
      ms: null, error: null, threads: threadsFor(), phases: kind === 'delete' || kind === 'move' ? [] : newPhases(),
      searchableAt: null, warnings: [], warningCount: 0,
      progress: { phase: 'queued', done: 0, total: 0, current: null, failed: [] },
    },
    run, abort: new AbortController(), done, resolve, reject, key,
  }
  jobs.set(rec.pub.id, rec)
  // Old jobs are only interesting while a page is still polling them.
  for (const [id, r] of jobs) if (jobs.size > 30 && r.pub.state !== 'running' && r.pub.state !== 'queued') jobs.delete(id)
  queue.push(rec)
  setImmediate(pump)
  return rec
}

function pump(): void {
  if (current || !queue.length) return
  const rec = queue.shift()!
  current = rec
  const job = rec.pub
  job.state = 'running'
  job.runningAt = new Date().toISOString()
  job.threads = threadsFor()
  const t0 = Date.now()
  const log = (msg: string) => indexLog(`[${job.id} ${job.kind}] ${msg}`)
  log(`${job.label}: started on ${job.threads} threads`)
  const ctx: JobCtx = { job, signal: rec.abort.signal, log, threads: job.threads, settings: indexSettings() }
  void (async () => {
    try {
      await rec.run(ctx)
      if (rec.abort.signal.aborted) throw new IndexCancelled()
      job.state = 'done'
    } catch (e) {
      if (rec.abort.signal.aborted || e instanceof IndexCancelled) {
        job.state = 'cancelled'
        for (const p of job.phases) if (p.state === 'running') { p.state = 'failed'; p.note = 'stopped' }
      } else {
        job.state = 'error'
        job.error = String((e as Error)?.message ?? e).slice(0, 600)
        for (const p of job.phases) if (p.state === 'running') p.state = 'failed'
      }
    } finally {
      job.ms = Date.now() - t0
      job.finishedAt = new Date().toISOString()
      if (job.progress) { job.progress.phase = job.state === 'done' ? 'done' : job.state; job.progress.current = null }
      log(`${job.label}: ${job.state} in ${fmtMs(job.ms)}${job.error ? `: ${job.error}` : ''}${job.warningCount ? ` (${job.warningCount} files could not be read)` : ''}`)
      // A folder's pass after an upload or a save is routine; its phases are
      // logged, and it is kept among the runs, only when it went wrong.
      const routine = job.kind === 'folder' && job.state === 'done'
      if (!routine) for (const p of job.phases) if (p.ms !== null || p.state === 'skipped') log(`  ${p.id}: ${p.state}${p.ms !== null ? ` in ${fmtMs(p.ms)}` : ''}${p.total ? `, ${p.done} of ${p.total} ${p.unit}` : ''}${p.note ? ` (${p.note})` : ''}`)
      lastFinished = { ...job }
      if (!routine) recordRun(job)
      if (job.state === 'error') status.lastError = `${job.label}: ${job.error}`
      else if (job.state === 'done' && (job.kind === 'build' || job.kind === 'delete')) status.lastError = null
      current = null
      if (job.state === 'done') rec.resolve({ ms: job.ms })
      else rec.reject(job.state === 'cancelled' ? new IndexCancelled() : new Error(job.error ?? 'failed'))
      setImmediate(pump)
      if (sweepWanted && !queue.length) setImmediate(runWantedSweep)
    }
  })()
}

const fmtMs = (ms: number) => ms < 1000 ? `${ms} ms` : ms < 120_000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`

/** Phase bookkeeping for a job. */
function phase(ctx: JobCtx, id: PhaseId) {
  const p = ctx.job.phases.find(x => x.id === id)!
  let t0 = 0
  // The summary pages written before phases read: `directories` with what
  // is being worked on and its count, as the folder-by-folder build had.
  const sync = () => {
    if (ctx.job.progress) {
      ctx.job.progress.phase = 'directories'
      ctx.job.progress.current = p.label
      ctx.job.progress.done = p.done
      ctx.job.progress.total = p.total
    }
  }
  return {
    start(total = 0) {
      if (ctx.signal.aborted) throw new IndexCancelled()
      t0 = Date.now()
      Object.assign(p, { state: 'running', done: 0, total, step: null, ms: null, note: null })
      sync()
    },
    progress(done: number, total: number, step?: string) {
      p.done = done
      p.total = total
      if (step !== undefined) p.step = step || null
      sync()
    },
    finish(note?: string) {
      p.state = 'done'
      p.ms = Date.now() - t0
      p.step = null
      if (p.total) p.done = p.total
      if (note) p.note = note
      sync()
    },
    skip(note: string) {
      Object.assign(p, { state: 'skipped', note, ms: null, step: null })
    },
  }
}

export function getIndexJob(id: number): IndexJob | null { return jobs.get(id)?.pub ?? null }

/** Whether any index job is queued or running (an update waits for none). */
export function indexJobsRunning(): boolean { return !!current || queue.length > 0 }

/** Stops a job: one still queued is dropped, the running one has its
 *  programs stopped. Returns the job, or null when there is none. */
export function cancelIndexJob(id: number): IndexJob | null {
  const rec = jobs.get(id)
  if (!rec) return null
  const i = queue.indexOf(rec)
  if (i >= 0) {
    queue.splice(i, 1)
    Object.assign(rec.pub, { state: 'cancelled', finishedAt: new Date().toISOString(), ms: 0 })
    indexLog(`[${rec.pub.id} ${rec.pub.kind}] ${rec.pub.label}: dropped from the queue`)
    rec.reject(new IndexCancelled())
  } else if (rec === current && !rec.abort.signal.aborted) {
    indexLog(`[${rec.pub.id} ${rec.pub.kind}] ${rec.pub.label}: stopping`)
    rec.abort.abort()
  }
  return rec.pub
}

/** Stops everything: the running job and every queued one. */
export function cancelAllIndexJobs(): number {
  let n = 0
  for (const rec of [...queue]) { cancelIndexJob(rec.pub.id); n++ }
  if (current) { cancelIndexJob(current.pub.id); n++ }
  return n
}

// ---- the record of runs, kept across restarts ----

interface RunsFile { lastBuild: (IndexJob & { root?: string }) | null; runs: (IndexJob & { root?: string })[] }

function loadRuns(): RunsFile {
  try {
    const r = JSON.parse(fs.readFileSync(RUNS_FILE, 'utf8')) as RunsFile
    return { lastBuild: r.lastBuild ?? null, runs: Array.isArray(r.runs) ? r.runs : [] }
  } catch { return { lastBuild: null, runs: [] } }
}

function recordRun(job: IndexJob): void {
  try {
    const r = loadRuns()
    const kept = { ...job, warnings: job.warnings.slice(0, 5), root: KB_ROOT }
    delete kept.progress
    r.runs = [kept, ...r.runs].slice(0, 25)
    if (job.kind === 'build' || job.kind === 'delete') r.lastBuild = kept
    fs.mkdirSync(path.dirname(RUNS_FILE), { recursive: true })
    fs.writeFileSync(RUNS_FILE, JSON.stringify(r))
  } catch { /* the record is a convenience */ }
}

// ---- the status every view reads ----

interface SweepStatus {
  lastSweepAt: string | null
  lastSweepMs: number | null
  lastChanges: string[]
  lastError: string | null
  sweepIntervalSec: number
}

const status: SweepStatus = {
  lastSweepAt: null,
  lastSweepMs: null,
  lastChanges: [],
  lastError: null,
  sweepIntervalSec: Number(process.env.SWEEP_INTERVAL_SEC ?? 300),
}

export interface IndexStatus extends SweepStatus {
  /** A job is running or waiting. */
  running: boolean
  job: IndexJob | null
  queued: { id: number; kind: JobKind; label: string }[]
  /** The job that finished last, this server start. */
  last: IndexJob | null
  /** The last whole-folder build (or delete) of this folder, across restarts. */
  lastBuild: IndexJob | null
  /** Recent jobs, newest first. */
  runs: IndexJob[]
  indexed: boolean
  /** The last build of this folder was stopped or failed part way. */
  incomplete: boolean
  threads: number
  folder: string
  indexDir: string
}

export function indexStatus(): IndexStatus {
  const r = loadRuns()
  const mine = (j: (IndexJob & { root?: string }) | null) => (j && (!j.root || j.root === KB_ROOT) ? j : null)
  const lastBuild = mine(r.lastBuild)
  return {
    ...status,
    running: !!current || queue.length > 0,
    job: current ? current.pub : null,
    queued: queue.map(q => ({ id: q.pub.id, kind: q.pub.kind, label: q.pub.label })),
    last: lastFinished,
    lastBuild,
    runs: r.runs.filter(j => mine(j)).slice(0, 10),
    indexed: fs.existsSync(path.join(GUFI_INDEX, 'db.db')),
    incomplete: !!lastBuild && lastBuild.kind === 'build' && lastBuild.state !== 'done',
    threads: threadsFor(),
    folder: KB_ROOT,
    indexDir: INDEX_DIR,
  }
}

// ---- running the tools ----

const WATCHDOG_MS = 30 * 60_000

/**
 * Run one indexing program as a job step: its `@progress` lines move the
 * phase, every other line goes to the index log, and lines about a single
 * file that failed are counted as the job's warnings. The program runs in
 * a process group of its own, so stopping the job stops its workers too.
 */
function runTool(ctx: JobCtx, ph: ReturnType<typeof phase> | null, cmd: string, args: string[], env: NodeJS.ProcessEnv): Promise<string[]> {
  if (ctx.signal.aborted) return Promise.reject(new IndexCancelled())
  const name = path.basename(args.find(a => /\.(py|js|ts)$/.test(a)) ?? cmd)
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] })
    const out: string[] = []
    const errTail: string[] = []
    let last = Date.now()
    const kill = (sig: NodeJS.Signals) => { try { if (child.pid) process.kill(-child.pid, sig) } catch { /* gone */ } }
    const onAbort = () => {
      kill('SIGTERM')
      setTimeout(() => kill('SIGKILL'), 5000).unref()
    }
    ctx.signal.addEventListener('abort', onAbort, { once: true })
    const watchdog = setInterval(() => {
      if (Date.now() - last > WATCHDOG_MS) {
        ctx.log(`${name}: no progress for ${WATCHDOG_MS / 60_000} minutes; stopping it`)
        errTail.push(`no progress for ${WATCHDOG_MS / 60_000} minutes`)
        onAbort()
      }
    }, 30_000)
    watchdog.unref()
    createInterface({ input: child.stdout! }).on('line', line => {
      last = Date.now()
      if (line.startsWith('@progress ')) {
        try {
          const p = JSON.parse(line.slice(10)) as { done?: number; total?: number; step?: string }
          ph?.progress(Number(p.done) || 0, Number(p.total) || 0, p.step)
        } catch { /* a malformed line is dropped */ }
        return
      }
      if (!line.trim() || ALREADY_EXISTS.test(line)) return
      out.push(line)
      // What a program reports on success is worth keeping for a build; a
      // folder's routine pass would fill the log with it.
      if (ctx.job.kind === 'build') ctx.log(`${name}: ${line.trim().slice(0, 500)}`)
    })
    createInterface({ input: child.stderr! }).on('line', line => {
      last = Date.now()
      const t = line.trim()
      if (!t || ALREADY_EXISTS.test(t)) return
      ctx.log(`${name}: ${t.slice(0, 500)}`)
      errTail.push(t)
      if (errTail.length > 20) errTail.shift()
      if (/\bfailed\b|^preextract: .*: |embed error/.test(t)) {
        ctx.job.warningCount++
        if (ctx.job.warnings.length < 20) ctx.job.warnings.push(t.replace(/^\s+/, '').slice(0, 300))
      }
    })
    child.on('error', e => {
      clearInterval(watchdog)
      ctx.signal.removeEventListener('abort', onAbort)
      reject(new Error(`${name}: ${e.message}`))
    })
    child.on('close', code => {
      clearInterval(watchdog)
      ctx.signal.removeEventListener('abort', onAbort)
      if (ctx.signal.aborted) return reject(new IndexCancelled())
      if (code !== 0) return reject(new Error(`${name}: ${errTail.slice(-3).join(' ') || `exit ${code}`}`))
      resolve(out)
    })
  })
}

function ensureSkipFile(): string {
  if (!fs.existsSync(SKIP_FILE)) {
    fs.mkdirSync(INDEX_BASE, { recursive: true })
    fs.writeFileSync(SKIP_FILE, [...EXCLUDE_DIRS, '.claude', '.github'].join('\n') + '\n')
  }
  return SKIP_FILE
}

/** Folders under a directory that indexing enters, for the names count. */
async function countDirs(root: string, maxLevel: number | null): Promise<number> {
  let n = 0
  const walk = async (dir: string, level: number): Promise<void> => {
    n++
    if (maxLevel !== null && level >= maxLevel) return
    let entries: fs.Dirent[]
    try { entries = await fsp.readdir(dir, { withFileTypes: true }) } catch { return }
    await Promise.all(entries.filter(e => e.isDirectory() && !EXCLUDE_DIRS.has(e.name) && e.name !== '.claude' && e.name !== '.github')
      .map(e => walk(path.join(dir, e.name), level + 1)))
  }
  await walk(root, 0)
  return n
}

/** db.db files written so far under a tree, for the names count. */
async function countDbs(root: string): Promise<number> {
  let n = 0
  const walk = async (dir: string): Promise<void> => {
    let entries: fs.Dirent[]
    try { entries = await fsp.readdir(dir, { withFileTypes: true }) } catch { return }
    for (const e of entries) if (e.name === 'db.db') n++
    await Promise.all(entries.filter(e => e.isDirectory()).map(e => walk(path.join(dir, e.name))))
  }
  await walk(root)
  return n
}

/** gufi_dir2index notes, on either stream, that the staging folder made
 *  for it exists: expected, and not worth a line in the log. */
const ALREADY_EXISTS = /Already exists!\s*$/

/** Names: gufi_dir2index into `staging`; returns the tree it wrote. The
 *  staging folder is emptied first: its name comes from the job number,
 *  which starts again at 1 when the Studio does, so a build that stopped
 *  partway leaves one a later build would otherwise write over, keeping
 *  folders deleted since. */
async function namesPhase(ctx: JobCtx, src: string, staging: string, maxLevel0 = false): Promise<string> {
  const ph = phase(ctx, 'names')
  ph.start()
  const total = await countDirs(src, maxLevel0 ? 0 : null)
  ph.progress(0, total)
  await fsp.rm(staging, { recursive: true, force: true })
  await fsp.mkdir(staging, { recursive: true })
  const built = path.join(staging, path.basename(src))
  // gufi_dir2index says nothing until it is done; the databases it has
  // written so far are the count.
  let counting = false
  const poll = setInterval(() => {
    if (counting) return
    counting = true
    void countDbs(built).then(n => ph.progress(Math.min(n, total), total)).finally(() => { counting = false })
  }, 1000)
  try {
    await runTool(ctx, null, path.join(GUFI_BIN, 'gufi_dir2index'),
      ['-x', '-n', String(ctx.threads), ...(maxLevel0 ? ['--max-level', '0'] : []), '--skip-file', ensureSkipFile(), src, staging],
      gufiEnv())
  } finally { clearInterval(poll) }
  ph.finish()
  return built
}

/** Documents: downmark into the extract cache. */
async function documentsPhase(ctx: JobCtx, scope: { subdir?: string; noRecurse?: boolean }): Promise<void> {
  const ph = phase(ctx, 'documents')
  if (!ctx.settings.documents) { ph.skip('the index depth is names'); return }
  ph.start()
  const args = ['--kb-root', KB_ROOT, '--extract-cache', EXTRACT_CACHE, '--jobs', String(ctx.threads), '--progress']
  if (scope.subdir) args.push('--subdir', scope.subdir)
  if (scope.noRecurse) args.push('--no-recurse')
  if (!ctx.settings.ocr) args.push('--no-ocr')
  const { cmd, args: argv } = preextractCommand(args)
  // OCR inside downmark runs tesseract; many side by side each do best on one thread.
  const out = await runTool(ctx, ph, cmd, argv, { ...process.env, OMP_THREAD_LIMIT: '1' })
  let note: string | undefined
  try {
    const s = JSON.parse(out[out.length - 1] ?? '') as { converted: number; reused: number; failed: number }
    note = [`${s.converted} converted`, s.reused ? `${s.reused} unchanged` : '', s.failed ? `${s.failed} could not be read` : '']
      .filter(Boolean).join(', ')
  } catch { /* no summary */ }
  if (!downmarkNative()) note = `${note ? `${note}; ` : ''}PDFs are read in the next phase`
  ph.finish(note)
}

function textEnv(ctx: JobCtx): NodeJS.ProcessEnv {
  // Settings can change the captioning model at runtime. Images are
  // described only where the depth reads them (Text + OCR and up); with AI
  // features off, or descriptions off, they are indexed by OCR and name only.
  const eff = effectiveSettings()
  const vm = eff.aiEnabled && ctx.settings.ocr && ctx.settings.imageDescriptions ? eff.visionModel : ''
  const env = indexerEnv(vm ? { ADE_VISION_MODEL: vm } : {})
  if (!vm) delete env.ADE_VISION_MODEL
  return env
}

/** Text: enrich.py over an index tree that mirrors `kbRoot`. */
async function textPhase(ctx: JobCtx, o: { kbRoot: string; index: string; cache: string; noRecurse?: boolean }): Promise<void> {
  const ph = phase(ctx, 'text')
  const depth = textArgs(ctx.settings.depth)
  if (!depth) { ph.skip('the index depth is names'); return }
  ph.start()
  const args = [path.join(PROJECT_ROOT, 'indexer', 'enrich.py'), '--kb-root', o.kbRoot, '--index', o.index,
    '--extract-cache', o.cache, '--jobs', String(ctx.threads), '--progress', '--pdf-reader', ctx.settings.pdfReader, ...depth]
  if (o.noRecurse) args.push('--no-recurse')
  const soffice = ctx.settings.legacyOffice ? sofficeFound() : null
  if (soffice) args.push('--soffice', soffice)
  const out = await runTool(ctx, ph, PYTHON_BIN, args, textEnv(ctx))
  const m = /: (\d+) files indexed/.exec(out.join('\n'))
  ph.finish(m ? `${Number(m[1]).toLocaleString('en-US')} files with text` : undefined)
}

/** Meaning: embed.py over an index tree, with the embedding cache. */
async function meaningPhase(ctx: JobCtx, o: { index: string; noRecurse?: boolean; prune?: boolean }): Promise<void> {
  const ph = phase(ctx, 'meaning')
  const level = meaningArgs(ctx.settings.meaning, ctx.settings.meaningSpeed)
  if (!level) { ph.skip('the index depth stops before search by meaning'); return }
  if (!fs.existsSync(EMBED_MODEL)) { ph.skip('no embedding model'); return }
  if (!fs.existsSync(path.join(GUFI_BIN, 'gufi_sqlite3'))) { ph.skip('no gufi_sqlite3'); return }
  ph.start()
  const args = [path.join(PROJECT_ROOT, 'indexer', 'embed.py'), '--index', o.index, '--model', EMBED_MODEL,
    '--gufi-sqlite3', path.join(GUFI_BIN, 'gufi_sqlite3'), '--jobs', String(ctx.threads), '--cache', EMBED_CACHE, '--progress', ...level]
  if (o.noRecurse) args.push('--no-recurse')
  if (o.prune) args.push('--prune')
  const out = await runTool(ctx, ph, PYTHON_BIN, args, indexerEnv({ STUDIO_TMP: INDEX_DIR }))
  const m = /chunked: (\d+) chunks .*?, (\d+) to embed/.exec(out.join('\n'))
  ph.finish(m ? `${Number(m[1]).toLocaleString('en-US')} passages, ${Number(m[2]).toLocaleString('en-US')} new` : undefined)
  // The semantic map follows the embeddings, in the background: soon after
  // a build, and no more than every ten minutes or so after a sync.
  if (o.prune || (m && Number(m[2]) > 0)) scheduleSemanticMapRebuild(ctx.job.kind === 'build' || o.prune ? 5_000 : 10 * 60_000)
}

/** Put a built tree at `dest`, the old one out of the way first. */
async function swapIn(built: string, dest: string): Promise<void> {
  await fsp.mkdir(path.dirname(dest), { recursive: true })
  const old = `${dest}.old-${process.pid}-${Date.now()}`
  let moved = false
  try { await fsp.rename(dest, old); moved = true } catch { /* nothing there */ }
  await fsp.rename(built, dest)
  if (moved) await fsp.rm(old, { recursive: true, force: true })
}

async function afterChange(rel: string): Promise<void> {
  invalidateDbList()
  invalidateContext()
  // A rebuilt tree lost any overlay-stored labels (xattr-less filesystems);
  // put them back. Lazy import avoids a module cycle.
  const { reapplyTagOverlay } = await import('./tags.js')
  await reapplyTagOverlay(rel).catch(() => {})
  const { forgetProbe } = await import('./libraries.js')
  forgetProbe(GUFI_INDEX)
}

// ---- one folder, and the files at the top ----

async function indexSubtree(ctx: JobCtx, cleaned: string): Promise<void> {
  const src = path.join(KB_ROOT, cleaned)
  if (!fs.existsSync(src) || !fs.statSync(src).isDirectory()) throw new Error(`not a directory: ${cleaned}`)
  if (ctx.job.progress) ctx.job.progress.current = cleaned
  const staging = path.join(INDEX_DIR, `inc-${ctx.job.id}-${Math.floor(Math.random() * 1e6)}`)
  try {
    // The whole subtree is built aside and swapped in at the end, so search
    // answers from the old one until the new one has its text.
    const built = await namesPhase(ctx, src, staging)
    await documentsPhase(ctx, { subdir: cleaned })
    await textPhase(ctx, { kbRoot: src, index: built, cache: path.join(EXTRACT_CACHE, cleaned) })
    await meaningPhase(ctx, { index: built })
    if (ctx.signal.aborted) throw new IndexCancelled()
    await swapIn(built, path.join(GUFI_INDEX, cleaned))
    ctx.job.searchableAt = new Date().toISOString()
    await afterChange(cleaned)
  } finally {
    await fsp.rm(staging, { recursive: true, force: true })
  }
}

async function indexRootFiles(ctx: JobCtx): Promise<void> {
  const staging = path.join(INDEX_DIR, `inc-root-${ctx.job.id}`)
  if (ctx.job.progress) ctx.job.progress.current = '(files at the top)'
  try {
    const built = await namesPhase(ctx, KB_ROOT, staging, true)
    await documentsPhase(ctx, { noRecurse: true })
    await textPhase(ctx, { kbRoot: KB_ROOT, index: built, cache: EXTRACT_CACHE, noRecurse: true })
    await meaningPhase(ctx, { index: built, noRecurse: true })
    if (ctx.signal.aborted) throw new IndexCancelled()
    await fsp.mkdir(GUFI_INDEX, { recursive: true })
    await fsp.rename(path.join(built, 'db.db'), path.join(GUFI_INDEX, 'db.db'))
    ctx.job.searchableAt = new Date().toISOString()
    // The rebuilt root db lost any overlay-stored labels on root-level
    // entries; a full re-apply is idempotent and cheap.
    await afterChange('')
  } finally {
    await fsp.rm(staging, { recursive: true, force: true })
  }
}

function cleanRel(rel: string): string {
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  const src = path.join(KB_ROOT, cleaned)
  if (!path.resolve(src).startsWith(KB_ROOT + path.sep)) throw new Error('path escapes the knowledge base')
  return cleaned
}

/**
 * Re-index a single KB subtree: gufi_dir2index into a staging area, its
 * text and embeddings there (from the caches where nothing changed), then
 * swap it in. Waits for the jobs ahead of it.
 */
export function incrementalIndexDir(rel: string): Promise<{ ms: number }> {
  let cleaned: string
  try {
    cleaned = cleanRel(rel)
    if (!cleaned) throw new Error('use a full rebuild for the root')
  } catch (e) { return Promise.reject(e) }
  return enqueue('folder', cleaned, `Updating ${cleaned}`, ctx => indexSubtree(ctx, cleaned), `folder:${cleaned}`).done
}

/** Rebuild only the root directory's db (files directly under KB_ROOT). */
export function indexRootDb(): Promise<{ ms: number }> {
  return enqueue('folder', '', 'Updating the files at the top', indexRootFiles, 'folder:').done
}

/** Re-index whatever covers a KB-relative FILE path: the file's top-level
 *  subtree, or the root db for files directly under the root. */
export function reindexForFile(rel: string): Promise<{ ms: number }> {
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  return cleaned.includes('/') ? incrementalIndexDir(cleaned.split('/')[0]) : indexRootDb()
}

/** Re-index whatever covers a KB-relative DIRECTORY: its top-level subtree,
 *  or the root db when the directory is the root itself. */
export function reindexForDir(rel: string): Promise<{ ms: number }> {
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  return cleaned ? incrementalIndexDir(cleaned.split('/')[0]) : indexRootDb()
}

/**
 * An index job over one directory, returned at once so an upload can answer
 * as soon as the files are on disk; its progress is read through
 * GET /api/index/job/:id. `work` replaces the default pass, for callers with
 * a job of their own (a library built from another folder, libraryBuild.ts):
 * that runs at once, beside the main folder's queue, with its own lock and
 * its own `progress`, and is not part of the main folder's status.
 */
export function startIndexJob(rel: string, work?: (job: IndexJob) => Promise<{ ms: number }>): IndexJob {
  if (work) return startOwnJob(rel, work)
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  const top = cleaned.split('/')[0]
  return top
    ? enqueue('folder', top, `Updating ${top}`, ctx => indexSubtree(ctx, cleanRel(top)), `folder:${top}`).pub
    : enqueue('folder', '', 'Updating the files at the top', indexRootFiles, 'folder:').pub
}

function startOwnJob(rel: string, work: (job: IndexJob) => Promise<{ ms: number }>): IndexJob {
  const job: IndexJob = {
    id: ++jobSeq, kind: 'folder', dir: rel, label: `Indexing ${rel}`, state: 'running', startedAt: new Date().toISOString(),
    runningAt: new Date().toISOString(), finishedAt: null, ms: null, error: null, threads: threadsFor(), phases: [],
    searchableAt: null, warnings: [], warningCount: 0,
  }
  const done = new Promise<{ ms: number }>(() => {})
  jobs.set(job.id, { pub: job, run: async () => {}, abort: new AbortController(), done, resolve: () => {}, reject: () => {}, key: null })
  const t0 = Date.now()
  void work(job).then(({ ms }) => { job.ms = ms; job.state = 'done' }, e => {
    job.error = String((e as Error)?.message ?? e).slice(0, 400)
    job.state = 'error'
  }).finally(() => { job.finishedAt = new Date().toISOString(); job.ms ??= Date.now() - t0 })
  return job
}

// ---- the whole folder ----

const stashDir = (root: string) => path.join(STASH, createHash('sha1').update(path.resolve(root)).digest('hex').slice(0, 16))

async function move(from: string, to: string): Promise<boolean> {
  try { await fsp.access(from) } catch { return false }
  await fsp.mkdir(path.dirname(to), { recursive: true })
  await fsp.rm(to, { recursive: true, force: true })
  try {
    await fsp.rename(from, to)
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== 'EXDEV') throw e
    await fsp.cp(from, to, { recursive: true })
    await fsp.rm(from, { recursive: true, force: true })
  }
  return true
}

function readMark(): string {
  for (const f of [ROOT_MARK, OLD_ROOT_MARK]) {
    try { return fs.readFileSync(f, 'utf8').trim() } catch { /* next */ }
  }
  return ''
}

/**
 * Caches and the index tree are keyed by the path inside the folder, so a
 * switch to another folder must not let them stand in for it. The index of
 * the folder used before is put aside (INDEX_DIR/folders), and one put
 * aside for this folder earlier is brought back, so pointing at it again
 * updates that index rather than building one from nothing. Runs at start
 * and before a build.
 */
let reconciling: Promise<void> | null = null
export function reconcileFolder(log: (m: string) => void = indexLog): Promise<void> {
  // One at a time: the start and a build asking at once must not both move things.
  reconciling ??= reconcileOnce(log).finally(() => { reconciling = null })
  return reconciling
}

/** The same folder, however the path was written (a trailing slash, a link). */
function sameFolder(a: string, b: string): boolean {
  if (path.resolve(a) === path.resolve(b)) return true
  try { return fs.realpathSync(a) === fs.realpathSync(b) } catch { return false }
}

async function reconcileOnce(log: (m: string) => void): Promise<void> {
  const was = readMark()
  if (was && sameFolder(was, KB_ROOT)) {
    // Recorded as written here, so a later comparison is exact.
    if (was !== KB_ROOT) await fsp.writeFile(ROOT_MARK, `${KB_ROOT}\n`)
    return
  }
  if (was) {
    const to = stashDir(was)
    await fsp.mkdir(to, { recursive: true })
    const moved = [
      await move(path.join(INDEX_DIR, 'gufi', path.basename(was)), path.join(to, 'gufi')),
      await move(EXTRACT_CACHE, path.join(to, 'extract')),
      await move(EMBED_CACHE, path.join(to, 'embed-cache.db')),
      await move(SWEEP_STATE, path.join(to, 'sweep-state.json')),
      await move(DEPTH_STATE, path.join(to, 'index-depth.json')),
    ]
    await fsp.writeFile(path.join(to, 'root'), `${was}\n`)
    if (moved.some(Boolean)) log(`folder: put the index of ${was} aside in ${to}`)
    // Previews are cheap to make again, and keyed the same way.
    for (const d of ['pdf-preview', 'pdf-pages', 'model-cache']) await fsp.rm(path.join(INDEX_BASE, d), { recursive: true, force: true })
    await fsp.rm(SEMANTIC_MAP_DIR, { recursive: true, force: true })
    // Keep the few most recent.
    const kept = (await fsp.readdir(STASH).catch(() => [] as string[])).map(n => path.join(STASH, n))
    const dated = await Promise.all(kept.map(async d => ({ d, t: (await fsp.stat(d).catch(() => null))?.mtimeMs ?? 0 })))
    for (const { d } of dated.sort((a, b) => b.t - a.t).slice(STASH_KEEP)) await fsp.rm(d, { recursive: true, force: true })
  }
  // A tree left for this folder's name by something else is not this folder's.
  if (was) await fsp.rm(GUFI_INDEX, { recursive: true, force: true })
  const mine = stashDir(KB_ROOT)
  if (fs.existsSync(path.join(mine, 'root')) && !fs.existsSync(path.join(GUFI_INDEX, 'db.db'))) {
    await move(path.join(mine, 'gufi'), GUFI_INDEX)
    await move(path.join(mine, 'extract'), EXTRACT_CACHE)
    await move(path.join(mine, 'embed-cache.db'), EMBED_CACHE)
    await move(path.join(mine, 'sweep-state.json'), SWEEP_STATE)
    await move(path.join(mine, 'index-depth.json'), DEPTH_STATE)
    await fsp.rm(mine, { recursive: true, force: true })
    log(`folder: brought back the earlier index of ${KB_ROOT}; the next build updates it`)
  }
  await fsp.mkdir(INDEX_DIR, { recursive: true })
  await fsp.writeFile(ROOT_MARK, `${KB_ROOT}\n`)
  if (OLD_ROOT_MARK !== ROOT_MARK) await fsp.rm(OLD_ROOT_MARK, { force: true })
  invalidateDbList()
}

/** An index of this folder from before, put aside when another folder was in use. */
export function earlierIndexOf(root: string): { builtAt: string | null } | null {
  const d = stashDir(root)
  if (!fs.existsSync(path.join(d, 'root'))) return null
  let builtAt: string | null = null
  try { builtAt = fs.statSync(path.join(d, 'gufi', 'db.db')).mtime.toISOString() } catch { /* no tree */ }
  return { builtAt }
}

async function buildWholeFolder(ctx: JobCtx): Promise<void> {
  await reconcileFolder(ctx.log)
  // Taken first, so a change made during the build is still new to the sweep.
  const snapshot = await sweepSnapshot()
  const live = fs.existsSync(path.join(GUFI_INDEX, 'db.db'))
  ctx.job.label = live ? 'Updating the index' : 'Building the index'
  const staging = path.join(INDEX_DIR, `gufi.staging-${ctx.job.id}`)
  try {
    const built = await namesPhase(ctx, KB_ROOT, staging)
    let target = built
    if (!live) {
      // Nothing to lose: names are searchable now, and the text and
      // embeddings fill in as they are made.
      await swapIn(built, GUFI_INDEX)
      target = GUFI_INDEX
      ctx.job.searchableAt = new Date().toISOString()
      invalidateDbList()
      ctx.log('names are searchable')
    }
    await documentsPhase(ctx, {})
    await textPhase(ctx, { kbRoot: KB_ROOT, index: target, cache: EXTRACT_CACHE })
    if (!live) invalidateContext()
    await meaningPhase(ctx, { index: target, prune: true })
    if (ctx.signal.aborted) throw new IndexCancelled()
    if (live) {
      await swapIn(built, GUFI_INDEX)
      ctx.job.searchableAt = new Date().toISOString()
    }
    await afterChange('')
    await saveSweepState(snapshot)
    await writeDepthRecord(DEPTH_STATE, depthRecordOf(ctx.settings))
  } finally {
    await fsp.rm(staging, { recursive: true, force: true })
  }
}

/** The whole main folder, as one job; a second request while one is
 *  queued or running gets that one. */
export function startBuild(): IndexJob {
  const running = [current, ...queue].find(r => r?.pub.kind === 'build')
  if (running) return running.pub
  return enqueue('build', '', 'Building the index', buildWholeFolder, 'build').pub
}

/** The running or queued whole-folder build, if any. */
export function currentBuild(): IndexJob | null {
  return [current, ...queue].find(r => r?.pub.kind === 'build')?.pub ?? null
}

/**
 * Deletes the main folder's index and its caches; the files in the folder
 * are never touched. Stops what is running first. Search finds nothing
 * until the next build.
 */
export function startDelete(): IndexJob {
  cancelAllIndexJobs()
  return enqueue('delete', '', 'Deleting the index', async ctx => {
    for (const p of [GUFI_INDEX, EXTRACT_CACHE, EMBED_CACHE, SWEEP_STATE, DEPTH_STATE, ROOT_MARK, SEMANTIC_MAP_DIR,
      path.join(INDEX_BASE, 'pdf-preview'), path.join(INDEX_BASE, 'pdf-pages'), path.join(INDEX_BASE, 'model-cache')]) {
      await fsp.rm(p, { recursive: true, force: true })
    }
    ctx.log(`deleted the index of ${KB_ROOT} (the folder's files are untouched)`)
    status.lastChanges = []
    await afterChange('')
  }).pub
}

/** Runs a job that is not an index pass (moving the index), in turn with the rest. */
export function startOtherJob(kind: 'move', label: string, work: (log: (m: string) => void) => Promise<void>): IndexJob {
  return enqueue(kind, '', label, ctx => work(ctx.log)).pub
}

// ---- the background sync ----

/** Remove cache entries whose source file no longer exists. Cache layouts
 *  mirror the KB tree with a suffix appended (extract: <rel>.txt keeps the
 *  original extension; pdf-preview: <rel minus extension>.pdf). */
async function pruneCache(cacheRoot: string, kind: '.txt' | '.pdf' | '.json'): Promise<void> {
  const walk = async (dir: string, rel: string) => {
    let entries
    try { entries = await fsp.readdir(dir, { withFileTypes: true }) } catch { return }
    for (const e of entries) {
      const abs = path.join(dir, e.name)
      const childRel = rel ? `${rel}/${e.name}` : e.name
      if (e.isDirectory()) { await walk(abs, childRel); continue }
      let exists = false
      if (e.name.endsWith('.tmp')) {
        // preextract writes <entry>.<pid>.tmp and renames; a pass killed
        // mid-write leaves one behind, and no later pass would ever look at
        // it again. Only sweep the stale ones: an in-flight conversion is
        // capped well under this, so anything older is abandoned.
        let age = 0
        try { age = Date.now() - (await fsp.stat(abs)).mtimeMs } catch { /* gone already */ }
        exists = age < 60 * 60_000
      } else if (kind === '.txt' && e.name.endsWith('.txt')) {
        exists = fs.existsSync(path.join(KB_ROOT, childRel.slice(0, -4)))
      } else if (kind === '.pdf' && e.name.endsWith('.pdf')) {
        const base = path.join(KB_ROOT, path.dirname(childRel), path.basename(e.name, '.pdf'))
        exists = ['.docx', '.pptx', '.xlsx', '.doc', '.ppt', '.xls', '.odt', '.odp', '.ods']
          .some(ext => fs.existsSync(base + ext))
      } else {
        exists = true
      }
      if (!exists) await fsp.rm(abs, { force: true }).catch(() => {})
    }
  }
  await walk(cacheRoot, '')
}

interface DirScan { latest: number }

/** Latest mtime a directory presents: itself plus its direct files. */
async function scanKbDirs(): Promise<Map<string, DirScan>> {
  const out = new Map<string, DirScan>()
  for (const [rel, latest] of await scanLatest(KB_ROOT, EXCLUDE_DIRS)) out.set(rel, { latest })
  return out
}

function loadSweepState(): Record<string, number> {
  try { return JSON.parse(fs.readFileSync(SWEEP_STATE, 'utf8')) } catch { return {} }
}

let sweeping = false

// ---- a change of depth ----

/**
 * The depth change the next sync applies, from the record of the depth the
 * index was last built or synced at: null when there is nothing to run.
 * With no index yet there is nothing to apply it to (the first build reads
 * at the depth set). An index with no record, one from before the record
 * was kept, counts as built at the depth set now. A depth that only went
 * down runs nothing and is recorded here.
 */
async function pendingDepthChange(settings: IndexSettings): Promise<DepthChange | null> {
  if (!fs.existsSync(path.join(GUFI_INDEX, 'db.db'))) return null
  const now = depthRecordOf(settings)
  const was = readDepthRecord(DEPTH_STATE)
  const ch = was ? depthChange(was, now) : null
  if (!was || (ch && !ch.text && !ch.meaning)) await writeDepthRecord(DEPTH_STATE, now)
  return ch && (ch.text || ch.meaning) ? ch : null
}

/**
 * Applies a change of depth over the whole folder, on the index in place:
 * only the phases the new depth adds (depthChange), with the caches, so
 * unchanged documents are not converted again and passages already
 * embedded are not embedded again. The names are not read again. The
 * record is written only when it succeeded, so a run that failed or was
 * stopped is tried again at the next sync.
 */
async function applyDepthChange(ctx: JobCtx): Promise<DepthChange | null> {
  const ch = await pendingDepthChange(ctx.settings)
  if (!ch) return null
  ctx.job.phases = newPhases()
  ctx.job.label = `Applying the new index depth: ${ch.label}`
  ctx.log(`depth: applying ${ctx.settings.depth} (${ch.label}) over the whole folder`)
  phase(ctx, 'names').skip('unchanged')
  if (ch.text) {
    await documentsPhase(ctx, {})
    await textPhase(ctx, { kbRoot: KB_ROOT, index: GUFI_INDEX, cache: EXTRACT_CACHE })
  } else {
    for (const id of ['documents', 'text'] as const) phase(ctx, id).skip('unchanged')
  }
  if (ch.meaning) await meaningPhase(ctx, { index: GUFI_INDEX, prune: true })
  else phase(ctx, 'meaning').skip('unchanged')
  if (ctx.signal.aborted) throw new IndexCancelled()
  await afterChange('')
  await writeDepthRecord(DEPTH_STATE, depthRecordOf(ctx.settings))
  return ch
}

/** Records the depth of an index that has none yet, at the start: an index
 *  from before the record was kept counts as built at its depth now. */
async function primeDepthState(): Promise<void> {
  if (!fs.existsSync(DEPTH_STATE)) await pendingDepthChange(indexSettings())
}

// ---- a sync soon, for a change of depth ----

let sweepTimerOn = false
let sweepLog: (msg: string) => void = indexLog
let sweepWanted = false
let kickTimer: NodeJS.Timeout | null = null

/**
 * Asks for a sync soon, as a change of depth does: after a short wait, so a
 * slider dragged across several stops syncs once, and after the job that
 * is running or waiting. Only where the background sync runs and is not
 * paused (Settings, General; Sync now applies it either way).
 */
export function requestSweep(delayMs = 2000): void {
  if (!sweepTimerOn || effectiveSettings().sweepIntervalSec <= 0) return
  sweepWanted = true
  if (kickTimer) clearTimeout(kickTimer)
  kickTimer = setTimeout(runWantedSweep, delayMs)
  kickTimer.unref?.()
}

function runWantedSweep(): void {
  kickTimer = null
  if (!sweepWanted || sweeping || current || queue.length) return
  sweepWanted = false
  void sweep(sweepLog)
}

/**
 * Find directories whose content is newer than the last sweep and re-index
 * just those subtrees, so files dropped into the KB by any means (scp,
 * generators, git) become searchable within one sweep interval; and apply
 * a change of depth since the last build or sync over the whole folder
 * (applyDepthChange), which no file's date shows. Skipped while another
 * index job is running or waiting; the next tick looks again.
 */
export async function sweep(log: (msg: string) => void): Promise<string[]> {
  if (sweeping || current || queue.length) return []
  sweeping = true
  const t0 = Date.now()
  try {
    const state = loadSweepState()
    const scan = await scanKbDirs()

    let rootChanged = false
    let changed: string[] = []
    for (const [rel, s] of scan) {
      if ((state[rel === '' ? '.' : rel] ?? 0) < s.latest) {
        if (rel === '') rootChanged = true
        else changed.push(rel)
      }
    }
    const removed = Object.keys(state).filter(rel => rel && rel !== '.' && !scan.has(rel))
    // A parent re-index covers its whole subtree; keep only subtree roots.
    changed.sort()
    changed = changed.filter(rel => !changed.some(other => other !== rel && rel.startsWith(other + '/')))
    const depth = await pendingDepthChange(indexSettings())
    const filesChanged = rootChanged || changed.length > 0 || removed.length > 0
    if (!filesChanged && !depth) {
      status.lastChanges = []
      return []
    }

    const failures: string[] = []
    const indexed: string[] = []
    let rootIndexed = false
    let depthApplied: string | null = null
    const rec = enqueue('sweep', '', filesChanged ? 'Syncing changes' : `Applying the new index depth: ${depth!.label}`, async ctx => {
      // Removed directories: drop their index subtrees.
      for (const rel of removed) {
        await fsp.rm(path.join(GUFI_INDEX, rel), { recursive: true, force: true }).catch(() => {})
      }
      // Stale extraction caches for files deleted outside the UI: prune them
      // so a later same-named file cannot reuse another file's text.
      await pruneCache(EXTRACT_CACHE, '.txt')
      await pruneCache(path.join(INDEX_BASE, 'pdf-preview'), '.pdf')
      await pruneCache(path.join(INDEX_BASE, 'model-cache'), '.json')
      const todo = [...(rootChanged ? [''] : []), ...changed]
      if (ctx.job.progress) ctx.job.progress.total = todo.length
      // One directory failing (a bad file, a full disk) must not poison the
      // whole sweep: index each in isolation, keep the failed ones' old state
      // so they retry next cycle, and record state only for what succeeded.
      for (const [i, rel] of todo.entries()) {
        if (ctx.signal.aborted) throw new IndexCancelled()
        ctx.job.phases = newPhases()
        ctx.job.label = `Syncing changes (${i + 1} of ${todo.length}): ${rel || 'files at the top'}`
        log(`sweep: re-indexing ${rel || 'root files'}`)
        try {
          if (rel) { await indexSubtree(ctx, rel); indexed.push(rel) } else { await indexRootFiles(ctx); rootIndexed = true }
        } catch (err) {
          if (err instanceof IndexCancelled || ctx.signal.aborted) throw err
          const msg = String((err as Error)?.message ?? err)
          failures.push(`${rel || '(root files)'}: ${msg}`)
          ctx.job.progress?.failed.push(`${rel || '(root files)'}: ${msg}`)
          log(`sweep: FAILED ${rel || 'root files'}: ${msg}`)
        }
        if (ctx.job.progress) ctx.job.progress.done = i + 1
      }
      if (rootIndexed || indexed.length || removed.length) {
        const failedSet = new Set(changed.filter(rel => !indexed.includes(rel)))
        const next: Record<string, number> = {}
        for (const [rel, s] of scan) {
          const key = rel === '' ? '.' : rel
          // A failed dir keeps its old timestamp (or stays absent) so the next
          // sweep still sees it as changed; likewise the root when it failed.
          if (key === '.' ? (rootChanged && !rootIndexed) : failedSet.has(rel)) {
            if (state[key] !== undefined) next[key] = state[key]
          } else {
            next[key] = s.latest
          }
        }
        await fsp.writeFile(SWEEP_STATE, JSON.stringify(next))
      }
      // After the folders that changed, so their state is kept even if this fails.
      if (await applyDepthChange(ctx)) depthApplied = ctx.settings.depth
      if (filesChanged) ctx.job.label = 'Syncing changes'
    })
    await rec.done.catch(() => {})
    status.lastChanges = [...(rootIndexed ? ['(root files)'] : []), ...indexed, ...removed.map(r => `${r} (removed)`),
      ...(depthApplied && rec.pub.state === 'done' ? [`(index depth: ${depthApplied})`] : [])]
    status.lastError = failures.length ? failures.join('; ') : rec.pub.state === 'error' ? rec.pub.error : null
    return indexed
  } catch (err: any) {
    status.lastError = String(err?.message ?? err)
    return []
  } finally {
    sweeping = false
    status.lastSweepAt = new Date().toISOString()
    status.lastSweepMs = Date.now() - t0
    if (sweepWanted) setImmediate(runWantedSweep)
  }
}

/**
 * With no recorded state every directory looks changed, and the first sweep
 * would rebuild the whole index one directory at a time. Assume the index is
 * current when no state exists (full rebuilds handle real drift) and record
 * the present scan without indexing.
 */
async function primeSweepState(): Promise<void> {
  if (fs.existsSync(SWEEP_STATE)) return
  await saveSweepState(await sweepSnapshot())
}

/** Each directory's latest change, keyed as the sweep records them. */
export async function sweepSnapshot(): Promise<Record<string, number>> {
  const scan = await scanKbDirs()
  const next: Record<string, number> = {}
  for (const [rel, s] of scan) next[rel === '' ? '.' : rel] = s.latest
  return next
}

/** Record a snapshot as indexed, so the next sweep looks only for later changes. */
export async function saveSweepState(state: Record<string, number>): Promise<void> {
  await fsp.mkdir(INDEX_BASE, { recursive: true })
  await fsp.writeFile(SWEEP_STATE, JSON.stringify(state))
}

export function startSweepTimer(log: (msg: string) => void): void {
  sweepTimerOn = true
  sweepLog = log
  // Self-rescheduling so a settings change to the interval applies at the
  // next cycle without a restart; 0 pauses sweeping but keeps checking.
  const tick = async (): Promise<void> => {
    const sec = effectiveSettings().sweepIntervalSec
    status.sweepIntervalSec = sec
    if (sec > 0 && !indexJobsRunning()) {
      await sweep(log)
      // History rides the sweep rather than waiting for a visit: a change
      // becomes a snapshot within one interval, and an unchanged pass
      // leaves the daily proof-of-life marker. Before this, a week of
      // churn nobody looked at collapsed into a single point at the next
      // visit. Dynamic import because history is otherwise downstream of
      // this module.
      await import('./history.js').then(m => m.captureIfStale())
        .then(snap => { if (snap) log(`history: captured snapshot ${snap.id} (${snap.files} files)`) })
        .catch(() => {})
    }
    setTimeout(() => { void tick() }, Math.max(sec, 60) * 1000).unref()
  }
  // A folder switched while the server was down is reconciled before the
  // sweep compares anything with what was recorded.
  void reconcileFolder().catch(e => log(`index: ${String((e as Error).message ?? e)}`))
    .then(() => primeSweepState())
    .then(() => primeDepthState())
    .then(() => {
      setTimeout(() => { void tick() }, Math.max(status.sweepIntervalSec, 60) * 1000).unref()
    })
}
