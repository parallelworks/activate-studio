/**
 * A stand-in for the server's /api/transfers, after the contract in the
 * movers design: locations, a folder tree per location, plans and
 * transfers. Shared by the page's tests and the screenshot run, so it uses
 * only syntax Node can run with its types stripped.
 */

export interface MockLocation {
  id: string; kind: string; label: string; detail: string
  writable: boolean; indexed: boolean; browsable: boolean; available: boolean; why?: string
}
type Entry = { name: string; type: 'file' | 'dir' | 'link'; size: number; mtime: string }
type Tree = { [name: string]: Tree | number | 'link' }

export const MOCK_LOCATIONS: MockLocation[] = [
  { id: 'library:kb', kind: 'library', label: 'Projects', detail: 'on this computer, /home/user/Projects', writable: true, indexed: true, browsable: true, available: true },
  { id: 'library:reference', kind: 'library', label: 'Reference', detail: 'on this computer, /data/reference', writable: false, indexed: true, browsable: true, available: true },
  { id: 'library:archive', kind: 'library', label: 'Archive', detail: 'archive-host, /srv/archive', writable: true, indexed: true, browsable: true, available: true },
  { id: 'computer:Downloads', kind: 'computer', label: 'Downloads', detail: 'on this computer', writable: true, indexed: false, browsable: true, available: true },
  { id: 'cluster:gpu-cluster', kind: 'cluster', label: 'gpu-cluster', detail: 'home directory over pw ssh', writable: true, indexed: false, browsable: true, available: true },
  { id: 'cluster:cpu-cluster', kind: 'cluster', label: 'cpu-cluster', detail: 'home directory over pw ssh', writable: true, indexed: false, browsable: true, available: false, why: 'the cluster is stopped' },
  { id: 'saved:scratch', kind: 'saved', label: 'Scratch on archive-host', detail: 'archive-host, /scratch/shared', writable: true, indexed: false, browsable: true, available: true },
  { id: 'host:archive-host', kind: 'host', label: 'archive-host home', detail: 'archive-host, the home folder', writable: true, indexed: false, browsable: true, available: true },
  { id: 'conduit:conduit', kind: 'conduit', label: 'CONDUIT', detail: 'paths in the service namespace', writable: true, indexed: false, browsable: false, available: true },
]

const MB = 1024 * 1024
const TREES: Record<string, Tree> = {
  'library:kb': {
    simulations: {
      '2026': {
        'run-042': {
          output: { 'pressure.h5': 812 * MB, 'velocity.h5': 1210 * MB, 'mesh.vtu': 96 * MB, 'summary.csv': 48_000 },
          'input.yaml': 2_400,
          'job.log': 380_000,
        },
        'run-043': { 'input.yaml': 2_500 },
      },
      'README.md': 4_100,
    },
    reports: { 'q3-review.pdf': 3 * MB, 'q3-review.docx': 900_000, 'figures': { 'fig1.png': 420_000, 'fig2.png': 380_000 } },
    datasets: { 'training.parquet': 2400 * MB, 'validation.parquet': 310 * MB, 'labels.json': 120_000 },
    'notes.md': 12_000,
    'plan.xlsx': 64_000,
    'latest': 'link',
  },
  'library:reference': { manuals: { 'solver-guide.pdf': 14 * MB }, 'standards.pdf': 6 * MB },
  'library:archive': { '2025': { 'run-001': { 'out.h5': 900 * MB } }, '2026': {} },
  'computer:Downloads': { 'dataset-v2.tar.gz': 3200 * MB, 'slides.pptx': 8 * MB, 'invoice-template.docx': 50_000 },
  'cluster:gpu-cluster': {
    scratch: { 'run-042': { 'pressure.h5': 812 * MB }, empty: {} },
    'jobs': { 'train.sbatch': 1_200, 'eval.sbatch': 900 },
    '.bashrc': 3_100,
  },
  'saved:scratch': { shared: { 'common.tar': 1200 * MB } },
  'host:archive-host': { notes: { 'todo.txt': 2 * 1024 } },
}

function stamp(i: number): string {
  return new Date(Date.UTC(2026, 10, 14 - (i % 9), 9 + (i % 8), (i * 7) % 60)).toISOString()
}

function walk(tree: Tree, path: string): Tree | null {
  let at: Tree = tree
  for (const part of path.split('/').filter(Boolean)) {
    const next = at[part]
    if (typeof next !== 'object') return null
    at = next
  }
  return at
}

function entries(t: Tree): Entry[] {
  return Object.entries(t).map(([name, v], i) => ({
    name,
    type: typeof v === 'object' ? 'dir' : v === 'link' ? 'link' : 'file',
    size: typeof v === 'number' ? v : 0,
    mtime: stamp(i + name.length),
  }))
}

const ago = (s: number) => new Date(Date.now() - s * 1000).toISOString()

export function mockTransfers(): Record<string, unknown>[] {
  return [
    {
      id: 't-run', label: '', action: 'copy', mover: 'rsync', state: 'running', startedBy: 'user',
      source: { location: 'cluster:gpu-cluster', label: 'gpu-cluster', paths: ['scratch/run-042'] },
      destination: { location: 'library:kb', label: 'Projects', path: 'simulations/2026' },
      requestedAt: ago(130), startedAt: ago(128),
      files: { done: 5, total: 12 }, bytes: { done: 1717986918, total: 4294967296 }, rate: 52428800, etaSeconds: 49,
      replaced: 0, deleted: 0, skipped: 0,
      events: [
        { at: ago(130), text: 'Plan confirmed: 12 files, 4 GB' },
        { at: ago(128), text: 'Started rsync on gpu-cluster to this computer' },
        { at: ago(20), text: '5 of 12 files sent' },
      ],
    },
    {
      id: 't-await', label: 'Results to Archive', action: 'move', mover: 'studio-copy', state: 'awaiting-approval', startedBy: 'assistant', conversationId: '6f1c2a90-3b7d-4e55-9c1a-2b8f0e7d4a11',
      source: { location: 'library:kb', label: 'Projects', paths: ['reports/q3-review.pdf', 'reports/q3-review.docx'] },
      destination: { location: 'library:archive', label: 'Archive', path: '2026' },
      requestedAt: ago(40), files: { done: 0, total: 2 }, bytes: { done: 0, total: 4045000 }, replaced: 0, deleted: 0, skipped: 0,
    },
    {
      id: 't-fail', label: '', action: 'sync', mover: 'rsync', state: 'failed', startedBy: 'user',
      source: { location: 'library:kb', label: 'Projects', paths: ['datasets'] },
      destination: { location: 'saved:scratch', label: 'Scratch on archive-host', path: 'shared' },
      requestedAt: ago(5400), startedAt: ago(5390), endedAt: ago(5100),
      files: { done: 1, total: 3 }, bytes: { done: 325058560, total: 2972057600 }, replaced: 0, deleted: 0, skipped: 0,
      error: 'The destination ran out of space after 310 MB. Nothing took its name; the staging folder was removed.',
      events: [
        { at: ago(5400), text: 'Plan confirmed: 3 files, 2.8 GB' },
        { at: ago(5390), text: 'Started rsync from this computer to archive-host' },
        { at: ago(5100), text: 'rsync stopped: no space left on device' },
        { at: ago(5100), text: 'Removed the staging folder .studio-transfer-t-fail' },
      ],
    },
    {
      id: 't-done', label: 'Weekly reports', action: 'copy', mover: 'studio-copy', state: 'succeeded', startedBy: 'assistant', conversationId: '0a7e4c21-9d3b-4f60-8a2e-5c1b7f9d3e44',
      source: { location: 'library:kb', label: 'Projects', paths: ['reports'] },
      destination: { location: 'computer:Downloads', label: 'Downloads', path: '' },
      requestedAt: ago(90000), startedAt: ago(89990), endedAt: ago(89900),
      files: { done: 4, total: 4 }, bytes: { done: 4600000, total: 4600000 }, replaced: 0, deleted: 0, skipped: 1,
      events: [{ at: ago(89990), text: 'Approved in the chat' }, { at: ago(89900), text: 'Checked 4 files, sizes match' }],
    },
    {
      id: 't-cancel', label: '', action: 'copy', mover: 'conduit', state: 'cancelled', startedBy: 'user',
      source: { location: 'conduit:conduit', label: 'CONDUIT', paths: ['/projects/shared/inputs'] },
      destination: { location: 'cluster:gpu-cluster', label: 'gpu-cluster', path: 'scratch' },
      requestedAt: ago(200000), startedAt: ago(199990), endedAt: ago(199000),
      files: { done: 30, total: -1 }, bytes: { done: 90 * MB, total: -1 }, replaced: 0, deleted: 0, skipped: 0,
    },
  ]
}

export interface MockOptions {
  locations?: MockLocation[]
  /** Status for the locations call, as an older server's 404. */
  locationsStatus?: number
  transfers?: Record<string, unknown>[]
  /** Status for POST /api/transfers, as 409 for a plan that grew. */
  startStatus?: number
  /** Cancel refused with this reason. */
  cancelRefused?: string
  /** Fields over every plan made. */
  planPatch?: Record<string, unknown>
  /** Settings, Transfers as the server holds them; `managed` for sign-in on. */
  settings?: Record<string, unknown>
  /** Answer for every settings test. */
  testReply?: Record<string, unknown>
  /** Adds this many generated finished transfers, a few minutes apart, for
   *  paging and search at scale. */
  finished?: number
}

const OUTCOMES = ['succeeded', 'succeeded', 'succeeded', 'failed', 'cancelled', 'interrupted']
const PLACES = [
  { location: 'library:kb', label: 'Projects' }, { location: 'cluster:gpu-cluster', label: 'gpu-cluster' },
  { location: 'computer:Downloads', label: 'Downloads' }, { location: 'library:archive', label: 'Archive' },
  { location: 'saved:scratch', label: 'Scratch on archive-host' },
]

/** `n` finished transfers, newest first, the first ending 2 hours ago and
 *  each one 7 minutes before the last. */
export function generatedFinished(n: number, now = Date.now()): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = []
  for (let i = 0; i < n; i++) {
    const end = now - 2 * 3600_000 - i * 7 * 60_000
    const src = PLACES[i % PLACES.length]
    const dst = PLACES[(i + 2) % PLACES.length]
    const state = OUTCOMES[i % OUTCOMES.length]
    const files = 1 + (i % 40)
    const bytes = files * 3_500_000 + i * 1000
    out.push({
      id: `t-${String(i).padStart(5, '0')}`, label: i % 10 === 0 ? `Nightly results ${i}` : '', action: ['copy', 'move', 'sync'][i % 3],
      mover: ['rsync', 'studio-copy', 'conduit'][i % 3], state, startedBy: i % 4 === 0 ? 'assistant' : 'user',
      conversationId: i % 4 === 0 ? `c0000000-0000-4000-8000-${String(i).padStart(12, '0')}` : undefined,
      source: { location: src.location, label: src.label, paths: [`run-${i}`] },
      destination: { location: dst.location, label: dst.label, path: `results/${i % 7}` },
      requestedAt: new Date(end - 120_000).toISOString(), startedAt: new Date(end - 110_000).toISOString(), endedAt: new Date(end).toISOString(),
      files: { done: state === 'succeeded' ? files : Math.floor(files / 2), total: files },
      bytes: { done: state === 'succeeded' ? bytes : Math.floor(bytes / 2), total: bytes },
      replaced: 0, deleted: 0, skipped: 0,
      error: state === 'failed' ? 'The connection to the destination closed part way.' : undefined,
    })
  }
  return out
}

const ACTIVE = ['awaiting-approval', 'queued', 'running', 'verifying']

export function mockSettings(): Record<string, unknown> {
  return {
    managed: false,
    savedLocations: [
      { id: 'scratch', label: 'Scratch on archive-host', host: 'archive-host', root: '/scratch/shared' },
      { id: 'results', label: 'Results', host: 'user@compute-host', root: '/data/results', rsyncPath: '/opt/bin/rsync' },
    ],
    computerFolders: [
      { path: '/home/user/Downloads', label: 'Downloads', builtIn: true, enabled: true },
      { path: '/home/user/Desktop', label: 'Desktop', builtIn: true, enabled: false },
      { path: '/home/user/Documents', label: 'Documents', builtIn: true, enabled: true },
      { path: '/home/user/Projects', label: 'Projects', builtIn: false, enabled: true },
    ],
    movers: { rsync: { enabled: true, path: 'rsync' }, studioCopy: { enabled: true }, conduit: { enabled: false, server: null, mounts: [{ location: 'saved:scratch', path: 'shared', conduit: '/site/scratch' }] } },
    defaults: { mover: 'auto', checksum: false, preserveTimes: true, parallel: 4 },
    mcpServers: [{ id: 'conduit', label: 'CONDUIT', connected: true }],
    detected: { rsyncLocal: true, rsyncVersion: '3.2.7', clusters: 2, libraryHosts: ['archive-host', 'storage-host'] },
    hostLocations: { enabled: true, hidden: ['storage-host'] },
  }
}

export interface MockReply { status: number; body: unknown }

/** A server in memory. `handle` answers one request; `calls` records them. */
export function createTransfersMock(opts: MockOptions = {}) {
  const locations = opts.locations ?? MOCK_LOCATIONS
  const transfers = [...(opts.transfers ?? mockTransfers()), ...generatedFinished(opts.finished ?? 0)]
  const plans = new Map<string, Record<string, unknown>>()
  const calls: { method: string; path: string; body?: unknown }[] = []
  let n = 0
  let settings = opts.settings ?? mockSettings()

  function plan(req: { action: string; source: { location: string; paths: string[] }; destination: { location: string; path: string }; options?: Record<string, unknown> }): MockReply {
    const src = locations.find(l => l.id === req.source.location)
    const dst = locations.find(l => l.id === req.destination.location)
    if (!src || !dst) return { status: 400, body: { error: 'That location is not known.' } }
    if (!dst.writable) return { status: 400, body: { error: `${dst.label} is read only.` } }
    let files = 0
    let dirs = 0
    let bytes = 0
    const count = (t: Tree | number | 'link' | undefined) => {
      if (typeof t === 'number') { files++; bytes += t; return }
      if (t && typeof t === 'object') { dirs++; for (const v of Object.values(t)) count(v) }
    }
    const tree = TREES[src.id] ?? {}
    for (const p of req.source.paths) {
      const parts = p.split('/').filter(Boolean)
      const name = parts.pop()
      const parent = walk(tree, parts.join('/'))
      if (name && parent) count(parent[name]); else if (!name) count(tree)
    }
    const destTree = walk(TREES[dst.id] ?? {}, req.destination.path) ?? {}
    const replaces = req.action === 'sync' ? [] : req.source.paths.map(p => p.split('/').pop() ?? '').filter(x => x in destTree)
    if (replaces.length && !req.options?.replace && req.action !== 'sync') {
      return { status: 400, body: { error: `${replaces.join(', ')} already exists in ${dst.label}. Choose Replace files that already exist, or another folder.` } }
    }
    const deletes = req.action === 'sync' && req.options?.deleteExtra ? ['old/run-017.h5', 'old/run-018.h5', 'notes-draft.md'] : []
    const id = `plan-${++n}`
    const mover = req.options?.mover && req.options.mover !== 'auto' ? String(req.options.mover) : 'rsync'
    const p = {
      id, action: req.action, label: String(req.options?.label ?? ''),
      mover, moverWhy: mover === 'rsync' ? 'both ends have rsync and this computer reaches both' : 'chosen in the options',
      movers: [
        { name: 'rsync', ok: true },
        { name: 'studio-copy', ok: true },
        { name: 'conduit', ok: false, why: 'only when both ends are in its namespace' },
      ],
      source: { location: src.id, label: src.label, paths: req.source.paths },
      destination: { location: dst.id, label: dst.label, path: req.destination.path },
      files, dirs, bytes,
      replaces, deletes, replacesCount: replaces.length, deletesCount: deletes.length,
      skipped: { hidden: 2, links: 1 },
      freeBytes: 512 * 1024 * MB,
      warnings: [] as string[],
      expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
      ...(opts.planPatch ?? {}),
    }
    plans.set(id, p)
    return { status: 200, body: { plan: p } }
  }

  function handle(method: string, url: string, body?: unknown): MockReply {
    const u = new URL(url, 'http://studio.test')
    calls.push({ method, path: u.pathname + u.search, body })
    const path = u.pathname
    if (path === '/api/transfers/locations') {
      if (opts.locationsStatus && opts.locationsStatus !== 200) return { status: opts.locationsStatus, body: { error: 'not found' } }
      return { status: 200, body: { locations } }
    }
    if (path === '/api/transfers/browse') {
      const loc = u.searchParams.get('location') ?? ''
      const p = (u.searchParams.get('path') ?? '').replace(/^\/+|\/+$/g, '')
      const l = locations.find(x => x.id === loc)
      if (!l) return { status: 400, body: { error: 'That location is not known.' } }
      if (!l.available) return { status: 502, body: { error: l.why ?? 'not reachable' } }
      if (p.split('/').includes('..')) return { status: 400, body: { error: 'That path leaves the location.' } }
      const t = walk(TREES[loc] ?? {}, p)
      if (!t) return { status: 404, body: { error: `No folder ${p} in ${l.label}.` } }
      const parent = p ? p.split('/').slice(0, -1).join('/') : null
      return { status: 200, body: { location: loc, path: p, parent, entries: entries(t), truncated: false } }
    }
    if (path === '/api/transfers/plan' && method === 'POST') return plan(body as Parameters<typeof plan>[0])
    if (path === '/api/transfers' && method === 'POST') {
      if (opts.startStatus && opts.startStatus !== 200) return { status: opts.startStatus, body: { error: 'The files changed since the plan was made.' } }
      const p = plans.get((body as { planId: string }).planId)
      if (!p) return { status: 409, body: { error: 'That plan expired.' } }
      const t = {
        id: `t-new-${n}`, label: p.label, action: p.action, mover: p.mover, state: 'queued', startedBy: 'user',
        source: p.source, destination: p.destination, requestedAt: new Date().toISOString(),
        files: { done: 0, total: p.files }, bytes: { done: 0, total: p.bytes }, replaced: 0, deleted: 0, skipped: 0,
        events: [{ at: new Date().toISOString(), text: 'Plan confirmed' }],
      }
      transfers.unshift(t)
      return { status: 200, body: { transfer: t } }
    }
    if (path === '/api/transfers/settings' && method === 'GET') return { status: 200, body: settings }
    if (path === '/api/transfers/settings' && method === 'PUT') {
      if (settings.managed) return { status: 403, body: { error: 'The operator sets these.' } }
      const b = body as Record<string, unknown>
      const locs = (b.savedLocations as Record<string, unknown>[]).map((l, i) => ({ ...l, id: l.id || `saved-${i}-${++n}` }))
      const { managed: _m, mcpServers: _x, detected: _d, ...input } = b
      void _m; void _x; void _d
      settings = { ...settings, ...input, savedLocations: locs }
      return { status: 200, body: settings }
    }
    if (path === '/api/transfers/settings/test' && method === 'POST') {
      const b = body as { host?: string; root?: string; conduit?: string }
      // As the server does: a 400 starts with the field it is about.
      if (b.host !== undefined && /\s/.test(b.host)) return { status: 400, body: { error: 'host: a host has no spaces' } }
      if (b.root !== undefined && !b.root.startsWith('/')) return { status: 400, body: { error: 'root: give an absolute path, starting with /' } }
      if (settings.managed) return { status: 403, body: { error: 'with sign-in on, saved locations and CONDUIT are not used' } }
      return { status: 200, body: opts.testReply ?? { ok: true, rsync: true, rsyncVersion: '3.2.7', writable: true, freeBytes: 2 * 1024 * 1024 * MB, roundTripMs: 38 } }
    }
    if (path === '/api/transfers/movers') {
      const conduitOn = (settings.movers as { conduit: { enabled: boolean } }).conduit.enabled
      return { status: 200, body: { movers: [
        { name: 'rsync', ok: true },
        { name: 'studio-copy', ok: true },
        { name: 'conduit', ok: conduitOn, ...(conduitOn ? {} : { why: 'turned off in Settings, Transfers' }) },
      ] } }
    }
    if (path === '/api/transfers/summary') {
      const day = Date.now() - 86_400_000
      return {
        status: 200,
        body: {
          active: transfers.filter(t => ACTIVE.includes(String(t.state))).length,
          awaitingApproval: transfers.filter(t => t.state === 'awaiting-approval').length,
          failedLast24h: transfers.filter(t => t.state === 'failed' && Date.parse(String(t.endedAt ?? '')) >= day).length,
        },
      }
    }
    if (path === '/api/transfers' && method === 'GET') {
      const sp = u.searchParams
      const state = sp.get('state') ?? 'all'
      const ended = (t: Record<string, unknown>) => Date.parse(String(t.endedAt ?? t.startedAt ?? t.requestedAt))
      const isActive = (t: Record<string, unknown>) => ACTIVE.includes(String(t.state))
      const strip = (t: Record<string, unknown>) => ({ ...t, events: undefined })
      const activeRows = transfers.filter(isActive)
      if (state === 'active') return { status: 200, body: { transfers: activeRows.map(strip), next: null } }
      const q = (sp.get('q') ?? '').toLowerCase()
      const since = sp.get('since') ? Date.parse(sp.get('since')!) : null
      let done = transfers.filter(t => !isActive(t))
        .filter(t => !sp.get('outcome') || t.state === sp.get('outcome'))
        .filter(t => !sp.get('startedBy') || t.startedBy === sp.get('startedBy'))
        .filter(t => since === null || ended(t) >= since)
        .filter(t => !q || JSON.stringify([t.label, t.source, t.destination, t.mover, t.action]).toLowerCase().includes(q))
        .sort((a, b) => ended(b) - ended(a))
      const limit = Number(sp.get('limit') ?? 50)
      const start = sp.get('before') ? Number(sp.get('before')) : 0
      done = done.slice(start)
      const page = done.slice(0, limit)
      const next = done.length > limit ? String(start + limit) : null
      return { status: 200, body: { transfers: [...(state === 'all' && !start ? activeRows : []), ...page].map(strip), next } }
    }
    const m = /^\/api\/transfers\/([^/]+)(\/cancel)?$/.exec(path)
    if (m) {
      const t = transfers.find(x => x.id === decodeURIComponent(m[1]))
      if (!t) return { status: 404, body: { error: 'No such transfer.' } }
      if (m[2] && method === 'POST') {
        if (opts.cancelRefused) return { status: 200, body: { cancelled: false, why: opts.cancelRefused } }
        t.state = 'cancelled'; t.endedAt = new Date().toISOString()
        return { status: 200, body: { cancelled: true } }
      }
      if (method === 'DELETE') { transfers.splice(transfers.indexOf(t), 1); return { status: 200, body: {} } }
      return { status: 200, body: { transfer: t } }
    }
    return { status: 404, body: { error: 'not found' } }
  }

  return { handle, calls, transfers, plans }
}
