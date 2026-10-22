import type { GufiCommand } from './components/GufiCommands'

/** A mounted library, as the server describes it. Paths stay on the server. */
export interface PublicLibrary {
  id: string
  label: string
  primary: boolean
  writable: boolean
  pinned: boolean
  source: boolean
  caps: { index: boolean; fullText: boolean; vectors: boolean }
  /** The cluster a user's own library lives on. */
  site?: string | null
}
/** Settings' listing adds where each library points. */
export interface SettingsLibrary extends PublicLibrary {
  indexRoot: string
  sourceRoot: string | null
  /** A library the Studio builds of a folder on this computer. */
  build?: {
    indexDir: string; managed: boolean; state: 'building' | 'ready' | 'updating' | 'failed'
    error: string | null; builtAt: string | null; files: number | null; dirs: number | null; progress: BuildProgress | null
  }
}
/** How far a build has got: a phase, and folders indexed so far. */
export interface BuildProgress { phase: string; done: number; total: number; current: string | null }
/** A user's own library on one of their clusters, or on another machine over ssh. */
export interface UserLibrary {
  id: string; label: string; site: string; sourceDir: string; sourceRoot: string | null; indexRoot: string | null
  transport?: 'pw' | 'ssh'
  /** An index that was there already, connected rather than built. */
  existing?: boolean
  destination?: string | null
  port?: number | null
  state: 'building' | 'ready' | 'updating' | 'failed'
  run: { slug: string; mode: 'build' | 'update'; startedAt: string } | null
  error: string | null; builtAt: string | null; directories: number | null; files: number | null
  scheduler: { partition?: string; account?: string } | null
  /** Whether the index carries the Studio's text and vector tables. */
  fullText?: boolean
  vectors?: boolean
  /** Built by the Studio on the remote system over the session; where the
   *  index is written, and how far a build has got. */
  built?: boolean
  indexDir?: string | null
  progress?: BuildProgress | null
}

/** What the Library view's side panel shows for a folder or file, from its index. */
export interface EntryDetails {
  path: string
  name: string
  type: 'dir' | 'file'
  library: { id: string; label: string; kind: string; site: string | null; writable: boolean }
  index: string | null
  fullText: boolean
  size: number
  files: number
  dirs: number
  mtime: number
  types: { ext: string; count: number; bytes: number }[]
  labels: { own: string[]; inherited: string[]; within: { label: string; count: number }[] }
  ms: number
}

// ---- which library requests address ----
// The primary is 'kb' and is what every request meant before libraries
// existed, so it is sent as nothing at all. Views read the current library
// through the helpers below and never build the query string themselves.
//
// The choice is kept per tab (session storage), so a reload keeps it and a
// new tab starts from the default: All libraries where there is more than
// one, else the primary. The library count is remembered from the last
// configuration so the first requests already use the right default.
const LIB_KEY = 'ade-library'
const COUNT_KEY = 'ade-library-count'
const PRIMARY = 'kb'
const ALL = 'all'
const stored = (): string | null => { try { return sessionStorage.getItem(LIB_KEY) } catch { return null } }
const defaultLibrary = (count: number): string => (count > 1 ? ALL : PRIMARY)
let current: string = stored() ?? (() => { try { return defaultLibrary(Number(localStorage.getItem(COUNT_KEY)) || 0) } catch { return PRIMARY } })()
const listeners = new Set<(id: string) => void>()
export function currentLibrary(): string { return current }
export function setLibrary(id: string): void {
  current = id || PRIMARY
  try { sessionStorage.setItem(LIB_KEY, current) } catch { /* storage unavailable */ }
  for (const fn of listeners) fn(current)
}
/** Called with the configured libraries: falls back to the default when
 *  the tab has no choice of its own or its choice no longer exists. */
export function settleLibraries(ids: string[]): void {
  try { localStorage.setItem(COUNT_KEY, String(ids.length)); localStorage.removeItem(LIB_KEY) } catch { /* storage unavailable */ }
  const mine = stored()
  const want = mine && (mine === ALL ? ids.length > 1 : ids.includes(mine)) ? mine : defaultLibrary(ids.length)
  if (want === current) return
  current = want
  for (const fn of listeners) fn(current)
}
export function onLibraryChange(fn: (id: string) => void): () => void {
  listeners.add(fn)
  return () => { listeners.delete(fn) }
}

/** In All mode a path reads `<library id>/<path>`: its library and the path
 *  inside it. Null for the top level, which is no library's. */
export function splitLibraryPath(p: string): { id: string; rel: string } | null {
  const parts = String(p ?? '').replace(/^\/+/, '').split('/')
  return parts[0] && parts[0] !== ALL ? { id: parts[0], rel: parts.slice(1).join('/') } : null
}
// Routes that take All mode's prefixed paths themselves. Every other route
// addresses one library, so in All mode a request on a path goes to that
// path's library, which is what keeps files viewable and the writable
// library writable while All is selected.
const ALL_AWARE = new Set(['/api/kb/tree', '/api/kb/file', '/api/kb/download', '/api/search', '/api/kb/stats'])
// A path that names a library's top folder addresses nothing to change or
// read inside it; a directory may (an upload or a paste into the library).
const target = (key: string, p: string) => {
  const t = splitLibraryPath(p)
  return t && (t.rel || key === 'dir' || key === 'dest') ? t : null
}
/** The library a request on `path` goes to in All mode, to put back on the
 *  paths the request returns. */
function prefixFor(p: string): (x: string) => string {
  const t = current === ALL ? splitLibraryPath(p) : null
  return t ? (x: string) => (x ? `${t.id}/${x}` : t.id) : (x: string) => x
}

/** Append the current library to a URL that already has a query string, or none. */
export function withLibrary(url: string): string {
  if (current === PRIMARY) return url
  if (current === ALL) {
    const [route, query = ''] = url.split('?')
    if (!ALL_AWARE.has(route)) {
      const pairs = query.split('&').filter(Boolean).map(kv => { const i = kv.indexOf('='); return [kv.slice(0, i < 0 ? kv.length : i), i < 0 ? '' : kv.slice(i + 1)] })
      const at = pairs.findIndex(([k]) => k === 'path' || k === 'dir')
      const t = at >= 0 ? target(pairs[at][0], decodeURIComponent(pairs[at][1])) : null
      if (t) {
        pairs[at] = [pairs[at][0], encodeURIComponent(t.rel)]
        if (t.id !== PRIMARY) pairs.push(['library', encodeURIComponent(t.id)])
        return `${route}?${pairs.map(([k, v]) => `${k}=${v}`).join('&')}`
      }
    }
  }
  return url + (url.includes('?') ? '&' : '?') + 'library=' + encodeURIComponent(current)
}
function withLib<T extends object>(body: T): T & { library?: string } {
  if (current === PRIMARY) return body
  if (current === ALL) {
    const b = body as Record<string, unknown>
    const single = ['path', 'dir', 'dest'].filter(k => typeof b[k] === 'string')
    const many = Array.isArray(b.paths) ? (b.paths as unknown[]).map(String) : []
    // An action on no path (a sync, say) acts on the primary.
    if (!single.length && !many.length) return body
    const ts = [...single.map(k => target(k, String(b[k]))), ...many.map(p => target('path', p))]
    const id = ts[0]?.id
    if (id && ts.every(t => t && t.id === id)) {
      const out: Record<string, unknown> = { ...b }
      for (const k of single) out[k] = splitLibraryPath(String(b[k]))!.rel
      if (many.length) out.paths = many.map(p => splitLibraryPath(p)!.rel)
      return (id === PRIMARY ? out : { ...out, library: id }) as T & { library?: string }
    }
  }
  return { ...body, library: current }
}

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
  format?: 'markdown' | 'text'
}

export interface SearchHit {
  path: string
  name: string
  size: number
  mtime: number
  snippet: string
  mode: 'fts' | 'name' | 'vector'
  tags?: string[]
  /** The library the hit is in, for a search over several. */
  library?: { id: string; label: string }
}

/** One library's line in statistics over all libraries. */
export interface LibraryStats { id: string; label: string; site: string | null; writable: boolean; available: boolean; files?: number; dirs?: number; totalBytes?: number; error?: string }

export interface IndexStatus {
  running: boolean
  lastSweepAt: string | null
  lastSweepMs: number | null
  lastChanges: string[]
  lastError: string | null
  sweepIntervalSec: number
}

async function getJson<T>(url: string): Promise<T> {
  const res = await fetch(withLibrary(url))
  if (!res.ok) throw new Error(`${url}: ${res.status}`)
  return res.json() as Promise<T>
}

async function postJson<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body && typeof body === 'object' ? withLib(body as object) : body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as any).error ?? `${url}: ${res.status}`)
  return data as T
}

export const api = {
  tree: (path: string) => getJson<{ entries: KbEntry[] }>(`/api/kb/tree?path=${encodeURIComponent(path)}`),
  file: (path: string) => getJson<FileContent>(`/api/kb/file?path=${encodeURIComponent(path)}`),
  downloadUrl: (path: string) => withLibrary(`/api/kb/download?path=${encodeURIComponent(path)}`),
  rawUrl: (path: string) => withLibrary(`/api/kb/raw?path=${encodeURIComponent(path)}`),
  pdfUrl: (path: string) => withLibrary(`/api/kb/pdf?path=${encodeURIComponent(path)}`),
  deleteFiles: async (paths: string[]) => {
    const back = prefixFor(paths[0] ?? '')
    const r = await postJson<{ deleted: string[]; skipped: { path: string; reason: string }[]; indexMs: number }>(`/api/kb/delete`, { paths })
    return { ...r, deleted: r.deleted.map(back), skipped: r.skipped.map(x => ({ ...x, path: back(x.path) })) }
  },
  deleteFile: async (path: string) => {
    const res = await fetch(withLibrary(`/api/kb/file?path=${encodeURIComponent(path)}`), { method: 'DELETE' })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as any).error ?? `delete: ${res.status}`)
    return data as { deleted: string; indexMs: number }
  },
  deleteDir: async (path: string) => {
    const res = await fetch(withLibrary(`/api/kb/dir?path=${encodeURIComponent(path)}`), { method: 'DELETE' })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as any).error ?? `delete: ${res.status}`)
    return data as { deleted: string }
  },
  stats: () => getJson<{ available: boolean; files?: number; dirs?: number; totalBytes?: number; libraries?: LibraryStats[] }>(`/api/kb/stats`),
  search: (q: string, tags?: string[], limit = 50) => getJson<{ hits: SearchHit[]; error?: string; gufi?: GufiCommand[] }>(`/api/search?q=${encodeURIComponent(q)}&limit=${limit}${tags?.length ? `&tags=${encodeURIComponent(tags.join(','))}` : ''}`),
  health: () => getJson<{ ok: boolean; gufi: boolean }>(`/healthz`),
  mkdir: async (path: string) => {
    const r = await postJson<{ created: string }>(`/api/kb/mkdir`, { path })
    return { ...r, created: prefixFor(path)(r.created) }
  },
  tagVocabulary: () => getJson<{ tags: { tag: string; count: number }[] }>(`/api/tags`),
  tagsFor: (path: string) => getJson<{ own: string[]; inherited: string[] }>(`/api/kb/tags?path=${encodeURIComponent(path)}`),
  applyTags: (paths: string[], add: string[], remove: string[]) =>
    postJson<{ updated: Record<string, string[]> }>(`/api/kb/tags`, { paths, add, remove }),
  indexStatus: () => getJson<IndexStatus>(`/api/index/status`),
  sweepNow: () => postJson<{ changed: string[] }>(`/api/index/sweep`, {}),
  uploadUrl: async (url: string, dir: string) => {
    const r = await postJson<{ saved: string[]; indexMs: number }>(`/api/kb/upload-url`, { url, dir })
    return { ...r, saved: r.saved.map(prefixFor(dir)) }
  },
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
    const url = withLibrary(`/api/kb/upload?dir=${encodeURIComponent(dir)}${opts.deferIndex ? '&index=0' : ''}`)
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
        resolve({ ...data, saved: (Array.isArray(data.saved) ? data.saved : []).map(prefixFor(dir)) } as UploadResult)
      }
      xhr.onerror = () => reject(new Error('upload: connection failed'))
      xhr.ontimeout = () => reject(new Error('upload: timed out'))
      xhr.send(form)
    })
  },

  dirs: () => getJson<{ dirs: string[] }>('/api/kb/dirs'),

  labelSuggestions: async (dir: string, limit = 40) => {
    const r = await postJson<{
      proposals: { path: string; labels: string[]; why: string }[]
      newLabels: string[]
      considered: number
      model: string
    }>('/api/kb/label-suggestions', { dir, limit })
    const back = prefixFor(dir)
    return { ...r, proposals: r.proposals.map(x => ({ ...x, path: back(x.path) })) }
  },

  copyJob: (paths: string[], dest: string) => postJson<Job>('/api/kb/copy', { paths, dest, async: true }),

  rename: async (path: string, name: string) => {
    const r = await postJson<{ renamed: { from: string; to: string } | null; indexMs: number }>('/api/kb/rename', { path, name })
    const back = prefixFor(path)
    return { ...r, renamed: r.renamed && { from: back(r.renamed.from), to: back(r.renamed.to) } }
  },

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
  /** `canBuild`: libraries can be built from folders on this computer. */
  libraries: () => getJson<{ libraries: SettingsLibrary[]; canBuild?: boolean }>('/api/libraries'),
  /** `ssh`: libraries over ssh are offered; `pw`: over pw ssh; `workflow`:
   *  building on a cluster through the platform's index workflow. */
  userLibraries: () => getJson<{ libraries: UserLibrary[]; available: boolean; ssh?: boolean; pw?: boolean; workflow?: boolean }>('/api/user-libraries'),
  rebuildLibrary: (id: string) => postJson<{ library: SettingsLibrary }>(`/api/libraries/${encodeURIComponent(id)}/rebuild`, {}),
  /** A folder or file of the current library, described from its index. */
  details: async (path: string): Promise<EntryDetails> => {
    const res = await fetch(withLibrary(`/api/kb/details?path=${encodeURIComponent(path)}`))
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as { error?: string }).error ?? `details: ${res.status}`)
    return data as EntryDetails
  },
  addUserLibrary: (body: { site: string; sourceDir: string; label?: string; scheduler?: { partition: string; account?: string } | null }) =>
    postJson<UserLibrary>('/api/user-libraries', body),
  refreshUserLibrary: (id: string) => postJson<UserLibrary>(`/api/user-libraries/${encodeURIComponent(id)}/refresh`, {}),
  removeUserLibrary: async (id: string) => {
    const r = await fetch(`/api/user-libraries/${encodeURIComponent(id)}`, { method: 'DELETE' })
    if (!r.ok) throw new Error(((await r.json().catch(() => ({}))) as { error?: string }).error ?? `${r.status}`)
  },
  agentSites: () => getJson<{ sites: { name: string; kind: 'local' | 'cluster'; type?: string }[] }>('/api/agent-sites'),
  addLibrary: (def: { id: string; label?: string; indexRoot: string; sourceRoot?: string }) =>
    postJson<{ library: SettingsLibrary }>('/api/libraries', def),
  removeLibrary: async (id: string) => {
    const res = await fetch(`/api/libraries/${encodeURIComponent(id)}`, { method: 'DELETE' })
    if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error ?? `remove: ${res.status}`)
  },
}

// ---- updates ----
// Only the macOS app installs updates; a clone is told the version and the
// command, and a hosted deployment has none (its routes answer 403).
export interface UpdatePolicy { check: boolean; install: 'manual' | 'auto'; channel: 'stable' | 'prerelease' }
/** One pull request's change: its title, and its description as markdown. */
export interface ChangeEntry { title: string; pr: number | null; body: string }
/** A release an update brings, with its notes as entries. */
export interface UpdateRelease { version: string; publishedAt: string | null; url: string | null; entries: ChangeEntry[] }
export type UpdateState = 'idle' | 'checking' | 'downloading' | 'verifying' | 'installing' | 'restarting' | 'error'
export interface UpdatesStatus {
  mode: 'app' | 'clone' | 'hosted'
  current: { version: string; builtAt: string | null }
  policy: UpdatePolicy
  latest: { version: string; publishedAt: string | null; notes: string | null; url: string | null } | null
  /** Every release newer than the running one, up to and including
   *  `latest`, newest first. */
  releases?: UpdateRelease[]
  available: boolean
  canInstall: boolean
  /** Why it cannot install, in plain words. */
  reason: string | null
  /** The commands that update a clone of the repository. */
  command?: string | null
  state: UpdateState
  progress: { bytes: number; total: number | null } | null
  error: string | null
  checkedAt: string | null
  /** The last check could not reach the release server; it checks again later. */
  offline?: boolean
  /** The version a rollback goes back to. */
  previous: { version: string } | null
  /** The last update changed the app's own window, which reopening runs. */
  reopen?: boolean
}

async function sendUpdates(url: string, method: 'POST' | 'PUT', body: unknown = {}): Promise<unknown> {
  const res = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as { error?: string }).error ?? `${url}: ${res.status}`)
  return data
}

export const updatesApi = {
  /** Null where there are no updates: a hosted deployment answers 403, and a
   *  server from before updates has no such route. */
  status: async (): Promise<UpdatesStatus | null> => {
    const res = await fetch('/api/updates')
    if (res.status === 403 || res.status === 404) return null
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error((data as { error?: string }).error ?? `/api/updates: ${res.status}`)
    return 'mode' in data && 'current' in data ? data as UpdatesStatus : null
  },
  check: () => sendUpdates('/api/updates/check', 'POST') as Promise<UpdatesStatus>,
  install: () => sendUpdates('/api/updates/install', 'POST') as Promise<UpdatesStatus>,
  rollback: () => sendUpdates('/api/updates/rollback', 'POST') as Promise<UpdatesStatus>,
  /** Saved with the other settings. */
  savePolicy: (updates: UpdatePolicy) => sendUpdates('/api/settings', 'PUT', { updates }).then(() => undefined),
}

const UPDATES_FOCUS = 'ade:updates-focus'
let updatesFocusPending = false

/** Opens Settings, General at the Updates section. */
export function openUpdates(): void {
  updatesFocusPending = true
  location.hash = '#view=settings:general'
  // Already on that page, the hash does not change; the section scrolls itself.
  window.dispatchEvent(new Event(UPDATES_FOCUS))
}

/** Calls `fn` when the Updates section is asked for, at once if it was asked
 *  for before the section was there to see it. */
export function onUpdatesFocus(fn: () => void): () => void {
  const run = () => { updatesFocusPending = false; fn() }
  if (updatesFocusPending) run()
  window.addEventListener(UPDATES_FOCUS, run)
  return () => window.removeEventListener(UPDATES_FOCUS, run)
}

// ---- the change log ----
/** A version of the change log with its entries. */
export interface ChangeVersion { version: string; date: string | null; entries: ChangeEntry[] }
export interface Changelog {
  /** False when this build carries no CHANGELOG.md. */
  included: boolean
  current: string
  /** Newest first. */
  versions: ChangeVersion[]
}
/** The change log; `after` keeps the versions newer than it. */
export const changelogApi = {
  get: async (after?: string): Promise<Changelog> => {
    const url = `/api/changelog${after ? `?after=${encodeURIComponent(after)}` : ''}`
    const res = await fetch(url)
    if (!res.ok) throw new Error(`/api/changelog: ${res.status}`)
    return res.json() as Promise<Changelog>
  },
}

/** Where What's new lives: a page of Help. */
export const WHATS_NEW_HASH = '#view=help:whats-new'
const WHATS_NEW = 'ade:whats-new'

/** Opens Help at What's new. */
export function openWhatsNew(): void {
  location.hash = WHATS_NEW_HASH
  // Already there, the hash does not change; Help switches pages itself.
  window.dispatchEvent(new Event(WHATS_NEW))
}

/** Calls `fn` when What's new is asked for. */
export function onWhatsNew(fn: () => void): () => void {
  window.addEventListener(WHATS_NEW, fn)
  return () => window.removeEventListener(WHATS_NEW, fn)
}

/** Where About lives: a page of Help. */
export const ABOUT_HASH = '#view=help:about'
const ABOUT = 'ade:about'

/** Opens Help at About. */
export function openAbout(): void {
  location.hash = ABOUT_HASH
  window.dispatchEvent(new Event(ABOUT))
}

/** Calls `fn` when About is asked for. */
export function onAbout(fn: () => void): () => void {
  window.addEventListener(ABOUT, fn)
  return () => window.removeEventListener(ABOUT, fn)
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

// ---- the model list ----
// Several places ask for it on the same mount: the chat package's picker,
// the availability banner, the credential notice, the fleet page. One
// in-flight request serves all of them, and a listing is reused for a
// short window so a remount does not refetch. The server keeps its own
// longer cache and refreshes in the background; this only stops the
// client asking three times for one answer.
export interface ModelsResponse {
  models: { id: string; name?: string; callable?: boolean; [k: string]: unknown }[]
  impaired?: { id: string; locked: boolean; reason?: string; unlock_url: string | null }[]
  unreachableSessions?: unknown[]
  error?: string
  credential?: string
}
const MODELS_REUSE_MS = 15_000
let modelsAt = 0
let modelsLast: ModelsResponse | null = null
let modelsInFlight: Promise<ModelsResponse> | null = null
export function fetchModels(opts: { refresh?: boolean } = {}): Promise<ModelsResponse> {
  if (!opts.refresh && modelsLast && Date.now() - modelsAt < MODELS_REUSE_MS) return Promise.resolve(modelsLast)
  if (modelsInFlight) return modelsInFlight
  modelsInFlight = fetch(opts.refresh ? '/api/chat/models?refresh=1' : '/api/chat/models')
    .then(async r => {
      if (!r.ok) throw new Error(`models: ${r.status}`)
      const d = (await r.json()) as ModelsResponse
      modelsLast = d; modelsAt = Date.now()
      return d
    })
    .finally(() => { modelsInFlight = null })
  return modelsInFlight
}
/** Forget the reused listing, so the next ask goes to the server. */
export function forgetModels(): void { modelsLast = null; modelsAt = 0 }
