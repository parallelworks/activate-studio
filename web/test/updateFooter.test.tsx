import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Changelog, UpdateRelease, UpdatesStatus } from '../src/api'
import { SEEN_KEY } from '../src/afterUpdate'
import { StatusFooter } from '../src/components/StatusFooter'
import { CHECK_EVENT, setUpdateFlowOptions } from '../src/updateFlow'
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
/** The footer's version line's action: update available, updating…, update failed. */
const action = (name: string) => screen.findByRole('button', { name })
/** Opens the Build panel from "update available", then its What's new dialog. */
async function openDetails() {
  fireEvent.click(await action('update available'))
  fireEvent.click(await screen.findByRole('button', { name: /^What's new \(/ }))
  return screen.findByRole('dialog', { name: /^Version / })
}
const settle = () => act(async () => { await new Promise(r => setTimeout(r, 20)) })

beforeEach(() => { localStorage.clear(); sessionStorage.clear(); history.replaceState(null, '', location.pathname) })
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); localStorage.clear(); sessionStorage.clear(); delete (window as { webkit?: unknown }).webkit })

/** The macOS app's window, answering the page's requests with `answer`. */
function appWindow(answer: (message: { action: string }) => Promise<unknown>) {
  const postMessage = vi.fn(answer)
  Object.assign(window, { webkit: { messageHandlers: { studioShell: { postMessage } } } })
  return postMessage
}
const REOPEN_KEY = 'ade-reopen-after-update'

describe('a newer version, in the footer', () => {
  it('in the app: "update available" beside the version, and the Build panel\'s What\'s new opens every newer version\'s changes, newest first, each opening to its description', async () => {
    stubServer(server())
    render(<StatusFooter />)
    fireEvent.click(await action('update available'))
    const panel = await screen.findByTestId('build-updates')
    expect(panel.textContent).toContain('Version v1.142 is available')
    expect(within(panel).getByRole('button', { name: 'Update now' })).toBeEnabled()
    fireEvent.click(within(panel).getByRole('button', { name: "What's new (3)" }))
    const box = await screen.findByRole('dialog', { name: 'Version v1.142' })
    expect(within(box).getByText('You have version v1.140. These are the changes since.')).toBeInTheDocument()
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
    // Nothing to put off: the footer is the only standing signal.
    expect(screen.queryByRole('button', { name: 'Later' })).toBeNull()
    fireEvent.click(within(box).getByRole('button', { name: 'Update settings' }))
    expect(location.hash).toBe('#view=settings:updates')
    expect(screen.queryByRole('dialog', { name: 'Version v1.142' })).toBeNull()
  })

  it('in a clone: the commands that update it, no Update now', async () => {
    stubServer(server({ updates: status({ mode: 'clone', canInstall: false, reason: 'This Studio runs from a clone of the repository.', command: 'git pull\npnpm install\npnpm build' }) }))
    render(<StatusFooter />)
    const box = await openDetails()
    expect(box.textContent).toContain('git pull\npnpm install\npnpm build')
    expect(within(box).getByText('pnpm start')).toBeInTheDocument()
    expect(within(box).queryByRole('button', { name: 'Update now' })).toBeNull()
  })

  it('for an .rpm install: the dnf command in the details, no Update now, no clone wording', async () => {
    const note = 'This copy was installed from an .rpm package, so it does not update itself. To update, run this in a terminal, then quit and reopen ACTIVATE Studio:'
    stubServer(server({ updates: status({
      mode: 'package', canInstall: false, reason: 'This copy was installed from an .rpm package.',
      command: 'sudo dnf upgrade --refresh activate-studio', commandNote: note,
      package: { kind: 'rpm', manager: 'dnf', name: 'activate-studio' },
    }) }))
    render(<StatusFooter />)
    const box = await openDetails()
    expect(within(box).getByText(note)).toBeInTheDocument()
    expect(box.textContent).toContain('sudo dnf upgrade --refresh activate-studio')
    expect(box.textContent).not.toContain('pnpm')
    expect(within(box).queryByRole('button', { name: 'Update now' })).toBeNull()
  })

  it('an app that cannot install says why, with the release page', async () => {
    const reason = 'The folder holding the app is not writable, so the app cannot replace itself.'
    stubServer(server({ updates: status({ mode: 'clone', canInstall: false, reason, command: null }) }))
    render(<StatusFooter />)
    const box = await openDetails()
    expect(within(box).getByText(reason)).toBeInTheDocument()
    expect(within(box).queryByText(/git pull/)).toBeNull()
    expect(within(box).getByRole('link', { name: 'Release page' })).toHaveAttribute('href', 'https://example.org/releases/v1.142')
  })

  it('a hosted deployment, refused or reported, shows none of it, expanded or on the rail', async () => {
    localStorage.setItem(SEEN_KEY, 'v1.139')
    for (const s of [
      server({ code: 403 }),
      server({ updates: status({ mode: 'hosted', reopen: true }) }),
    ]) {
      for (const collapsed of [false, true]) {
        stubServer(s)
        const { unmount } = render(<StatusFooter collapsed={collapsed} />)
        await waitFor(() => expect(s.calls.some(c => c.url === '/api/updates')).toBe(true))
        if (!collapsed) await screen.findByText('v1.140')
        await settle()
        expect(screen.queryByRole('button', { name: /update available|updating|update failed|what's new|check for updates|is available|Reopen/i })).toBeNull()
        expect(screen.queryByTestId('footer-reopen')).toBeNull()
        unmount()
        vi.unstubAllGlobals()
      }
    }
    expect(localStorage.getItem(SEEN_KEY)).toBe('v1.139')
  })

  it('reads the status again when the window is shown or focused, and follows a check the server runs', async () => {
    setUpdateFlowOptions({ focusGapMs: 0, checkingMs: 5 })
    const s = server({ updates: status({ available: false, latest: null, releases: [] }) })
    stubServer(s)
    render(<StatusFooter />)
    await action('check for updates')
    expect(s.calls.filter(c => c.url === '/api/updates').length).toBe(1)
    // While the window was hidden, the server's check became due: the read on
    // focus starts it, and the page reads again until it ends.
    s.updates = status({ state: 'checking', available: false, latest: null, releases: [] })
    act(() => { window.dispatchEvent(new Event('focus')) })
    await waitFor(() => expect(s.calls.filter(c => c.url === '/api/updates').length).toBe(2))
    s.updates = status()
    await action('update available')
    const reads = s.calls.filter(c => c.url === '/api/updates').length
    act(() => { document.dispatchEvent(new Event('visibilitychange')) })
    await waitFor(() => expect(s.calls.filter(c => c.url === '/api/updates').length).toBe(reads + 1))
  })

  it('the app menu\'s Check for Updates event runs a check', async () => {
    const s = server()
    stubServer(s)
    render(<StatusFooter />)
    await action('update available')
    act(() => { window.dispatchEvent(new Event(CHECK_EVENT)) })
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/updates/check' && c.method === 'POST')).toBe(true))
  })

  it('Update now in the details: the footer reads "updating…" and the Build panel opens on the progress, then the restart, then the reload', async () => {
    const s = server({
      onPost: (url, srv) => {
        if (url === '/api/updates/install') srv.updates = status({ state: 'downloading', progress: { bytes: 0, total: 2048 } })
      },
    })
    stubServer(s)
    const reload = vi.fn()
    setUpdateFlowOptions({ pollMs: 5, reload })
    render(<StatusFooter />)
    const box = await openDetails()
    fireEvent.click(within(box).getByRole('button', { name: 'Update now' }))
    await action('updating…')
    expect(screen.queryByRole('button', { name: 'update available' })).toBeNull()
    expect(screen.queryByRole('dialog', { name: 'Version v1.142' })).toBeNull()
    const panel = await screen.findByTestId('build-updates')
    expect(await within(panel).findByRole('progressbar')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Update now' })).toBeNull()
    s.updates = status({ state: 'downloading', progress: { bytes: 1024, total: 2048 } })
    expect(await within(panel).findByText('1 KB of 2 KB')).toBeInTheDocument()
    s.updates = status({ state: 'installing' })
    expect(await within(panel).findByText('Installing version v1.142')).toBeInTheDocument()
    s.code = 0
    expect(await within(panel).findByText('Restarting…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'updating…' })).toBeInTheDocument()
    s.version = 'v1.142'
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1))
    expect(s.calls.filter(c => c.url === '/api/updates/install').length).toBe(1)
    // Started here, so the page that loads may reopen the app's window.
    expect(sessionStorage.getItem(REOPEN_KEY)).toBe('v1.142')
  })

  it('an install started from Settings, or by the server, reads "updating…" too, and "updating…" opens the progress', async () => {
    setUpdateFlowOptions({ pollMs: 5 })
    const s = server({ updates: status({ state: 'verifying' }) })
    stubServer(s)
    const intervals = vi.spyOn(window, 'setInterval')
    render(<><StatusFooter /><UpdatesSection /></>)
    fireEvent.click(await action('updating…'))
    expect(within(await screen.findByTestId('build-updates')).getByText("Checking the download's signature and checksum")).toBeInTheDocument()
    // One page, one follower: the two do not each poll. Counted by the
    // timers at the poll interval, which a slow runner cannot change.
    const before = s.calls.filter(c => c.url === '/api/updates').length
    await act(async () => { await new Promise(r => setTimeout(r, 60)) })
    expect(s.calls.filter(c => c.url === '/api/updates').length - before).toBeGreaterThan(2)
    expect(intervals.mock.calls.filter(([, ms]) => ms === 5)).toHaveLength(1)
    intervals.mockRestore()
  })

  it('an install that fails reads "update failed", and the Build panel says why; a failed check does not', async () => {
    setUpdateFlowOptions({ pollMs: 5 })
    const why = 'The update failed: the download did not match its checksum.'
    const s = server({ updates: status({ state: 'downloading', progress: { bytes: 10, total: 2048 } }) })
    stubServer(s)
    const first = render(<StatusFooter />)
    await action('updating…')
    s.updates = status({ state: 'error', error: why })
    fireEvent.click(await action('update failed'))
    expect(within(await screen.findByTestId('build-updates')).getByRole('alert').textContent).toBe(why)
    expect(screen.queryByRole('button', { name: 'updating…' })).toBeNull()
    first.unmount()
    vi.unstubAllGlobals()

    // The install request itself refused.
    const refused = server()
    stubServer(refused)
    const second = render(<StatusFooter />)
    fireEvent.click(await action('update available'))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => String(input) === '/api/updates/install'
      ? reply({ error: 'Another update is under way.' }, 409)
      : reply(refused.updates)))
    fireEvent.click(within(await screen.findByTestId('build-updates')).getByRole('button', { name: 'Update now' }))
    await action('update failed')
    expect(screen.getByRole('alert').textContent).toBe('Another update is under way.')
    second.unmount()
    vi.unstubAllGlobals()

    stubServer(server({ updates: status({ state: 'error', error: 'The check for updates failed: GitHub answered HTTP 500.' }) }))
    render(<StatusFooter />)
    await action('update available')
    await settle()
    expect(screen.queryByRole('button', { name: 'update failed' })).toBeNull()
  })

  it('on the collapsed rail: a dot for a newer version, an install, and a failed one', async () => {
    setUpdateFlowOptions({ pollMs: 5 })
    const s = server()
    stubServer(s)
    render(<StatusFooter collapsed />)
    await action('Version v1.142 is available')
    s.updates = status({ state: 'installing' })
    act(() => { window.dispatchEvent(new Event(CHECK_EVENT)) })
    await action('Updating')
    s.updates = status({ state: 'error', error: 'The update failed: no room on the disk.' })
    fireEvent.click(await action('The update failed'))
    expect(location.hash).toBe('#view=settings:updates')
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
  const link = () => screen.queryByRole('button', { name: "what's new" })

  it('the first visit only records the version; a newer one shows "what\'s new" beside the version until it is opened, once', async () => {
    const s = server({ version: 'v1.140', updates: status({ current: { version: 'v1.140', builtAt: null }, available: false, latest: null, releases: [] }) })
    stubServer(s)
    const first = render(<StatusFooter />)
    await waitFor(() => expect(localStorage.getItem(SEEN_KEY)).toBe('v1.140'))
    await action('check for updates')
    expect(link()).toBeNull()
    first.unmount()

    // Updated to v1.142.
    s.version = 'v1.142'
    s.updates = status({ current: { version: 'v1.142', builtAt: null }, available: false, latest: null, releases: [] })
    const second = render(<StatusFooter />)
    fireEvent.click(await action("what's new"))
    expect(location.hash).toBe('#view=help:whats-new')
    expect(localStorage.getItem(SEEN_KEY)).toBe('v1.142')
    expect(link()).toBeNull()
    // The line's own action is back.
    expect(screen.getByRole('button', { name: 'check for updates' })).toBeInTheDocument()
    second.unmount()

    // Once only.
    render(<StatusFooter />)
    await action('check for updates')
    await settle()
    expect(link()).toBeNull()
  })

  it('gives way to "update available" when a newer version is already out', async () => {
    localStorage.setItem(SEEN_KEY, 'v1.139')
    stubServer(server())
    render(<StatusFooter />)
    await action('update available')
    await settle()
    expect(link()).toBeNull()
    expect(localStorage.getItem(SEEN_KEY)).toBe('v1.140')
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

describe('after an update that changed the app\'s window', () => {
  const updated = () => status({ current: { version: 'v1.142', builtAt: null }, available: false, latest: null, releases: [], reopen: true })
  const line = () => screen.queryByTestId('footer-reopen')

  it('after an install the person started, the app reopens on its own, and the reopened window is the one that says it updated', async () => {
    localStorage.setItem(SEEN_KEY, 'v1.140')
    sessionStorage.setItem(REOPEN_KEY, 'v1.142')
    stubServer(server({ version: 'v1.142', updates: updated() }))
    const post = appWindow(async () => true)
    render(<StatusFooter />)
    await waitFor(() => expect(post).toHaveBeenCalledWith({ action: 'relaunch' }))
    await screen.findByText('v1.142')
    await settle()
    expect(line()).toBeNull()
    expect(screen.queryByRole('button', { name: "what's new" })).toBeNull()
    expect(localStorage.getItem(SEEN_KEY)).toBe('v1.140')
    expect(sessionStorage.getItem(REOPEN_KEY)).toBeNull()
  })

  it('otherwise a footer line offers Reopen now, and a window that cannot reopen keeps the words', async () => {
    localStorage.setItem(SEEN_KEY, 'v1.140')
    stubServer(server({ version: 'v1.142', updates: updated() }))
    // A window from before the app could reopen itself.
    const post = appWindow(async () => { throw new Error('unknown request relaunch') })
    render(<StatusFooter />)
    const reopen = await screen.findByTestId('footer-reopen')
    expect(post).not.toHaveBeenCalled()
    expect(reopen.textContent).toContain('Reopen to finish updating')
    expect(screen.getByRole('button', { name: "what's new" })).toBeInTheDocument()
    expect(localStorage.getItem(SEEN_KEY)).toBe('v1.142')
    fireEvent.click(within(reopen).getByRole('button', { name: 'Reopen now' }))
    await waitFor(() => expect(within(reopen).queryByRole('button', { name: 'Reopen now' })).toBeNull())
    expect(post).toHaveBeenCalledWith({ action: 'relaunch' })
    expect(reopen.textContent).toContain('Reopen to finish updating')
    expect(reopen.title).toContain('Quit and reopen the app to finish.')
  })

  it('clears Reopen now once the server says the window runs the new copy, without a reload', async () => {
    setUpdateFlowOptions({ focusGapMs: 0 })
    const s = server({ updates: updated() })
    stubServer(s)
    appWindow(async () => true)
    render(<UpdatesSection />)
    expect(await screen.findByText("The update also changed the app's window, which updates when the app reopens.")).toBeInTheDocument()
    // The window was reopened: the shell removed updates/reopen, and the
    // server reports reopen false at the next read (here, the window focused).
    s.updates = { ...updated(), reopen: false }
    act(() => { window.dispatchEvent(new Event('focus')) })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Reopen now' })).toBeNull())
    expect(screen.queryByText(/updates when the app reopens/)).toBeNull()
  })

  it('the reopened window offers what\'s new with no reopen line', async () => {
    localStorage.setItem(SEEN_KEY, 'v1.140')
    stubServer(server({ version: 'v1.142', updates: { ...updated(), reopen: false } }))
    appWindow(async () => true)
    render(<StatusFooter />)
    await action("what's new")
    expect(line()).toBeNull()
  })

  it('until the app reopens, every page load has the line again, expanded and as a dot on the rail', async () => {
    localStorage.setItem(SEEN_KEY, 'v1.142')
    const s = server({ version: 'v1.142', updates: updated() })
    stubServer(s)
    const post = appWindow(async () => true)
    const first = render(<StatusFooter />)
    await screen.findByTestId('footer-reopen')
    first.unmount()

    const rail = render(<StatusFooter collapsed />)
    fireEvent.click(await action('Reopen to finish updating'))
    expect(location.hash).toBe('#view=settings:updates')
    rail.unmount()

    render(<StatusFooter />)
    fireEvent.click(within(await screen.findByTestId('footer-reopen')).getByRole('button', { name: 'Reopen now' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith({ action: 'relaunch' }))
  })

  it('a release that waits for the app to reopen has the line, and no "updating…"', async () => {
    stubServer(server({ updates: status({ state: 'relaunch', available: false, reopen: true }) }))
    appWindow(async () => true)
    render(<StatusFooter />)
    expect(within(await screen.findByTestId('footer-reopen')).getByRole('button', { name: 'Reopen now' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'updating…' })).toBeNull()
  })

  it('the line goes once the server says the window runs the new copy', async () => {
    setUpdateFlowOptions({ focusGapMs: 0 })
    localStorage.setItem(SEEN_KEY, 'v1.142')
    const s = server({ version: 'v1.142', updates: updated() })
    stubServer(s)
    appWindow(async () => true)
    render(<StatusFooter />)
    await screen.findByTestId('footer-reopen')
    s.updates = { ...updated(), reopen: false }
    act(() => { window.dispatchEvent(new Event('focus')) })
    await waitFor(() => expect(line()).toBeNull())
  })

  it('Settings, Updates asks the window to reopen', async () => {
    stubServer(server({ updates: updated() }))
    const post = appWindow(async () => true)
    render(<UpdatesSection />)
    fireEvent.click(await screen.findByRole('button', { name: 'Reopen now' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith({ action: 'relaunch' }))
    expect(screen.queryByText('Quit and reopen ACTIVATE Studio to finish.')).toBeNull()
  })
})
