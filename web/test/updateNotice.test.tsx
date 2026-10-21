import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Changelog, UpdateRelease, UpdatesStatus } from '../src/api'
import { StatusFooter } from '../src/components/StatusFooter'
import { UpdateNotice } from '../src/components/UpdateNotice'
import { SEEN_KEY, UpdatedNotice } from '../src/components/UpdatedNotice'
import { UpdatesSection } from '../src/components/UpdatesSection'
import { WhatsNew } from '../src/components/WhatsNew'
import { HelpView } from '../src/views/HelpView'

const RELEASES: UpdateRelease[] = [
  {
    version: 'v1.142', publishedAt: '2026-10-21T12:00:00Z', url: 'https://example.org/releases/v1.142',
    entries: [{ title: 'Icons: a drawn icon for the app', pr: 506, body: 'The app showed a plain letter.\n\n- **Icon.** Drawn from SVG.' }],
  },
  {
    // Its macOS build failed; its changes come with v1.142.
    version: 'v1.141', publishedAt: '2026-10-20T12:00:00Z', url: 'https://example.org/releases/v1.141',
    entries: [
      { title: 'Search: faster on large libraries', pr: 503, body: 'Reads the folders in parallel.' },
      { title: 'Settings: a sidebar switch', pr: 504, body: 'Each page has a switch.' },
    ],
  },
]

function status(over: Partial<UpdatesStatus> = {}): UpdatesStatus {
  return {
    mode: 'app',
    current: { version: 'v1.140', builtAt: '2026-10-01T12:00:00Z' },
    policy: { check: true, install: 'manual', channel: 'stable' },
    latest: { version: 'v1.142', publishedAt: '2026-10-21T12:00:00Z', notes: '### Icons: a drawn icon for the app (#506)\n\nThe app showed a plain letter.', url: 'https://example.org/releases/v1.142' },
    releases: RELEASES,
    available: true,
    canInstall: true,
    reason: null,
    command: null,
    state: 'idle',
    progress: null,
    error: null,
    checkedAt: '2026-10-21T08:00:00Z',
    previous: null,
    ...over,
  }
}

const CHANGELOG: Changelog = {
  included: true,
  current: 'v1.142',
  versions: [
    { version: 'v1.142', date: '2026-10-21', entries: RELEASES[0].entries },
    { version: 'v1.141', date: '2026-10-20', entries: RELEASES[1].entries },
    { version: 'v1.140', date: '2026-10-19', entries: [{ title: 'Chat: retry a reply', pr: 500, body: 'A reply that failed offers **Retry**.' }] },
  ],
}

interface Server {
  updates: UpdatesStatus | null
  /** Status code for GET /api/updates; 0 drops the connection. */
  code: number
  version: string
  changelog: Changelog
  calls: { method: string; url: string }[]
  onPost?: (url: string, s: Server) => void
}

const reply = (body: unknown, code = 200) => new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } })

function stubServer(s: Server) {
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    s.calls.push({ method, url })
    if (url === '/api/updates') {
      if (s.code === 0) throw new TypeError('Failed to fetch')
      return s.code === 200 ? reply(s.updates) : reply({ error: 'not available here' }, s.code)
    }
    if (url.startsWith('/api/updates/') && method === 'POST') {
      s.onPost?.(url, s)
      return reply(s.updates)
    }
    if (url.startsWith('/api/changelog')) {
      const after = new URL(url, 'http://studio.test').searchParams.get('after')
      const n = (v: string) => Number(v.replace(/^v1\./, ''))
      return reply({ ...s.changelog, versions: s.changelog.versions.filter(v => !after || n(v.version) > n(after)) })
    }
    if (url === '/api/version') return reply({ version: s.version, commit: s.version, builtAt: null, startedAt: '2026-10-21T08:00:00Z', source: 'bundle' })
    if (url === '/api/help') return new Response('Intro.\n\n# Basics\n\n## Chat\n\nAsk.\n', { headers: { 'Content-Type': 'text/markdown' } })
    if (url.startsWith('/api/kb/stats')) return reply({ available: true, files: 3, dirs: 1, totalBytes: 10 })
    if (url.startsWith('/api/index/status')) return reply({ running: false, lastSweepAt: null, lastSweepMs: null, lastChanges: [], lastError: null, sweepIntervalSec: 60 })
    if (url === '/api/whoami') return reply({ authEnabled: false, header: '', verified: false, user: null, token: null, claims: null })
    if (url.startsWith('/healthz')) return reply({ ok: true, gufi: true })
    return reply({ error: 'not found' }, 404)
  }))
}

const server = (over: Partial<Server> = {}): Server => ({ updates: status(), code: 200, version: 'v1.140', changelog: CHANGELOG, calls: [], ...over })
const notice = () => screen.queryByTestId('update-notice')
const settle = () => act(async () => { await new Promise(r => setTimeout(r, 20)) })

beforeEach(() => { localStorage.clear(); history.replaceState(null, '', location.pathname) })
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); localStorage.clear() })

describe('the update notice', () => {
  it('in the app: the version, every newer version\'s changes newest first, each opening to its description, Update now and Later', async () => {
    stubServer(server())
    render(<UpdateNotice />)
    expect(await screen.findByText('Version v1.142 is available')).toBeInTheDocument()
    const box = notice()!
    expect(within(box).getByText('You have version v1.140.')).toBeInTheDocument()
    const versions = within(box).getAllByRole('heading', { level: 3 }).map(h => h.textContent)
    expect(versions).toEqual([expect.stringMatching(/^v1\.142/), expect.stringMatching(/^v1\.141/)])
    expect(within(box).getAllByRole('button', { name: /^(Icons|Search|Settings):/ }).map(b => b.textContent)).toEqual([
      'Icons: a drawn icon for the app', 'Search: faster on large libraries', 'Settings: a sidebar switch',
    ])
    // Closed until asked for; then the description is rendered as markdown.
    expect(within(box).queryByText('The app showed a plain letter.')).toBeNull()
    fireEvent.click(within(box).getByRole('button', { name: 'Icons: a drawn icon for the app' }))
    expect(await within(box).findByText('The app showed a plain letter.')).toBeInTheDocument()
    // Markdown, not its source: the list item and the bold run are elements.
    expect(within(box).getByText('Icon.').closest('li')).not.toBeNull()
    expect(box.textContent).not.toContain('**')
    expect(within(box).getByRole('link', { name: 'Pull request #506' })).toHaveAttribute('href', 'https://github.com/parallelworks/activate-studio/pull/506')
    expect(within(box).getByRole('button', { name: 'Update now' })).toBeEnabled()
    expect(within(box).getByRole('button', { name: 'Later' })).toBeInTheDocument()
  })

  it('in a clone: the commands that update it, no Update now', async () => {
    stubServer(server({ updates: status({ mode: 'clone', canInstall: false, reason: 'This Studio runs from a clone of the repository.', command: 'git pull\npnpm install\npnpm build' }) }))
    render(<UpdateNotice />)
    const box = await screen.findByTestId('update-notice')
    expect(box.textContent).toContain('git pull\npnpm install\npnpm build')
    expect(within(box).getByText('pnpm start')).toBeInTheDocument()
    expect(within(box).queryByRole('button', { name: 'Update now' })).toBeNull()
    expect(within(box).getByRole('button', { name: 'Later' })).toBeInTheDocument()
  })

  it('an app that cannot install says why, with the release page', async () => {
    const reason = 'The folder holding the app is not writable, so the app cannot replace itself.'
    stubServer(server({ updates: status({ mode: 'clone', canInstall: false, reason, command: null }) }))
    render(<UpdateNotice />)
    const box = await screen.findByTestId('update-notice')
    expect(within(box).getByText(reason)).toBeInTheDocument()
    expect(within(box).queryByText(/git pull/)).toBeNull()
    expect(within(box).getByRole('link', { name: 'Release page' })).toHaveAttribute('href', 'https://example.org/releases/v1.142')
  })

  it('never shows on a hosted deployment, refused or reported, nor with nothing newer', async () => {
    for (const s of [
      server({ code: 403 }),
      server({ updates: status({ mode: 'hosted', available: false, latest: null, releases: [] }) }),
      server({ updates: status({ available: false, releases: [] }) }),
    ]) {
      stubServer(s)
      const { unmount } = render(<UpdateNotice />)
      await waitFor(() => expect(s.calls.some(c => c.url === '/api/updates')).toBe(true))
      await settle()
      expect(notice()).toBeNull()
      unmount()
      vi.unstubAllGlobals()
    }
  })

  it('Later hides it for that version, in this browser; a newer version shows it again', async () => {
    const s = server()
    stubServer(s)
    const first = render(<UpdateNotice />)
    fireEvent.click(await screen.findByRole('button', { name: 'Later' }))
    expect(notice()).toBeNull()
    expect(localStorage.getItem('ade-update-later')).toBe('v1.142')
    first.unmount()

    // Opened again: still hidden for v1.142.
    const again = render(<UpdateNotice />)
    await waitFor(() => expect(s.calls.filter(c => c.url === '/api/updates').length).toBe(2))
    await settle()
    expect(notice()).toBeNull()
    again.unmount()

    // v1.143 comes out.
    s.updates = status({
      latest: { version: 'v1.143', publishedAt: '2026-10-22T12:00:00Z', notes: '', url: 'https://example.org/releases/v1.143' },
      releases: [{ version: 'v1.143', publishedAt: '2026-10-22T12:00:00Z', url: 'https://example.org/releases/v1.143', entries: [{ title: 'Chat: a newer change', pr: 510, body: 'Newer.' }] }, ...RELEASES],
    })
    render(<UpdateNotice />)
    expect(await screen.findByText('Version v1.143 is available')).toBeInTheDocument()
    expect(within(notice()!).getByRole('button', { name: 'Chat: a newer change' })).toBeInTheDocument()
  })

  it('Update now shows the install\'s progress in place of the buttons, then the restart, then reloads', async () => {
    const s = server({
      onPost: (url, srv) => {
        if (url === '/api/updates/install') srv.updates = status({ state: 'downloading', progress: { bytes: 0, total: 2048 } })
      },
    })
    stubServer(s)
    const reload = vi.fn()
    render(<UpdateNotice pollMs={5} reload={reload} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Update now' }))
    expect(await screen.findByText('Updating to version v1.142')).toBeInTheDocument()
    expect(within(notice()!).getByRole('progressbar')).toBeInTheDocument()
    expect(within(notice()!).queryByRole('button', { name: 'Update now' })).toBeNull()
    expect(within(notice()!).queryByRole('button', { name: 'Later' })).toBeNull()
    s.updates = status({ state: 'downloading', progress: { bytes: 1024, total: 2048 } })
    expect(await screen.findByText('1 KB of 2 KB')).toBeInTheDocument()
    s.updates = status({ state: 'installing' })
    expect(await screen.findByText('Installing version v1.142')).toBeInTheDocument()
    s.code = 0
    expect(await screen.findByText('Restarting…')).toBeInTheDocument()
    s.version = 'v1.142'
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    expect(s.calls.filter(c => c.url === '/api/updates/install').length).toBe(1)
  })

  it('an install started from Settings, or a deferred version being installed, shows its progress too', async () => {
    localStorage.setItem('ade-update-later', 'v1.142')
    const s = server({ updates: status({ state: 'verifying' }) })
    stubServer(s)
    const intervals = vi.spyOn(window, 'setInterval')
    render(<><UpdateNotice pollMs={5} /><UpdatesSection pollMs={5} /></>)
    expect(await screen.findByText('Updating to version v1.142')).toBeInTheDocument()
    expect(within(notice()!).getByText("Checking the download's signature and checksum")).toBeInTheDocument()
    // One page, one follower: the two do not each poll. Counted by the
    // timers at the poll interval, which a slow runner cannot change.
    const before = s.calls.filter(c => c.url === '/api/updates').length
    await act(async () => { await new Promise(r => setTimeout(r, 60)) })
    expect(s.calls.filter(c => c.url === '/api/updates').length - before).toBeGreaterThan(2)
    expect(intervals.mock.calls.filter(([, ms]) => ms === 5)).toHaveLength(1)
    intervals.mockRestore()
  })
})

describe("What's new", () => {
  it('lists the versions newest first, each change opening to its description', async () => {
    stubServer(server())
    render(<WhatsNew />)
    expect(await screen.findByText(/This is version v1\.142\./)).toBeInTheDocument()
    expect(screen.getAllByRole('heading', { level: 2 }).map(h => h.textContent?.slice(0, 6))).toEqual(['v1.142', 'v1.141', 'v1.140'])
    fireEvent.click(screen.getByRole('button', { name: 'Chat: retry a reply' }))
    // The bold run is an element of its own, not "**Retry**" in the text.
    expect(await screen.findByText('Retry')).toBeInTheDocument()
    expect(document.body.textContent).not.toContain('**')
    expect(screen.queryByText(/not included in this build/)).toBeNull()
  })

  it('says when the build carries no change log', async () => {
    stubServer(server({ changelog: { included: false, current: 'v1.142', versions: [] } }))
    render(<WhatsNew />)
    expect(await screen.findByText('The change log is not included in this build.')).toBeInTheDocument()
  })

  it('opens in Help from its link, and from the footer\'s version panel and Settings, Updates', async () => {
    stubServer(server())
    location.hash = '#view=help:whats-new'
    const help = render(<HelpView />)
    expect(await screen.findByRole('heading', { level: 1, name: "What's new" })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: "What's new" })).toHaveClass('active')
    help.unmount()

    history.replaceState(null, '', location.pathname)
    const footer = render(<StatusFooter />)
    fireEvent.click(await screen.findByRole('button', { name: 'v1.140' }))
    fireEvent.click(await screen.findByRole('button', { name: "What's new" }))
    expect(location.hash).toBe('#view=help:whats-new')
    footer.unmount()

    history.replaceState(null, '', location.pathname)
    render(<UpdatesSection />)
    fireEvent.click(await screen.findByRole('button', { name: "What's new" }))
    expect(location.hash).toBe('#view=help:whats-new')
  })
})

describe('after an update', () => {
  it('the first visit only records the version; a newer one is announced once, with the changes since', async () => {
    const s = server({ version: 'v1.140', updates: status({ current: { version: 'v1.140', builtAt: null }, available: false, latest: null, releases: [] }) })
    stubServer(s)
    const first = render(<UpdatedNotice />)
    await waitFor(() => expect(localStorage.getItem(SEEN_KEY)).toBe('v1.140'))
    await settle()
    expect(screen.queryByTestId('updated-notice')).toBeNull()
    expect(s.calls.some(c => c.url.startsWith('/api/changelog'))).toBe(false)
    first.unmount()

    // Updated to v1.142.
    s.updates = status({ current: { version: 'v1.142', builtAt: null }, available: false, latest: null, releases: [] })
    const second = render(<UpdatedNotice />)
    const box = await screen.findByTestId('updated-notice')
    expect(within(box).getByText('Updated to v1.142')).toBeInTheDocument()
    expect(within(box).getAllByRole('listitem').map(li => li.textContent)).toEqual([
      'Icons: a drawn icon for the app', 'Search: faster on large libraries', 'Settings: a sidebar switch',
    ])
    expect(within(box).queryByText('Chat: retry a reply')).toBeNull()
    expect(s.calls.find(c => c.url.startsWith('/api/changelog'))?.url).toBe('/api/changelog?after=v1.140')
    expect(localStorage.getItem(SEEN_KEY)).toBe('v1.142')
    fireEvent.click(within(box).getByRole('button', { name: "What's new" }))
    expect(location.hash).toBe('#view=help:whats-new')
    expect(screen.queryByTestId('updated-notice')).toBeNull()
    second.unmount()

    // Once only.
    render(<UpdatedNotice />)
    await waitFor(() => expect(s.calls.filter(c => c.url === '/api/updates').length).toBe(3))
    await settle()
    expect(screen.queryByTestId('updated-notice')).toBeNull()
  })

  it('dismisses, and a hosted deployment neither shows it nor records anything', async () => {
    localStorage.setItem(SEEN_KEY, 'v1.141')
    stubServer(server({ updates: status({ current: { version: 'v1.142', builtAt: null }, available: false, latest: null, releases: [] }) }))
    const one = render(<UpdatedNotice />)
    const box = await screen.findByTestId('updated-notice')
    fireEvent.click(within(box).getByRole('button', { name: /dismiss/i }))
    expect(screen.queryByTestId('updated-notice')).toBeNull()
    one.unmount()
    vi.unstubAllGlobals()

    localStorage.setItem(SEEN_KEY, 'v1.141')
    const hosted = server({ code: 403 })
    stubServer(hosted)
    render(<UpdatedNotice />)
    await waitFor(() => expect(hosted.calls.some(c => c.url === '/api/updates')).toBe(true))
    await settle()
    expect(screen.queryByTestId('updated-notice')).toBeNull()
    expect(localStorage.getItem(SEEN_KEY)).toBe('v1.141')
    expect(hosted.calls.some(c => c.url.startsWith('/api/changelog'))).toBe(false)
  })
})

describe('Settings, Updates', () => {
  it('lists every newer version\'s changes the same way', async () => {
    stubServer(server())
    render(<UpdatesSection />)
    expect(await screen.findByText('Version v1.142 is available')).toBeInTheDocument()
    expect(screen.getAllByRole('heading', { level: 3 }).map(h => h.textContent?.slice(0, 6))).toEqual(['v1.142', 'v1.141'])
    expect(screen.getByRole('button', { name: 'Search: faster on large libraries' })).toBeInTheDocument()
  })
})
