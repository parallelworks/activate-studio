import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { addChoices } from '../src/components/AddLibrary'
import { addedMessage } from '../src/components/LibraryAddDialogs'

const caps = (fullText: boolean, vectors = false) => ({ index: true, fullText, vectors })

const PRIMARY = { id: 'kb', label: 'Notes', primary: true, writable: true, pinned: true, source: true, caps: caps(true, true), indexRoot: '/data/studio/gufi/Notes', sourceRoot: '/home/u/Notes' }
const SITE = { id: 'site', label: 'Site index', primary: false, writable: false, pinned: false, source: false, caps: caps(true), indexRoot: '/data/site-index', sourceRoot: null }
const PINNED = { id: 'scratch', label: 'Scratch', primary: false, writable: false, pinned: true, source: true, caps: { index: false, fullText: false, vectors: false }, indexRoot: '/data/scratch-index', sourceRoot: '/data/scratch' }
const SSH = {
  id: 'u-box-index', label: 'index on box', transport: 'ssh', site: 'me@box', destination: 'me@box', port: 2222, gufiVersion: '0.7.0',
  sourceDir: '', sourceRoot: null, indexRoot: '/data/index', state: 'ready', run: null, error: null,
  builtAt: '2026-10-01T00:00:00Z', directories: 12, files: 40, scheduler: null,
}
const CLUSTER = {
  id: 'u-c1-workdir', label: 'c1 $WORKDIR', transport: 'pw', site: 'c1', destination: null, port: null, gufiVersion: null,
  sourceDir: '$WORKDIR', sourceRoot: null, indexRoot: null, state: 'building', run: { slug: 'run-17', mode: 'build', startedAt: '2026-10-01T00:00:00Z' },
  error: null, builtAt: null, directories: null, files: null, scheduler: null,
}

const LISTINGS: Record<string, { path: string; parent: string | null; writable: boolean; dirs: { name: string; path: string }[] }> = {
  '/home/u': { path: '/home/u', parent: '/home', writable: true, dirs: [{ name: 'Notes', path: '/home/u/Notes' }, { name: 'Research', path: '/home/u/Research' }] },
  '/home/u/Notes': { path: '/home/u/Notes', parent: '/home/u', writable: true, dirs: [] },
  '/home/u/Research': { path: '/home/u/Research', parent: '/home/u', writable: true, dirs: [] },
}

const BUILT_HERE = {
  id: 'papers', label: 'Papers', primary: false, writable: false, pinned: false, source: true, caps: caps(true), indexRoot: '/home/u/indexes/papers/papers', sourceRoot: '/home/u/papers',
  build: { indexDir: '/home/u/indexes/papers', managed: false, state: 'building', error: null, builtAt: null, files: null, dirs: null, progress: { phase: 'Indexing folders', done: 120, total: 0, current: '120 folders' } },
}
const BUILT_REMOTE = {
  ...SSH, id: 'u-box-data', label: 'data on box', existing: false, built: true, indexDir: '/home/me/.activate-studio/indexes/data-1a2b3c4d', sourceDir: '/home/me/data',
  indexRoot: null, state: 'building', directories: null, files: null, builtAt: null, progress: { phase: 'Extracting text', done: 0, total: 0, current: null },
}
/** A copy of a folder in a Transfers location: an SFTP drop's upload folder. */
const COPY = {
  id: 'upload', label: 'upload', primary: false, writable: false, pinned: false, source: true, caps: caps(true),
  indexRoot: '/data/studio/libraries/upload/index/files', sourceRoot: '/data/studio/libraries/upload/files',
  build: { indexDir: '/data/studio/libraries/upload/index', managed: true, state: 'ready', error: null, builtAt: '2026-11-16T12:00:00Z', files: 3, dirs: 1, progress: null },
  copyOf: {
    location: 'saved:drop', path: 'upload', label: 'Drop', every: 15, checkedAt: '2026-11-16T12:30:00Z', changedAt: '2026-11-16T12:00:00Z',
    transfer: 'abc', files: 3, deleted: 0, error: null as string | null, looking: false, running: null as string | null,
  },
}
/** A bucket of an rclone remote, indexed from its listing. */
const BUCKET = {
  id: 'research', label: 'research', primary: false, writable: false, pinned: false, source: false, caps: caps(false), fromBucket: true,
  indexRoot: '/data/studio/libraries/research/index/files', sourceRoot: null,
  build: { indexDir: '/data/studio/libraries/research/index', managed: true, state: 'ready', error: null, builtAt: '2026-11-19T12:00:00Z', files: 2_500_000, dirs: 4_000, progress: null },
  bucket: {
    location: 'rclone:store', path: 'research', label: 'store', every: 60, listedAt: '2026-11-19T12:00:00Z',
    objects: 2_500_000, bytes: 3 * 1024 ** 4, folders: 4_000, skipped: 0, requests: 2_500, provider: 'AWS', seconds: 95, error: null as string | null, listing: false,
  },
}
/** A build job as /api/index/status carries it, in its text phase. */
const phases = (state: 'running' | 'done') => [
  { id: 'names', label: 'Names and folders', state: 'done', done: 3, total: 3, unit: 'folders', step: null, ms: 40, note: null },
  { id: 'documents', label: 'Documents (PDF, Word, PowerPoint, Excel)', state: 'done', done: 2, total: 2, unit: 'documents', step: null, ms: 900, note: '2 converted' },
  { id: 'text', label: 'Text, images and full-text search', state, done: state === 'done' ? 9 : 4, total: 9, unit: 'items', step: 'extract', ms: state === 'done' ? 1200 : null, note: null },
  { id: 'meaning', label: 'Search by meaning', state: state === 'done' ? 'done' : 'pending', done: state === 'done' ? 30 : 0, total: state === 'done' ? 30 : 0, unit: 'passages', step: null, ms: state === 'done' ? 400 : null, note: null },
]
const buildJob = (state: 'running' | 'done' | 'cancelled') => ({
  id: 7, kind: 'build', dir: '', label: 'Updating the index', state, startedAt: '2026-10-01T00:00:00Z', runningAt: '2026-10-01T00:00:00Z',
  finishedAt: state === 'running' ? null : '2026-10-01T00:00:03Z', ms: state === 'running' ? null : 2540, error: null, threads: 8,
  phases: phases(state === 'done' ? 'done' : 'running'), searchableAt: null, warnings: [], warningCount: 0,
})

interface Server {
  platform: boolean
  standalone: boolean
  ssh: boolean
  pw?: boolean
  /** pw ssh shown though it cannot be used, and why. */
  pwOffered?: boolean
  pwWhy?: string
  workflow?: boolean
  canBuild?: boolean
  /** An rclone remote among the Transfers locations. */
  rclone?: boolean
  libs: Record<string, unknown>[]
  user: Record<string, unknown>[]
  primary: string
  indexed: boolean
  startedAt: string
  jobDone: boolean
  /** A whole-folder build the index status reports: none, running, stopped or done. */
  build: 'none' | 'running' | 'cancelled' | 'done'
  deleted?: boolean
  fail?: Record<string, string>
  calls: { method: string; url: string; body: unknown }[]
}

const server = (over: Partial<Server> = {}): Server => ({
  platform: false, standalone: true, ssh: true, libs: [PRIMARY, SITE], user: [], primary: '/home/u/Notes', indexed: true,
  startedAt: 'first start', jobDone: false, build: 'none', calls: [], ...over,
})

function stubServer(s: Server) {
  const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    s.calls.push({ method, url, body })
    const path = url.split('?')[0]
    if (s.fail?.[`${method} ${path}`]) return reply({ error: s.fail[`${method} ${path}`] }, 400)
    if (path === '/api/config') {
      return reply({ appName: 'Studio', platform: s.platform, user: { id: 'u', username: 'u' }, libraries: s.libs.map(l => ({ ...l, indexRoot: undefined, sourceRoot: undefined })) })
    }
    const depth = /^\/api\/libraries\/([^/]+)\/depth$/.exec(path)
    if (depth && method === 'PUT') {
      s.libs = s.libs.map(l => (l.id === depth[1] ? { ...l, depth: (body as { depth: string }).depth } : l))
      s.user = s.user.map(l => (l.id === depth[1] ? { ...l, depth: (body as { depth: string }).depth } : l))
      return reply({ id: depth[1], depth: (body as { depth: string }).depth })
    }
    if (path === '/api/libraries' && method === 'GET') return reply({ canBuild: !!s.canBuild, libraries: s.libs.map(l => (l.primary ? { ...l, sourceRoot: s.primary, label: s.primary.split('/').pop() } : l)) })
    const through = /^\/api\/libraries\/([^/]+)\/read-through(\/test)?$/.exec(path)
    if (through) {
      const b = body as { destination: string; root?: string; sshOptions?: string }
      if (through[2]) {
        return b.destination === 'me@down'
          ? reply({ ok: false, steps: [{ name: 'ssh', ok: false, detail: 'Nothing accepted the connection on port 22.' }] })
          : reply({ ok: true, steps: [{ name: 'ssh', ok: true, detail: `Connected to ${b.destination}, a Linux host.` }, { name: 'source', ok: true, detail: `${b.root ?? '/data/site-files'} is readable on ${b.destination}.` }] })
      }
      const rt = b.destination ? { destination: b.destination, port: null, sshOptions: b.sshOptions ?? null, root: b.root ?? null } : null
      const set = (l: Record<string, unknown>) => (l.id === through[1] ? { ...l, readThrough: rt } : l)
      s.libs = s.libs.map(set)
      s.user = s.user.map(set)
      return reply({ readThrough: rt })
    }
    if (path === '/api/libraries/build/check') {
      const b = body as { sourceRoot: string; indexRoot?: string }
      return reply({ ok: true, create: [b.sourceRoot, ...(b.indexRoot ? [b.indexRoot] : [])], steps: [
        { name: 'gufi', ok: true, detail: 'GUFI is in /opt/gufi/bin.' },
        { name: 'source', ok: true, detail: `${b.sourceRoot} does not exist yet; it is created, empty, when the build starts.` },
        { name: 'index', ok: true, detail: 'The index is written in the Studio\'s own data folder.' },
        { name: 'extract', ok: true, detail: 'Text is extracted from documents and images.' },
      ] })
    }
    if (path === '/api/libraries/build') {
      s.libs = [...s.libs, BUILT_HERE]
      return reply({ library: BUILT_HERE, job: { id: 9 } })
    }
    if (/^\/api\/libraries\/[^/]+\/rebuild$/.test(path)) return reply({ library: s.libs.find(l => path.includes(String(l.id))) })
    if (path === '/api/transfers/locations') {
      return reply({ locations: [
        { id: 'library:kb', kind: 'library', label: 'Notes', available: true, writable: true, browsable: true, indexed: true, detail: 'this computer' },
        { id: 'saved:drop', kind: 'saved', label: 'Drop', available: true, writable: true, browsable: true, indexed: false, detail: 'drop@files.example.com, /srv/drop' },
        ...(s.rclone ? [{ id: 'rclone:store', kind: 'rclone', label: 'store', available: true, writable: true, browsable: true, indexed: false, detail: 'rclone remote (s3)' }] : []),
      ] })
    }
    if (path === '/api/transfers/browse') {
      const q = new URL(url, 'http://x').searchParams
      const at = q.get('path') ?? ''
      return reply(at === ''
        ? { location: q.get('location'), path: '', parent: null, truncated: false, entries: [
          { name: 'upload', type: 'dir', size: 0, mtime: '2026-11-16T00:00:00Z' }, { name: 'readme.txt', type: 'file', size: 9, mtime: '2026-11-16T00:00:00Z' }] }
        : { location: q.get('location'), path: at, parent: '', truncated: false, entries: [{ name: 'poster.pdf', type: 'file', size: 9, mtime: '2026-11-16T00:00:00Z' }] })
    }
    if (path === '/api/libraries/copy') {
      s.libs = [...s.libs, { ...COPY, build: { ...COPY.build, state: 'building', builtAt: null, files: null, dirs: null, progress: { phase: 'Copying from Drop, upload', done: 1, total: 0, current: '1 of 3 files' } } }]
      return reply({ library: s.libs.at(-1), job: { id: 9 } })
    }
    if (/^\/api\/libraries\/[^/]+\/update$/.test(path)) {
      // The look is on from the answer until it ends.
      s.libs = s.libs.map(l => (l.copyOf && path.includes(String(l.id)) ? { ...l, copyOf: { ...(l.copyOf as object), looking: true } } : l))
      return reply({ library: s.libs.find(l => path.includes(String(l.id))), started: true })
    }
    if (path === '/api/libraries/bucket') {
      s.libs = [...s.libs, { ...BUCKET, build: { ...BUCKET.build, state: 'building', builtAt: null, files: null, dirs: null, progress: { phase: 'Listing store, research', done: 0, total: 0, current: '12,000 objects' } }, bucket: { ...BUCKET.bucket, listedAt: null, objects: 0, bytes: 0, requests: 0 } }]
      return reply({ library: s.libs.at(-1), job: { id: 9 } })
    }
    const bucketEvery = /^\/api\/libraries\/([^/]+)\/bucket$/.exec(path)
    if (bucketEvery && method === 'PUT') {
      s.libs = s.libs.map(l => (l.id === bucketEvery[1] ? { ...l, bucket: { ...(l.bucket as object), every: (body as { every: number }).every } } : l))
      return reply({ library: s.libs.find(l => l.id === bucketEvery[1]) })
    }
    const every = /^\/api\/libraries\/([^/]+)\/copy$/.exec(path)
    if (every && method === 'PUT') {
      s.libs = s.libs.map(l => (l.id === every[1] ? { ...l, copyOf: { ...(l.copyOf as object), every: (body as { every: number }).every } } : l))
      return reply({ library: s.libs.find(l => l.id === every[1]) })
    }
    if (path === '/api/user-libraries/ssh/build/test') {
      return reply({ ok: true, create: ['/home/me/data'], indexDir: '/home/me/.activate-studio/indexes/data-1a2b3c4d', gufiBin: '/opt/gufi/bin', steps: [
        { name: 'ssh', ok: true, detail: 'Connected to me@box on port 2222, a Linux host.' },
        { name: 'gufi', ok: true, detail: 'gufi_query 0.7.0 in /opt/gufi/bin.' },
        { name: 'source', ok: true, detail: '/home/me/data does not exist yet; it is created, empty, when the build starts.' },
        { name: 'index', ok: true, detail: 'The index is written in /home/me/.activate-studio/indexes/data-1a2b3c4d.' },
        { name: 'extract', ok: true, warn: true, detail: 'No Python 3.10 or later with SQLite\'s full-text module on me@box, so the index has names and metadata only.' },
      ] })
    }
    if (path === '/api/user-libraries/ssh/build') {
      s.user = [...s.user, BUILT_REMOTE]
      return reply(BUILT_REMOTE)
    }
    if (path === '/api/libraries' && method === 'POST') {
      const b = body as { id: string; label?: string; indexRoot: string }
      const lib = { id: b.id || 'lab', label: b.label ?? 'lab', primary: false, writable: false, pinned: false, source: false, caps: caps(false), indexRoot: b.indexRoot, sourceRoot: null }
      s.libs = [...s.libs, lib]
      return reply({ library: lib })
    }
    if (path.startsWith('/api/libraries/') && method === 'DELETE') {
      s.libs = s.libs.filter(l => l.id !== decodeURIComponent(path.slice('/api/libraries/'.length)))
      return reply({ ok: true })
    }
    if (path === '/api/user-libraries' && method === 'GET') return reply({ libraries: s.user, available: true, ssh: s.ssh, pw: !!s.pw, pwOffered: s.pwOffered ?? !!s.pw, pwWhy: s.pwWhy ?? null, workflow: !!s.workflow })
    if (path === '/api/user-libraries/pw/systems') {
      return reply({ usable: !!s.pw, why: s.pwWhy ?? null, systems: s.pw ? [{ name: 'c1', label: 'c1', status: 'active', running: true, type: null }] : [], error: null })
    }
    if (path === '/api/user-libraries' && method === 'POST') {
      const lib = { ...CLUSTER, id: 'u-c2', site: (body as { site: string }).site }
      s.user = [...s.user, lib]
      return reply(lib)
    }
    if (path === '/api/user-libraries/ssh/test') return reply({ ok: true, steps: [{ name: 'ssh', ok: true, detail: 'Connected to me@lab, a Linux host.' }], gufiBin: '/usr/local/bin' })
    if (path === '/api/user-libraries/ssh') {
      const lib = { ...SSH, id: 'u-lab', label: 'index on lab', destination: 'me@lab', site: 'me@lab', port: null }
      s.user = [...s.user, lib]
      return reply({ id: lib.id, label: lib.label })
    }
    if (/^\/api\/user-libraries\/[^/]+\/refresh$/.test(path)) return reply(s.user.find(l => path.includes(String(l.id))) ?? {})
    if (path === '/api/agent-sites') return reply({ sites: [{ name: 'c1', kind: 'cluster' }, { name: 'here', kind: 'local' }] })
    if (path === '/api/setup/status') {
      if (!s.standalone) return reply({ firstRun: false, standalone: false, canRestart: false })
      return reply({
        firstRun: false, standalone: true, canRestart: true, launcher: true,
        primary: { path: s.primary, exists: true, writable: true, indexed: s.indexed, ...(url.includes('files=1') ? { files: 12 } : {}) },
        gufi: { found: true, bin: '/opt/gufi/bin', semantic: false }, job: null,
      })
    }
    if (path === '/api/setup/browse') {
      const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
      return LISTINGS[p] ? reply(LISTINGS[p]) : reply({ error: `no such folder: ${p}` }, 404)
    }
    if (path === '/api/version') return reply({ version: 'dev', startedAt: s.startedAt })
    if (path === '/api/setup/primary-folder') {
      s.primary = (body as { path: string }).path
      s.startedAt = 'second start'
      s.indexed = false
      return reply({ saved: true, restarting: true })
    }
    if (path === '/api/index/status') {
      const job = s.build === 'running' ? buildJob('running') : null
      const last = s.build === 'done' || s.build === 'cancelled' ? buildJob(s.build) : null
      return reply({
        running: !!job, job, queued: [], last, lastBuild: last, runs: last ? [last] : [], indexed: s.indexed && !s.deleted,
        incomplete: s.build === 'cancelled', threads: 8, lastSweepAt: null, lastSweepMs: null, lastChanges: [], lastError: null, sweepIntervalSec: 300,
        log: { path: '/data/logs/indexing.log', bytes: 10, reveal: false },
      })
    }
    if (path === '/api/index/build') { s.build = 'running'; return reply(buildJob('running')) }
    if (path === '/api/index/cancel') { s.build = 'cancelled'; return reply({ stopped: 1 }) }
    if (path === '/api/index' && method === 'DELETE') { s.deleted = true; s.indexed = false; return reply({ id: 8, kind: 'delete', state: 'queued' }) }
    if (path === '/api/index/log') return new Response('2026-10-01 00:00:00 [7 build] Updating the index: started on 8 threads\n', { status: 200 })
    if (path === '/api/setup/index') return reply({ id: 7, state: 'running', ms: null, error: null, progress: { phase: 'files', done: 0, total: 3, current: null, failed: [] } })
    if (path === '/api/index/job/7') {
      if (!s.jobDone) return reply({ id: 7, state: 'running', ms: null, error: null, progress: { phase: 'directories', done: 1, total: 3, current: 'papers', failed: [] } })
      s.indexed = true
      return reply({ id: 7, state: 'done', ms: 2500, error: null, progress: { phase: 'done', done: 3, total: 3, current: null, failed: [] } })
    }
    return reply({ error: 'not found' }, 404)
  }))
}

// The app's configuration is kept per module, so each test renders the page
// from fresh modules and sees its own server's platform flag.
async function renderPage(s: Server) {
  stubServer(s)
  vi.resetModules()
  ;(await import('../src/indexActivity')).setIndexPollingForTests(10, 10)
  const { LibrariesSection } = await import('../src/components/LibrariesSection')
  return render(<LibrariesSection pollMs={5} />)
}

/** A library's card. */
const rowOf = (name: string) => screen.getByText(name, { selector: '.lib-name' }).closest('[data-library]') as HTMLElement
/** What runs under a library's details, and its foot: a build's progress, the last run and the log. */
const detailOf = (name: string) => rowOf(name)
/** Opens a library's More menu and picks one of its actions. */
async function moreAction(name: string, action: string) {
  fireEvent.click(within(rowOf(name)).getByRole('button', { name: `More actions for ${name}` }))
  const item = await screen.findByRole('menuitem', { name: new RegExp(`^${action}`) })
  fireEvent.click(item)
  return item
}
const posted = (s: Server, url: string) => s.calls.filter(c => c.method === 'POST' && c.url.split('?')[0] === url)

describe('what Add a library offers', () => {
  it('where, what, and how a folder on a system is built, from what this Studio offers', () => {
    // With sign-in on (no building here), the libraries on this computer
    // are the deployment's, so nothing is offered without a system.
    const none = addChoices({ buildHere: false, ssh: false, pw: false, workflow: false })
    expect(none.where).toEqual([])
    // A computer of its own: this computer, and a system over ssh or pw ssh, never the workflow.
    const mine = addChoices({ buildHere: true, ssh: true, pw: true, workflow: false })
    expect(mine.where).toEqual(['here', 'system'])
    expect(mine.how('here')).toEqual(['build', 'connect'])
    expect(mine.how('system')).toEqual(['build', 'connect'])
    expect(mine.how('system', true)).toEqual(['build', 'connect'])
    expect(mine.builders()).toEqual(['pw'])
    // A hosted Studio: no ssh, and the workflow builds on a cluster too.
    const hosted = addChoices({ buildHere: false, ssh: false, pw: true, workflow: true })
    expect(hosted.where).toEqual(['system'])
    expect(hosted.how('system')).toEqual(['build', 'connect'])
    expect(hosted.builders()).toEqual(['pw', 'workflow'])
    const workflowOnly = addChoices({ buildHere: false, ssh: false, pw: false, workflow: true })
    expect(workflowOnly.how('system')).toEqual(['build'])
    // A folder of one's own comes first on a system.
    expect(addChoices({ buildHere: false, ssh: false, pw: true, workflow: false, space: true }).how('system')).toEqual(['own', 'build', 'connect'])
    // A copy of a folder in a Transfers location is made from Transfers.
    const copyOnly = addChoices({ buildHere: true, ssh: false, pw: false, workflow: false, copy: true })
    expect(copyOnly.where).toEqual(['here'])
    // A bucket is its own place, with nothing more to choose.
    const buckets = addChoices({ buildHere: true, ssh: false, pw: false, workflow: false, copy: true, bucket: true })
    expect(buckets.where).toEqual(['here', 'bucket'])
    expect(buckets.how('bucket')).toEqual([])
    expect(addedMessage({ label: 'research', bucket: {} }, true, true)).toBe('Listing research. Its progress shows in the list. It is searchable by name once the listing is indexed.')
    expect(addedMessage({ label: 'Papers' }, true, true)).toMatch(/^Building Papers\./)
  })
})

/** Opens a picker and chooses an option. Its list draws only the rows that
 *  fit its height, which jsdom measures as zero, so layout is given a size
 *  while it is open; `absent` are options that must not be offered. */
async function pickOption(name: string, label: string, absent: string[] = []) {
  const rect = Element.prototype.getBoundingClientRect
  const sizes = ['offsetHeight', 'clientHeight', 'offsetWidth', 'clientWidth'] as const
  for (const k of sizes) Object.defineProperty(HTMLElement.prototype, k, { configurable: true, get: () => 400 })
  Element.prototype.getBoundingClientRect = () => ({ x: 0, y: 0, top: 0, left: 0, width: 400, height: 400, right: 400, bottom: 400, toJSON() {} }) as DOMRect
  try {
    const u = userEvent.setup()
    await u.click(screen.getByRole('combobox', { name }))
    const option = await screen.findByRole('option', { name: new RegExp(`^${label}`) })
    for (const a of absent) expect(screen.queryByRole('option', { name: new RegExp(`^${a}`) })).toBeNull()
    await u.click(option)
  } finally {
    Element.prototype.getBoundingClientRect = rect
    for (const k of sizes) delete (HTMLElement.prototype as unknown as Record<string, unknown>)[k]
  }
}

describe('Settings, Indexing, Libraries', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('brings a library named in a search\'s left-out line into view and marks its row', async () => {
    sessionStorage.setItem('studio.focusLibrary', 'site')
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled
    await renderPage(server())
    const site = await waitFor(() => rowOf('Site index'))
    await waitFor(() => expect(site).toHaveClass('lib-focus'))
    expect(site).toHaveAttribute('data-library', 'site')
    await waitFor(() => expect(scrolled).toHaveBeenCalled())
    expect(sessionStorage.getItem('studio.focusLibrary')).toBeNull()
    expect(rowOf('Notes')).not.toHaveClass('lib-focus')
  })

  it('brings the Add a library form into view when its button opens it, below the list', async () => {
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled
    await renderPage(server())
    await waitFor(() => rowOf('Notes'))
    expect(scrolled).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    const form = await screen.findByRole('region', { name: 'Add a library' })
    await waitFor(() => expect(scrolled).toHaveBeenCalled())
    expect(scrolled.mock.contexts.at(-1)).toBe(form)
  })

  it('and when the page is open already, as after visiting Settings before the search', async () => {
    Element.prototype.scrollIntoView = vi.fn()
    await renderPage(server())
    const notes = await waitFor(() => rowOf('Notes'))
    expect(notes).not.toHaveClass('lib-focus')
    const { openLibraryRow } = await import('../src/components/OmittedLine')
    openLibraryRow('kb')
    await waitFor(() => expect(rowOf('Notes')).toHaveClass('lib-focus'))
  })

  it('says which libraries are written: the main folder, and My library as your account, not the main folder alone', async () => {
    const s = server({ libs: [PRIMARY, SITE], user: [SSH] })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    // Without sign-in the main library is the person's own folder here.
    expect(screen.getByText(/your folder on this computer, is written here\. The libraries you index on your remote computers are written as your account there/)).toBeInTheDocument()
    expect(screen.getByText(/The others are read only/)).toBeInTheDocument()
    expect(screen.queryByText(/only one written to/)).toBeNull()
  })

  it('names another host for reading a library\'s originals, saves it only after its test, and can remove it', async () => {
    const s = server({ libs: [PRIMARY, SITE], user: [SSH] })
    await renderPage(s)
    const row = await waitFor(() => rowOf('Site index'))
    expect(within(row).getByText('Originals via')).toBeInTheDocument()
    // The main folder's files are here: nothing to read through.
    expect(within(rowOf('Notes')).queryByText('Originals via')).toBeNull()
    fireEvent.click(within(row).getByRole('button', { name: /Set the host originals are read through for Site index/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.change(within(dialog).getByLabelText(/^Host/), { target: { value: 'me@down' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test connection' }))
    await within(dialog).findByText(/Nothing accepted the connection/)
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
    // A host that passes, with a folder, then a change after the test asks for another.
    fireEvent.change(within(dialog).getByLabelText(/^Host/), { target: { value: 'me@xfer' } })
    fireEvent.change(within(dialog).getByLabelText(/Folder on that host/), { target: { value: '/mnt/site' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test connection' }))
    await within(dialog).findByText(/\/mnt\/site is readable on me@xfer/)
    fireEvent.change(within(dialog).getByLabelText(/Folder on that host/), { target: { value: '/mnt/other' } })
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(within(dialog).getByText('Fields changed: test again to save.')).toBeInTheDocument()
    fireEvent.change(within(dialog).getByLabelText(/Folder on that host/), { target: { value: '/mnt/site' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test connection' }))
    await within(dialog).findByText(/is readable on me@xfer/)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(within(rowOf('Site index')).getByText('me@xfer:/mnt/site')).toBeInTheDocument())
    expect(s.calls.find(c => c.method === 'PUT' && c.url === '/api/libraries/site/read-through')?.body).toEqual({ destination: 'me@xfer', root: '/mnt/site' })
    // A library over ssh keeps it with its own settings, and it can be removed.
    const ssh = rowOf('index on box')
    fireEvent.click(within(ssh).getByRole('button', { name: /Set the host originals are read through for index on box/ }))
    const second = await screen.findByRole('dialog')
    fireEvent.change(within(second).getByLabelText(/^Host/), { target: { value: 'me@xfer' } })
    fireEvent.click(within(second).getByRole('button', { name: 'Test connection' }))
    await within(second).findByText(/is readable on me@xfer/)
    fireEvent.click(within(second).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(within(rowOf('index on box')).getByText('me@xfer')).toBeInTheDocument())
    fireEvent.click(within(rowOf('index on box')).getByRole('button', { name: /Change the host originals are read through for index on box/ }))
    fireEvent.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(within(rowOf('index on box')).queryByText('me@xfer')).toBeNull())
    expect(s.calls.filter(c => c.method === 'PUT' && c.url === '/api/libraries/u-box-index/read-through').map(c => c.body)).toEqual([{ destination: 'me@xfer' }, { destination: '' }])
  })

  it('where the Studio signs people in, the host is shown when set and cannot be changed, and a deployment\'s pinned library takes none', async () => {
    const set = { ...SITE, readThrough: { destination: 'me@xfer', port: null, sshOptions: null, root: '/mnt/site' } }
    await renderPage(server({ ssh: false, libs: [PRIMARY, set, PINNED] }))
    const row = await waitFor(() => rowOf('Site index'))
    expect(within(row).getByText('me@xfer:/mnt/site')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: /originals are read through/ })).toBeNull()
    expect(within(rowOf('Scratch')).queryByText('Originals via')).toBeNull()
  })

  it('a library built here whose index is gone says so, not Ready', async () => {
    const gone = { ...BUILT_HERE, id: 'gone', label: 'Gone', caps: { index: false, fullText: false, vectors: false }, build: { ...BUILT_HERE.build, state: 'ready', progress: null, builtAt: '2026-10-01T00:00:00Z' } }
    await renderPage(server({ libs: [PRIMARY, gone] }))
    const row = await waitFor(() => rowOf('Gone'))
    expect(within(row).getByText('Index not found')).toBeInTheDocument()
    expect(within(row).getByText('Rebuild makes it again.')).toBeInTheDocument()
  })

  it('a remote build that could not read some files says how many and why on its row, and one that read everything says Ready', async () => {
    const line = '312 PDFs have no text: this system has no PDF reader'
    const done = { ...BUILT_REMOTE, indexRoot: '/home/me/.activate-studio/indexes/data-1a2b3c4d/data', state: 'ready', directories: 4, files: 400, builtAt: '2026-10-01T00:00:00Z', progress: null, error: null }
    await renderPage(server({ libs: [PRIMARY], user: [{ ...done, gaps: [line] }, { ...done, id: 'u-box-whole', label: 'whole on box', gaps: [] }] }))
    const row = await waitFor(() => rowOf('data on box'))
    expect(within(row).getByText('Ready; some files were not read')).toBeInTheDocument()
    expect(within(row).getByText(line)).toBeInTheDocument()
    const whole = await waitFor(() => rowOf('whole on box'))
    expect(within(whole).getByText('Ready')).toBeInTheDocument()
    expect(screen.getAllByText(line)).toHaveLength(1)
  })

  it('an index GUFI 0.8.0 reads wrongly says why on its row, whatever kind of library it is, and no other row has a note', async () => {
    const note = 'Written by a GUFI before 0.8.0 and rolled up, so the files of the folders it rolled up are listed more than once and counts and sizes over them are too high; rebuild it with GUFI 0.8.0 or later.'
    const rolled = { ...SITE, id: 'rolled', label: 'Rolled up', formatNote: note }
    const remote = { ...SSH, id: 'u-box-rolled', label: 'rolled on box', formatNote: note }
    await renderPage(server({ libs: [PRIMARY, rolled, SITE], user: [remote, { ...SSH, id: 'u-box-fine', label: 'fine on box', formatNote: null }] }))
    const row = await waitFor(() => rowOf('Rolled up'))
    expect(within(row).getByText(note)).toBeInTheDocument()
    expect(within(await waitFor(() => rowOf('rolled on box'))).getByText(note)).toBeInTheDocument()
    expect(screen.getAllByText(note)).toHaveLength(2)
  })

  it('lists every library in one table: kind, where it is, state, what it searches, and its actions', async () => {
    const s = server({ platform: true, libs: [PRIMARY, SITE, PINNED], user: [SSH, CLUSTER] })
    await renderPage(s)

    const main = await waitFor(() => rowOf('Notes'))
    expect(within(main).getByText('Main folder')).toBeInTheDocument()
    expect(within(main).getByText(/· Read\/write$/)).toBeInTheDocument()
    expect(within(main).getByTitle('/home/u/Notes')).toBeInTheDocument()
    expect(await within(main).findByText('12 files')).toBeInTheDocument()
    // Ready, as every other library that can be searched says.
    expect(within(main).getByText('Ready')).toBeInTheDocument()
    expect(within(main).getByRole('button', { name: 'Change folder' })).toBeInTheDocument()
    expect(await within(main).findByRole('button', { name: 'Update index' })).toBeEnabled()
    // Delete index is behind More, not beside the everyday actions.
    expect(within(main).queryByRole('button', { name: 'Delete index' })).toBeNull()
    expect(within(main).getByRole('button', { name: 'More actions for Notes' })).toBeInTheDocument()
    expect(within(detailOf('Notes')).getByRole('button', { name: 'View log' })).toBeInTheDocument()
    expect(within(main).getByRole('combobox', { name: 'Index depth of Notes' })).toHaveTextContent('Meaning on documents')

    const site = rowOf('Site index')
    expect(within(site).getByText('GUFI index on this machine, connected as it is')).toBeInTheDocument()
    expect(within(site).getByText(/· Read only$/)).toBeInTheDocument()
    // No files here: the row says what the index can be searched by, from what it holds.
    expect(within(site).getByText('Holds')).toBeInTheDocument()
    expect(within(site).getByText('Names, metadata and text')).toBeInTheDocument()
    expect(within(site).getByText('Ready')).toBeInTheDocument()
    expect(within(site).getByText('Text')).toBeInTheDocument()
    expect(within(site).getByText('as built')).toBeInTheDocument()

    const pinned = rowOf('Scratch')
    expect(within(pinned).getByText('GUFI index on this machine, connected as it is, set by the deployment')).toBeInTheDocument()
    expect(within(pinned).getByText('Index not found')).toBeInTheDocument()
    expect(within(pinned).queryByRole('button', { name: /Remove|More actions/ })).toBeNull()
    expect(within(pinned).getByText(/Named in the server's STUDIO_LIBRARIES setting; remove it there/)).toBeInTheDocument()

    const ssh = rowOf('index on box')
    expect(within(ssh).getByText('GUFI index over ssh, connected as it is')).toBeInTheDocument()
    expect(within(ssh).getByText(/· Read only \(on me@box\)$/)).toBeInTheDocument()
    expect(within(ssh).getByTitle('me@box:/data/index')).toHaveTextContent('me@box:/data/index')
    expect(within(ssh).getByText('2222')).toBeInTheDocument()
    expect(within(ssh).getByText('Names and metadata')).toBeInTheDocument()
    expect(within(ssh).getByText('12 folders, 40 files')).toBeInTheDocument()
    expect(within(ssh).getByRole('button', { name: 'Check' })).toBeInTheDocument()

    const cluster = rowOf('c1 $WORKDIR')
    expect(within(cluster).getByText('Index built on a cluster by a workflow job, yours alone')).toBeInTheDocument()
    expect(within(cluster).getByText('Building')).toBeInTheDocument()
    expect(within(cluster).getByText('run run-17')).toBeInTheDocument()
    expect(within(cluster).getByText('being built')).toBeInTheDocument()
    expect(within(cluster).getByRole('button', { name: 'Refresh' })).toBeDisabled()

    // Check reruns the ssh connection test.
    fireEvent.click(within(ssh).getByRole('button', { name: 'Check' }))
    await waitFor(() => expect(posted(s, `/api/user-libraries/${SSH.id}/refresh`)).toHaveLength(1))
    // Remove asks first and says the index stays; then it takes the library out. A pinned one has no Remove.
    await moreAction('Site index', 'Remove')
    const ask = await screen.findByRole('dialog', { name: 'Remove Site index?' })
    expect(ask.textContent).toMatch(/The index stays where it is, in \/data\/site-index/)
    expect(s.calls.some(c => c.method === 'DELETE')).toBe(false)
    fireEvent.click(within(ask).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(screen.queryByText('Site index', { selector: '.lib-name' })).toBeNull())
    expect(s.calls.some(c => c.method === 'DELETE' && c.url === '/api/libraries/site')).toBe(true)
  })

  it('an ssh library shows the account ssh reaches, on one line cut in the middle, whole on hover and in Copy', async () => {
    const lib = { ...SSH, id: 'u-alias', label: 'alias index', site: 'box', destination: 'box', port: null, reached: 'me@a-long-host-name.example.org', indexRoot: '/data/groups/me/projects/archive' }
    await renderPage(server({ user: [lib] }))
    const row = await waitFor(() => rowOf('alias index'))
    const value = within(row).getByTitle('me@a-long-host-name.example.org:/data/groups/me/projects/archive')
    expect(value).toHaveTextContent('me@a-long-host-name.example.org:/data/groups/me/projects/archive')
    // Read whole by a screen reader; the start shortens first and the last folders stay.
    expect(value).toHaveAttribute('aria-label', 'me@a-long-host-name.example.org:/data/groups/me/projects/archive')
    expect(value.querySelector('.lib-path-tail')).toHaveTextContent(/^\/projects\/archive$/)
    expect(within(row).getByRole('button', { name: 'Copy the index path of alias index' })).toBeInTheDocument()
  })

  it('opens with the add form showing when the + Add menu sent us', async () => {
    location.hash = '#view=settings:libraries?add=1'
    try {
      const s = server({ platform: true, ssh: true, canBuild: true })
      await renderPage(s)
      expect(await screen.findByRole('button', { name: 'This computer' })).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: 'Add a library' })).toBeNull()
    } finally { location.hash = '' }
  })

  it('opens the add form when the + Add menu sends us to the page already open', async () => {
    const s = server({ platform: true, ssh: true, canBuild: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    expect(screen.queryByRole('button', { name: 'This computer' })).toBeNull()
    try {
      location.hash = '#view=settings:libraries?add=1'
      window.dispatchEvent(new HashChangeEvent('hashchange'))
      expect(await screen.findByRole('button', { name: 'This computer' })).toBeInTheDocument()
    } finally { location.hash = '' }
  })

  it('a standalone install chooses where and how, and connects an index over ssh', async () => {
    const s = server({ platform: true, ssh: true, canBuild: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    expect(await screen.findByRole('button', { name: 'This computer' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Index a folder' })).toBeInTheDocument()
    expect(screen.getByLabelText('Folder to index')).toBeInTheDocument()
    expect(screen.getByText(/^A folder on this computer, read with the Studio's account, .* Read only\.$/)).toBeInTheDocument()
    // The optional fields wait under More options.
    expect(screen.queryByLabelText('Identifier')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Remote computer' }))
    // Read as the person, never the Studio's account.
    expect(screen.getByTestId('read-as')).toHaveTextContent('Read as you, with your own ssh keys, agent and ~/.ssh/config')
    // No workflow or scheduler on a computer of its own, even with a platform.
    expect(screen.queryByRole('button', { name: 'Workflow job' })).toBeNull()
    expect(document.body.textContent).not.toMatch(/Slurm|\$WORKDIR/)

    // Connect an existing index over ssh: the shared form, and the new library joins the list.
    fireEvent.click(screen.getByRole('button', { name: 'Connect an index' }))
    fireEvent.change(await screen.findByLabelText('Host'), { target: { value: 'me@lab' } })
    fireEvent.change(screen.getByLabelText('Index directory'), { target: { value: '/data/index' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await screen.findByText('Connected to me@lab, a Linux host.')
    fireEvent.click(screen.getByRole('button', { name: 'Add library' }))
    expect(await screen.findByText('index on lab', { selector: '.lib-name' })).toBeInTheDocument()
    expect(posted(s, '/api/user-libraries/ssh')[0].body).toEqual({ destination: 'me@lab', indexRoot: '/data/index', gufiBin: '/usr/local/bin' })
    // The form closes once the library is added.
    expect(screen.queryByLabelText('Host')).toBeNull()
  })

  it('offers pw ssh beside ssh on a computer of its own, and says why it cannot be used yet', async () => {
    const why = 'The Studio does not find the pw CLI on this computer. Install it, or choose where it is under Settings, Model access, pw CLI.'
    const s = server({ platform: true, ssh: true, canBuild: true, pw: false, pwOffered: true, pwWhy: why })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Remote computer' }))
    // The systems over pw ssh come first, and say why they cannot be used yet.
    expect(await screen.findByText(why)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Open Model access' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Test connection' })).toBeNull()
    // A host over ssh is beside them.
    fireEvent.click(screen.getByRole('combobox', { name: 'Remote computer' }))
    const host = await screen.findByRole('option', { name: /^A host over ssh…/ })
    fireEvent.pointerDown(host)
    fireEvent.click(host)
    expect(await screen.findByLabelText('Host')).toBeInTheDocument()
    expect(screen.queryByText(why)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Connect an index' }))
    expect(screen.getByRole('button', { name: 'Test connection' })).toBeInTheDocument()
  })

  it('builds a folder on this computer: check, confirm the folders it makes, then progress in the list', async () => {
    const s = server({ ssh: false, canBuild: true })
    await renderPage(s)
    const success = vi.spyOn((await import('@parallelworks/ui')).toast, 'success')
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    // Building here comes first. Without ssh or pw ssh there is no system,
    // and a copy of a Transfers location is made from Transfers.
    expect(screen.queryByRole('button', { name: 'Remote computer' })).toBeNull()
    fireEvent.change(await screen.findByLabelText('Folder to index'), { target: { value: '~/papers' } })
    // The depth starts at Meaning on documents, with what it costs, the
    // order the phases run in, and the faster stop for a very large folder.
    expect(screen.getByRole('button', { name: 'Meaning on documents' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText(/^Search by meaning is the slowest part of indexing, so it runs last, in the background: names are searchable first, then words\./)).toBeInTheDocument()
    expect(screen.getByText(/For a folder of more than about 500,000 files, Text \+ OCR is faster; meaning can be added later in Settings, Indexing, Libraries\.$/)).toBeInTheDocument()
    // This folder is taken to Text, where the note does not apply.
    fireEvent.click(screen.getByRole('button', { name: 'Text' }))
    expect(screen.queryByText(/slowest part of indexing/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Build the index' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Check' }))
    expect(await screen.findByText('~/papers does not exist yet; it is created, empty, when the build starts.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Build the index' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create this folder?' })
    expect(within(dialog).getByText('~/papers')).toBeInTheDocument()
    expect(posted(s, '/api/libraries/build')).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create it and build' }))
    await waitFor(() => expect(posted(s, '/api/libraries/build')).toHaveLength(1))
    expect(posted(s, '/api/libraries/build')[0].body).toEqual({ sourceRoot: '~/papers', depth: 'text', create: true })
    await waitFor(() => expect(success).toHaveBeenCalledWith(expect.stringMatching(/searchable by name once its folders are indexed; text and meaning, where its depth has them, follow in that order\.$/)))
    const row = await waitFor(() => rowOf('Papers'))
    expect(within(row).getByText('Index of a folder on this machine, kept in step with it')).toBeInTheDocument()
    expect(within(row).getByText('Building')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Rebuild' })).toBeDisabled()
    expect(screen.getByRole('progressbar', { name: /Indexing folders/ })).toBeInTheDocument()
    expect(screen.getByText('120 folders')).toBeInTheDocument()
  })

  it('builds on a remote system over ssh, with the plain note when text extraction cannot run there', async () => {
    const s = server({ ssh: true, canBuild: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Remote computer' }))
    fireEvent.change(await screen.findByLabelText('Host'), { target: { value: 'me@box:2222' } })
    fireEvent.change(screen.getByLabelText('Source folder'), { target: { value: '~/data' } })
    // Built there, the depth starts at Meaning on documents, made with the
    // Studio's model, and the form says what the first build sends.
    expect(screen.getByRole('button', { name: 'Meaning on documents' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText(/^Search by meaning there uses the Studio's embedding model\./)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/no Python 3\.10 or later/i)).toBeInTheDocument()
    expect(posted(s, '/api/user-libraries/ssh/build/test')[0].body).toEqual({ destination: 'me@box:2222', sourceRoot: '~/data' })
    fireEvent.click(screen.getByRole('button', { name: 'Build the index' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Create it and build' }))
    const row = await waitFor(() => rowOf('data on box'))
    expect(posted(s, '/api/user-libraries/ssh/build')[0].body).toEqual({ destination: 'me@box:2222', sourceRoot: '~/data', gufiBin: '/opt/gufi/bin', depth: 'meaning-documents', create: true })
    expect(within(row).getByText('Index built there over ssh')).toBeInTheDocument()
    expect(within(row).getByText(/· Read only \(on me@box\)$/)).toBeInTheDocument()
    expect(within(row).getByTitle('me@box:/home/me/data')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: /Extracting text/ })).toBeInTheDocument()
  })

  it('copies a folder from a Transfers location here and indexes it, for a host that takes SFTP only', async () => {
    const s = server({ ssh: true, canBuild: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Remote computer' }))
    // Add a library points to Transfers, where the copy is made.
    expect(screen.getByRole('link', { name: 'Transfers' })).toHaveAttribute('href', '#view=transfers')
    expect(screen.queryByRole('button', { name: 'Copy here and index it' })).toBeNull()
    const { KeepCopyDialog } = await import('../src/components/KeepCopyDialog')
    const onClose = vi.fn()
    render(<KeepCopyDialog open onClose={onClose} />)
    expect(screen.getByRole('dialog', { name: 'Keep a local copy and index it' })).toBeInTheDocument()
    // Hosts, clusters and remotes are offered; a library is not.
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/transfers/locations')).toBe(true))
    await pickOption('Location', 'Drop', ['Notes'])
    // Browsing starts where the location does, often a whole home folder,
    // which is not copied until it is chosen.
    await screen.findByRole('button', { name: 'upload/' })
    expect(screen.getByRole('button', { name: 'Copy and index' })).toBeDisabled()
    expect(screen.getByText('Open the folder to copy below, or choose this whole folder.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Copy this whole folder' }))
    expect(screen.getByRole('button', { name: 'Copy and index' })).toBeEnabled()
    // A folder opened is chosen.
    fireEvent.click(screen.getByRole('button', { name: 'upload/' }))
    expect(await screen.findByText('Drop/upload')).toBeInTheDocument()
    expect(screen.getByText('No folders in it; 1 file.')).toBeInTheDocument()
    expect(screen.getByText(/takes as much space as the folder holds/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Text' }))
    fireEvent.click(screen.getByRole('button', { name: 'Copy and index' }))
    await waitFor(() => expect(posted(s, '/api/libraries/copy')).toHaveLength(1))
    expect(posted(s, '/api/libraries/copy')[0].body).toEqual({ location: 'saved:drop', path: 'upload', every: 15, depth: 'text' })
    // The dialog closes once the copy starts; the copy is listed with the
    // other libraries.
    await waitFor(() => expect(onClose).toHaveBeenCalled())
  })

  it('a copy library says where it comes from and what the last look found, looks now on Update now, and takes another interval', { timeout: 15_000 }, async () => {
    const s = server({ libs: [PRIMARY, COPY] })
    await renderPage(s)
    const row = await waitFor(() => rowOf('upload'))
    expect(within(row).getByText('Ready')).toBeInTheDocument()
    expect(row.textContent).toMatch(/looked for changes .*last brought 3 files/)
    expect(row.querySelector('a[href="#view=transfers?transfer=abc"]')).not.toBeNull()
    expect(row.textContent).toContain('/data/studio/libraries/upload/files')
    fireEvent.click(within(row).getByRole('button', { name: 'Update now' }))
    await waitFor(() => expect(posted(s, '/api/libraries/upload/update')).toHaveLength(1))
    // The row follows the look until it ends, then shows what it found.
    expect(await within(rowOf('upload')).findByText('Looking for changes')).toBeInTheDocument()
    expect(screen.getByText('Looking for changes at Drop, upload.')).toBeInTheDocument()
    expect(within(rowOf('upload')).getByRole('button', { name: 'Update now' })).toBeDisabled()
    s.libs = s.libs.map(l => (l.id === 'upload' ? { ...l, copyOf: { ...COPY.copyOf, checkedAt: '2026-11-16T12:45:00Z', changedAt: '2026-11-16T12:45:00Z', files: 0, deleted: 2, transfer: 'def' } } : l))
    await waitFor(() => expect(within(rowOf('upload')).getByText('Ready')).toBeInTheDocument(), { timeout: 5000 })
    expect(rowOf('upload').textContent).toMatch(/last brought 0 files and removed 2/)
    await pickOption('How often upload looks for changes', 'Every hour')
    await waitFor(() => expect(s.calls.find(c => c.method === 'PUT' && c.url === '/api/libraries/upload/copy')?.body).toEqual({ every: 60 }))
    // Removing it says the copy goes and the location's files stay.
    await moreAction('upload', 'Remove')
    const ask = await screen.findByRole('alertdialog', { name: 'Remove upload?' })
    expect(ask.textContent).toMatch(/Its copy and its index, which .* keeps in a folder of its own, are deleted\. The files at Drop, upload are not touched\./)
  })

  it('a copy library\'s Copied from is a path like Folder and Index: one line, a leading slash, whole on hover and in Copy (O)', async () => {
    await renderPage(server({ libs: [PRIMARY, COPY] }))
    const row = await waitFor(() => rowOf('upload'))
    const from = within(row).getByLabelText('Drop:/upload')
    expect(from).toHaveAttribute('title', 'Drop:/upload')
    expect(from.querySelector('.lib-path-tail')?.textContent).toBe('/upload')
    expect(within(row).getByRole('button', { name: 'Copy the folder upload is copied from' })).toBeInTheDocument()
    expect(from.closest('dd')?.textContent).not.toContain(',')
  })

  it('a copy library whose last look failed says why and stays Ready', async () => {
    const s = server({ libs: [PRIMARY, { ...COPY, build: { ...COPY.build, dirs: 1, files: 1 }, copyOf: { ...COPY.copyOf, error: 'its location, Drop, upload, is no longer in Transfers' } }] })
    await renderPage(s)
    const row = await waitFor(() => rowOf('upload'))
    expect(within(row).getByText('1 folder, 1 file')).toBeInTheDocument()
    expect(within(row).getByText('Ready; the last pass failed')).toBeInTheDocument()
    expect(row.textContent).toMatch(/the last look failed: its location, Drop, upload, is no longer in Transfers/)
  })

  it('indexes a bucket of an rclone remote from its listing', async () => {
    const s = server({ ssh: true, canBuild: true, rclone: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Bucket' }))
    expect(screen.getByText(/any S3 compatible store, AWS or on premises/)).toBeInTheDocument()
    // Only rclone remotes are offered: not an SFTP host, not a library.
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/transfers/locations')).toBe(true))
    await pickOption('Remote', 'store', ['Drop', 'Notes'])
    fireEvent.click(await screen.findByRole('button', { name: 'upload/' }))
    expect(await screen.findByText('store/upload')).toBeInTheDocument()
    expect(screen.getByText('No folders in it; 1 object.')).toBeInTheDocument()
    // What the listing reads and costs is said before anything is listed.
    expect(screen.getByText(/one fast list, about one request per 1,000 objects/)).toBeInTheDocument()
    expect(screen.getByText(/No object is read to index it/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'List and index' }))
    await waitFor(() => expect(posted(s, '/api/libraries/bucket')).toHaveLength(1))
    expect(posted(s, '/api/libraries/bucket')[0].body).toEqual({ location: 'rclone:store', path: 'upload', every: 1440 })
    const row = await waitFor(() => rowOf('research'))
    expect(within(row).getByText('Index of store, research, from its listing')).toBeInTheDocument()
    expect(within(row).getByText('Listing and indexing')).toBeInTheDocument()
  })

  it('a bucket library says what it holds and what a listing costs, lists again when asked, and takes another interval', async () => {
    const s = server({ libs: [PRIMARY, BUCKET] })
    await renderPage(s)
    const row = await waitFor(() => rowOf('research'))
    expect(within(row).getByText('Ready')).toBeInTheDocument()
    expect(row.textContent).toMatch(/2,500,000 objects, 3(\.0)? TB, listed /)
    expect(row.textContent).toContain('2,500 list requests a listing, about $0.01 at S3 Standard prices, about $9.00 a month at this interval')
    // A bucket stays at names for now: no depth to choose.
    expect(within(row).queryByRole('combobox', { name: /depth/i })).toBeNull()
    fireEvent.click(within(row).getByRole('button', { name: 'List again' }))
    await waitFor(() => expect(posted(s, '/api/libraries/research/rebuild')).toHaveLength(1))
    await pickOption('How often research is listed again', 'Once a day')
    await waitFor(() => expect(s.calls.find(c => c.method === 'PUT' && c.url === '/api/libraries/research/bucket')?.body).toEqual({ every: 1440 }))
    await moreAction('research', 'Remove')
    const ask = await screen.findByRole('alertdialog', { name: 'Remove research?' })
    expect(ask.textContent).toMatch(/Its index, which .* keeps in a folder of its own, is deleted\. The objects in store, research are not touched\./)
  })

  it('a bucket library whose last listing failed says why and stays Ready', async () => {
    const s = server({ libs: [PRIMARY, { ...BUCKET, bucket: { ...BUCKET.bucket, provider: 'Minio', error: 'rclone: directory not found' } }] })
    await renderPage(s)
    const row = await waitFor(() => rowOf('research'))
    expect(within(row).getByText('Ready; the last listing failed')).toBeInTheDocument()
    expect(row.textContent).toContain('the last listing failed: rclone: directory not found')
    expect(row.textContent).toContain("2,500 list requests a listing; Minio's own request prices apply, and a store on premises has none")
  })

  it('removing a built library whose index the Studio keeps is red and asks as an alert', async () => {
    const s = server({ libs: [PRIMARY, { ...BUILT_HERE, build: { ...BUILT_HERE.build, managed: true, state: 'ready', files: 40, dirs: 6, builtAt: '2026-10-20T00:00:00Z', progress: null } }] })
    await renderPage(s)
    const row = await waitFor(() => rowOf('Papers'))
    expect(row).toBeInTheDocument()
    const remove = await moreAction('Papers', 'Remove')
    expect(remove.className).toMatch(/error/)
    const ask = await screen.findByRole('alertdialog', { name: 'Remove Papers?' })
    expect(ask.textContent).toMatch(/Its index, which .* keeps in a folder of its own, is deleted\. The files in \/home\/u\/papers are not touched/)
    fireEvent.click(within(ask).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(s.calls.some(c => c.method === 'DELETE')).toBe(false)
  })

  it('a built library rebuilds from its row', async () => {
    const s = server({ libs: [PRIMARY, { ...BUILT_HERE, build: { ...BUILT_HERE.build, state: 'ready', files: 40, dirs: 6, builtAt: '2026-10-20T00:00:00Z', progress: null } }] })
    await renderPage(s)
    const row = await waitFor(() => rowOf('Papers'))
    expect(within(row).getByText('Ready')).toBeInTheDocument()
    expect(within(row).getByText('6 folders, 40 files')).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Rebuild' }))
    await waitFor(() => expect(posted(s, '/api/libraries/papers/rebuild')).toHaveLength(1))
  })

  it('sets the depth per library the Studio builds; an index it only reads shows what it has', async () => {
    const s = server({ libs: [PRIMARY, { ...BUILT_HERE, depth: 'ocr', build: { ...BUILT_HERE.build, state: 'ready', files: 40, dirs: 6, builtAt: '2026-10-20T00:00:00Z', progress: null } }, SITE] })
    await renderPage(s)
    const success = vi.spyOn((await import('@parallelworks/ui')).toast, 'success')
    const row = await waitFor(() => rowOf('Papers'))
    const select = within(row).getByRole('combobox', { name: 'Index depth of Papers' })
    expect(select).toHaveTextContent('Text + OCR')
    fireEvent.click(select)
    const options = await screen.findAllByRole('option')
    expect(options.map(o => o.textContent)).toEqual(['Names', 'Text', 'Text + OCR', 'Meaning on documents', 'Meaning on all files'])
    const all = options.find(o => o.textContent === 'Meaning on all files')!
    fireEvent.pointerDown(all)
    fireEvent.click(all)
    await waitFor(() => expect(s.calls.find(c => c.method === 'PUT' && c.url === '/api/libraries/papers/depth')?.body).toEqual({ depth: 'meaning-full' }))
    await waitFor(() => expect(within(rowOf('Papers')).getByRole('combobox', { name: 'Index depth of Papers' })).toHaveTextContent('Meaning on all files'))
    // Built here, the sync applies it: no rebuild is asked for.
    await waitFor(() => expect(success).toHaveBeenCalledWith('Papers: Meaning on all files. It is applied in the background now.'))
    expect(posted(s, '/api/libraries/papers/rebuild')).toHaveLength(0)
    expect(screen.queryByRole('button', { name: 'Rebuild now' })).toBeNull()
    expect(within(rowOf('Papers')).queryByText(/^Depth changed/)).toBeNull()
    expect(within(rowOf('Site index')).queryByRole('combobox')).toBeNull()
    expect(within(rowOf('Site index')).getByText('as built')).toBeInTheDocument()
    expect(screen.getByText(/Depth is how far an index goes/)).toBeInTheDocument()
  })

  describe('a new depth on a library built on a remote system', () => {
    const OVER_SSH = {
      ...BUILT_REMOTE, indexRoot: '/home/me/.activate-studio/indexes/data-1a2b3c4d/data', state: 'ready', directories: 6, files: 40, builtAt: '2026-10-20T00:00:00Z', progress: null,
      depth: 'ocr', builtDepth: 'ocr', buildingDepth: null,
    }
    const OVER_PW = { ...OVER_SSH, id: 'u-c1-data', label: 'data on c1', transport: 'pw', site: 'c1', destination: null, port: null }
    const pick = async (name: string, stop: string) => {
      fireEvent.click(within(rowOf(name)).getByRole('combobox', { name: `Index depth of ${name}` }))
      const option = (await screen.findAllByRole('option')).find(o => o.textContent === stop)!
      fireEvent.pointerDown(option)
      fireEvent.click(option)
    }
    const open = async (lib: Record<string, unknown>) => {
      const s = server({ libs: [PRIMARY], user: [lib] })
      await renderPage(s)
      await waitFor(() => rowOf(String(lib.label)))
      return s
    }

    it.each([['ssh', OVER_SSH], ['pw ssh', OVER_PW]])('over %s it offers to rebuild, says what that does, and Rebuild now starts it', async (_over, lib) => {
      const s = await open(lib)
      await pick(lib.label, 'Meaning on all files')
      await screen.findByText('Rebuild at Meaning on all files?')
      const choice = detailOf(lib.label)
      expect(choice).toHaveTextContent('The current index keeps answering searches until the new one replaces it.')
      expect(choice).toHaveTextContent('about three times as long as on documents')
      expect(posted(s, `/api/user-libraries/${lib.id}/refresh`)).toHaveLength(0)
      s.user = [{ ...s.user[0], state: 'updating', buildingDepth: 'meaning-full', progress: { phase: 'Indexing folders', done: 0, total: 0, current: null } }]
      fireEvent.click(within(choice).getByRole('button', { name: 'Rebuild now' }))
      await waitFor(() => expect(posted(s, `/api/user-libraries/${lib.id}/refresh`)).toHaveLength(1))
      await waitFor(() => expect(screen.queryByText('Rebuild at Meaning on all files?')).toBeNull())
      // The row says so until that build finishes, and then stops.
      expect(within(rowOf(lib.label)).getByText('Depth changed, applies when this build finishes')).toBeInTheDocument()
      s.user = [{ ...s.user[0], state: 'ready', builtDepth: 'meaning-full', buildingDepth: null, progress: null }]
      await waitFor(() => expect(within(rowOf(lib.label)).queryByText(/^Depth changed/)).toBeNull(), { timeout: 4000 })
    })

    it('Later leaves a note on the row, which is there when the page is opened again, until a build at that depth finishes', async () => {
      const s = await open(OVER_SSH)
      await pick('data on box', 'Meaning on documents')
      fireEvent.click(await screen.findByRole('button', { name: 'Later' }))
      await waitFor(() => expect(screen.queryByRole('button', { name: 'Rebuild now' })).toBeNull())
      expect(posted(s, `/api/user-libraries/${OVER_SSH.id}/refresh`)).toHaveLength(0)
      expect(within(rowOf('data on box')).getByText('Depth changed, Rebuild to apply')).toBeInTheDocument()

      cleanup()
      await renderPage(s)
      await waitFor(() => expect(within(rowOf('data on box')).getByText('Depth changed, Rebuild to apply')).toBeInTheDocument())
      expect(screen.queryByRole('button', { name: 'Rebuild now' })).toBeNull()

      cleanup()
      s.user = [{ ...s.user[0], builtDepth: 'meaning-documents' }]
      await renderPage(s)
      await waitFor(() => rowOf('data on box'))
      expect(within(rowOf('data on box')).queryByText(/^Depth changed/)).toBeNull()
    })

    it('the row\'s own Rebuild answers the choice too', async () => {
      const s = await open(OVER_SSH)
      await pick('data on box', 'Meaning on documents')
      await screen.findByRole('button', { name: 'Rebuild now' })
      fireEvent.click(within(rowOf('data on box')).getByRole('button', { name: 'Rebuild' }))
      await waitFor(() => expect(posted(s, `/api/user-libraries/${OVER_SSH.id}/refresh`)).toHaveLength(1))
      await waitFor(() => expect(screen.queryByRole('button', { name: 'Rebuild now' })).toBeNull())
    })

    it('after a failed rebuild the note stays', async () => {
      await open({ ...OVER_SSH, depth: 'meaning-full', error: 'gufi_dir2index failed' })
      const row = rowOf('data on box')
      expect(within(row).getByText('Ready; the last build failed')).toBeInTheDocument()
      expect(within(row).getByText('Depth changed, Rebuild to apply')).toBeInTheDocument()
    })

    it('with a build running, it says the change waits for the next one and offers no rebuild', async () => {
      const s = await open({ ...OVER_SSH, state: 'updating', buildingDepth: 'ocr', progress: { phase: 'Extracting text', done: 0, total: 0, current: null } })
      await pick('data on box', 'Meaning on documents')
      await waitFor(() => expect(screen.getByText('Meaning on documents applies to the next build')).toBeInTheDocument())
      expect(detailOf('data on box')).toHaveTextContent('A build is running on')
      expect(screen.queryByRole('button', { name: 'Rebuild now' })).toBeNull()
      expect(within(rowOf('data on box')).getByText('Depth changed, applies at the next Rebuild')).toBeInTheDocument()
      expect(posted(s, `/api/user-libraries/${OVER_SSH.id}/refresh`)).toHaveLength(0)
    })

    it('a lower depth is offered too, without saying search needs it', async () => {
      await open({ ...OVER_SSH, depth: 'meaning-full', builtDepth: 'meaning-full' })
      await pick('data on box', 'Text')
      await screen.findByRole('button', { name: 'Rebuild now' })
      const choice = detailOf('data on box')
      expect(within(choice).getByRole('button', { name: 'Later' })).toBeInTheDocument()
      expect(choice).toHaveTextContent('Search keeps working without a rebuild.')
      expect(choice).not.toHaveTextContent('keeps answering searches until')
    })

    it('a library with no recorded depth is offered a rebuild, and shows no note', async () => {
      await open({ ...OVER_SSH, builtDepth: null })
      expect(within(rowOf('data on box')).queryByText(/^Depth changed/)).toBeNull()
      await pick('data on box', 'Meaning on documents')
      fireEvent.click(await screen.findByRole('button', { name: 'Later' }))
      await waitFor(() => expect(screen.queryByRole('button', { name: 'Rebuild now' })).toBeNull())
      expect(within(rowOf('data on box')).queryByText(/^Depth changed/)).toBeNull()
    })

    it('back at the depth its index has, there is nothing to rebuild', async () => {
      await open({ ...OVER_SSH, depth: 'meaning-documents' })
      expect(within(rowOf('data on box')).getByText('Depth changed, Rebuild to apply')).toBeInTheDocument()
      await pick('data on box', 'Text + OCR')
      await waitFor(() => expect(within(rowOf('data on box')).queryByText(/^Depth changed/)).toBeNull())
      expect(screen.queryByRole('button', { name: 'Rebuild now' })).toBeNull()
    })
  })

  it('a hosted Studio with a platform builds on a cluster through the workflow', async () => {
    const s = server({ platform: true, ssh: false, workflow: true, standalone: false })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    // A system is the one place offered with sign-in on, so there is no
    // Where to choose; only the workflow reaches it here, and it only builds.
    await screen.findByLabelText('Directory')
    expect(screen.queryByRole('button', { name: 'This computer' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Connect an index' })).toBeNull()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Build the index' })).toBeEnabled())
    expect(screen.getByLabelText('Directory')).toHaveValue('$WORKDIR')
    fireEvent.click(screen.getByRole('button', { name: 'Build the index' }))
    expect(await screen.findByText('c1 $WORKDIR', { selector: '.lib-name' })).toBeInTheDocument()
    expect(posted(s, '/api/user-libraries')[0].body).toEqual({ site: 'c1', sourceDir: '$WORKDIR', scheduler: null })
  })

  it('adds a GUFI index on this machine, and shows what the server refused', async () => {
    const s = server({ ssh: false, fail: { 'POST /api/libraries': '/nope does not look like a GUFI index (no db.db at its root)' } })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    // One kind only: the button opens its form directly.
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    const add = screen.getByRole('button', { name: 'Add library' })
    expect(add).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Index directory'), { target: { value: '/nope' } })
    fireEvent.click(add)
    expect(await screen.findByText('/nope does not look like a GUFI index (no db.db at its root)')).toBeInTheDocument()

    s.fail = {}
    fireEvent.change(screen.getByLabelText('Index directory'), { target: { value: '/data/lab' } })
    fireEvent.click(screen.getByRole('button', { name: 'More options' }))
    fireEvent.change(screen.getByLabelText('Label'), { target: { value: 'Lab' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add library' }))
    expect(await screen.findByText('Lab', { selector: '.lib-name' })).toBeInTheDocument()
    expect(posted(s, '/api/libraries').at(-1)?.body).toEqual({ id: '', label: 'Lab', indexRoot: '/data/lab' })
  })

  it('changes the main folder through a restart and lists the new one', async () => {
    const s = server()
    await renderPage(s)
    fireEvent.click(await screen.findByRole('button', { name: 'Change folder' }))
    const dialog = await screen.findByRole('dialog', { name: 'Change the main folder' })
    expect(await within(dialog).findByText('No folders inside')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Use this folder' })).toBeDisabled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Up' }))
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Research' }))
    await waitFor(() => expect(within(dialog).getByRole('textbox', { name: 'Folder' })).toHaveValue('/home/u/Research'))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Use this folder' }))
    expect(await within(dialog).findByText('The Studio restarted and now uses /home/u/Research.')).toBeInTheDocument()
    expect(posted(s, '/api/setup/primary-folder')[0].body).toEqual({ path: '/home/u/Research' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
    const main = await waitFor(() => rowOf('Research'))
    expect(within(main).getByText('Not indexed yet')).toBeInTheDocument()
    expect(within(main).getByRole('button', { name: 'Build index' })).toBeInTheDocument()
  })

  it('updates the main folder\'s index, showing each phase with its count, then how long it took', async () => {
    const s = server()
    await renderPage(s)
    fireEvent.click(await screen.findByRole('button', { name: 'Update index' }))
    expect(await screen.findByText('4 of 9 items')).toBeInTheDocument()
    expect(screen.getByText('3 of 3 folders')).toBeInTheDocument()
    expect(screen.getByText('2 of 2 documents · 2 converted')).toBeInTheDocument()
    expect(screen.getAllByRole('progressbar').length).toBeGreaterThan(0)
    expect(within(rowOf('Notes')).getByText('Indexing')).toBeInTheDocument()
    expect(posted(s, '/api/index/build')).toHaveLength(1)
    s.build = 'done'
    expect(await screen.findByText(/Updated the index in 2\.5 s \(names 40 ms, documents 900 ms, text 1\.2 s, meaning 400 ms\), on 8 threads\./)).toBeInTheDocument()
    await waitFor(() => expect(within(rowOf('Notes')).getByText('Ready')).toBeInTheDocument())
  })

  it('stops a running build, and says the index is incomplete', async () => {
    const s = server({ build: 'running' })
    await renderPage(s)
    expect(await within(await waitFor(() => rowOf('Notes'))).findByText('Indexing')).toBeInTheDocument()
    // The row's own Stop, and the one beside the progress, both stop it.
    fireEvent.click(within(rowOf('Notes')).getByRole('button', { name: 'Stop indexing' }))
    await waitFor(() => expect(posted(s, '/api/index/cancel')).toHaveLength(1))
    expect(await within(rowOf('Notes')).findByText('Incomplete')).toBeInTheDocument()
    expect(screen.getByText(/stopped after 2\.5 s/)).toBeInTheDocument()
    expect(within(rowOf('Notes')).getByRole('button', { name: 'Update index' })).toBeInTheDocument()
  })

  it('deletes the index only after a confirmation dialog, from a red button, and never the files', async () => {
    const s = server()
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    await within(rowOf('Notes')).findByText('12 files')
    const del = await moreAction('Notes', 'Delete index')
    expect(del.className).toMatch(/error/)
    let dialog = await screen.findByRole('alertdialog', { name: 'Delete the index?' })
    expect(dialog.textContent).toMatch(/The files in the folder are not touched/)
    // Keeping it sends nothing.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Keep it' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(s.calls.some(c => c.method === 'DELETE')).toBe(false)
    await moreAction('Notes', 'Delete index')
    dialog = await screen.findByRole('alertdialog', { name: 'Delete the index?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete index' }))
    await waitFor(() => expect(s.calls.filter(c => c.method === 'DELETE').map(c => c.url)).toEqual(['/api/index?confirm=1']))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(await within(rowOf('Notes')).findByText('Not indexed yet')).toBeInTheDocument()
    expect(within(rowOf('Notes')).getByRole('button', { name: 'Build index' })).toBeInTheDocument()
  })

  it('shows the index log from the main folder\'s row', async () => {
    const s = server()
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(await within(detailOf('Notes')).findByRole('button', { name: 'View log' }))
    const dialog = await screen.findByRole('dialog', { name: 'Index log' })
    expect(await within(dialog).findByText(/Updating the index: started on 8 threads/)).toBeInTheDocument()
    expect(within(dialog).getByText('/data/logs/indexing.log')).toBeInTheDocument()
    expect(within(dialog).getByRole('link', { name: 'Open the full log' })).toHaveAttribute('href', '/api/index/log?full=1')
  })

  it('where users sign in, the main folder is listed without folder actions', async () => {
    const s = server({ standalone: false, ssh: false })
    await renderPage(s)
    const main = await waitFor(() => rowOf('Notes'))
    expect(within(main).getByText('Ready')).toBeInTheDocument()
    expect(within(main).queryByRole('button', { name: 'Change folder' })).toBeNull()
    expect(within(main).queryByRole('button', { name: /^(Build|Update|Delete) index/ })).toBeNull()
    // The log is there to read on any Studio, under the row.
    expect(await within(detailOf('Notes')).findByRole('button', { name: 'View log' })).toBeInTheDocument()
    expect(within(main).queryByRole('button', { name: /Delete index|More actions/ })).toBeNull()
    expect(s.calls.some(c => c.url.startsWith('/api/setup/browse'))).toBe(false)
  })
})
