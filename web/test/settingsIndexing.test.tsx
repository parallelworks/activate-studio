import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ADD_LIBRARY_HASH } from '../src/components/UploadMenu'
import { INDEXING_TABS, SettingsView, settingsSections, settingsTarget } from '../src/views/SettingsView'

// A switch or checkbox forwards its press to its hidden input as a
// PointerEvent, which jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

// The list of libraries has tests of its own (librariesPage); here it only
// has to be on the right tab, without its page heading.
vi.mock('../src/components/LibrariesSection', () => ({
  KINDS: {},
  LibrariesSection: ({ heading = true }: { heading?: boolean }) => (
    <p>The libraries list{heading ? ', with its heading' : ''}</p>
  ),
}))

const EFFECTIVE = {
  appName: 'Studio', kbLabel: 'Notes', theme: 'light', accent: 'navy', surface: 'neutral', iconUrl: '', iconUrlDark: '',
  sweepIntervalSec: 300, searchEngine: 'parallel', gufiLog: false, historyIntervalSec: 86400, suggestedPrompts: [],
  visionModel: '', disabledTools: [], customTools: [], ragDefaultModel: '', ragTopK: 6, ragAllowDeploymentKey: false,
  ragAdvertiseRagModel: false, ragAdvertiseAgentModel: true, mcpEnabled: true, mcpServers: [], delegationEnabled: true,
  delegationMaxAgents: 6, maxToolRounds: 50, delegationMaxDepth: 1, agentExecution: 'runner', voiceEnabled: false, voiceUrl: '',
  ragProxyEnabled: true, ragEndpointAutoStart: false, ragEndpointName: '', requirePersonalKey: false, chatSharedHistory: true,
  bannerText: '', bannerColor: '#24612e', bannerWhenEmbedded: false, allowKeySharing: true, kbMission: '',
  sections: [], aiEnabled: true,
}

const SETTINGS = { threads: null, depth: 'text', documents: true, pdfReader: 'auto', ocr: false, imageDescriptions: false, legacyOffice: false, meaning: 'off', meaningSpeed: 'standard' }
const INDEX_INFO = {
  settings: SETTINGS, defaults: SETTINGS, single: true, cpus: 8, threads: 8,
  readers: { extractors: [], formats: [{ formats: 'PDF', readers: ['pdftotext'], ok: true, note: '' }], missing: [], install: { platform: 'linux', brew: null, apt: true, items: [] } },
  ai: { enabled: true, visionModel: '' },
  location: { current: '/data/index', default: '/data/index', folder: '/home/u/Notes', canChange: true, saved: '' },
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function stub() {
  const s = { puts: [] as Record<string, unknown>[], indexPuts: [] as unknown[] }
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input).split('?')[0]
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (url === '/api/settings' && method === 'PUT') { s.puts.push(body); return reply({ effective: { ...EFFECTIVE, ...body } }) }
    if (url === '/api/settings') return reply({ effective: EFFECTIVE, deploymentCredential: false, gufiLogFile: { path: '/data/logs/gufi-commands.log', bytes: 2048 } })
    if (url === '/api/setup/status') return reply({ firstRun: false, standalone: true, canRestart: true, launcher: true, port: 4180 })
    if (url === '/api/me/model-key') return reply({ authEnabled: false, single: true, verified: true, mode: 'none' })
    if (url === '/api/index/settings' && method === 'PUT') { s.indexPuts.push(body); return reply({ settings: { ...SETTINGS, ...body }, threads: 8, readers: INDEX_INFO.readers }) }
    if (url === '/api/index/settings') return reply(INDEX_INFO)
    if (url === '/api/index/status') return reply({ running: false, job: null, queued: [], last: null, lastBuild: null, runs: [], indexed: true, threads: 8 })
    return reply({})
  }))
  return s
}

const at = (hash: string) => history.replaceState(null, '', `${location.pathname}${hash}`)
const tabNamed = (name: string) => screen.getByRole('tab', { name })
const panel = () => screen.getByRole('tabpanel')
const saveButton = () => within(panel()).queryByRole('button', { name: 'Save settings' })

beforeEach(() => { localStorage.clear() })
afterEach(() => { vi.unstubAllGlobals(); at('') })

describe('Settings, Indexing', () => {
  it('sits under Studio after General, and Libraries is no longer a section of its own', () => {
    const rows = settingsSections({ authEnabled: false })
    expect(rows.slice(0, 2)).toEqual([
      { id: 'general', label: 'General', group: 'Studio' },
      { id: 'indexing', label: 'Indexing', group: 'Studio' },
    ])
    expect(rows.map(r => r.id)).not.toContain('libraries')
    expect(INDEXING_TABS.map(t => t.label)).toEqual(['Libraries', 'Sync and search', 'Building the index', 'Storage and logs'])
  })

  it('maps its tabs and the old addresses to the section', () => {
    expect(settingsTarget('libraries')).toEqual({ section: 'indexing', tab: 'libraries' })
    expect(settingsTarget('sync-search')).toEqual({ section: 'indexing', tab: 'sync-search' })
    expect(settingsTarget('building')).toEqual({ section: 'indexing', tab: 'building' })
    expect(settingsTarget('storage')).toEqual({ section: 'indexing', tab: 'storage' })
    // General's Indexing tab opened on the search and sync settings.
    expect(settingsTarget('indexing')).toEqual({ section: 'indexing', tab: 'sync-search' })
  })

  it('#view=settings:libraries opens the Libraries tab, without a second heading', async () => {
    stub()
    at('#view=settings:libraries')
    render(<SettingsView />)
    expect(await screen.findByRole('heading', { level: 1, name: 'Indexing' })).toBeInTheDocument()
    expect(tabNamed('Libraries')).toHaveAttribute('aria-selected', 'true')
    expect(within(panel()).getByText('The libraries list')).toBeInTheDocument()
    expect(saveButton()).toBeNull()
    // The rail shows Indexing as the section open.
    expect(screen.getByRole('button', { name: 'Indexing' })).toHaveClass('active')
    expect(screen.queryByRole('button', { name: 'Libraries' })).toBeNull()
  })

  it('the + Add menu\'s address opens the Libraries tab and keeps ?add=1 for its form', async () => {
    stub()
    at(ADD_LIBRARY_HASH)
    render(<SettingsView />)
    expect(await screen.findByRole('tab', { name: 'Libraries' })).toHaveAttribute('aria-selected', 'true')
    expect(location.hash).toBe('#view=settings:libraries?add=1')
  })

  it('#view=settings:indexing opens Sync and search, which saves through Save settings', async () => {
    const s = stub()
    at('#view=settings:indexing')
    render(<SettingsView />)
    expect(await screen.findByRole('tab', { name: 'Sync and search' })).toHaveAttribute('aria-selected', 'true')
    for (const l of ['Search across libraries', 'Background sync interval (seconds, 0 pauses)', 'Corpus history snapshot (seconds between points)']) {
      expect(within(panel()).getByText(l)).toBeInTheDocument()
    }
    const box = within(panel()).getByRole('textbox', { name: 'Background sync interval (seconds, 0 pauses)' })
    fireEvent.change(box, { target: { value: '600' } })
    fireEvent.blur(box)
    fireEvent.click(saveButton()!)
    await waitFor(() => expect(s.puts.at(-1)?.sweepIntervalSec).toBe(600))
    expect(s.puts.at(-1)?.searchEngine).toBe('parallel')
  })

  it('the rail opens the section on Libraries, and each tab has its own address', async () => {
    stub()
    at('#view=settings:general')
    render(<SettingsView />)
    fireEvent.click(await screen.findByRole('button', { name: 'Indexing' }))
    expect(location.hash).toBe('#view=settings:libraries')
    expect(tabNamed('Libraries')).toHaveAttribute('aria-selected', 'true')
    for (const t of INDEXING_TABS.slice(1)) {
      fireEvent.click(tabNamed(t.label))
      expect(location.hash).toBe(`#view=settings:${t.value}`)
    }
    // As App routes an address while Settings is open.
    act(() => { window.dispatchEvent(new CustomEvent('ade-view-section', { detail: { view: 'settings', section: 'libraries' } })) })
    expect(tabNamed('Libraries')).toHaveAttribute('aria-selected', 'true')
  })

  it('Building the index holds how it is built, what is read and what this computer reads, saved as they change', async () => {
    const s = stub()
    at('#view=settings:building')
    render(<SettingsView />)
    expect(await screen.findByRole('heading', { level: 2, name: 'How the index is built' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'What is read from files' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'What this computer can read' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 2, name: 'Where the index is kept' })).toBeNull()
    expect(saveButton()).toBeNull()
    fireEvent.click(screen.getByRole('switch', { name: /LibreOffice/ }))
    await waitFor(() => expect(s.indexPuts).toContainEqual({ legacyOffice: true }))
    expect(s.puts).toEqual([])
  })

  it('Storage and logs holds where the index is kept and the GUFI command log, which Save settings saves', async () => {
    const s = stub()
    at('#view=settings:storage')
    render(<SettingsView />)
    expect(await screen.findByRole('heading', { level: 2, name: 'Where the index is kept' })).toBeInTheDocument()
    expect(screen.getByText('/data/index')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Choose another place…' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 2, name: 'How the index is built' })).toBeNull()
    const box = screen.getByRole('checkbox', { name: /Log GUFI commands/ })
    expect(screen.queryByText('/data/logs/gufi-commands.log')).toBeNull()
    fireEvent.click(box)
    expect(screen.getByText('/data/logs/gufi-commands.log')).toBeInTheDocument()
    expect(screen.getByText(/One JSON object per line in/)).toHaveTextContent('(2 KB), one previous file kept past 10 MB.')
    fireEvent.click(saveButton()!)
    await waitFor(() => expect(s.puts.at(-1)?.gufiLog).toBe(true))
  })
})
