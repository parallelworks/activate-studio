import { useSyncExternalStore } from 'react'
import { api, type Job } from './api'
import { uploadBatched, type UploadItem, type UploadProgress } from './upload'

/**
 * Every upload, move, copy and delete under way in the library, each with
 * its own row in the panel below the tree.
 *
 * Kept outside the view so a transfer outlives a trip to another page, and
 * so a second upload starts a row of its own instead of taking over the
 * first one's. Uploads take turns sending their bytes, which keeps a large
 * folder from starving the one queued behind it; one upload's indexing runs
 * while the next one sends.
 */
export interface Transfer extends UploadProgress {
  id: number
  /** Stops an upload; the files already sent stay. */
  cancel?: () => void
}

/** How long a transfer that went cleanly stays on screen. Anything that
 *  failed stays until it is dismissed. */
const STAYS_MS = 4000
/** The most paths the server takes in one request. */
const PATHS_PER_JOB = 500

let transfers: readonly Transfer[] = []
let lastId = 0
const listeners = new Set<() => void>()

function publish(next: readonly Transfer[]): void {
  transfers = next
  for (const fn of listeners) fn()
}

function update(id: number, patch: Partial<Transfer>): void {
  publish(transfers.map(t => (t.id === id ? { ...t, ...patch } : t)))
}

export function dismissTransfer(id: number): void {
  publish(transfers.filter(t => t.id !== id))
}

function finish(id: number, p: UploadProgress): void {
  update(id, { ...p, phase: 'done', finished: true, cancel: undefined })
  if (!p.failed.length && !p.indexError) setTimeout(() => dismissTransfer(id), STAYS_MS)
}

export function useTransfers(): readonly Transfer[] {
  return useSyncExternalStore(
    fn => { listeners.add(fn); return () => { listeners.delete(fn) } },
    () => transfers,
  )
}

/** The files have moved: the tree shows it now, not after indexing. */
const treeChanged = () => window.dispatchEvent(new CustomEvent('ade-kb-changed'))

let lastTurn: Promise<unknown> = Promise.resolve()

/** Wait for the uploads ahead, or for this one to be cancelled. */
function takeTurn(signal: AbortSignal): Promise<() => void> {
  let release!: () => void
  const mine = new Promise<void>(r => { release = r })
  const ahead = lastTurn
  lastTurn = Promise.all([ahead, mine])
  const cancelled = new Promise<void>(r => signal.addEventListener('abort', () => r(), { once: true }))
  return Promise.race([ahead, cancelled]).then(() => release)
}

export async function startUpload(items: UploadItem[], dir: string): Promise<UploadProgress> {
  const id = ++lastId
  const abort = new AbortController()
  publish([...transfers, {
    id, kind: 'upload', phase: 'queued', total: items.length, done: 0, bytes: 0,
    totalBytes: items.reduce((n, i) => n + (i.size ?? i.file.size), 0), current: '', failed: [], indexMs: 0,
    finished: false, dir, cancel: () => abort.abort(),
  }])
  const result = await uploadBatched(items, dir, p => update(id, p), {
    signal: abort.signal, turn: () => takeTurn(abort.signal), onLanded: treeChanged,
  })
  finish(id, result)
  treeChanged()
  return result
}

/** Run a move, copy or delete as server jobs, a request's worth of paths at
 *  a time, and report them as one transfer. */
export async function startPathJob(
  kind: 'move' | 'copy' | 'delete',
  paths: string[],
  dir: string,
  start: (paths: string[]) => Promise<Job>,
): Promise<UploadProgress> {
  const id = ++lastId
  const p: UploadProgress = {
    kind, phase: 'uploading', total: paths.length, done: 0, bytes: 0, totalBytes: 0,
    current: paths[0] ?? '', failed: [], indexMs: 0, finished: false, dir,
  }
  publish([...transfers, { ...p, id }])
  try {
    for (let i = 0; i < paths.length && !p.indexError; i += PATHS_PER_JOB) {
      const job = await start(paths.slice(i, i + PATHS_PER_JOB))
      let landed = false
      for (;;) {
        await new Promise(r => setTimeout(r, 500))
        const s = await api.job(job.id)
        p.done = i + s.done
        p.current = s.current
        p.phase = s.phase === 'indexing' ? 'indexing' : 'uploading'
        if (p.phase === 'indexing' && !landed) { landed = true; treeChanged() }
        if (s.state === 'done') {
          const r = s.result as { skipped?: { path: string; reason: string }[]; indexMs?: number } | null
          const skipped = r?.skipped ?? []
          p.failed.push(...skipped.map(x => ({ name: x.path, error: x.reason })))
          p.done = i + s.total - skipped.length
          p.indexMs += r?.indexMs ?? 0
          break
        }
        if (s.state === 'error') { p.indexError = s.error ?? `${kind} failed`; break }
        update(id, p)
      }
    }
  } catch (e) {
    p.indexError = String((e as Error).message ?? e)
  }
  finish(id, p)
  treeChanged()
  return p
}
