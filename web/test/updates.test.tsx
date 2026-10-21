import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { UpdatesStatus } from '../src/api'
import { StatusFooter } from '../src/components/StatusFooter'
import { CLONE_UPDATE_COMMAND, UpdatesSection } from '../src/components/UpdatesSection'

// A switch forwards its press to its hidden input as a PointerEvent, which
// jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const NOTES = '## Changes\n\n- Search is faster on large libraries.\n- Fixed the folder picker.'

function status(over: Partial<UpdatesStatus> = {}): UpdatesStatus {
  return {
    mode: 'app',
    current: { version: 'v1.140', builtAt: '2026-10-01T12:00:00Z' },
    policy: { check: true, install: 'manual', channel: 'stable' },
    latest: { version: 'v1.141', publishedAt: '2026-10-20T12:00:00Z', notes: NOTES, url: 'https://example.org/releases/v1.141' },
    available: true,
    canInstall: true,
    reason: null,
    state: 'idle',
    progress: null,
    error: null,
    checkedAt: '2026-10-21T08:00:00Z',
    previous: null,
    ...over,
  }
}

interface Server {
  updates: UpdatesStatus | null
  /** Status code for GET /api/updates; 0 drops the connection. */
  code: number
  version: string
  calls: { method: string; url: string; body: unknown }[]
  onPost?: (url: string, s: Server) => void
}

const reply = (body: unknown, code = 200) => new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } })

function stubServer(s: Server) {
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    s.calls.push({ method, url, body })
    if (url === '/api/updates') {
      if (s.code === 0) throw new TypeError('Failed to fetch')
      return s.code === 200 ? reply(s.updates) : reply({ error: 'not available here' }, s.code)
    }
    if (url.startsWith('/api/updates/') && method === 'POST') {
      s.onPost?.(url, s)
      return reply(s.updates)
    }
    if (url === '/api/settings' && method === 'PUT') {
      if (s.updates) s.updates = { ...s.updates, policy: (body as { updates: UpdatesStatus['policy'] }).updates }
      return reply({})
    }
    if (url === '/api/version') return reply({ version: s.version, commit: s.version, builtAt: null, startedAt: '2026-10-21T08:00:00Z', source: 'bundle' })
    if (url.startsWith('/api/kb/stats')) return reply({ available: true, files: 3, dirs: 1, totalBytes: 10 })
    if (url.startsWith('/api/index/status')) return reply({ running: false, lastSweepAt: null, lastSweepMs: null, lastChanges: [], lastError: null, sweepIntervalSec: 60 })
    if (url === '/api/whoami') return reply({ authEnabled: false, header: '', verified: false, user: null, token: null, claims: null })
    if (url.startsWith('/healthz')) return reply({ ok: true, gufi: true })
    return reply({ error: 'not found' }, 404)
  }))
}

const server = (over: Partial<Server> = {}): Server => ({ updates: status(), code: 200, version: 'v1.140', calls: [], ...over })

describe('the Updates section', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  it('shows an available update with its notes rendered from markdown, and Update now', async () => {
    stubServer(server())
    render(<UpdatesSection />)
    expect(await screen.findByText('Version v1.141 is available')).toBeInTheDocument()
    expect(screen.getByText(/^Version v1\.140/)).toBeInTheDocument()
    expect(await screen.findByRole('heading', { name: 'Changes' })).toBeInTheDocument()
    expect(screen.getByText('Search is faster on large libraries.').closest('li')).not.toBeNull()
    expect(screen.getByRole('link', { name: 'Release page' })).toHaveAttribute('href', 'https://example.org/releases/v1.141')
    expect(screen.getByRole('button', { name: 'Update now' })).toBeEnabled()
    expect(screen.queryByRole('button', { name: /^Roll back/ })).toBeNull()
  })

  it('installs: download progress, then restarting, then reloads once the new version answers', async () => {
    const s = server({
      onPost: (url, srv) => {
        if (url === '/api/updates/install') srv.updates = status({ state: 'downloading', progress: { bytes: 0, total: 2048 } })
      },
    })
    stubServer(s)
    const reload = vi.fn()
    render(<UpdatesSection pollMs={5} reload={reload} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))

    expect(await screen.findByRole('progressbar')).toBeInTheDocument()
    expect(screen.getByText('Downloading version v1.141')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
    s.updates = status({ state: 'downloading', progress: { bytes: 1024, total: 2048 } })
    expect(await screen.findByText('1 KB of 2 KB')).toBeInTheDocument()
    // A read that fails during the download is not taken for the restart.
    s.code = 0
    const failedFrom = s.calls.length
    await waitFor(() => expect(s.calls.length).toBeGreaterThan(failedFrom + 2))
    expect(screen.queryByText('Restarting…')).toBeNull()
    s.code = 200
    s.updates = status({ state: 'verifying' })
    expect(await screen.findByText("Checking the download's signature and checksum")).toBeInTheDocument()
    s.updates = status({ state: 'installing' })
    expect(await screen.findByText('Installing version v1.141')).toBeInTheDocument()

    // The server goes away to restart, comes back on the old version for a
    // moment, then on the new one.
    s.code = 0
    expect(await screen.findByText('Restarting…')).toBeInTheDocument()
    expect(screen.getByText('This page reloads when version v1.141 is running.')).toBeInTheDocument()
    await waitFor(() => expect(s.calls.filter(c => c.url === '/api/version').length).toBeGreaterThan(1))
    expect(reload).not.toHaveBeenCalled()
    s.version = 'v1.141'
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    expect(s.calls.some(c => c.url === '/api/updates/install' && c.method === 'POST')).toBe(true)
  })

  it('waits on a restart it finds under way, and says so when the new version does not come back in time', async () => {
    stubServer(server({ updates: status({ state: 'restarting' }) }))
    const reload = vi.fn()
    // Long enough that a slow runner still sees the wait before it gives up.
    render(<UpdatesSection pollMs={5} giveUpMs={500} reload={reload} />)
    expect(await screen.findByText('Restarting…')).toBeInTheDocument()
    expect(await screen.findByText('Version v1.141 did not start within three minutes', {}, { timeout: 3000 })).toBeInTheDocument()
    expect(reload).not.toHaveBeenCalled()
  })

  it('shows why it cannot install, with no Update now', async () => {
    const reason = 'This copy of the app has no update signing key, so it reports new versions without installing them.'
    stubServer(server({ updates: status({ canInstall: false, reason }) }))
    render(<UpdatesSection />)
    expect(await screen.findByText(reason)).toBeInTheDocument()
    expect(screen.getByText('Version v1.141 is available')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
  })

  it('in a clone, shows the commands that update it and no automatic install', async () => {
    const reason = 'This Studio runs from a clone of the repository.'
    stubServer(server({ updates: status({ mode: 'clone', canInstall: false, reason, command: CLONE_UPDATE_COMMAND }) }))
    const { container } = render(<UpdatesSection />)
    expect(await screen.findByText(reason)).toBeInTheDocument()
    expect(container.querySelector('pre, code')).not.toBeNull()
    expect(container.textContent).toContain(CLONE_UPDATE_COMMAND)
    expect(CLONE_UPDATE_COMMAND).toBe('git pull\npnpm install\npnpm build')
    expect(screen.getByText('pnpm start')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Update now' })).toBeNull()
    expect(screen.getByRole('switch', { name: 'Check for updates automatically' })).toBeInTheDocument()
    expect(screen.queryByRole('switch', { name: 'Install updates automatically when idle' })).toBeNull()
  })

  it('renders nothing for a hosted deployment, refused or reported', async () => {
    const refused = server({ code: 403 })
    stubServer(refused)
    const first = render(<UpdatesSection />)
    await waitFor(() => expect(refused.calls.length).toBe(1))
    await act(async () => {})
    expect(first.container).toBeEmptyDOMElement()
    first.unmount()
    vi.unstubAllGlobals()

    const reported = server({ updates: status({ mode: 'hosted', available: false, latest: null }) })
    stubServer(reported)
    const second = render(<UpdatesSection />)
    await waitFor(() => expect(reported.calls.length).toBe(1))
    await act(async () => {})
    expect(second.container).toBeEmptyDOMElement()
  })

  it('saves the policy through the settings route', async () => {
    const s = server({ onPost: () => {} })
    stubServer(s)
    const saved = vi.fn()
    render(<UpdatesSection onPolicySaved={saved} />)
    const auto = await screen.findByRole('switch', { name: 'Install updates automatically when idle' })
    fireEvent.click(auto)
    await waitFor(() => expect(saved).toHaveBeenLastCalledWith({ check: true, install: 'auto', channel: 'stable' }))
    expect(s.calls.filter(c => c.url === '/api/settings').map(c => [c.method, c.body])).toEqual([
      ['PUT', { updates: { check: true, install: 'auto', channel: 'stable' } }],
    ])

    // Install waits on checking: with checks off it cannot be changed.
    const check = screen.getByRole('switch', { name: 'Check for updates automatically' })
    await waitFor(() => expect(check).not.toHaveAttribute('data-disabled'))
    fireEvent.click(check)
    await waitFor(() => expect(saved).toHaveBeenLastCalledWith({ check: false, install: 'auto', channel: 'stable' }))
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Install updates automatically when idle' })).toHaveAttribute('data-disabled'))

    // Another channel saves and checks again.
    fireEvent.click(screen.getByRole('combobox', { name: 'Channel' }))
    const pre = (await screen.findAllByRole('option')).find(o => o.textContent === 'Pre-release')!
    fireEvent.pointerDown(pre)
    fireEvent.click(pre)
    await waitFor(() => expect(saved).toHaveBeenLastCalledWith({ check: false, install: 'auto', channel: 'prerelease' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/updates/check' && c.method === 'POST')).toBe(true))
    expect(s.calls.filter(c => c.url === '/api/settings').length).toBe(3)
  })

  it('rolls back to the previous version after asking', async () => {
    const s = server({
      updates: status({ available: false, previous: { version: 'v1.139' } }),
      onPost: (url, srv) => { if (url === '/api/updates/rollback') srv.updates = status({ state: 'restarting', available: false, previous: null }) },
    })
    stubServer(s)
    const reload = vi.fn()
    render(<UpdatesSection pollMs={5} reload={reload} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Roll back to v1.139' }))
    expect(await screen.findByRole('dialog', { name: 'Roll back to v1.139?' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Roll back' }))
    expect(await screen.findByText('This page reloads when version v1.139 is running.')).toBeInTheDocument()
    s.version = 'v1.139'
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
  })
})

describe('the footer version line', () => {
  afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

  it('says an update is available only when one is, and opens Settings, General', async () => {
    stubServer(server())
    const { unmount } = render(<StatusFooter />)
    const link = await screen.findByRole('button', { name: 'update available' })
    expect(screen.getByText('v1.140')).toBeInTheDocument()
    fireEvent.click(link)
    expect(location.hash).toBe('#view=settings:general')
    unmount()
    vi.unstubAllGlobals()

    const none = server({ updates: status({ available: false }) })
    stubServer(none)
    render(<StatusFooter />)
    expect(await screen.findByText('v1.140')).toBeInTheDocument()
    await waitFor(() => expect(none.calls.some(c => c.url === '/api/updates')).toBe(true))
    await act(async () => {})
    expect(screen.queryByRole('button', { name: 'update available' })).toBeNull()
  })

  it('shows nothing for a hosted deployment', async () => {
    const hosted = server({ code: 403 })
    stubServer(hosted)
    render(<StatusFooter />)
    expect(await screen.findByText('v1.140')).toBeInTheDocument()
    await waitFor(() => expect(hosted.calls.some(c => c.url === '/api/updates')).toBe(true))
    await act(async () => {})
    expect(screen.queryByRole('button', { name: 'update available' })).toBeNull()
  })
})

describe('versions', () => {
  it('compare as releases, ignoring a leading v and a git describe suffix', async () => {
    const { bare } = await import('../src/components/UpdatesSection')
    expect(bare('v1.140-1-gabc1234')).toBe(bare('v1.140'))
    expect(bare(' V1.140 ')).toBe('1.140')
    expect(bare('v1.140')).not.toBe(bare('v1.141'))
  })
})

describe('after an update that changed the window', () => {
  it('says to reopen the app', async () => {
    const { render, screen } = await import('@testing-library/react')
    const { UpdatesSection } = await import('../src/components/UpdatesSection')
    const status = { mode: 'app', current: { version: 'v1.140', builtAt: null }, policy: { check: true, install: 'manual', channel: 'stable' }, latest: null, available: false, canInstall: true, reason: null, state: 'idle', progress: null, error: null, checkedAt: null, previous: { version: 'v1.139' }, reopen: true }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(status), { headers: { 'Content-Type': 'application/json' } })))
    render(<UpdatesSection />)
    expect(await screen.findByText(/Quit and reopen ACTIVATE Studio to finish/)).toBeInTheDocument()
    vi.unstubAllGlobals()
  })
})
