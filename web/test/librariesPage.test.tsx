import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { addChoices } from '../src/components/AddLibrary'

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
  workflow?: boolean
  canBuild?: boolean
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
    if (path === '/api/libraries' && method === 'GET') return reply({ canBuild: !!s.canBuild, libraries: s.libs.map(l => (l.primary ? { ...l, sourceRoot: s.primary, label: s.primary.split('/').pop() } : l)) })
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
    if (path === '/api/user-libraries' && method === 'GET') return reply({ libraries: s.user, available: true, ssh: s.ssh, pw: !!s.pw, workflow: !!s.workflow })
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

const rowOf = (name: string) => screen.getByText(name, { selector: '.lib-name' }).closest('tr') as HTMLElement
/** The line under a library's row: a build's progress, the index's other controls. */
const detailOf = (name: string) => rowOf(name).nextElementSibling as HTMLElement
const posted = (s: Server, url: string) => s.calls.filter(c => c.method === 'POST' && c.url.split('?')[0] === url)

describe('what Add a library offers', () => {
  it('this computer always; building here, a remote system, and the workflow only where offered', () => {
    const none = addChoices({ buildHere: false, ssh: false, pw: false, workflow: false })
    expect(none.where).toEqual(['here'])
    expect(none.how('here')).toEqual(['connect'])
    // A computer of its own: build here, and a remote system over ssh or pw ssh, never the workflow.
    const mine = addChoices({ buildHere: true, ssh: true, pw: true, workflow: false })
    expect(mine.where).toEqual(['here', 'remote'])
    expect(mine.how('here')).toEqual(['build', 'connect'])
    expect(mine.how('remote')).toEqual(['build', 'connect'])
    expect(mine.via('remote', 'build')).toEqual(['ssh', 'pw'])
    // A hosted Studio: no ssh, and the workflow builds on a cluster.
    const hosted = addChoices({ buildHere: false, ssh: false, pw: true, workflow: true })
    expect(hosted.how('here')).toEqual(['connect'])
    expect(hosted.via('remote', 'build')).toEqual(['pw', 'workflow'])
    expect(hosted.via('remote', 'connect')).toEqual(['pw'])
    const workflowOnly = addChoices({ buildHere: false, ssh: false, pw: false, workflow: true })
    expect(workflowOnly.how('remote')).toEqual(['build'])
  })
})

describe('Settings, Libraries', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('lists every library in one table: kind, where it is, state, what it searches, and its actions', async () => {
    const s = server({ platform: true, libs: [PRIMARY, SITE, PINNED], user: [SSH, CLUSTER] })
    await renderPage(s)

    const main = await waitFor(() => rowOf('Notes'))
    expect(within(main).getByText('Main folder, read and write')).toBeInTheDocument()
    expect(within(main).getByTitle('/home/u/Notes')).toBeInTheDocument()
    expect(await within(main).findByText('12 files')).toBeInTheDocument()
    expect(within(main).getByText('Indexed')).toBeInTheDocument()
    expect(within(main).getByRole('button', { name: 'Change folder' })).toBeInTheDocument()
    expect(await within(main).findByRole('button', { name: 'Update index' })).toBeEnabled()
    expect(within(detailOf('Notes')).getByRole('button', { name: 'Delete index' })).toBeInTheDocument()
    expect(within(detailOf('Notes')).getByRole('button', { name: 'View log' })).toBeInTheDocument()
    expect(within(main).getAllByText('Yes')).toHaveLength(2)

    const site = rowOf('Site index')
    expect(within(site).getByText('GUFI index on this machine, read only')).toBeInTheDocument()
    expect(within(site).getByText('not on this machine: names and metadata only')).toBeInTheDocument()
    expect(within(site).getByText('Ready')).toBeInTheDocument()
    expect(within(site).getByText('Yes')).toBeInTheDocument()
    expect(within(site).getByText('No')).toBeInTheDocument()

    const pinned = rowOf('Scratch')
    expect(within(pinned).getByText('GUFI index on this machine, read only, set by the deployment')).toBeInTheDocument()
    expect(within(pinned).getByText('Index not found')).toBeInTheDocument()
    expect(within(pinned).queryByRole('button', { name: /Remove/ })).toBeNull()

    const ssh = rowOf('index on box')
    expect(within(ssh).getByText('GUFI index over ssh, read only')).toBeInTheDocument()
    expect(within(ssh).getByText('me@box:/data/index')).toBeInTheDocument()
    expect(within(ssh).getByText('2222')).toBeInTheDocument()
    expect(within(ssh).getByText('none: names and metadata only')).toBeInTheDocument()
    expect(within(ssh).getByText('12 folders, 40 files')).toBeInTheDocument()
    expect(within(ssh).getByRole('button', { name: 'Check' })).toBeInTheDocument()

    const cluster = rowOf('c1 $WORKDIR')
    expect(within(cluster).getByText('Index built on a cluster, read only, yours alone')).toBeInTheDocument()
    expect(within(cluster).getByText('Building')).toBeInTheDocument()
    expect(within(cluster).getByText('run run-17')).toBeInTheDocument()
    expect(within(cluster).getByText('being built')).toBeInTheDocument()
    expect(within(cluster).getByRole('button', { name: 'Refresh' })).toBeDisabled()

    // Check reruns the ssh connection test.
    fireEvent.click(within(ssh).getByRole('button', { name: 'Check' }))
    await waitFor(() => expect(posted(s, `/api/user-libraries/${SSH.id}/refresh`)).toHaveLength(1))
    // Remove takes an added library out; a pinned one has no Remove.
    fireEvent.click(within(site).getByRole('button', { name: 'Remove Site index' }))
    await waitFor(() => expect(screen.queryByText('Site index', { selector: '.lib-name' })).toBeNull())
    expect(s.calls.some(c => c.method === 'DELETE' && c.url === '/api/libraries/site')).toBe(true)
  })

  it('a standalone install chooses where and how, and connects an index over ssh', async () => {
    const s = server({ platform: true, ssh: true, canBuild: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    expect(await screen.findByRole('button', { name: 'This computer' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Build an index of a folder' })).toBeInTheDocument()
    expect(screen.getByLabelText('Source folder')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'A remote system' }))
    // No workflow or scheduler on a computer of its own, even with a platform.
    expect(screen.queryByRole('button', { name: 'Platform workflow' })).toBeNull()
    expect(document.body.textContent).not.toMatch(/Slurm|\$WORKDIR/)

    // Connect an existing index over ssh: the shared form, and the new library joins the list.
    fireEvent.click(screen.getByRole('button', { name: 'Connect an existing index' }))
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

  it('builds a folder on this computer: check, confirm the folders it makes, then progress in the list', async () => {
    const s = server({ ssh: false, canBuild: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    // Only this computer here: no Where choice, and build comes first.
    expect(screen.queryByRole('button', { name: 'A remote system' })).toBeNull()
    fireEvent.change(await screen.findByLabelText('Source folder'), { target: { value: '~/papers' } })
    expect(screen.getByRole('button', { name: 'Build the index' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Check' }))
    expect(await screen.findByText('~/papers does not exist yet; it is created, empty, when the build starts.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Build the index' }))
    const dialog = await screen.findByRole('dialog', { name: 'Create this folder?' })
    expect(within(dialog).getByText('~/papers')).toBeInTheDocument()
    expect(posted(s, '/api/libraries/build')).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create it and build' }))
    await waitFor(() => expect(posted(s, '/api/libraries/build')).toHaveLength(1))
    expect(posted(s, '/api/libraries/build')[0].body).toEqual({ sourceRoot: '~/papers', create: true })
    const row = await waitFor(() => rowOf('Papers'))
    expect(within(row).getByText('Index of a folder on this machine, kept in step with it, read only')).toBeInTheDocument()
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
    fireEvent.click(await screen.findByRole('button', { name: 'A remote system' }))
    fireEvent.change(await screen.findByLabelText('Host'), { target: { value: 'me@box:2222' } })
    fireEvent.change(screen.getByLabelText('Source folder'), { target: { value: '~/data' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/no Python 3\.10 or later/i)).toBeInTheDocument()
    expect(posted(s, '/api/user-libraries/ssh/build/test')[0].body).toEqual({ destination: 'me@box:2222', sourceRoot: '~/data' })
    fireEvent.click(screen.getByRole('button', { name: 'Build the index' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Create it and build' }))
    const row = await waitFor(() => rowOf('data on box'))
    expect(posted(s, '/api/user-libraries/ssh/build')[0].body).toEqual({ destination: 'me@box:2222', sourceRoot: '~/data', gufiBin: '/opt/gufi/bin', create: true })
    expect(within(row).getByText('Index built on me@box over ssh, read only')).toBeInTheDocument()
    expect(within(row).getByTitle('me@box:/home/me/data')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: /Extracting text/ })).toBeInTheDocument()
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

  it('a hosted Studio with a platform builds on a cluster through the workflow', async () => {
    const s = server({ platform: true, ssh: false, workflow: true, standalone: false })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    fireEvent.click(await screen.findByRole('button', { name: 'A remote system' }))
    // Only the workflow reaches a remote system here, and it only builds.
    expect(screen.queryByRole('button', { name: 'Connect an existing index' })).toBeNull()
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
    await waitFor(() => expect(within(rowOf('Notes')).getByText('Indexed')).toBeInTheDocument())
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

  it('deletes the index only after an inline confirmation, and never the files', async () => {
    const s = server()
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(await within(detailOf('Notes')).findByRole('button', { name: 'Delete index' }))
    const confirm = within(detailOf('Notes')).getByRole('group', { name: 'Delete the index' })
    expect(confirm.textContent).toMatch(/The files in the folder are not touched/)
    // Keeping it sends nothing.
    fireEvent.click(within(confirm).getByRole('button', { name: 'Keep it' }))
    expect(s.calls.some(c => c.method === 'DELETE')).toBe(false)
    fireEvent.click(within(detailOf('Notes')).getByRole('button', { name: 'Delete index' }))
    fireEvent.click(within(detailOf('Notes')).getByRole('button', { name: 'Delete the index' }))
    await waitFor(() => expect(s.calls.filter(c => c.method === 'DELETE').map(c => c.url)).toEqual(['/api/index?confirm=1']))
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
    expect(within(main).getByText('Indexed')).toBeInTheDocument()
    expect(within(main).queryByRole('button', { name: 'Change folder' })).toBeNull()
    expect(within(main).queryByRole('button', { name: /index/ })).toBeNull()
    // The log is there to read on any Studio, under the row.
    expect(await within(detailOf('Notes')).findByRole('button', { name: 'View log' })).toBeInTheDocument()
    expect(within(detailOf('Notes')).queryByRole('button', { name: 'Delete index' })).toBeNull()
    expect(s.calls.some(c => c.url.startsWith('/api/setup/browse'))).toBe(false)
  })
})
