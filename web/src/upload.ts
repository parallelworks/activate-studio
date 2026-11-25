import { api, type UploadTarget } from './api'
import { shellSendsParts, type DroppedFile } from './nativeShell'

export interface UploadItem {
  file: File
  rel: string
  /** A file the desktop window listed, which the window itself sends: its id
   *  there. `file` is then a stand-in, and `size` its size. */
  linked?: string
  size?: number
}

const sizeOf = (i: UploadItem) => i.size ?? i.file.size
export interface UploadProgress {
  /** Uploading from the desktop, or work inside the corpus. */
  kind?: 'upload' | 'move' | 'copy' | 'delete'
  /** 'queued' behind another upload, 'uploading' while bytes move (or a job
   *  works through its paths), 'indexing' while the corpus catches up. */
  phase: 'queued' | 'uploading' | 'indexing' | 'done'
  total: number
  done: number
  /** Bytes sent and total bytes, so one large file still shows movement. */
  bytes: number
  totalBytes: number
  current: string
  failed: { name: string; error: string }[]
  indexMs: number
  finished: boolean
  dir: string
  /** Files landed, but the indexing pass after them did not. */
  indexError?: string
  /** The library updates its own index after the upload (a person's library
   *  on a system), so no pass was asked for here. */
  reindexing?: boolean
  /** Stopped part way; the files already sent are still there. */
  cancelled?: boolean
}

/**
 * Batch size and how many requests are in flight at once.
 *
 * Through the platform tunnel the round trip dominates: the cluster writes
 * 5 MB in under a tenth of a second, so a sequential batch-at-a-time upload
 * spends its life waiting. Several requests in flight hide that latency,
 * and small files ride in larger batches because each one is nearly all
 * overhead.
 */
const CONCURRENCY = 3
const SMALL_FILE = 1024 * 1024
/** A file over this goes up in parts, each a request of its own. The parts
 *  are the size the server says (UploadPart, the same). */
export const PART_FILE = 16 * 1024 * 1024
/** What the server takes of one file in a request that carries it whole. */
const WHOLE_FILE = 200 * 1024 * 1024
/** Waits before a request that failed is sent again. */
export const RETRY_MS = [1000, 2000, 5000, 10_000, 20_000]

function batchSize(items: UploadItem[]): number {
  const avg = items.reduce((n, i) => n + sizeOf(i), 0) / Math.max(items.length, 1)
  if (avg < SMALL_FILE) return 12
  // No byte counts come back for what the window sends, so progress moves a
  // request at a time.
  return items[0]?.linked ? 1 : 4
}

/** The server takes no names that start with a dot, and one in a batch fails
 *  the batch, so a folder's .DS_Store never goes up. */
const uploadable = (rel: string) => !rel.split('/').some(part => part.startsWith('.'))

/** Files chosen in a file or folder picker, folders kept. */
export function collectChosen(files: File[]): UploadItem[] {
  return files.map(file => ({ file, rel: file.webkitRelativePath || file.name })).filter(i => uploadable(i.rel))
}

/** Files the desktop window listed, sent by the window when their turn
 *  comes, so their bytes never pass through the page. */
export function collectListed(files: DroppedFile[]): UploadItem[] {
  return files.map(f => ({ file: new File([], f.rel.split('/').pop() ?? 'file'), rel: f.rel, linked: f.id, size: f.size }))
}

/** Walk a drop, descending directories. */
export async function collectDropped(items: DataTransferItemList): Promise<UploadItem[]> {
  const out: UploadItem[] = []
  const walkEntry = async (entry: any, prefix: string): Promise<void> => {
    if (!entry || !uploadable(entry.name)) return
    if (entry.isFile) {
      const file: File = await new Promise((res, rej) => entry.file(res, rej))
      out.push({ file, rel: prefix + file.name })
    } else if (entry.isDirectory) {
      const reader = entry.createReader()
      for (;;) {
        const batch: any[] = await new Promise(res => reader.readEntries(res, () => res([])))
        if (!batch.length) break
        for (const child of batch) await walkEntry(child, prefix + entry.name + '/')
      }
    }
  }
  const entries = [...items].map(i => (i as any).webkitGetAsEntry?.()).filter(Boolean)
  for (const e of entries) await walkEntry(e, '')
  return out
}

/** Not worth sending again: cancelled, refused, or out of space. */
function final(e: unknown): boolean {
  const status = (e as { status?: number }).status
  if ((e as Error).name === 'AbortError' || status === 507) return true
  return typeof status === 'number' && status >= 400 && status < 500 && status !== 408 && status !== 429
}

async function again<T>(call: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  for (let tries = 0; ; tries++) {
    try {
      return await call()
    } catch (e) {
      if (tries >= RETRY_MS.length || final(e)) throw e
      await new Promise<void>((resolve, reject) => {
        const cancel = () => { clearTimeout(wait); reject(new DOMException('cancelled', 'AbortError')) }
        const wait = setTimeout(() => { signal?.removeEventListener('abort', cancel); resolve() }, RETRY_MS[tries])
        if (signal?.aborted) cancel()
        else signal?.addEventListener('abort', cancel, { once: true })
      })
    }
  }
}

/** One large file, a part at a time. One that fails or is cancelled is taken
 *  back, so nothing of it stays in the library. */
async function sendInParts(
  item: UploadItem, to: UploadTarget, signal: AbortSignal | undefined,
  onPart: (part: number, sent: number, landed: boolean) => void,
) {
  const size = sizeOf(item)
  const u = await again(() => api.startParts(item.rel, size, to, signal), signal)
  // The page goes: so does what it sent.
  const gone = () => { void api.dropParts(u.id) }
  window.addEventListener('pagehide', gone)
  // The first part to fail stops the others.
  const stop = new AbortController()
  const cancel = () => stop.abort()
  signal?.addEventListener('abort', cancel, { once: true })
  if (signal?.aborted) stop.abort()
  try {
    let next = 0
    let failure: unknown
    const worker = async (): Promise<void> => {
      for (;;) {
        const n = next++
        if (n >= u.parts || stop.signal.aborted) return
        const from = n * u.part
        const length = Math.min(u.part, size - from)
        try {
          await again(() => item.linked
            ? api.sendListedPart(u.id, n, item.linked, from, length, stop.signal)
            : api.sendPart(u.id, n, item.file.slice(from, from + length), {
              signal: stop.signal, onBytes: sent => onPart(n, Math.min(sent, length), false),
            }), stop.signal)
          onPart(n, length, true)
        } catch (e) {
          failure ??= e
          stop.abort()
          return
        }
      }
    }
    await Promise.all(Array.from({ length: Math.min(CONCURRENCY, u.parts) }, worker))
    if (signal?.aborted) throw new DOMException('cancelled', 'AbortError')
    if (failure) throw failure
    return await again(() => api.finishParts(u.id, to, signal), signal)
  } catch (e) {
    void api.dropParts(u.id)
    throw e
  } finally {
    window.removeEventListener('pagehide', gone)
    signal?.removeEventListener('abort', cancel)
  }
}

/**
 * Bulk upload with progress the whole way through.
 *
 * Indexing used to run inside every upload request, so the bar sat still
 * for as long as the corpus took to catch up and a large drop read as hung.
 * Files now go up in small batches with byte-level progress, and the single
 * indexing pass afterwards is its own phase, watched as a background job.
 */
export async function uploadBatched(
  items: UploadItem[],
  dir: string,
  onProgress: (p: UploadProgress) => void,
  { signal, turn, onLanded }: {
    signal?: AbortSignal
    /** Resolves when this upload may send, with what ends its turn. */
    turn?: () => Promise<() => void>
    /** The files are on disk; indexing has yet to run. */
    onLanded?: () => void
  } = {},
): Promise<UploadProgress> {
  const inParts = (i: UploadItem) => sizeOf(i) > PART_FILE
  // A window from before it could send a part sends the file whole, and the
  // server fails a whole request over one file it does not take.
  const listed = (i: UploadItem) => Boolean(i.linked) && !shellSendsParts()
  const over = items.filter(i => listed(i) && sizeOf(i) > WHOLE_FILE)
  const whole = items.filter(i => (!inParts(i) || listed(i)) && !over.includes(i))
  const large = items.filter(i => inParts(i) && !listed(i))
  const to = api.uploadTarget(dir)
  const totalBytes = [...whole, ...large].reduce((n, i) => n + sizeOf(i), 0)
  const p: UploadProgress = {
    kind: 'upload', phase: turn ? 'queued' : 'uploading', total: items.length, done: 0, bytes: 0, totalBytes,
    current: (whole[0] ?? large[0])?.rel ?? '', indexMs: 0, finished: false, dir,
    failed: over.map(i => ({ name: i.rel, error: 'over 200 MB, which takes a newer version of this app' })),
  }
  onProgress({ ...p })
  const endTurn = await turn?.()
  p.phase = 'uploading'
  onProgress({ ...p })

  const size = batchSize(whole)
  const batches: UploadItem[][] = []
  for (let i = 0; i < whole.length; i += size) batches.push(whole.slice(i, i + size))

  // Bytes are counted per batch and summed, since several are in the air at
  // once and each reports its own progress.
  const settled = new Map<string, number>()
  const inFlight = new Map<string, number>()
  const publish = () => {
    let n = 0
    for (const v of settled.values()) n += v
    for (const v of inFlight.values()) n += v
    p.bytes = n
    onProgress({ ...p })
  }

  let next = 0
  const worker = async (): Promise<void> => {
    for (;;) {
      const idx = next++
      if (idx >= batches.length || signal?.aborted) return
      const batch = batches[idx]
      p.current = batch[0].rel
      const bytes = batch.reduce((n, b) => n + sizeOf(b), 0)
      const key = `${idx}`
      try {
        const r = batch[0].linked
          ? await api.sendListed(batch, dir, signal, to)
          : await api.uploadFiles(batch, dir, {
            deferIndex: true,
            signal,
            to,
            onBytes: sent => { inFlight.set(key, sent); publish() },
          })
        p.done += r.saved.length
        if (r.reindexing) p.reindexing = true
      } catch (e) {
        if ((e as Error).name === 'AbortError') { p.cancelled = true; return }
        for (const it of batch) p.failed.push({ name: it.rel, error: String((e as Error).message ?? e) })
      } finally {
        inFlight.delete(key)
        settled.set(key, bytes)
        publish()
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(CONCURRENCY, batches.length) }, worker))
  // Then the large files, one at a time, the parts of one side by side.
  for (const [i, item] of large.entries()) {
    if (signal?.aborted) break
    p.current = item.rel
    try {
      const r = await sendInParts(item, to, signal, (part, sent, landed) => {
        const key = `${i}/${part}`
        if (landed) { inFlight.delete(key); settled.set(key, sent) } else inFlight.set(key, sent)
        publish()
      })
      p.done += 1
      if (r.reindexing) p.reindexing = true
    } catch (e) {
      if ((e as Error).name === 'AbortError') { p.cancelled = true; break }
      p.failed.push({ name: item.rel, error: String((e as Error).message ?? e) })
    }
    for (const key of [...settled.keys(), ...inFlight.keys()]) {
      if (key.startsWith(`${i}/`)) { settled.delete(key); inFlight.delete(key) }
    }
    settled.set(`${i}/`, sizeOf(item))
    publish()
  }
  endTurn?.()
  if (signal?.aborted) p.cancelled = true

  // One indexing pass for everything that landed, watched rather than
  // waited on inside a request. A library that keeps its own index updates
  // it after the upload, so the main library's pass is not asked for.
  if (p.done > 0 && p.reindexing) onLanded?.()
  else if (p.done > 0) {
    onLanded?.()
    p.phase = 'indexing'
    p.current = ''
    onProgress({ ...p })
    try {
      const job = await api.startIndexJob(dir, to)
      const t0 = Date.now()
      for (;;) {
        await new Promise(r => setTimeout(r, 1000))
        const s = await api.indexJob(job.id)
        if (s.state === 'done') { p.indexMs = s.ms ?? Date.now() - t0; break }
        if (s.state === 'error') { p.indexError = s.error ?? 'indexing failed'; break }
        if (s.state === 'cancelled') { p.indexError = 'indexing was stopped; the next sync picks these files up'; break }
        onProgress({ ...p })
      }
    } catch (e) {
      p.indexError = String((e as Error).message ?? e)
    }
  }

  p.phase = 'done'
  p.finished = true
  onProgress({ ...p })
  return p
}
