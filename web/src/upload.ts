import { api } from './api'
import type { DroppedFile } from './nativeShell'

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
/** What the server takes in one file (MAX_UPLOAD_BYTES). Told apart here, as
 *  the server fails the whole request over one such file. */
const MAX_FILE = 200 * 1024 * 1024

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
  const sendable = items.filter(i => sizeOf(i) <= MAX_FILE)
  const totalBytes = sendable.reduce((n, i) => n + sizeOf(i), 0)
  const p: UploadProgress = {
    kind: 'upload', phase: turn ? 'queued' : 'uploading', total: items.length, done: 0, bytes: 0, totalBytes,
    current: sendable[0]?.rel ?? '', indexMs: 0, finished: false, dir,
    failed: items.filter(i => sizeOf(i) > MAX_FILE).map(i => ({ name: i.rel, error: 'over the 200 MB limit for one file' })),
  }
  onProgress({ ...p })
  const endTurn = await turn?.()
  p.phase = 'uploading'
  onProgress({ ...p })

  const size = batchSize(sendable)
  const batches: UploadItem[][] = []
  for (let i = 0; i < sendable.length; i += size) batches.push(sendable.slice(i, i + size))

  // Bytes are counted per batch and summed, since several are in the air at
  // once and each reports its own progress.
  const settled = new Map<number, number>()
  const inFlight = new Map<number, number>()
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
      try {
        const r = batch[0].linked
          ? await api.sendListed(batch, dir, signal)
          : await api.uploadFiles(batch, dir, {
            deferIndex: true,
            signal,
            onBytes: sent => { inFlight.set(idx, sent); publish() },
          })
        p.done += r.saved.length
      } catch (e) {
        if ((e as Error).name === 'AbortError') { p.cancelled = true; return }
        for (const it of batch) p.failed.push({ name: it.rel, error: String((e as Error).message ?? e) })
      } finally {
        inFlight.delete(idx)
        settled.set(idx, bytes)
        publish()
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(CONCURRENCY, batches.length) }, worker))
  endTurn?.()
  if (signal?.aborted) p.cancelled = true

  // One indexing pass for everything that landed, watched rather than
  // waited on inside a request.
  if (p.done > 0) {
    onLanded?.()
    p.phase = 'indexing'
    p.current = ''
    onProgress({ ...p })
    try {
      const job = await api.startIndexJob(dir)
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
