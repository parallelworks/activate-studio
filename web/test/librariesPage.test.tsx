import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { libraryKinds } from '../src/components/LibrariesSection'

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

interface Server {
  platform: boolean
  standalone: boolean
  ssh: boolean
  libs: Record<string, unknown>[]
  user: Record<string, unknown>[]
  primary: string
  indexed: boolean
  startedAt: string
  jobDone: boolean
  fail?: Record<string, string>
  calls: { method: string; url: string; body: unknown }[]
}

const server = (over: Partial<Server> = {}): Server => ({
  platform: false, standalone: true, ssh: true, libs: [PRIMARY, SITE], user: [], primary: '/home/u/Notes', indexed: true,
  startedAt: 'first start', jobDone: false, calls: [], ...over,
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
    if (path === '/api/libraries' && method === 'GET') return reply({ libraries: s.libs.map(l => (l.primary ? { ...l, sourceRoot: s.primary, label: s.primary.split('/').pop() } : l)) })
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
    if (path === '/api/user-libraries' && method === 'GET') return reply({ libraries: s.user, available: true, ssh: s.ssh })
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
  const { LibrariesSection } = await import('../src/components/LibrariesSection')
  return render(<LibrariesSection pollMs={5} />)
}

const rowOf = (name: string) => screen.getByText(name, { selector: '.lib-name' }).closest('tr') as HTMLElement
const posted = (s: Server, url: string) => s.calls.filter(c => c.method === 'POST' && c.url.split('?')[0] === url)

describe('the kinds of library an install offers', () => {
  it('always an index on this machine, ssh where allowed, and a cluster only with a platform', () => {
    expect(libraryKinds({ ssh: false, platform: false, userLibraries: true })).toEqual(['local'])
    expect(libraryKinds({ ssh: true, platform: false, userLibraries: true })).toEqual(['local', 'ssh'])
    expect(libraryKinds({ ssh: true, platform: true, userLibraries: true })).toEqual(['local', 'ssh', 'cluster'])
    expect(libraryKinds({ ssh: false, platform: true, userLibraries: false })).toEqual(['local'])
    // An existing index on a cluster, where the pw CLI is here and signed in.
    expect(libraryKinds({ ssh: true, platform: true, userLibraries: true, pw: true })).toEqual(['local', 'ssh', 'pw', 'cluster'])
    expect(libraryKinds({ ssh: true, platform: false, userLibraries: true, pw: true })).toEqual(['local', 'ssh', 'pw'])
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
    expect(within(main).getByRole('button', { name: 'Rebuild index' })).toBeEnabled()
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

  it('a standalone install offers only the kinds it supports, in plain words', async () => {
    const s = server({ platform: false, ssh: true })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    expect(await screen.findByRole('button', { name: /A GUFI index on this machine/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /A GUFI index on another machine over ssh/ })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /An index built on a cluster/ })).toBeNull()
    expect(document.body.textContent).not.toMatch(/platform|Slurm|\$WORKDIR|cluster/i)
    expect(s.calls.some(c => c.url.startsWith('/api/agent-sites'))).toBe(false)

    // Over ssh: the shared form, and the new library joins the list.
    fireEvent.click(screen.getByRole('button', { name: /A GUFI index on another machine over ssh/ }))
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

  it('with a platform, offers an index built on a cluster and starts the build', async () => {
    const s = server({ platform: true, ssh: false })
    await renderPage(s)
    await waitFor(() => rowOf('Notes'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a library' }))
    expect(screen.queryByRole('button', { name: /over ssh/ })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: /An index built on a cluster/ }))
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

  it('rebuilds the main folder\'s index with its progress', async () => {
    const s = server()
    await renderPage(s)
    fireEvent.click(await screen.findByRole('button', { name: 'Rebuild index' }))
    expect(await screen.findByText('papers (1 of 3)')).toBeInTheDocument()
    expect(screen.getByRole('progressbar')).toBeInTheDocument()
    expect(within(rowOf('Notes')).getByText('Indexing')).toBeInTheDocument()
    s.jobDone = true
    expect(await screen.findByText('Index built in 3 s.')).toBeInTheDocument()
    await waitFor(() => expect(within(rowOf('Notes')).getByText('Indexed')).toBeInTheDocument())
  })

  it('where users sign in, the main folder is listed without folder actions', async () => {
    const s = server({ standalone: false, ssh: false })
    await renderPage(s)
    const main = await waitFor(() => rowOf('Notes'))
    expect(within(main).getByText('Indexed')).toBeInTheDocument()
    expect(within(main).queryByRole('button', { name: 'Change folder' })).toBeNull()
    expect(within(main).queryByRole('button', { name: /index/ })).toBeNull()
    expect(s.calls.some(c => c.url.startsWith('/api/setup/browse'))).toBe(false)
  })
})
