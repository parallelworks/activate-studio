import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AppConfig } from '../src/config'
import { reloadPanelState, setPanelView, setSideShown } from '../src/panelState'
import { share } from '../src/statusShare'
import { PageSideViewHost } from '../src/components/RunPane'
import {
  PanelToggle, StatusPanel, activitySummary, connectionsSummary, connectionLines, librariesSummary, libraryLines, modelSummary, resetStatusPanelForTests,
} from '../src/components/StatusPanel'

const lib = { id: 'kb', label: 'Knowledge base', primary: true, writable: true, pinned: true, source: true, caps: { index: true, fullText: true, vectors: false } }
const cfg = (over: Partial<AppConfig> = {}): AppConfig => ({
  appName: 'Studio', iconUrl: null, iconUrlDark: null, faviconUrl: null, faviconUrlDark: null, theme: 'light', accent: 'navy', surface: 'neutral',
  kbLabel: 'Knowledge base', suggestedPrompts: [], bannerText: '', bannerColor: '#000', bannerWhenEmbedded: false,
  user: { id: 'u1', username: 'me' }, loaded: true, platform: true, libraries: [lib], ...over,
})

const ago = (s: number) => new Date(Date.now() - s * 1000).toISOString()
const DATA: Record<string, unknown> = {
  '/api/tasks': { tasks: [
    { id: '2026-10-21-review-ab12', objective: 'Review the results', state: 'input-required', running: 1, waiting: 1 },
    { id: '2026-10-20-old-cd34', objective: 'Finished long ago', state: 'completed' },
  ] },
  '/api/fleet': { agents: [{ id: 'watcher-1', name: 'Queue watcher', state: 'working' }, { id: 'gone-1', name: 'Retired one', state: 'retired' }] },
  '/api/runs': { runs: [
    { slug: 'bench-00041', workflow: 'bench', owner: null, launchedAt: ago(300), state: 'running', endedAt: null },
    { slug: 'hello-00007', workflow: 'hello', owner: 'me', launchedAt: ago(4000), state: 'completed', endedAt: ago(3900) },
    { slug: 'theirs-00001', workflow: 'theirs', owner: 'someone-else', launchedAt: ago(100), state: 'running', endedAt: null },
  ] },
  '/api/sessions': { sessions: [
    { name: 'explorer', user: 'me', type: 'endpoint', status: 'running', healthy: true, url: 'https://explorer.sessions.test/', ownHost: true, run: null, resource: null, createdAt: null },
    { name: 'stopped', user: 'me', type: 'endpoint', status: 'off', healthy: null, url: null, ownHost: false, run: null, resource: null, createdAt: null },
  ] },
  '/api/index/status': { running: false, lastSweepAt: ago(120), lastSweepMs: 10, lastChanges: [], lastError: null, sweepIntervalSec: 300 },
  '/api/user-libraries': { libraries: [], available: false },
  '/api/settings/mcp-servers': { servers: [{ name: 'crm', url: 'http://crm.test', tools: 0, error: '401 unauthorized', checkedAt: ago(5) }] },
}
let asked: string[] = []

beforeEach(() => {
  asked = []
  localStorage.clear()
  resetStatusPanelForTests()
  reloadPanelState()
  setPanelView('search')
  setSideShown('chat', false); setSideShown('page', false)
  share({ ai: { ok: true, status: 'ok', models: 3, gatewayHost: 'gw.test', message: null, lastCallFailure: null, checkedAt: ago(1) }, stats: null, authEnabled: false, model: 'model-a', modelProvider: 'Provider A' })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    asked.push(url)
    const body = DATA[url.split('?')[0]]
    return body ? new Response(JSON.stringify(body), { status: 200 }) : new Response('{}', { status: 404 })
  }))
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); location.hash = '' })

const openPanel = () => { localStorage.setItem('ade-status-panel', '1'); reloadPanelState() }
const head = (title: string) => screen.getByRole('button', { name: new RegExp(`^${title}`) })

describe('the status panel toggle', () => {
  it('opens and closes the panel and remembers the choice', () => {
    render(<><PanelToggle /><StatusPanel view="search" cfg={cfg()} /></>)
    // jsdom is 1024 wide: narrow, so closed until asked for.
    expect(screen.queryByRole('complementary', { name: 'Status' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show the status panel' }))
    expect(screen.getByRole('complementary', { name: 'Status' })).toBeInTheDocument()
    expect(localStorage.getItem('ade-status-panel')).toBe('1')
    // While it shows, the page's toggle gives way to the panel's own.
    expect(screen.queryByRole('button', { name: 'Show the status panel' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Hide the status panel' }))
    expect(screen.queryByRole('complementary', { name: 'Status' })).toBeNull()
    expect(localStorage.getItem('ade-status-panel')).toBe('0')
  })

  it('starts open when it was left open, and keeps each card as it was left', async () => {
    openPanel()
    const { unmount } = render(<StatusPanel view="search" cfg={cfg()} />)
    expect(screen.getByRole('complementary', { name: 'Status' })).toBeInTheDocument()
    await waitFor(() => expect(head('Libraries')).toHaveAttribute('aria-expanded', 'false'))
    fireEvent.click(head('Libraries'))
    expect(head('Libraries')).toHaveAttribute('aria-expanded', 'true')
    expect(JSON.parse(localStorage.getItem('ade-status-cards')!)).toEqual({ libraries: true })
    unmount()
    render(<StatusPanel view="search" cfg={cfg()} />)
    await waitFor(() => expect(head('Libraries')).toHaveAttribute('aria-expanded', 'true'))
    expect(screen.getByText('Library settings')).toBeInTheDocument()
  })

  it('steps aside for the page\'s side view, and its toggle hides the side view', () => {
    openPanel()
    setPanelView('chat')
    render(<><PanelToggle /><StatusPanel view="chat" cfg={cfg()} /></>)
    expect(screen.getByRole('complementary', { name: 'Status' })).toBeInTheDocument()
    act(() => setSideShown('chat', true))
    expect(screen.queryByRole('complementary', { name: 'Status' })).toBeNull()
    const hide = vi.fn()
    window.addEventListener('ade:side-view-hide', e => hide((e as CustomEvent).detail))
    fireEvent.click(screen.getByRole('button', { name: 'Show the status panel' }))
    expect(hide).toHaveBeenCalledWith('chat')
    expect(localStorage.getItem('ade-status-panel')).toBe('1')
  })
})

describe('the cards', () => {
  it('say the one thing worth knowing on each header line', async () => {
    openPanel()
    render(<StatusPanel view="search" cfg={cfg()} />)
    // A task waiting on the viewer; a working agent and a running run of the
    // viewer's (someone else's run is not counted).
    await waitFor(() => expect(head('Activity')).toHaveTextContent('1 needs you · 2 running'))
    expect(head('Libraries')).toHaveTextContent('synced 2m ago')
    expect(head('Model')).toHaveTextContent('model-a')
    expect(head('Connections')).toHaveTextContent('1 needs attention')
    // Activity opens by default: live work only, then the viewer's runs.
    expect(screen.getByText('Review the results')).toBeInTheDocument()
    expect(screen.queryByText('Finished long ago')).toBeNull()
    expect(screen.queryByText('Retired one')).toBeNull()
    expect(screen.queryByText('theirs')).toBeNull()
    await waitFor(() => expect(screen.getByText('explorer')).toBeInTheDocument())
    expect(screen.queryByText('stopped')).toBeNull()
  })

  it('leave out the Model card while AI features are off', async () => {
    openPanel()
    render(<StatusPanel view="search" cfg={cfg({ aiEnabled: false })} />)
    await waitFor(() => expect(head('Libraries')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /^Model/ })).toBeNull()
    expect(head('Connections')).toBeInTheDocument()
  })

  it('say what Connections lists, grouped as the platform, MCP servers, and machines with remote libraries', async () => {
    openPanel()
    const remote = { ...lib, id: 'lab', label: 'Lab index', primary: false, site: 'cluster-a' }
    render(<StatusPanel view="search" cfg={cfg({ libraries: [lib, remote] })} />)
    await waitFor(() => expect(head('Connections')).toHaveTextContent('1 needs attention'))
    fireEvent.click(head('Connections'))
    const card = document.querySelector('[data-card=connections]') as HTMLElement
    expect(card).toHaveTextContent('What this Studio connects to beyond its own folder and index.')
    const groups = [...card.querySelectorAll('.status-group')].map(g => [
      g.querySelector('.status-group-label')?.textContent,
      [...g.querySelectorAll('.status-row-name')].map(n => n.textContent),
    ])
    expect(groups).toEqual([
      ['ACTIVATE platform', ['Platform']],
      ['Attached MCP servers', ['crm']],
      ['Machines with remote libraries', ['cluster-a']],
    ])
    fireEvent.click(screen.getByRole('button', { name: 'MCP server settings' }))
    expect(location.hash).toBe('#view=settings:rag')
  })

  it('summarize from the data alone', () => {
    expect(activitySummary({ tasks: [], fleet: [], runs: [], sessions: [] }).text).toBe('idle')
    expect(activitySummary({ tasks: null, fleet: null, runs: null, sessions: [{} as never, {} as never] }).text).toBe('2 sessions')
    const lines = libraryLines([lib], [{ id: 'u-a', label: 'Mine', site: 'cluster-a', sourceDir: '~', sourceRoot: null, indexRoot: null, state: 'failed', run: null, error: 'boom', builtAt: null, directories: null, files: null, scheduler: null }], null)
    expect(lines.map(l => [l.label, l.kind, l.state])).toEqual([['Knowledge base', 'knowledge base', 'indexed'], ['Mine', 'on cluster-a', 'failed']])
    expect(librariesSummary(lines, null).text).toBe('1 failed')
    const conns = connectionLines({ platform: false, authEnabled: true, ai: null, mcp: [{ name: 'crm', tools: 4, error: null, checkedAt: ago(1) }], libs: [lib], userLibs: null })
    expect(conns.map(c => `${c.kind} ${c.name}: ${c.meta}`)).toEqual(['platform Platform: needs your key', 'mcp crm: connected, 4 tools'])
    expect(connectionsSummary(conns).text).toBe('1 needs attention')
    const ai = { ok: false, models: 0, gatewayHost: '', message: null, lastCallFailure: null, checkedAt: ago(1) }
    expect(modelSummary({ ...ai, status: 'off' }, null)).toEqual({ text: 'AI features off', tone: 'muted' })
    expect(modelSummary({ ...ai, status: 'auth' }, null)).toEqual({ text: 'model key rejected', tone: 'warning' })
  })

  it('leave out agents where the Agents page is hidden, and runs and sessions without Workflows or a platform', async () => {
    openPanel()
    const { unmount } = render(<StatusPanel view="search" cfg={cfg({ sections: ['chat', 'library', 'workflows'] })} />)
    await waitFor(() => expect(head('Activity')).toBeInTheDocument())
    expect(screen.queryByText('Review the results')).toBeNull()
    expect(screen.getByText('bench')).toBeInTheDocument()
    expect(asked).not.toContain('/api/tasks')
    expect(asked).not.toContain('/api/fleet')
    unmount()
    resetStatusPanelForTests(); asked = []
    render(<StatusPanel view="search" cfg={cfg({ platform: false })} />)
    await waitFor(() => expect(screen.getByText('Review the results')).toBeInTheDocument())
    expect(screen.queryByText('bench')).toBeNull()
    expect(screen.queryByText('explorer')).toBeNull()
    expect(asked).not.toContain('/api/runs')
    expect(asked).not.toContain('/api/sessions')
  })

  it('polls only while open, and reads attached servers without contacting them after the first check', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      openPanel()
      const { unmount } = render(<StatusPanel view="search" cfg={cfg()} />)
      await waitFor(() => expect(asked).toContain('/api/settings/mcp-servers'))
      const first = asked.filter(u => u === '/api/tasks').length
      await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
      expect(asked.filter(u => u === '/api/tasks').length).toBe(first + 1)
      expect(asked).toContain('/api/settings/mcp-servers?cached=1')
      // The platform's session list waits for its minute.
      expect(asked.filter(u => u === '/api/sessions').length).toBe(1)
      unmount()
      const n = asked.length
      await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
      expect(asked.length).toBe(n)
    } finally { vi.useRealTimers() }
  })
})

describe('opening from the panel', () => {
  it('sends a run to the chat\'s side view on the chat page', async () => {
    openPanel()
    const post = vi.spyOn(window, 'postMessage')
    render(<StatusPanel view="chat" cfg={cfg()} />)
    fireEvent.click(await screen.findByText('bench'))
    expect(post).toHaveBeenCalledWith({ type: 'ade-side-view', run: 'bench-00041' }, window.location.origin)
  })

  it('goes to the Workflows page from elsewhere and opens the run in its side view', async () => {
    openPanel()
    const post = vi.spyOn(window, 'postMessage')
    render(<StatusPanel view="library" cfg={cfg()} />)
    fireEvent.click(await screen.findByText('bench'))
    expect(location.hash).toBe('#view=workflows')
    expect(post).not.toHaveBeenCalled()
    // The Workflows page's side view takes it once it is mounted.
    render(<PageSideViewHost />)
    expect(post).toHaveBeenCalledWith({ type: 'ade-side-view', target: 'page', run: 'bench-00041' }, window.location.origin)
  })

  it('opens an agent\'s task on the Agents page', async () => {
    openPanel()
    render(<StatusPanel view="library" cfg={cfg()} />)
    fireEvent.click(await screen.findByText('Review the results'))
    expect(location.hash).toBe('#view=agents:2026-10-21-review-ab12')
    fireEvent.click(screen.getByText('Queue watcher'))
    expect(location.hash).toBe('#view=agents:fleet')
  })
})
