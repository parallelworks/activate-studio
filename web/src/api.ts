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
}

async function getJson<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) throw new Error(`${url}: ${res.status}`)
  return res.json() as Promise<T>
}

export const api = {
  tree: (path: string) => getJson<{ entries: KbEntry[] }>(`/api/kb/tree?path=${encodeURIComponent(path)}`),
  file: (path: string) => getJson<FileContent>(`/api/kb/file?path=${encodeURIComponent(path)}`),
  downloadUrl: (path: string) => `/api/kb/download?path=${encodeURIComponent(path)}`,
  stats: () => getJson<{ available: boolean; files?: number; dirs?: number; totalBytes?: number }>(`/api/kb/stats`),
  search: (q: string) => getJson<{ hits: SearchHit[]; error?: string }>(`/api/search?q=${encodeURIComponent(q)}`),
  health: () => getJson<{ ok: boolean; gufi: boolean }>(`/healthz`),
}
