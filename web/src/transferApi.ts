/**
 * The Transfers page's view of the server: the types and calls of the
 * /api/transfers contract, and the plain rules the page applies to them
 * (which pane can send where, what a plan's confirm button says).
 *
 * Not to be confused with transfers.ts, which tracks uploads and moves
 * inside one library from the Library page.
 */

export type LocationKind = 'library' | 'computer' | 'cluster' | 'saved' | 'host' | 'rclone' | 'conduit'

export interface TransferLocation {
  id: string
  kind: LocationKind
  label: string
  detail: string
  writable: boolean
  indexed: boolean
  browsable: boolean
  available: boolean
  why?: string
  /** Why a reachable library is read only for transfers. */
  readOnly?: string
  /** For a library whose files Studio cannot reach or write, with sign-in
   *  off: a saved location for its files, with what Studio knows of their
   *  host and folder. A field Studio does not know is left out. */
  suggest?: LocationSuggest
  /** For such a library with sign-in on, where saved locations are off:
   *  what can be written instead. */
  hint?: string
}

export interface LocationSuggest { label: string; host?: string; root?: string; port?: number; sshOptions?: string }

export interface BrowseEntry { name: string; type: 'file' | 'dir' | 'link'; size: number; mtime: string }
export interface BrowseResult { location: string; path: string; parent: string | null; entries: BrowseEntry[]; truncated: boolean }

export type TransferAction = 'copy' | 'move' | 'sync'
export type MoverName = 'auto' | 'rsync' | 'rclone' | 'studio-copy' | 'conduit'

export interface PlanOptions {
  mover?: MoverName
  replace?: boolean
  deleteExtra?: boolean
  checksum?: boolean
  preserveTimes?: boolean
  label?: string
}

export interface PlanRequest {
  action: TransferAction
  source: { location: string; paths: string[] }
  destination: { location: string; path: string }
  options?: PlanOptions
}

export interface MoverChoice { name: string; ok: boolean; why?: string }
/** A mover's name as the page shows it. */
export const MOVER_TEXT: Record<string, string> = { rsync: 'rsync', rclone: 'rclone', 'studio-copy': 'Studio copy', conduit: 'CONDUIT' }

export interface Plan {
  id: string
  action: TransferAction
  label: string
  mover: string
  moverWhy: string
  movers: { name: string; ok: boolean; why?: string }[]
  source: { location: string; label: string; paths: string[] }
  destination: { location: string; label: string; path: string }
  files: number
  dirs: number
  bytes: number
  replaces: string[]
  deletes: string[]
  replacesCount: number
  deletesCount: number
  skipped: { hidden: number; links: number }
  freeBytes: number | null
  warnings: string[]
  /** When the index of a library the transfer changes, other than the
   *  main one, shows the change. */
  indexNotes?: string[]
  expiresAt: string
  /** How a sync tells changed files, such as "size and md5". */
  compare?: string
}

export type TransferState = 'awaiting-approval' | 'queued' | 'running' | 'verifying' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'

export interface Transfer {
  id: string
  label: string
  action: TransferAction
  mover: string
  source: { location: string; label: string; paths: string[] }
  destination: { location: string; label: string; path: string }
  state: TransferState
  startedBy: 'user' | 'assistant'
  conversationId?: string
  requestedAt: string
  startedAt?: string
  endedAt?: string
  files: { done: number; total: number }
  bytes: { done: number; total: number }
  rate?: number
  etaSeconds?: number
  replaced: number
  deleted: number
  skipped: number
  error?: string
  /** The plan's index notes. */
  indexNotes?: string[]
  events?: { at: string; text: string }[]
}

/** One page of the list, newest first. Active transfers are never paged. */
export const PAGE_SIZE = 50
export type Outcome = 'succeeded' | 'failed' | 'cancelled' | 'interrupted'
export interface ListQuery {
  state: 'active' | 'done' | 'all'
  limit?: number
  /** The `next` of the page before, for the next older page. */
  before?: string | null
  q?: string
  outcome?: Outcome | null
  startedBy?: 'user' | 'assistant' | null
  /** RFC 3339: only transfers that finished at or after this time. */
  since?: string | null
}
export interface TransferSummary { active: number; awaitingApproval: number; failedLast24h: number }

/** Settings, Transfers. `managed` and `mcpServers` are read only. */
export interface SavedLocation {
  id: string; label: string; host: string; root: string; rsyncPath?: string
  /** For a host the ssh config does not describe. */
  port?: number; sshOptions?: string
}
/** A folder of a Studio location that is a path in CONDUIT's namespace. */
export interface ConduitMount { location: string; path: string; conduit: string }
export interface ComputerFolder { path: string; label: string; builtIn: boolean; enabled: boolean }
export interface TransferSettings {
  managed: boolean
  savedLocations: SavedLocation[]
  computerFolders: ComputerFolder[]
  movers: {
    rsync: { enabled: boolean; path: string }
    studioCopy: { enabled: true }
    conduit: { enabled: boolean; server: string | null; mounts?: ConduitMount[] }
    /** rclone on this computer; its own configuration is used as it is. */
    rclone?: { enabled: boolean; path: string }
  }
  defaults: { mover: MoverName; checksum: boolean; preserveTimes: boolean; parallel: number }
  mcpServers: { id: string; label: string; connected: boolean }[]
  /** Read only: what Studio found with no setup. */
  detected?: {
    rsyncLocal: boolean; rsyncVersion?: string
    /** The rsync program found here, and which transfers it is used for. */
    rsyncPath?: string; rsyncText?: string
    clusters: number; libraryHosts: string[]
    rclone?: RcloneDetected
  }
  /** The home folder of each host a remote library uses, offered as a
   *  `host` location (sign-in off). `hidden` holds host names. */
  hostLocations?: { enabled: boolean; hidden: string[] }
}
export const HOST_LOCATIONS = 'hostLocations' as const
/** rclone as Settings found it: whether it is on, the program and its
 *  version, and the remotes of its configuration with their types. */
export interface RcloneDetected {
  enabled: boolean
  found: boolean
  path?: string
  version?: string
  remotes: { name: string; type: string }[]
  /** What is missing, in words: not installed, no remotes, sign-in on. */
  why?: string
}
export type TransferSettingsInput = Omit<TransferSettings, 'managed' | 'mcpServers' | 'detected'>
export interface SettingTest {
  ok: boolean; why?: string; rsync?: boolean; rsyncVersion?: string; writable?: boolean; freeBytes?: number; roundTripMs?: number
  /** Which transfers the host's rsync is used for. */
  rsyncText?: string
  /** The account takes SFTP only: Studio reaches it over SFTP, copies use
   *  Studio copy, and rsync is not available there. */
  sftpOnly?: boolean
  /** Whether rclone is on the host, for information (transfers do not run it there). */
  rclone?: boolean; rcloneVersion?: string
}
export interface SettingTestBody { host: string; root: string; rsyncPath?: string; port?: number; sshOptions?: string }

/** A test's result in one line. */
export function testText(r: SettingTest): string {
  if (!r.ok) return `Test failed: ${r.why ?? 'no reason given'}`
  // A test passes only once the folder was listed, so it exists.
  const parts = [r.sftpOnly ? 'Reached over SFTP (the account takes SFTP only), folder exists' : 'Reached, folder exists']
  if (r.sftpOnly) parts.push('rsync not available, Studio copy will be used')
  else if (r.rsync !== undefined) parts.push(r.rsync ? `rsync${r.rsyncVersion ? ` ${r.rsyncVersion}` : ''} found` : 'no rsync, Studio copy will be used')
  if (r.rclone !== undefined) parts.push(r.rclone ? `rclone${r.rcloneVersion ? ` ${r.rcloneVersion}` : ''} there` : 'no rclone there')
  if (r.writable !== undefined) parts.push(r.writable ? 'writable' : 'read only')
  if (r.freeBytes !== undefined) parts.push(`${bytesText(r.freeBytes)} free`)
  if (r.roundTripMs !== undefined) parts.push(`${Math.round(r.roundTripMs)} ms round trip`)
  return parts.join(', ') + '.' + (r.rsync && r.rsyncText ? ` ${r.rsyncText}.` : '') + (r.why ? ` Note: ${r.why}.` : '')
}

/** A 400 from the settings routes starts with the field it is about
 *  ("root: give an absolute path"); this splits it off. */
export function fieldError(message: string): { field: string; text: string } | null {
  const m = /^([A-Za-z]+(?:\[\d+\])?(?:\.[A-Za-z]+(?:\[\d+\])?)*): (.+)$/s.exec(message)
  return m ? { field: m[1], text: m[2].charAt(0).toUpperCase() + m[2].slice(1) } : null
}

/** A refused request, with the server's reason and its status. */
export class TransferError extends Error {
  constructor(message: string, readonly status: number) { super(message) }
}

const BASE = '/api/transfers'

/** The server leaves an empty `paths` or `path` out of a plan's or a
 *  transfer's ends; the page reads them as empty. */
function ends<T extends { source: { paths?: string[] }; destination: { path?: string } }>(x: T): T {
  if (!x) return x
  return { ...x, source: { ...x.source, paths: x.source?.paths ?? [] }, destination: { ...x.destination, path: x.destination?.path ?? '' } }
}

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(url, init)
  } catch (e) {
    throw new TransferError(`Studio did not answer: ${(e as Error).message}`, 0)
  }
  const body = await res.json().catch(() => ({})) as { error?: string; message?: string; reason?: string; why?: string }
  if (!res.ok) throw new TransferError(body.error ?? body.message ?? body.reason ?? body.why ?? `The request failed (HTTP ${res.status})`, res.status)
  return body as T
}

const post = (body: unknown): RequestInit => ({ method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })

export const transferApi = {
  locations: () => call<{ locations: TransferLocation[] }>(`${BASE}/locations`).then(r => r.locations ?? []),
  browse: (location: string, path: string) =>
    call<BrowseResult>(`${BASE}/browse?${new URLSearchParams({ location, path })}`),
  plan: (req: PlanRequest) => call<{ plan: Plan }>(`${BASE}/plan`, post(req)).then(r => ends(r.plan)),
  start: (planId: string) => call<{ transfer: Transfer }>(BASE, post({ planId })).then(r => ends(r.transfer)),
  list: (query: ListQuery) => {
    const q = new URLSearchParams({ state: query.state, limit: String(query.limit ?? PAGE_SIZE) })
    if (query.before) q.set('before', query.before)
    if (query.q?.trim()) q.set('q', query.q.trim())
    if (query.outcome) q.set('outcome', query.outcome)
    if (query.startedBy) q.set('startedBy', query.startedBy)
    if (query.since) q.set('since', query.since)
    return call<{ transfers?: Transfer[]; next?: string | null }>(`${BASE}?${q}`)
      .then(r => ({ transfers: (r.transfers ?? []).map(ends), next: r.next ?? null }))
  },
  summary: () => call<TransferSummary>(`${BASE}/summary`),
  /** Which movers can reach between two locations, in that direction. */
  movers: (source: string, destination: string) =>
    call<{ movers?: MoverChoice[] }>(`${BASE}/movers?${new URLSearchParams({ source, destination })}`).then(r => r.movers ?? []),
  settings: () => call<TransferSettings>(`${BASE}/settings`),
  saveSettings: (s: TransferSettingsInput) =>
    call<TransferSettings>(`${BASE}/settings`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(s) }),
  testSetting: (body: SettingTestBody | { conduit: string }) => call<SettingTest>(`${BASE}/settings/test`, post(body)),
  /** Adds one saved location (sign-in off), as tested in the dialog; the
   *  answer has it with its id, and its location id. */
  addLocation: (l: SavedLocation) => call<{ location: SavedLocation; id: string }>(`${BASE}/settings/locations`, post(l)),
  get: (id: string) => call<{ transfer: Transfer }>(`${BASE}/${encodeURIComponent(id)}`).then(r => ends(r.transfer)),
  cancel: (id: string) => call<{ cancelled: boolean; why?: string }>(`${BASE}/${encodeURIComponent(id)}/cancel`, post({})),
  remove: (id: string) => call<unknown>(`${BASE}/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}

/** Whether this server has transfers at all: an older one answers 404 on
 *  the locations, and the page stays out of the menu. Asked once per load.
 *  A success counts only with a list of locations in it; an error other
 *  than 404 counts as present, so a server that is briefly down still shows
 *  the page and its error. */
let probe: Promise<boolean> | null = null
export function transfersPresent(): Promise<boolean> {
  probe ??= fetch(`${BASE}/locations`).then(
    async r => {
      if (r.status === 404) return false
      if (!r.ok) return true
      const body = await r.json().catch(() => null) as { locations?: unknown } | null
      return Array.isArray(body?.locations)
    },
    () => true,
  )
  return probe
}
/** For tests. */
export function resetTransfersProbe(): void { probe = null }

// ---- Rules the page applies ----

export const KIND_ORDER: { kind: LocationKind; label: string }[] = [
  { kind: 'library', label: 'Libraries' },
  { kind: 'computer', label: 'This computer' },
  { kind: 'cluster', label: 'Clusters' },
  { kind: 'saved', label: 'Saved locations' },
  { kind: 'host', label: 'Library hosts' },
  { kind: 'rclone', label: 'rclone remotes' },
  { kind: 'conduit', label: 'CONDUIT' },
]

export const ACTIVE_STATES: readonly TransferState[] = ['awaiting-approval', 'queued', 'running', 'verifying']
export const isActive = (t: Pick<Transfer, 'state'>) => ACTIVE_STATES.includes(t.state)

export const STATE_TEXT: Record<TransferState, string> = {
  'awaiting-approval': 'Awaiting approval',
  queued: 'Queued',
  running: 'Running',
  verifying: 'Verifying',
  succeeded: 'Succeeded',
  failed: 'Failed',
  cancelled: 'Cancelled',
  interrupted: 'Interrupted',
}

export const ACTION_TEXT: Record<TransferAction, string> = { copy: 'Copy', move: 'Move', sync: 'Sync' }

/** A path inside a folder, relative to the location's root. */
export const joinPath = (dir: string, name: string) => (dir ? `${dir.replace(/\/+$/, '')}/${name}` : name)

/** Folders first, then by name as a person reads it. */
export function sortEntries(entries: BrowseEntry[]): BrowseEntry[] {
  return [...entries].sort((a, b) => (Number(b.type === 'dir') - Number(a.type === 'dir')) || a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' }))
}

/** "12 files", "1 folder", "3 files and 2 folders"; empty when both are 0. */
export function countWords(files: number, dirs = 0): string {
  const f = files === 1 ? '1 file' : `${files.toLocaleString()} files`
  const d = dirs === 1 ? '1 folder' : `${dirs.toLocaleString()} folders`
  if (files > 0 && dirs > 0) return `${f} and ${d}`
  if (dirs > 0) return d
  if (files > 0) return f
  return ''
}

/** The words on a plan's confirm button, which carry the consequence:
 *  "Copy 12 files", "Move 12 files", "Sync and delete 3 files". */
export function confirmText(p: Pick<Plan, 'action' | 'files' | 'dirs' | 'replacesCount' | 'deletesCount'>): string {
  const what = p.files < 0 ? 'files' : countWords(p.files, p.files === 0 ? p.dirs : 0) || 'files'
  if (p.action === 'sync') {
    if (p.deletesCount > 0) return `Sync and delete ${countWords(p.deletesCount)}`
    return p.files === 0 ? 'Sync, nothing to send' : `Sync ${what}`
  }
  const verb = ACTION_TEXT[p.action]
  return p.replacesCount > 0 ? `${verb} ${what} and replace ${p.replacesCount.toLocaleString()}` : `${verb} ${what}`
}

/** A transfer's name in the list: its label, else "Copy 12 files". */
export function transferTitle(t: Pick<Transfer, 'label' | 'action' | 'files' | 'source'>): string {
  if (t.label?.trim()) return t.label
  const n = t.files.total >= 0 ? countWords(t.files.total) : ''
  if (n) return `${ACTION_TEXT[t.action]} ${n}`
  const one = t.source.paths.length === 1 ? baseName(t.source.paths[0]) : ''
  return one ? `${ACTION_TEXT[t.action]} ${one}` : `${ACTION_TEXT[t.action]} ${plural(t.source.paths.length, 'item')}`
}

export function baseName(p: string): string {
  const parts = p.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? ''
}

/** Where a transfer reads from, in one line. */
export function sourceLine(s: { label: string; paths: string[] }): string {
  if (!s.paths.length) return s.label
  const first = s.paths[0] || '/'
  return s.paths.length === 1 ? `${s.label}: ${first}` : `${s.label}: ${first} and ${s.paths.length - 1} more`
}
export const destLine = (d: { label: string; path: string }) => `${d.label}: ${d.path || '/'}`

/** A size the way the list shows it. */
export function bytesText(n: number): string {
  if (n < 0) return 'counted at start'
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1).replace(/\.0$/, '')} ${units[i]}`
}

export function durationText(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

/** Why a location cannot be the source of an action, or null when it can:
 *  it must be reachable, and writable for a move. */
export function sourceWhy(action: TransferAction, loc: TransferLocation | null): string | null {
  if (!loc) return 'Choose a location to send from.'
  if (!loc.available) return `${loc.label} is not reachable${loc.why ? `: ${loc.why.replace(/\.$/, '')}` : ''}.`
  if (action === 'move' && !loc.writable) return `${loc.label} is read only, so nothing can be moved out of it.`
  return null
}

/** The source paths a pane offers for an action, or why it offers none. */
export function sourcePaths(
  action: TransferAction,
  pane: { location: TransferLocation | null; path: string; selected: string[]; entries: BrowseEntry[] },
): { paths: string[] } | { why: string } {
  const loc = pane.location
  const why = sourceWhy(action, loc)
  if (why || !loc) return { why: why ?? '' }
  if (!loc.browsable) {
    // Nothing to tick: the path typed is what is sent.
    return pane.path ? { paths: [pane.path] } : { why: `Type a path in ${loc.label} to send.` }
  }
  if (action === 'sync') {
    if (pane.selected.length === 0) return { paths: [pane.path] }
    if (pane.selected.length > 1) return { why: 'Sync takes one folder. Select one folder, or none to sync the folder open.' }
    const e = pane.entries.find(x => x.name === pane.selected[0])
    if (e?.type !== 'dir') return { why: 'Sync takes a folder. Select a folder, or none to sync the folder open.' }
    return { paths: [joinPath(pane.path, e.name)] }
  }
  if (!pane.selected.length) return { why: 'Select files or folders to send.' }
  return { paths: pane.selected.map(n => joinPath(pane.path, n)) }
}

/** The library location id Transfers uses for a library. */
export const libraryLocation = (libraryId: string) => `library:${libraryId}`

/** The main library's location; Studio updates its index after a transfer. */
export const MAIN_LIBRARY_LOCATION = libraryLocation('kb')

/** Whether a transfer changed the main library's files: it wrote there, or
 *  moved files out of it. */
export function touchesMainLibrary(t: Pick<Transfer, 'action' | 'source' | 'destination'>): boolean {
  return t.destination.location === MAIN_LIBRARY_LOCATION || (t.action === 'move' && t.source.location === MAIN_LIBRARY_LOCATION)
}

/** The event the Library tree and its details listen to, to read the main
 *  library's files again. */
export const KB_CHANGED_EVENT = 'ade-kb-changed'

/** A Search or Query path as a transfer takes it: relative to the
 *  library's root, with no leading "./" or "/" and no trailing "/". Both
 *  pages print paths relative to the index root, which mirrors the
 *  library's root; a cell that is absolute (the index root itself, or a raw
 *  SELECT's own text) names no path inside the library, and gives null. */
export function libraryRelative(p: string): string | null {
  const s = p.trim().replace(/^(\.\/)+/, '').replace(/\/+$/, '')
  if (!s || s === '.' || s.startsWith('/') || s.split('/').includes('..')) return null
  return s
}

/** Why a pane cannot receive, or null when it can. */
export function destinationWhy(loc: TransferLocation | null): string | null {
  if (!loc) return 'Choose a location to send to.'
  if (!loc.available) return `${loc.label} is not reachable${loc.why ? `: ${loc.why.replace(/\.$/, '')}` : ''}.`
  if (!loc.writable) return `${loc.label} is read only.`
  return null
}

/** The names a shift-click selects: every visible name between the last
 *  one clicked and this one, inclusive, added to what was selected. */
export function rangeSelect(visible: string[], selected: string[], anchor: string | null, name: string): string[] {
  const a = anchor ? visible.indexOf(anchor) : -1
  const b = visible.indexOf(name)
  if (a < 0 || b < 0) return selected.includes(name) ? selected : [...selected, name]
  const [lo, hi] = a < b ? [a, b] : [b, a]
  const out = new Set(selected)
  for (const n of visible.slice(lo, hi + 1)) out.add(n)
  return visible.filter(n => out.has(n)).concat(selected.filter(n => !visible.includes(n)))
}

/** What works with no setup: the places listed now, by kind, the library
 *  hosts offered as places, and whether rsync is here. */
export interface PlacesReady {
  total: number
  libraries: number
  folders: number
  clusters: number
  saved: number
  hosts: number
  /** rclone remotes listed as places. */
  rclone: number
  rsync: { found: boolean; version?: string; text?: string } | null
  /** Why rclone offers no remotes (not installed, none configured), when it is on. */
  rcloneWhy: string | null
}

export function placesReady(locations: TransferLocation[], settings: TransferSettings | null): PlacesReady {
  const on = locations.filter(l => l.available)
  const by = (k: LocationKind) => on.filter(l => l.kind === k).length
  // Library hosts are listed as `host` locations once offered, so they are
  // counted from the list, as the rest are.
  const r = { libraries: by('library'), folders: by('computer'), clusters: by('cluster'), saved: by('saved') + by('conduit'), hosts: by('host'), rclone: by('rclone') }
  const rc = settings?.detected?.rclone
  return {
    ...r,
    total: r.libraries + r.folders + r.clusters + r.saved + r.hosts + r.rclone,
    rsync: settings?.detected ? { found: settings.detected.rsyncLocal, version: settings.detected.rsyncVersion, text: settings.detected.rsyncText } : null,
    rcloneWhy: rc?.enabled && !r.rclone ? rcloneMissing(rc) : null,
  }
}

/** A count and its noun: "1 file", "3 files", "2 libraries". */
export const plural = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`

/** The ready summary's parts, as short phrases. */
export function readyParts(p: PlacesReady): string[] {
  const out = [plural(p.libraries, 'library', 'libraries'), plural(p.folders, 'folder on this computer', 'folders on this computer'), plural(p.clusters, 'cluster')]
  if (p.hosts) out.push(plural(p.hosts, 'library host'))
  if (p.saved) out.push(plural(p.saved, 'saved location'))
  if (p.rclone) out.push(plural(p.rclone, 'rclone remote'))
  else if (p.rcloneWhy) out.push(p.rcloneWhy)
  if (p.rsync) out.push(!p.rsync.found ? 'no rsync here, Studio copy is used' : p.rsync.text ?? `rsync ${p.rsync.version ?? ''}`.trim() + ' found')
  return out
}
/** Why rclone offers no remotes, in a few words for the Ready line. */
export function rcloneMissing(d: RcloneDetected): string {
  if (!d.found) return 'rclone not found'
  if (!d.remotes.length) return 'rclone has no remotes'
  return 'no rclone remotes'
}
export const placesText = (p: PlacesReady) => `${plural(p.total, 'place')} ready`
