import { api } from './api'

export interface UploadItem { file: File; rel: string }
export interface UploadProgress {
  /** Uploading from the desktop, or moving inside the corpus. */
  kind?: 'upload' | 'move'
  /** 'uploading' while bytes move, 'indexing' while the corpus catches up. */
  phase: 'uploading' | 'indexing' | 'done'
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
}

/** Small batches keep the count moving; one request per file would spend
 *  more time in round trips than in bytes. */
const BATCH = 4

/** Walk dropped DataTransfer items, descending directories. */
export async function collectDropped(items: DataTransferItemList): Promise<UploadItem[]> {
  const out: UploadItem[] = []
  const walkEntry = async (entry: any, prefix: string): Promise<void> => {
    if (!entry) return
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
): Promise<UploadProgress> {
  const totalBytes = items.reduce((n, i) => n + i.file.size, 0)
  const p: UploadProgress = {
    phase: 'uploading', total: items.length, done: 0, bytes: 0, totalBytes,
    current: items[0]?.rel ?? '', failed: [], indexMs: 0, finished: false, dir,
  }
  onProgress({ ...p })

  let sentBefore = 0
  for (let i = 0; i < items.length; i += BATCH) {
    const batch = items.slice(i, i + BATCH)
    p.current = batch[0].rel
    try {
      const r = await api.uploadFiles(batch, dir, {
        deferIndex: true,
        onBytes: sent => { p.bytes = sentBefore + sent; onProgress({ ...p }) },
      })
      p.done += r.saved.length
    } catch (e) {
      for (const it of batch) p.failed.push({ name: it.rel, error: String((e as Error).message ?? e) })
    }
    sentBefore += batch.reduce((n, b) => n + b.file.size, 0)
    p.bytes = sentBefore
    onProgress({ ...p })
  }

  // One indexing pass for everything that landed, watched rather than
  // waited on inside a request.
  if (p.done > 0) {
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
