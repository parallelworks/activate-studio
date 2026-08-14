import { api } from './api'

export interface UploadItem { file: File; rel: string }
export interface UploadProgress {
  total: number
  done: number
  failed: { name: string; error: string }[]
  indexMs: number
  finished: boolean
  dir: string
}

const BATCH = 25

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
 * Bulk upload in batches with progress: a thousand papers arrive as ~40
 * sequential requests, each indexed on the server before it returns, so
 * progress reflects searchable files, not just transferred bytes.
 */
export async function uploadBatched(
  items: UploadItem[],
  dir: string,
  onProgress: (p: UploadProgress) => void,
): Promise<UploadProgress> {
  const p: UploadProgress = { total: items.length, done: 0, failed: [], indexMs: 0, finished: false, dir }
  onProgress({ ...p })
  for (let i = 0; i < items.length; i += BATCH) {
    const batch = items.slice(i, i + BATCH)
    try {
      const r = await api.uploadFiles(batch, dir)
      p.done += r.saved.length
      p.indexMs += r.indexMs
    } catch (e) {
      for (const it of batch) p.failed.push({ name: it.rel, error: String((e as Error).message ?? e) })
    }
    onProgress({ ...p })
  }
  p.finished = true
  onProgress({ ...p })
  return p
}
