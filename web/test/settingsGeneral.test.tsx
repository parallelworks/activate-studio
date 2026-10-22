import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { openUpdates } from '../src/api'
import { ServerSection, addressOn } from '../src/components/ServerSection'
import { GENERAL_TABS, SettingsView, settingsTarget } from '../src/views/SettingsView'

// A switch forwards its press to its hidden input as a PointerEvent, which
// jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const EFFECTIVE = {
  appName: 'Studio', kbLabel: 'Notes', theme: 'light', accent: 'navy', surface: 'neutral', iconUrl: '', iconUrlDark: '',
  sweepIntervalSec: 300, searchEngine: 'parallel', historyIntervalSec: 86400, suggestedPrompts: ['What is in my folder?'],
  visionModel: '', disabledTools: [], customTools: [], ragDefaultModel: '', ragTopK: 6, ragAllowDeploymentKey: false,
  ragAdvertiseRagModel: false, ragAdvertiseAgentModel: true, mcpEnabled: true, mcpServers: [], delegationEnabled: true,
  delegationMaxAgents: 6, maxToolRounds: 50, delegationMaxDepth: 1, agentExecution: 'runner', voiceEnabled: false, voiceUrl: '',
  ragProxyEnabled: true, ragEndpointAutoStart: false, ragEndpointName: '', requirePersonalKey: false, chatSharedHistory: true,
  bannerText: '', bannerColor: '#24612e', bannerWhenEmbedded: false, allowKeySharing: true, kbMission: 'My own files.',
  sections: [], aiEnabled: true,
}

interface Server {
  setup: Record<string, unknown>
  puts: Record<string, unknown>[]
  posts: { url: string; body: unknown }[]
  portReply: Record<string, unknown>
  noCors: string[]
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function stub(over: Partial<Server> = {}): Server {
  const s: Server = {
    setup: { firstRun: false, standalone: true, canRestart: true, launcher: true, port: 4180, portNext: null, branding: { light: null, dark: null } },
    puts: [], posts: [], noCors: [],
    portReply: { saved: true, restarting: true, port: 4190 },
    ...over,
  }
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (init?.mode === 'no-cors') { s.noCors.push(url); return new Response(null, { status: 200 }) }
    if (url === '/api/settings' && method === 'PUT') { s.puts.push(body); return reply({ effective: { ...EFFECTIVE, ...body } }) }
    if (url === '/api/settings') return reply({ effective: EFFECTIVE, deploymentCredential: false })
    if (url.startsWith('/api/setup/status')) return reply(s.setup)
    if (url === '/api/setup/port') { s.posts.push({ url, body }); return reply(s.portReply) }
    if (url === '/api/me/model-key') return reply({ authEnabled: false, single: true, verified: true, mode: 'none' })
    if (url === '/api/config') return reply({ appName: 'Studio', user: { id: 'u', username: 'u' } })
    if (url === '/api/updates') return reply({ error: 'not here' }, 404)
    return reply({})
  }))
  return s
}

const tabNamed = (name: string) => screen.getByRole('tab', { name })
const at = (hash: string) => history.replaceState(null, '', `${location.pathname}${hash}`)

beforeEach(() => { localStorage.clear() })
afterEach(() => { vi.unstubAllGlobals(); at('') })

describe('Settings, General tabs', () => {
  it('maps an address to its section and tab, old ones included', () => {
    expect(settingsTarget('general')).toEqual({ section: 'general', tab: 'appearance' })
    expect(settingsTarget(undefined)).toEqual({ section: 'general', tab: 'appearance' })
    expect(settingsTarget('updates')).toEqual({ section: 'general', tab: 'updates' })
    expect(settingsTarget('server')).toEqual({ section: 'general', tab: 'server' })
    expect(settingsTarget('access')).toEqual({ section: 'access', tab: 'appearance' })
    expect(settingsTarget('libraries')).toEqual({ section: 'libraries', tab: 'appearance' })
    expect(settingsTarget('nonsense')).toEqual({ section: 'general', tab: 'appearance' })
    // No tab shares an id with a section.
    for (const t of GENERAL_TABS) expect(settingsTarget(t.value).section).toBe('general')
  })

  it('opens #view=settings:general on Appearance, keeps every setting a tab away, and gives each tab its address', async () => {
    stub()
    at('#view=settings:general')
    render(<SettingsView />)
    expect(await screen.findByRole('heading', { level: 1, name: 'General' })).toBeInTheDocument()
    expect(tabNamed('Appearance')).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('button', { name: 'Run setup again' })).toBeInTheDocument()
    // Without updates (a server that has none), no Updates tab.
    await waitFor(() => expect(screen.queryByRole('tab', { name: 'Updates' })).toBeNull())

    // Appearance last, since a click on the tab already open changes nothing.
    const where: Record<string, (string | RegExp)[]> = {
      Branding: ['Product name', 'Icon', 'Classification banner (empty for none)'],
      Sidebar: ['Library', 'Search', 'Query'],
      'AI features': ['Chat, Agents, and other features that call a model', 'Mission and context', 'Suggested questions (one per line)', 'Vision model for image captioning'],
      Indexing: ['Search across libraries', 'Background sync interval (seconds, 0 pauses)', 'Corpus history snapshot (seconds between points)'],
      Server: ['Port'],
      Appearance: ['Theme', 'Surface tone', 'Accent color'],
    }
    for (const [tab, labels] of Object.entries(where)) {
      fireEvent.click(tabNamed(tab))
      const id = GENERAL_TABS.find(t => t.label === tab)!.value
      expect(location.hash).toBe(`#view=settings:${id}`)
      const panel = screen.getByRole('tabpanel')
      for (const l of labels) {
        await waitFor(() => expect(within(panel).getAllByText(l).length).toBeGreaterThan(0), { timeout: 2000 })
      }
    }
    // A tab's own address opens it.
    at('#view=settings:indexing')
    act(() => { window.dispatchEvent(new CustomEvent('ade-view-section', { detail: { view: 'settings', section: 'indexing' } })) })
    expect(tabNamed('Indexing')).toHaveAttribute('aria-selected', 'true')
  })

  it('#view=settings:setup, the address the macOS menu uses, opens setup over General where setup runs', async () => {
    stub()
    const opened = vi.fn()
    window.addEventListener('ade:setup-open', opened)
    try {
      at('#view=settings:setup')
      const { unmount } = render(<SettingsView />)
      await waitFor(() => expect(opened).toHaveBeenCalledTimes(1))
      expect(location.hash).toBe('#view=settings:general')
      expect(tabNamed('Appearance')).toHaveAttribute('aria-selected', 'true')
      // Asked again while Settings is open.
      act(() => { window.dispatchEvent(new CustomEvent('ade-view-section', { detail: { view: 'settings', section: 'setup' } })) })
      await waitFor(() => expect(opened).toHaveBeenCalledTimes(2))
      // Run setup again sits beside the General heading, on every tab.
      fireEvent.click(screen.getByRole('button', { name: 'Run setup again' }))
      expect(opened).toHaveBeenCalledTimes(3)
      unmount()

      // Where users sign in there is no setup to open.
      stub({ setup: { firstRun: false, standalone: false, canRestart: false } })
      at('#view=settings:setup')
      render(<SettingsView />)
      await waitFor(() => expect(location.hash).toBe('#view=settings:general'))
      expect(opened).toHaveBeenCalledTimes(3)
      expect(screen.queryByRole('button', { name: 'Run setup again' })).toBeNull()
    } finally { window.removeEventListener('ade:setup-open', opened) }
  })

  it('the update notice and the footer open the Updates tab', async () => {
    stub()
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url === '/api/updates') {
        return reply({ mode: 'app', current: { version: 'v1.140', builtAt: null }, policy: { check: true, install: 'manual', channel: 'stable' },
          latest: null, available: false, canInstall: true, reason: null, state: 'idle', progress: null, error: null, checkedAt: null, previous: null })
      }
      if (url === '/api/settings') return reply({ effective: EFFECTIVE })
      if (url.startsWith('/api/setup/status')) return reply({ standalone: true, launcher: true, port: 4180 })
      return reply({})
    }))
    at('#view=settings:branding')
    render(<SettingsView />)
    expect(await screen.findByRole('tab', { name: 'Branding' })).toHaveAttribute('aria-selected', 'true')
    openUpdates()
    expect(location.hash).toBe('#view=settings:updates')
    // The app routes the address to the page, as App's hash handler does.
    act(() => { window.dispatchEvent(new CustomEvent('ade-view-section', { detail: { view: 'settings', section: 'updates' } })) })
    expect(await screen.findByRole('tab', { name: 'Updates' })).toHaveAttribute('aria-selected', 'true')
  })

  it('Appearance saves as it changes: the theme for this window and as the default, the accent and surface', async () => {
    const s = stub()
    at('#view=settings:appearance')
    render(<SettingsView />)
    fireEvent.click(await screen.findByRole('button', { name: 'Dark' }))
    await waitFor(() => expect(s.puts).toContainEqual({ theme: 'dark' }))
    expect(localStorage.getItem('ade-theme')).toBe('dark')
    fireEvent.click(screen.getByRole('button', { name: 'Teal' }))
    await waitFor(() => expect(s.puts).toContainEqual({ accent: 'teal' }))
  })

  it('Feature previews: voice says what it does, and shows its setup only when on', async () => {
    stub()
    at('#view=settings:previews')
    render(<SettingsView />)
    expect(await screen.findByText(/^Voice conversations: talk with the assistant out loud/)).toBeInTheDocument()
    expect(screen.getByRole('switch', { name: 'Voice conversations' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.queryByLabelText('Unmute session URL')).toBeNull()
    expect(screen.queryByText(/Kyutai/)).toBeNull()
    fireEvent.click(screen.getByRole('switch', { name: 'Voice conversations' }))
    expect(await screen.findByLabelText('Unmute session URL')).toBeInTheDocument()
    expect(screen.getByText(/Kyutai/)).toBeInTheDocument()
  })
})

describe('Settings, General, Server', () => {
  it('saves a new port for the launcher, and the macOS window follows the Studio there', async () => {
    const s = stub()
    const asked: unknown[] = []
    vi.stubGlobal('webkit', { messageHandlers: { studioShell: { postMessage: async (m: unknown) => { asked.push(m); return true } } } })
    at('#view=settings:server')
    render(<ServerSection pollMs={5} navigate={() => { throw new Error('the window moves itself') }} />)
    expect(await screen.findByText(/now 4180/)).toBeInTheDocument()
    expect(screen.getByText(`${location.origin}/api/mcp`)).toBeInTheDocument()
    const box = screen.getByRole('textbox', { name: 'Port' })
    expect(screen.getByRole('button', { name: 'Save and restart' })).toBeDisabled()
    fireEvent.change(box, { target: { value: '4190' } })
    fireEvent.blur(box)
    fireEvent.click(screen.getByRole('button', { name: 'Save and restart' }))
    await waitFor(() => expect(asked).toEqual([{ action: 'studioMoved', port: 4190, hash: '#view=settings:server' }]))
    expect(s.posts).toEqual([{ url: '/api/setup/port', body: { port: 4190 } }])
    expect(screen.getByText('Restarting the Studio on port 4190…')).toBeInTheDocument()
  })

  it('in a browser, moves to the new address once it answers', async () => {
    const s = stub()
    const went: string[] = []
    at('#view=settings:server')
    render(<ServerSection pollMs={5} navigate={u => went.push(u)} />)
    const box = await screen.findByRole('textbox', { name: 'Port' })
    fireEvent.change(box, { target: { value: '4190' } })
    fireEvent.blur(box)
    fireEvent.click(screen.getByRole('button', { name: 'Save and restart' }))
    await waitFor(() => expect(went).toEqual([`${addressOn(4190)}#view=settings:server`]))
    expect(s.noCors[0]).toBe(`${addressOn(4190)}api/version`)
    expect(addressOn(4190, { protocol: 'http:', hostname: '127.0.0.1' })).toBe('http://127.0.0.1:4190/')
  })

  it('says a saved port waits for the next start, and without a launcher names the variable', async () => {
    stub({ portReply: { saved: true, restarting: false, port: 4190, note: 'Saved. Restart the Studio to use this port.' } })
    const { unmount } = render(<ServerSection pollMs={5} />)
    const box = await screen.findByRole('textbox', { name: 'Port' })
    fireEvent.change(box, { target: { value: '4190' } })
    fireEvent.blur(box)
    fireEvent.click(screen.getByRole('button', { name: 'Save and restart' }))
    expect(await screen.findByText('Saved. Restart the Studio to use this port.')).toBeInTheDocument()
    expect(screen.getByText('Port 4190 is saved for the next start.')).toBeInTheDocument()
    unmount()

    stub({ setup: { standalone: true, launcher: false, port: 4080 } })
    render(<ServerSection />)
    expect(await screen.findByText(/It is the/)).toHaveTextContent('PORT environment variable, read when the Studio starts')
    expect(screen.queryByRole('textbox', { name: 'Port' })).toBeNull()
  })
})
