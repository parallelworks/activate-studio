import crypto from 'node:crypto'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { authEnabled } from '../auth.js'
import { INDEX_BASE } from '../config.js'
import { appendConversationNote } from '../conversations.js'
import { KbError } from '../kb.js'
import { getLibrary } from '../libraries.js'
import { assertFits, assertNoneExist, checkDestination, checkFiles, checkProblem, freeBytes, makeStaging, publish, removeStaging, shownFolder, size, stagedData, topNames } from './destination.js'
import { canonicalTransfer, plural, shownText, transferArgs, type TransferPlan } from './plan.js'
import { listSource, routeId, sourceRoutes } from './source.js'
import { moverById, type Mover, type MoverStatus, type SourceRoute } from './types.js'

/**
 * Transfers as jobs a person can leave: a job starts from an approved held
 * call and from nowhere else, and is kept beside the index so a reload, or a
 * restart, finds it.
 *
 * A job lists the source, gives the list to a mover, polls it, then checks
 * the destination itself: the files listed, each the size listed, nothing
 * else. Only then do they appear under their names, so `done` means the
 * Studio saw the result, whatever the mover said. A job that finds itself
 * `running` in the file when the Studio starts died with the last process
 * and is `interrupted`, never `done`: what it wrote was in its staging
 * folder, which goes with it.
 */

export type JobState = 'running' | 'done' | 'failed' | 'cancelled' | 'interrupted'

export interface TransferJob {
  id: string
  viewer: string
  conversationId: string
  /** The held call it came from, which starts one job at most. */
  hold: string
  title: string
  library: string
  source: string
  dest: string
  destShown: string
  /** What landed, or will land, at the top of the destination. */
  names: string[]
  mover: string
  /** The mover's own id for the transfer. */
  ref: string | null
  state: JobState
  phase: 'listing' | 'copying' | 'checking' | 'finishing' | null
  files: { done: number; total: number | null }
  bytes: { done: number; total: number | null }
  /** What was left out of a folder, on purpose. */
  skipped: { links: number; hidden: number }
  message: string | null
  /** The conversation has been told how it ended. */
  reported: boolean
  startedAt: string
  finishedAt: string | null
  stage: string | null
}

const FILE = path.join(INDEX_BASE, 'transfers.json')
/** Finished jobs kept for the panel and the assistant. */
const KEEP = 100
const SAVE_EVERY_MS = 1000
/** status calls that may fail in a row before the job gives up on the mover. */
const STATUS_TRIES = 5
const CANCEL_WAIT_MS = 30_000
/** How long an approved call waits for its job, so a small copy is reported
 *  in the same reply. A longer one carries on in the status panel. */
const REPORT_MS = Number(process.env.STUDIO_TRANSFER_REPORT_MS) || 15_000

let jobs: Map<string, TransferJob> | null = null
const cancels = new Set<string>()
const finished = new Map<string, Promise<void>>()
/** Jobs whose reply is waiting to report them itself. */
const waited = new Set<string>()
let writing: Promise<void> = Promise.resolve()
let savedAt = 0

function load(): Map<string, TransferJob> {
  if (jobs) return jobs
  jobs = new Map()
  try {
    for (const j of JSON.parse(fs.readFileSync(FILE, 'utf8')) as TransferJob[]) if (j?.id) jobs.set(j.id, j)
  } catch { /* none yet */ }
  // Nothing in this process runs yet, so whatever says running does not.
  let stale = false
  for (const j of jobs.values()) {
    if (j.state !== 'running') continue
    const placing = j.phase === 'finishing'
    end(j, 'interrupted', placing
      ? `The Studio stopped while the checked copy was being put in ${shownText(j.destShown)}, so some of it may be there. Look before relying on it.`
      : `The Studio stopped before this finished. Nothing was put in ${shownText(j.destShown)}.`)
    if (j.stage) void removeStaging(j.stage)
    j.stage = null
    tell(j)
    stale = true
  }
  if (stale) save(true)
  return jobs
}

/** Finds what the last process left. Called once as the Studio starts. */
export function recoverTransfers(): void { load() }

/** Written whole to a temporary file and renamed into place: at once for a
 *  change of state, at most once a second for progress. */
function save(now = false): Promise<void> {
  if (!now && Date.now() - savedAt < SAVE_EVERY_MS) return writing
  savedAt = Date.now()
  const all = [...load().values()]
  const over = all.filter(j => j.state !== 'running').length - KEEP
  if (over > 0) for (const j of all.filter(j => j.state !== 'running').slice(0, over)) load().delete(j.id)
  const snapshot = JSON.stringify([...load().values()])
  const tmp = `${FILE}.${process.pid}.tmp`
  writing = writing.then(async () => {
    await fsp.writeFile(tmp, snapshot, { mode: 0o600 })
    await fsp.rename(tmp, FILE)
  }).catch(e => { console.warn(`transfers not saved: ${(e as Error).message}`) })
  return writing
}

function end(j: TransferJob, state: Exclude<JobState, 'running'>, message: string): void {
  j.state = state
  j.phase = null
  j.message = message
  j.finishedAt = new Date().toISOString()
}

/** Puts the result in the conversation that asked, for a job that ended
 *  after the reply that started it stopped waiting. */
function tell(j: TransferJob): void {
  if (j.reported) return
  j.reported = true
  appendConversationNote(j.conversationId, `Copy to ${shownText(j.destShown)}: ${j.state}. ${j.message ?? ''}`.trim())
}

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))

/** Polls a mover until the transfer ends, passing on progress and a request
 *  to cancel. A mover that stops answering loses the job. */
async function follow(j: TransferJob, mover: Mover): Promise<MoverStatus> {
  let misses = 0
  let asked = 0
  for (;;) {
    await sleep(mover.pollMs)
    if (cancels.has(j.id) && !asked) {
      asked = Date.now()
      const can = await mover.cancel(j.ref!).catch(() => false)
      if (!can) j.message = `${mover.label} cannot stop a transfer once it has started; the Studio stops following it when it ends.`
    }
    let st: MoverStatus
    try { st = await mover.status(j.ref!); misses = 0 } catch (e) {
      if (++misses < STATUS_TRIES) continue
      return { state: 'lost', message: String((e as Error).message ?? e).slice(0, 300) }
    }
    if (typeof st.bytes === 'number') j.bytes.done = st.bytes
    if (typeof st.files === 'number') j.files.done = st.files
    if (st.state !== 'running') return st
    if (asked && Date.now() - asked > CANCEL_WAIT_MS) return { state: 'lost', message: 'it did not stop when asked' }
    void save()
  }
}

async function run(j: TransferJob, route: SourceRoute, mover: Mover, items: string[], plan: TransferPlan): Promise<void> {
  try {
    const names = topNames(items)
    const listed = await listSource(route, items)
    if (cancels.has(j.id)) { end(j, 'cancelled', 'Cancelled before anything was copied.'); return }
    if (listed.unreadable.length) throw new KbError(403, `${listed.unreadable.length} ${listed.unreadable.length === 1 ? 'entry' : 'entries'} could not be read (${listed.unreadable.slice(0, 3).map(shownText).join(', ')}), so nothing was copied.`)
    if (!listed.files.length) throw new KbError(404, 'There are no files to copy there.')
    j.files.total = listed.files.length
    j.bytes.total = listed.bytes
    j.skipped = { links: listed.links, hidden: listed.hidden }
    const dest = await checkDestination(plan.dest)
    if (dest.folder !== plan.dest) throw new KbError(409, 'The destination changed after this was approved, so nothing was copied.')
    assertFits(listed.bytes, await freeBytes(dest.existing), dest.folder)
    await assertNoneExist(dest.folder, names)
    j.stage = await makeStaging(dest.folder, j.id)
    j.phase = 'copying'
    await save(true)
    j.ref = await mover.start({ job: j.id, source: route, items, files: listed.files, dest: stagedData(j.stage) })
    await save(true)
    const st = await follow(j, mover)
    if (st.state === 'cancelled' || cancels.has(j.id)) { end(j, 'cancelled', 'Cancelled. Nothing was put in the destination.'); return }
    if (st.state === 'lost') { end(j, 'interrupted', `${mover.label} lost track of this transfer${st.message ? `: ${st.message}` : ''}. Nothing was put in the destination.`); return }
    if (st.state === 'failed') throw new KbError(502, st.message || `${mover.label} reported a failure.`)
    // The mover says done. See for ourselves.
    j.phase = 'checking'
    await save(true)
    const seen = await checkFiles(stagedData(j.stage), listed.files)
    if (!seen.ok) throw new KbError(502, `${mover.label} reported success, but the copy does not match the source: ${shownText(checkProblem(seen))}. Nothing was put in the destination.`)
    j.phase = 'finishing'
    await save(true)
    await publish(j.stage, dest.folder, names)
    j.files.done = seen.files
    j.bytes.done = seen.bytes
    const notes = [
      listed.links ? `${listed.links} ${listed.links === 1 ? 'link or special file' : 'links or special files'} not copied` : '',
      listed.hidden ? `${listed.hidden} hidden ${listed.hidden === 1 ? 'entry' : 'entries'} not copied` : '',
      plan.files !== null && plan.files !== seen.files ? `the plan said ${plural(plan.files, 'file')}, and the source had ${seen.files.toLocaleString('en-US')}` : '',
    ].filter(Boolean)
    end(j, 'done', `Copied ${plural(seen.files, 'file')} (${size(seen.bytes)}) to ${shownText(shownFolder(dest.folder))}. The Studio checked the file count and sizes against the source.${notes.length ? ` Note: ${notes.join('; ')}.` : ''}`)
  } catch (e) {
    end(j, 'failed', String((e as Error).message ?? e).slice(0, 800))
  } finally {
    if (j.stage) await removeStaging(j.stage)
    j.stage = null
    cancels.delete(j.id)
    if (!waited.has(j.id)) tell(j)
    await save(true)
  }
}

/** The viewer id the library lists take: none where nobody signs in. */
const libraryViewer = (viewer: string) => (authEnabled() ? viewer : null)

export interface ApprovedTransfer {
  conversationId: string
  toolCallId: string
  viewer: string
  args: string
  plan?: TransferPlan
}

/**
 * Starts the job for a held call that its viewer approved. approvals.ts
 * calls this after taking the record out of the store, and nothing else
 * does: no route or tool starts a transfer. Everything that could have
 * changed since the plan is looked at again, and a call whose source or
 * mover is no longer the one the person saw is refused.
 */
export function startApproved(rec: ApprovedTransfer): TransferJob {
  const plan = rec.plan
  if (!plan) throw new KbError(400, 'This call has no plan; nothing ran.')
  const hold = `${rec.conversationId}\0${rec.toolCallId}`
  if ([...load().values()].some(j => j.hold === hold)) throw new KbError(409, 'This approval already ran; nothing more ran.')
  const args = transferArgs(rec.args)
  if (canonicalTransfer(args) !== rec.args || args.paths.join('\0') !== plan.items.join('\0')) throw new KbError(400, 'The plan does not match the call that was held; nothing ran.')
  const viewerId = libraryViewer(rec.viewer)
  const lib = getLibrary(plan.library, undefined, viewerId)
  const route = sourceRoutes(lib, viewerId).routes.find(r => routeId(r) === plan.route)
  if (!route) throw new KbError(409, `How ${lib.label} is reached changed after this was asked; nothing ran. Ask again to see the new plan.`)
  const mover = moverById(plan.mover)
  if (!mover) throw new KbError(409, `${plan.moverLabel} is no longer available; nothing ran.`)
  const can = mover.canReach(route)
  if (!can.ok) throw new KbError(409, `${can.why}; nothing ran.`)
  const j: TransferJob = {
    id: crypto.randomBytes(8).toString('hex'), viewer: rec.viewer, conversationId: rec.conversationId, hold,
    title: `${plan.items.length === 1 ? plan.items[0].slice(plan.items[0].lastIndexOf('/') + 1) : plural(plan.items.length, 'item')} to ${plan.destShown}`,
    library: plan.libraryLabel, source: plan.source, dest: plan.dest, destShown: plan.destShown, names: topNames(plan.items),
    mover: mover.id, ref: null, state: 'running', phase: 'listing',
    files: { done: 0, total: plan.files }, bytes: { done: 0, total: plan.bytes }, skipped: { links: 0, hidden: 0 },
    message: null, reported: false, startedAt: new Date().toISOString(), finishedAt: null, stage: null,
  }
  load().set(j.id, j)
  void save(true)
  const done = run(j, route, mover, plan.items, plan)
  finished.set(j.id, done)
  void done.finally(() => finished.delete(j.id))
  return j
}

/** Waits a moment for a job, so a small copy is reported in the same reply.
 *  One that ends later is reported in the conversation by itself. */
export async function settled(j: TransferJob, ms = REPORT_MS): Promise<TransferJob> {
  const p = finished.get(j.id)
  if (p) {
    waited.add(j.id)
    let timer: ReturnType<typeof setTimeout>
    await Promise.race([p, new Promise<void>(r => { timer = setTimeout(r, ms) })]).finally(() => clearTimeout(timer))
    waited.delete(j.id)
  }
  if (j.state !== 'running') { j.reported = true; void save(true) }
  return j
}

/** A job as the client and the assistant see it. */
export function publicJob(j: TransferJob) {
  const { viewer: _v, hold: _h, stage: _s, ref: _r, reported: _t, ...rest } = j
  return { ...rest, title: shownText(j.title), names: j.names.map(shownText) }
}

export function jobsOf(viewer: string): TransferJob[] {
  return [...load().values()].filter(j => j.viewer === viewer).sort((a, b) => (a.startedAt < b.startedAt ? 1 : -1))
}

export function jobOf(viewer: string, id: string): TransferJob | null {
  const j = load().get(id)
  return j && j.viewer === viewer ? j : null
}

/** Asks a running job to stop. */
export function cancelJob(j: TransferJob): boolean {
  if (j.state !== 'running') return false
  cancels.add(j.id)
  return true
}

/** The job's state, for the model. */
export function jobReport(j: TransferJob): string {
  if (j.state === 'running') return `The copy is running as job ${j.id}. It goes on without this chat: the user can follow or cancel it in the status panel, and its result will appear in this conversation.`
  return `Job ${j.id} ${j.state}. ${j.message ?? ''}`.trim()
}

/** A line for the system prompt saying where this conversation's copies
 *  stand, so the assistant can answer "is it done?". Counts only: a file
 *  name does not belong in the prompt. */
export function transfersNote(viewer: string, conversationId: string | undefined): string {
  if (!conversationId) return ''
  const hour = Date.now() - 3_600_000
  const line = (j: TransferJob) => `job ${j.id}: ${j.state}, ${j.files.done}${j.files.total === null ? '' : ` of ${j.files.total}`} files`
  const mine = jobsOf(viewer).filter(j => j.conversationId === conversationId && (j.state === 'running' || Date.parse(j.finishedAt ?? '') > hour)).slice(0, 5)
  return mine.length ? `\n\nCopies started in this conversation, as of now: ${mine.map(line).join('; ')}.` : ''
}

/** Tests start from an empty store, or reread the file as a restart does. */
export function resetTransfersForTests(o: { reread?: boolean } = {}): void {
  jobs = o.reread ? null : new Map()
  cancels.clear()
  finished.clear()
}

/** Waits for pending writes; tests use it before reading the file. */
export const transfersWritten = () => writing
