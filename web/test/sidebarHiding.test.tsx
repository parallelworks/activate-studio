import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'

// The navigation is under test, not the pages; each page is a stand-in.
const { page } = vi.hoisted(() => ({ page: (name: string) => ({ [name]: () => `${name} page` }) }))
vi.mock('../src/views/ChatView', () => page('ChatView'))
vi.mock('../src/views/LibraryView', () => page('LibraryView'))
vi.mock('../src/views/SearchView', () => page('SearchView'))
vi.mock('../src/views/QueryView', () => page('QueryView'))
vi.mock('../src/views/OverviewView', () => page('OverviewView'))
vi.mock('../src/views/HistoryView', () => page('HistoryView'))
vi.mock('../src/views/AgentsView', () => page('AgentsView'))
vi.mock('../src/views/WorkflowsView', () => page('WorkflowsView'))
vi.mock('../src/views/SettingsView', () => page('SettingsView'))
vi.mock('../src/views/HelpView', () => page('HelpView'))
vi.mock('../src/components/StatusFooter', () => page('StatusFooter'))
vi.mock('../src/components/RunPane', () => ({ ...page('RunPane'), ...page('FullViewHost') }))

let firstRun = false
let aiEnabled = true
let sections: string[] | undefined = ['library', 'search', 'settings', 'help']

describe('sidebar switches', () => {
  beforeEach(() => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      const body = url === '/api/config'
        ? { appName: 'Studio', sections, aiEnabled, platform: false, user: { id: 'u', username: 'u' } }
        : url.startsWith('/api/setup/status') ? { firstRun, standalone: true, canRestart: false } : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  })
  afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

  it('hides switched-off pages, in the sidebar and the phone sheet, and sends their links to the first page shown', async () => {
    history.replaceState(null, '', `${location.pathname}#view=agents`)
    const { container } = render(<App />)
    await waitFor(() => expect(location.hash).toBe('#view=library'))
    const nav = container.querySelector('.sidenav') as HTMLElement
    expect(within(nav).queryByTitle('Chat')).toBeNull()
    expect(within(nav).queryByTitle('Agents')).toBeNull()
    expect(within(nav).queryByTitle('Query')).toBeNull()
    expect(within(nav).getByTitle('Library')).toBeInTheDocument()
    expect(within(nav).getByTitle('Search')).toBeInTheDocument()
    expect(within(nav).getByTitle('Settings')).toBeInTheDocument()
    expect(within(nav).getByTitle('Help')).toBeInTheDocument()
    fireEvent.click(within(nav).getByTitle('More'))
    const sheet = container.querySelector('.nav-more-sheet') as HTMLElement
    expect(within(sheet).queryByText('Agents')).toBeNull()
    expect(within(sheet).queryByText('History')).toBeNull()
    expect(within(sheet).getByText('Settings')).toBeInTheDocument()

    // A link to a hidden page, followed later, lands on the first shown one.
    history.pushState(null, '', `${location.pathname}#view=chat`)
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await waitFor(() => expect(location.hash).toBe('#view=library'))
  })

  it('with AI features off, leaves out Chat and Agents even when every page is on', async () => {
    aiEnabled = false
    sections = undefined
    history.replaceState(null, '', `${location.pathname}#view=agents`)
    const { container } = render(<App />)
    // The configuration is cached from the last test; ask for it again.
    window.dispatchEvent(new Event('ade:config-changed'))
    await waitFor(() => expect(location.hash).toBe('#view=library'))
    await waitFor(() => expect(within(container.querySelector('.sidenav') as HTMLElement).getByTitle('Query')).toBeInTheDocument())
    const nav = container.querySelector('.sidenav') as HTMLElement
    expect(within(nav).queryByTitle('Chat')).toBeNull()
    expect(within(nav).queryByTitle('Agents')).toBeNull()
    expect(within(nav).getByTitle('Query')).toBeInTheDocument()
    expect(within(nav).getByTitle('Library')).toBeInTheDocument()
    aiEnabled = true
    sections = ['library', 'search', 'settings', 'help']
    window.dispatchEvent(new Event('ade:config-changed'))
  })

  it('opens the setup wizard over the app on a first run; Finish later records it as done, and it opens again on request', async () => {
    firstRun = true
    render(<App />)
    expect(await screen.findByRole('dialog', { name: 'Welcome' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Finish later' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const calls = (fetch as unknown as { mock: { calls: [string, RequestInit?][] } }).mock.calls
    expect(calls.some(([url, init]) => url === '/api/setup/complete' && init?.method === 'POST')).toBe(true)
    firstRun = false
    // Run setup again (Settings, Help, the macOS menu) opens it the same way.
    act(() => { window.dispatchEvent(new Event('ade:setup-open')) })
    expect(await screen.findByRole('dialog', { name: 'Welcome' })).toBeInTheDocument()
  })
})
