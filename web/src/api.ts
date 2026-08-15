export interface KbEntry {
  name: string
  path: string
  type: 'dir' | 'file'
  size: number
  mtime: number
  kind: 'text' | 'extracted' | 'image' | 'model' | 'binary' | 'dir'
  tags?: string[]
}

export interface FileContent {
  path: string
  kind: KbEntry['kind']
  size: number
  mtime: number
  truncated: boolean
  content: string | null
  source: 'raw' | 'extracted' | 'none'
}

export interface SearchHit {
  path: string
  name: string
  size: number
  mtime: number
  snippet: string
  mode: 'fts' | 'name' | 'vector'
  tags?: string[]
}

export interface IndexStatus {
  running: boolean
  lastSweepAt: string | null
  lastSweepMs: number | null
  lastChanges: string[]
  lastError: string | null
  sweepIntervalSec: number
}

async function getJson<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) throw new Error(`${url}: ${res.status}`)
  return res.json() as Promise<T>
}

async function postJson<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as any).error ?? `${url}: ${res.status}`)
  return data as T
}

export const api = {
  tree: (path: string) => getJson<{ entries: KbEntry[] }>(`/api/kb/tree?path=${encodeURIComponent(path)}`),
  file: (path: string) => getJson<FileContent>(`/api/kb/file?path=${encodeURIComponent(path)}`),
  downloadUrl: (path: string) => `/api/kb/download?path=${encodeURIComponent(path)}`,
  rawUrl: (path: string) => `/api/kb/raw?path=${encodeURIComponent(path)}`,
  pdfUrl: (path: string) => `/api/kb/pdf?path=${encodeURIComponent(path)}`,
  deleteFiles: (paths: string[]) =>
    postJson<{ deleted: string[]; skipped: { path: string; reason: string }[]; indexMs: number }>(`/api/kb/delete`, { paths }),
  deleteFile: async (path: string) => {
    const res = await fetch(`/api/kb/file?path=${encodeURIComponent(path)}`, { method: 'DELETE' })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as any).error ?? `delete: ${res.status}`)
    return data as { deleted: string; indexMs: number }
  },
  deleteDir: async (path: string) => {
    const res = await fetch(`/api/kb/dir?path=${encodeURIComponent(path)}`, { method: 'DELETE' })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as any).error ?? `delete: ${res.status}`)
    return data as { deleted: string }
  },
  stats: () => getJson<{ available: boolean; files?: number; dirs?: number; totalBytes?: number }>(`/api/kb/stats`),
  search: (q: string, tags?: string[], limit = 50) => getJson<{ hits: SearchHit[]; error?: string }>(`/api/search?q=${encodeURIComponent(q)}&limit=${limit}${tags?.length ? `&tags=${encodeURIComponent(tags.join(','))}` : ''}`),
  health: () => getJson<{ ok: boolean; gufi: boolean }>(`/healthz`),
  mkdir: (path: string) => postJson<{ created: string }>(`/api/kb/mkdir`, { path }),
  tagVocabulary: () => getJson<{ tags: { tag: string; count: number }[] }>(`/api/tags`),
  tagsFor: (path: string) => getJson<{ own: string[]; inherited: string[] }>(`/api/kb/tags?path=${encodeURIComponent(path)}`),
  applyTags: (paths: string[], add: string[], remove: string[]) =>
    postJson<{ updated: Record<string, string[]> }>(`/api/kb/tags`, { paths, add, remove }),
  indexStatus: () => getJson<IndexStatus>(`/api/index/status`),
  sweepNow: () => postJson<{ changed: string[] }>(`/api/index/sweep`, {}),
  uploadUrl: (url: string, dir: string) =>
    postJson<{ saved: string[]; indexMs: number }>(`/api/kb/upload-url`, { url, dir }),
  uploadFiles: async (
    files: (File | { file: File; rel: string })[],
    dir: string,
    opts: { deferIndex?: boolean; onBytes?: (sent: number) => void; signal?: AbortSignal } = {},
  ) => {
    const form = new FormData()
    for (const f of files) {
      if (f instanceof File) form.append('file', f, f.name)
      else form.append('file', f.file, f.rel)
    }
    const url = `/api/kb/upload?dir=${encodeURIComponent(dir)}${opts.deferIndex ? '&index=0' : ''}`
    // XHR rather than fetch: only XHR reports how much of the body has gone
    // out, which is what makes a large upload legible while it runs.
    return await new Promise<UploadResult>((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      xhr.open('POST', url)
      // Cancelling stops the transfer mid-batch; whatever already reached
      // the server stays, which the panel says out loud.
      if (opts.signal) {
        if (opts.signal.aborted) { xhr.abort(); return reject(new DOMException('cancelled', 'AbortError')) }
        opts.signal.addEventListener('abort', () => xhr.abort(), { once: true })
      }
      xhr.onabort = () => reject(new DOMException('cancelled', 'AbortError'))
      xhr.upload.onprogress = e => { if (e.lengthComputable) opts.onBytes?.(e.loaded) }
      xhr.onload = () => {
        let data: any = {}
        try { data = JSON.parse(xhr.responseText) } catch { /* non-JSON error body */ }
        if (xhr.status < 200 || xhr.status >= 300) return reject(new Error(data.error ?? `upload: ${xhr.status}`))
        resolve(data as UploadResult)
      }
      xhr.onerror = () => reject(new Error('upload: connection failed'))
      xhr.ontimeout = () => reject(new Error('upload: timed out'))
      xhr.send(form)
    })
  },

  dirs: () => getJson<{ dirs: string[] }>('/api/kb/dirs'),

  labelSuggestions: (dir: string, limit = 40) =>
    postJson<{
      proposals: { path: string; labels: string[]; why: string }[]
      newLabels: string[]
      considered: number
      model: string
    }>('/api/kb/label-suggestions', { dir, limit }),

  copyJob: (paths: string[], dest: string) => postJson<Job>('/api/kb/copy', { paths, dest, async: true }),

  rename: (path: string, name: string) =>
    postJson<{ renamed: { from: string; to: string } | null; indexMs: number }>('/api/kb/rename', { path, name }),

  /** Bulk move as a watchable job: the interface polls /api/jobs/<id>. */
  moveJob: (paths: string[], dest: string) =>
    postJson<Job>('/api/kb/move', { paths, dest, async: true }),
  job: (id: number) => getJson<Job>(`/api/jobs/${id}`),

  move: (paths: string[], dest: string) =>
    postJson<{ moved: { from: string; to: string }[]; skipped: { path: string; reason: string }[]; indexMs: number }>(
      '/api/kb/move', { paths, dest }),

  extractors: () => getJson<{ extractors: { name: string; available: boolean; covers: string }[] }>('/api/index/extractors'),

  startIndexJob: (dir: string) => postJson<IndexJob>('/api/index/job', { path: dir }),
  indexJob: (id: number) => getJson<IndexJob>(`/api/index/job/${id}`),
}

export interface UploadResult { saved: string[]; indexMs?: number; indexed?: boolean; indexError?: string; deferred?: boolean }
export interface Job {
  id: number
  kind: 'move' | 'index'
  phase: string
  state: 'running' | 'done' | 'error'
  done: number
  total: number
  current: string
  result: unknown
  error: string | null
  ms: number | null
}

export interface IndexJob {
  id: number
  dir: string
  state: 'queued' | 'running' | 'done' | 'error'
  ms: number | null
  error: string | null
}
