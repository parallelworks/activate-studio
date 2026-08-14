export interface KbEntry {
  name: string
  path: string
  type: 'dir' | 'file'
  size: number
  mtime: number
  kind: 'text' | 'extracted' | 'image' | 'binary' | 'dir'
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
  deleteFile: async (path: string) => {
    const res = await fetch(`/api/kb/file?path=${encodeURIComponent(path)}`, { method: 'DELETE' })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as any).error ?? `delete: ${res.status}`)
    return data as { deleted: string; indexMs: number }
  },
  stats: () => getJson<{ available: boolean; files?: number; dirs?: number; totalBytes?: number }>(`/api/kb/stats`),
  search: (q: string) => getJson<{ hits: SearchHit[]; error?: string }>(`/api/search?q=${encodeURIComponent(q)}`),
  health: () => getJson<{ ok: boolean; gufi: boolean }>(`/healthz`),
  tagVocabulary: () => getJson<{ tags: { tag: string; count: number }[] }>(`/api/tags`),
  tagsFor: (path: string) => getJson<{ own: string[]; inherited: string[] }>(`/api/kb/tags?path=${encodeURIComponent(path)}`),
  applyTags: (paths: string[], add: string[], remove: string[]) =>
    postJson<{ updated: Record<string, string[]> }>(`/api/kb/tags`, { paths, add, remove }),
  indexStatus: () => getJson<IndexStatus>(`/api/index/status`),
  sweepNow: () => postJson<{ changed: string[] }>(`/api/index/sweep`, {}),
  uploadUrl: (url: string, dir: string) =>
    postJson<{ saved: string[]; indexMs: number }>(`/api/kb/upload-url`, { url, dir }),
  uploadFiles: async (files: (File | { file: File; rel: string })[], dir: string) => {
    const form = new FormData()
    for (const f of files) {
      if (f instanceof File) form.append('file', f, f.name)
      else form.append('file', f.file, f.rel)
    }
    const res = await fetch(`/api/kb/upload?dir=${encodeURIComponent(dir)}`, { method: 'POST', body: form })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as any).error ?? `upload: ${res.status}`)
    return data as { saved: string[]; indexMs: number }
  },
}
