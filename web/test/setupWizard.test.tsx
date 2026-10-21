import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SECTION_SWITCHES, sectionAllowed, sectionsSetting, shownView } from '../src/config'
import { SetupWizard, SidebarSwitches } from '../src/views/SetupWizard'

// The real form arrives with the ssh library support; this one adds a
// library the way it reports one.
vi.mock('../src/components/SshLibraryForm', () => ({
  SshLibraryForm: ({ onAdded }: { onAdded?: (lib: { id: string; label: string }) => void }) => (
    <button type="button" onClick={() => onAdded?.({ id: 'lab', label: 'Lab index' })}>Add test index</button>
  ),
}))

// A switch forwards its press to its hidden input as a PointerEvent, which
// jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const ORDER = ['chat', 'library', 'search', 'query', 'overview', 'agents', 'workflows', 'history', 'settings', 'help']

describe('sidebar sections', () => {
  it('shows every page when nothing is saved, and Settings and Help always', () => {
    expect(SECTION_SWITCHES.every(s => s.id === 'workflows' || sectionAllowed(s.id, {}))).toBe(true)
    const cfg = { sections: ['library', 'search', 'settings', 'help'] }
    expect(sectionAllowed('chat', cfg)).toBe(false)
    expect(sectionAllowed('agents', cfg)).toBe(false)
    expect(sectionAllowed('library', cfg)).toBe(true)
    expect(sectionAllowed('settings', { sections: ['library'] })).toBe(true)
    expect(sectionAllowed('help', { sections: ['library'] })).toBe(true)
    // Workflows lists platform workflows, so it needs a platform as well.
    expect(sectionAllowed('workflows', {})).toBe(false)
    expect(sectionAllowed('workflows', { platform: true })).toBe(true)
  })

  it('sends a hidden page, from a link or the address, to the first one shown', () => {
    const cfg = { sections: ['search', 'query', 'settings', 'help'] }
    expect(shownView('agents', ORDER, cfg)).toBe('search')
    expect(shownView('chat', ORDER, cfg)).toBe('search')
    expect(shownView('query', ORDER, cfg)).toBe('query')
    expect(shownView('chat', ORDER, {})).toBe('chat')
    expect(shownView('library', ORDER, { sections: ['settings', 'help'] })).toBe('settings')
  })

  it('saves nothing when every page is on, so a page added later shows too', () => {
    expect(sectionsSetting(SECTION_SWITCHES.map(s => s.id))).toEqual([])
    expect(sectionsSetting(['agents', 'library'])).toEqual(['library', 'agents', 'settings', 'help'])
    expect(sectionsSetting([])).toEqual(['settings', 'help'])
  })

  it('switches reflect the saved list and report the new one', () => {
    const onChange = vi.fn()
    render(<SidebarSwitches value={['library', 'search', 'settings', 'help']} platform={false} onChange={onChange} />)
    expect(screen.getByRole('switch', { name: 'Chat' })).toHaveAttribute('aria-checked', 'false')
    expect(screen.getByRole('switch', { name: 'Library' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByText('(needs a platform login)')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: 'Library' }))
    expect(onChange).toHaveBeenLastCalledWith(['search', 'settings', 'help'])
    fireEvent.click(screen.getByRole('switch', { name: 'Agents' }))
    expect(onChange).toHaveBeenLastCalledWith(['search', 'agents', 'settings', 'help'])
  })
})

interface Server {
  primary: string
  indexed: boolean
  startedAt: string
  restartOnSave: boolean
  jobDone: boolean
  sections: string[]
  calls: { method: string; url: string; body: unknown }[]
}

const LISTINGS: Record<string, { path: string; parent: string | null; writable: boolean; dirs: { name: string; path: string }[] }> = {
  '/home/u': { path: '/home/u', parent: '/home', writable: true, dirs: [{ name: 'Notes', path: '/home/u/Notes' }, { name: 'Research', path: '/home/u/Research' }] },
  '/home/u/Notes': { path: '/home/u/Notes', parent: '/home/u', writable: true, dirs: [] },
  '/home/u/Research': { path: '/home/u/Research', parent: '/home/u', writable: true, dirs: [] },
}

function stubServer(s: Server) {
  const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    s.calls.push({ method, url, body })
    if (url === '/api/config') return reply({ appName: 'Studio', platform: false, sections: s.sections.length ? s.sections : undefined, user: { id: 'u', username: 'u' } })
    if (url === '/api/settings' && method === 'PUT') { s.sections = (body as { sections: string[] }).sections; return reply({}) }
    if (url.startsWith('/api/setup/status')) {
      return reply({
        firstRun: true, standalone: true, canRestart: s.restartOnSave, launcher: s.restartOnSave,
        primary: { path: s.primary, exists: true, writable: true, indexed: s.indexed, ...(s.indexed ? { files: 12 } : {}) },
        gufi: { found: true, bin: '/opt/gufi/bin', semantic: false },
        embedModel: { path: '/x/minilm384.gguf', found: false },
        libraries: [{ id: 'kb', label: 'Notes', primary: true, writable: true, pinned: true, source: true, caps: { index: s.indexed, fullText: false, vectors: false } }],
        job: null,
      })
    }
    if (url.startsWith('/api/setup/browse')) {
      const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
      return LISTINGS[p] ? reply(LISTINGS[p]) : reply({ error: `no such folder: ${p}` }, 404)
    }
    if (url === '/api/version') return reply({ version: 'dev', startedAt: s.startedAt })
    if (url === '/api/setup/primary-folder') {
      const path = (body as { path: string }).path
      if (!s.restartOnSave) return reply({ saved: false, restarting: false, note: `Set KB_ROOT=${path} in the Studio's environment and restart it.` })
      s.primary = path
      s.startedAt = 'second start'
      return reply({ saved: true, restarting: true })
    }
    if (url === '/api/setup/index') {
      return reply({ id: 7, state: 'running', ms: null, error: null, progress: { phase: 'files', done: 0, total: 3, current: null, failed: [] } })
    }
    if (url === '/api/index/job/7') {
      if (!s.jobDone) return reply({ id: 7, state: 'running', ms: null, error: null, progress: { phase: 'directories', done: 1, total: 3, current: 'papers', failed: [] } })
      s.indexed = true
      return reply({ id: 7, state: 'done', ms: 2500, error: null, progress: { phase: 'done', done: 3, total: 3, current: null, failed: [] } })
    }
    if (url === '/api/setup/complete') return reply({ setupDone: true })
    return reply({ error: 'not found' }, 404)
  }))
}

const next = () => fireEvent.click(screen.getByRole('button', { name: 'Next' }))

describe('the setup wizard', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('walks the steps: sidebar, a new folder through a restart, the index, a remote index, done', async () => {
    const s: Server = { primary: '/home/u/Notes', indexed: false, startedAt: 'first start', restartOnSave: true, jobDone: false, sections: [], calls: [] }
    stubServer(s)
    const onClose = vi.fn()
    render(<SetupWizard onClose={onClose} pollMs={5} />)

    expect(screen.getByRole('heading', { name: 'Welcome' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Back' })).toBeNull()
    next()

    expect(screen.getByRole('heading', { name: 'Sidebar' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: 'Chat' }))
    await waitFor(() => expect(s.sections).toEqual(['library', 'search', 'query', 'overview', 'history', 'workflows', 'agents', 'settings', 'help']))
    next()

    expect(screen.getByRole('heading', { name: 'Your folder' })).toBeInTheDocument()
    expect(await screen.findByText('/home/u/Notes')).toBeInTheDocument()
    expect(screen.getByText('Not indexed yet.')).toBeInTheDocument()
    // The folder in use cannot be chosen again; its parent lists the others.
    expect(await screen.findByText('No folders inside')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Use this folder' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Up' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Research' }))
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Folder' })).toHaveValue('/home/u/Research'))
    fireEvent.click(screen.getByRole('button', { name: 'Use this folder' }))

    expect(await screen.findByText('The Studio restarted and now uses /home/u/Research.')).toBeInTheDocument()
    expect(s.calls.find(c => c.url === '/api/setup/primary-folder')?.body).toEqual({ path: '/home/u/Research' })

    fireEvent.click(screen.getByRole('button', { name: 'Build the index' }))
    expect(await screen.findByText('papers (1 of 3)')).toBeInTheDocument()
    expect(screen.getByRole('progressbar')).toBeInTheDocument()
    s.jobDone = true
    expect(await screen.findByText('Index built in 3 s.')).toBeInTheDocument()
    expect(await screen.findByText('Indexed: 12 files.')).toBeInTheDocument()
    next()

    expect(screen.getByRole('heading', { name: 'Remote indexes' })).toBeInTheDocument()
    expect(screen.getByText('No remote indexes yet')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Add test index' }))
    expect(screen.getByText('Lab index')).toBeInTheDocument()
    next()

    expect(screen.getByRole('heading', { name: 'Done' })).toBeInTheDocument()
    expect(screen.getByText('Folder: /home/u/Research. Indexed: 12 files.')).toBeInTheDocument()
    expect(screen.getByText('Remote indexes: Lab index')).toBeInTheDocument()
    expect(screen.getByText(/^Sidebar: Library, Search/)).not.toHaveTextContent('Chat')
    fireEvent.click(screen.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(onClose).toHaveBeenCalledWith(true))
    expect(s.calls.some(c => c.url === '/api/setup/complete' && c.method === 'POST')).toBe(true)
  })

  it('without a launcher says how to change the folder, and Skip setup records it as done', async () => {
    const s: Server = { primary: '/home/u/Notes', indexed: true, startedAt: 'first start', restartOnSave: false, jobDone: false, sections: [], calls: [] }
    stubServer(s)
    const onClose = vi.fn()
    render(<SetupWizard onClose={onClose} pollMs={5} />)
    next()
    next()
    expect(await screen.findByText('Indexed: 12 files.')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Rebuild the index' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Up' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Research' }))
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Folder' })).toHaveValue('/home/u/Research'))
    fireEvent.click(screen.getByRole('button', { name: 'Use this folder' }))
    expect(await screen.findByText("Set KB_ROOT=/home/u/Research in the Studio's environment and restart it.")).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Skip setup' }))
    await waitFor(() => expect(onClose).toHaveBeenCalledWith(false))
    expect(s.calls.some(c => c.url === '/api/setup/complete')).toBe(true)
  })
})
