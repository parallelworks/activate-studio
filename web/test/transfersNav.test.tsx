import { act, render, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { resetTransfersProbe } from '../src/transferApi'
import { reloadAppConfig } from '../src/config'

// The menu is under test, not the pages; each page is a stand-in.
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
vi.mock('../src/views/TransfersView', () => page('TransfersView'))
vi.mock('../src/components/StatusFooter', () => page('StatusFooter'))
vi.mock('../src/components/RunPane', () => ({ ...page('RunPane'), ...page('FullViewHost') }))

let locationsStatus = 200
let sections: string[] | undefined

describe('Transfers in the menu', () => {
  beforeEach(() => {
    resetTransfersProbe()
    sections = undefined
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url === '/api/transfers/summary') return new Response(JSON.stringify({ active: 2, awaitingApproval: 1, failedLast24h: 1 }), { status: 200 })
      if (url === '/api/transfers/locations') return new Response(JSON.stringify(locationsStatus === 200 ? { locations: [] } : { error: 'not found' }), { status: locationsStatus })
      const body = url === '/api/config'
        ? { appName: 'Studio', aiEnabled: true, platform: false, sections, user: { id: 'u', username: 'u' } }
        : url.startsWith('/api/setup/status') ? { firstRun: false, standalone: true, canRestart: false } : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  })
  afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

  it('shows after Library and opens the page, on a server that has transfers', async () => {
    locationsStatus = 200
    history.replaceState(null, '', `${location.pathname}#view=transfers:new`)
    const { container } = render(<App />)
    const nav = container.querySelector('.sidenav') as HTMLElement
    await waitFor(() => expect(within(nav).getByTitle('Transfers')).toBeInTheDocument())
    const titles = [...nav.querySelectorAll('.sidenav-item')].map(b => b.getAttribute('title'))
    expect(titles.indexOf('Transfers')).toBe(titles.indexOf('Library') + 1)
    await waitFor(() => expect(container.textContent).toContain('TransfersView page'))
    // The entry's mark: the count under way, and a warning for a failure.
    const entry = within(nav).getByTitle('Transfers')
    await waitFor(() => expect(entry).toHaveTextContent('2 under way, 1 awaiting approval, 1 failed in the last day'))
    expect(entry.querySelector('.nav-xfer-mark')).not.toBeNull()
    // The page's own part of the hash stays.
    expect(location.hash).toBe('#view=transfers:new')
  })

  it('is left out, and its link goes to chat, on an older server that answers 404', async () => {
    locationsStatus = 404
    history.replaceState(null, '', `${location.pathname}#view=transfers`)
    const { container } = render(<App />)
    const nav = container.querySelector('.sidenav') as HTMLElement
    await waitFor(() => expect(within(nav).getByTitle('Library')).toBeInTheDocument())
    await waitFor(() => expect(location.hash).toBe(''))
    expect(within(nav).queryByTitle('Transfers')).toBeNull()
    // The address changes first; the page goes at the next render.
    await waitFor(() => expect(container.textContent).not.toContain('TransfersView page'))
  })

  it('is left out when the pages setting switches it off', async () => {
    locationsStatus = 200
    sections = ['chat', 'library', 'search', 'settings', 'help']
    history.replaceState(null, '', `${location.pathname}#view=transfers`)
    const { container } = render(<App />)
    // An earlier test's configuration is cached; read this one.
    act(() => reloadAppConfig())
    const nav = container.querySelector('.sidenav') as HTMLElement
    await waitFor(() => expect(within(nav).getByTitle('Library')).toBeInTheDocument())
    await waitFor(() => expect(location.hash).toBe(''))
    expect(within(nav).queryByTitle('Transfers')).toBeNull()
    // The address changes first; the page goes at the next render.
    await waitFor(() => expect(container.textContent).not.toContain('TransfersView page'))
  })
})
