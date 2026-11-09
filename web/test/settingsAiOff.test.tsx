import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AI_ONLY_SECTIONS, SettingsView, settingsSections, shownSettingsTarget } from '../src/views/SettingsView'

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
  appName: 'Studio', kbLabel: 'Notes', theme: 'light', accent: 'navy', surface: 'neutral', navWidth: 240, iconUrl: '', iconUrlDark: '',
  sweepIntervalSec: 300, searchEngine: 'parallel', historyIntervalSec: 86400, suggestedPrompts: ['What is in my folder?'],
  visionModel: '', disabledTools: [], customTools: [], ragDefaultModel: '', ragTopK: 6, ragAllowDeploymentKey: false,
  ragAdvertiseRagModel: false, ragAdvertiseAgentModel: true, mcpEnabled: true, mcpServers: [], delegationEnabled: true,
  delegationMaxAgents: 6, maxToolRounds: 50, delegationMaxDepth: 1, agentExecution: 'runner', voiceEnabled: false, voiceUrl: '',
  ragProxyEnabled: true, ragEndpointAutoStart: false, ragEndpointName: '', requirePersonalKey: false, chatSharedHistory: true,
  bannerText: '', bannerColor: '#24612e', bannerWhenEmbedded: false, allowKeySharing: true, kbMission: 'My own files.',
  sections: ['library', 'search', 'query', 'overview', 'history', 'settings', 'help'], aiEnabled: false,
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

interface Server { saved: Record<string, unknown>; puts: Record<string, unknown>[]; gets: string[] }

function stub(over: Record<string, unknown> = {}): Server {
  const s: Server = { saved: { ...EFFECTIVE, ...over }, puts: [], gets: [] }
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    if (method === 'GET') s.gets.push(url)
    if (url === '/api/settings' && method === 'PUT') {
      const body = JSON.parse(String(init!.body))
      s.puts.push(body)
      s.saved = { ...s.saved, ...body }
      return reply({ effective: s.saved })
    }
    if (url === '/api/settings') return reply({ effective: s.saved, deploymentCredential: false })
    if (url.startsWith('/api/setup/status')) return reply({ firstRun: false, standalone: true, branding: { light: null, dark: null } })
    if (url === '/api/me/model-key') return reply({ authEnabled: false, single: true, verified: true, mode: 'none' })
    if (url === '/api/config') return reply({ appName: 'Studio', aiEnabled: s.saved.aiEnabled, user: { id: 'u', username: 'u' } })
    if (url === '/api/chat/tools') return reply({ tools: [{ name: 'search_kb', description: 'Search the corpus', builtin: true, enabled: true }] })
    if (url === '/api/updates') return reply({ error: 'not here' }, 404)
    return reply({})
  }))
  return s
}

const at = (hash: string) => history.replaceState(null, '', `${location.pathname}${hash}`)
const rail = () => [...document.querySelectorAll('.help-nav button:not(.help-nav-group-head)')].map(b => b.textContent)
/** Requests to routes only the assistant's settings use. */
const AI_ROUTES = ['/api/chat/models', '/api/chat/tools', '/api/extensions', '/api/tasks/runtime', '/api/rag/endpoint', '/api/rag/calls', '/api/settings/mcp-servers']
const aiReads = (s: Server) => s.gets.filter(u => AI_ROUTES.some(r => u.startsWith(r)))

beforeEach(() => { localStorage.clear() })
afterEach(() => { vi.unstubAllGlobals(); at('') })

describe('the settings rail with AI features off', () => {
  it('leaves out the assistant sections, and names Model access for the ACTIVATE credential it keeps', () => {
    const on = settingsSections({ authEnabled: false, single: true, platform: true })
    const off = settingsSections({ authEnabled: false, single: true, platform: true, aiEnabled: false })
    expect(on.map(s => s.id)).toEqual(['general', 'indexing', 'workflows', 'access', 'tools', 'previews', 'ext', 'rag'])
    expect(off.map(s => s.id)).toEqual(['general', 'indexing', 'workflows', 'access', 'rag'])
    expect(off.find(s => s.id === 'access')).toEqual({ id: 'access', label: 'ACTIVATE access', group: 'Studio' })
    expect(off.some(s => s.group === 'Assistant')).toBe(false)
    // Without sign-in or a single user there is no access section either way.
    expect(settingsSections({ authEnabled: false, aiEnabled: false }).map(s => s.id)).toEqual(['general', 'indexing', 'rag'])
  })

  it('sends an AI-only section to General, AI features, only while AI features are off', () => {
    for (const id of AI_ONLY_SECTIONS) {
      expect(shownSettingsTarget({ section: id, tab: 'appearance' }, false)).toEqual({ section: 'general', tab: 'ai' })
      expect(shownSettingsTarget({ section: id, tab: 'appearance' }, true)).toEqual({ section: id, tab: 'appearance' })
    }
    expect(shownSettingsTarget({ section: 'rag', tab: 'appearance' }, false)).toEqual({ section: 'rag', tab: 'appearance' })
    expect(shownSettingsTarget({ section: 'access', tab: 'appearance' }, false)).toEqual({ section: 'access', tab: 'appearance' })
  })
})

describe('Settings with AI features off', () => {
  it('shows no AI-only section or field, and reads none of their routes', async () => {
    const s = stub()
    at('#view=settings:ai')
    render(<SettingsView />)
    const sw = await screen.findByRole('switch', { name: 'AI features' })
    expect(sw).toHaveAttribute('aria-checked', 'false')
    expect(rail()).toEqual(['General', 'Indexing', 'ACTIVATE access', 'External access'])
    const panel = screen.getByRole('tabpanel')
    expect(within(panel).getByText(/^Off\. On adds Chat and Agents/)).toBeInTheDocument()
    for (const label of ['Mission and context', 'Suggested questions (one per line)', 'Vision model for image captioning']) {
      expect(within(panel).queryByText(label)).toBeNull()
    }
    expect(within(panel).queryByRole('button', { name: 'Save settings' })).toBeNull()

    // The sidebar offers no switch for Chat or Agents.
    fireEvent.click(screen.getByRole('tab', { name: 'Sidebar' }))
    expect(await screen.findByRole('switch', { name: 'Library' })).toBeInTheDocument()
    expect(screen.queryByRole('switch', { name: 'Chat' })).toBeNull()
    expect(screen.queryByRole('switch', { name: 'Agents' })).toBeNull()

    // External access keeps MCP and nothing that runs the assistant.
    fireEvent.click(screen.getByRole('button', { name: 'External access' }))
    expect(await screen.findByRole('switch', { name: 'Serve MCP' })).toBeInTheDocument()
    expect(screen.getByText('Other MCP clients (settings JSON)')).toBeInTheDocument()
    for (const name of ['Delegate to concurrent agents', 'Serve the grounded model']) expect(screen.queryByRole('switch', { name })).toBeNull()
    expect(screen.queryByText('Attached MCP servers (tools from elsewhere)')).toBeNull()
    expect(screen.queryByText(/Individual tools can be disabled/)).toBeNull()

    expect(aiReads(s)).toEqual([])
  })

  it('an AI-only address lands on General, AI features, with the switch', async () => {
    stub()
    for (const id of ['tools', 'previews', 'ext']) {
      at(`#view=settings:${id}`)
      const { unmount } = render(<SettingsView />)
      expect(await screen.findByRole('switch', { name: 'AI features' })).toBeInTheDocument()
      expect(screen.getByRole('tab', { name: 'AI features' })).toHaveAttribute('aria-selected', 'true')
      await waitFor(() => expect(location.hash).toBe('#view=settings:ai'))
      expect(screen.queryByRole('heading', { level: 1, name: /Assistant tools|Feature previews|Extensions/ })).toBeNull()
      unmount()
    }
  })

  it('switching AI features on brings every section and field back at once, without a reload', async () => {
    const s = stub()
    at('#view=settings:tools')
    render(<SettingsView />)
    const sw = await screen.findByRole('switch', { name: 'AI features' })
    fireEvent.click(sw)
    await waitFor(() => expect(s.puts.at(-1)).toMatchObject({ aiEnabled: true }))
    // Chat and Agents go back into the sidebar's pages with it.
    expect(s.puts.at(-1)!.sections).toEqual(expect.arrayContaining(['chat', 'agents', 'library']))

    // The Assistant group is back, closed as groups are when they do not
    // hold the page open, which stays General, AI features.
    fireEvent.click(await screen.findByRole('button', { name: 'Assistant' }))
    await waitFor(() => expect(rail()).toEqual(['General', 'Indexing', 'Model access', 'Assistant tools', 'Feature previews', 'Extensions', 'External access']))
    expect(screen.getByRole('tab', { name: 'AI features' })).toHaveAttribute('aria-selected', 'true')
    const panel = screen.getByRole('tabpanel')
    for (const label of ['Mission and context', 'Suggested questions (one per line)', 'Vision model for image captioning']) {
      expect(within(panel).getByText(label)).toBeInTheDocument()
    }
    // The assistant's routes are read now that their settings show.
    await waitFor(() => expect(aiReads(s)).toEqual(expect.arrayContaining(['/api/chat/tools', '/api/settings/mcp-servers', '/api/extensions'])))

    fireEvent.click(screen.getByRole('button', { name: 'Assistant tools' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Assistant tools' })).toBeInTheDocument()
    expect(await screen.findByRole('checkbox', { name: 'search_kb' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'External access' }))
    expect(await screen.findByRole('switch', { name: 'Serve the grounded model' })).toBeInTheDocument()
    expect(screen.getByRole('switch', { name: 'Delegate to concurrent agents' })).toBeInTheDocument()
  })
})

describe('Settings, General, Appearance with Branding', () => {
  it('#view=settings:branding opens Appearance at the Branding group', async () => {
    stub({ aiEnabled: true })
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled
    at('#view=settings:branding')
    render(<SettingsView />)
    expect(await screen.findByRole('tab', { name: 'Appearance' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('heading', { level: 2, name: 'Branding' })).toHaveAttribute('id', 'settings-branding')
    await waitFor(() => expect(scrolled).toHaveBeenCalled())
    expect(scrolled.mock.contexts[0]).toBe(document.getElementById('settings-branding'))
  })

  it('saves the name as it is typed and on leaving the box, and the banner only with its own Save', async () => {
    const s = stub({ aiEnabled: true })
    at('#view=settings:appearance')
    render(<SettingsView />)
    const name = await screen.findByLabelText('Product name')
    fireEvent.change(name, { target: { value: 'Lab notes' } })
    fireEvent.blur(name)
    await waitFor(() => expect(s.puts).toContainEqual({ appName: 'Lab notes' }))
    expect(screen.queryByRole('button', { name: 'Save settings' })).toBeNull()

    const n = s.puts.length
    fireEvent.change(screen.getByPlaceholderText('Text across the top, or pick a marking'), { target: { value: '***** UNCLASSIFIED *****' } })
    await new Promise(r => setTimeout(r, 900))
    expect(s.puts.length).toBe(n)
    fireEvent.click(screen.getByRole('button', { name: 'Save banner' }))
    await waitFor(() => expect(s.puts.at(-1)).toEqual({ bannerText: '***** UNCLASSIFIED *****', bannerColor: '#24612e', bannerWhenEmbedded: false }))
  })
})
