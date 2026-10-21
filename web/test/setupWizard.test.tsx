import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SECTION_SWITCHES, sectionAllowed, sectionsSetting, shownView } from '../src/config'
import { SetupWizard, SidebarSwitches, keyTestProblem } from '../src/views/SetupWizard'

// The ssh and cluster forms have their own tests; here each adds a library
// the way it reports one.
vi.mock('../src/components/SshLibraryForm', () => ({
  SshLibraryForm: ({ onAdded, transport = 'ssh' }: { onAdded?: (lib: { id: string; label: string }) => void; transport?: string }) => (
    <button type="button" onClick={() => onAdded?.(transport === 'pw' ? { id: 'u-cluster-a-idx', label: 'Cluster index' } : { id: 'lab', label: 'Lab index' })}>
      Add test index over {transport}
    </button>
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
    // Chat and Agents call a model, so they need AI features on.
    expect(sectionAllowed('chat', { aiEnabled: false })).toBe(false)
    expect(sectionAllowed('agents', { aiEnabled: false })).toBe(false)
    expect(sectionAllowed('query', { aiEnabled: false })).toBe(true)
  })

  it('sends a hidden page, from a link or the address, to the first one shown', () => {
    const cfg = { sections: ['search', 'query', 'settings', 'help'] }
    expect(shownView('agents', ORDER, cfg)).toBe('search')
    expect(shownView('chat', ORDER, cfg)).toBe('search')
    expect(shownView('query', ORDER, cfg)).toBe('query')
    expect(shownView('chat', ORDER, {})).toBe('chat')
    expect(shownView('chat', ORDER, { aiEnabled: false })).toBe('library')
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

  it('with AI features off, Chat and Agents are off and cannot be switched on', () => {
    render(<SidebarSwitches value={undefined} platform aiEnabled={false} onChange={() => {}} />)
    for (const name of ['Chat', 'Agents']) {
      expect(screen.getByRole('switch', { name })).toHaveAttribute('aria-checked', 'false')
      expect(screen.getByRole('switch', { name })).toHaveAttribute('data-disabled')
    }
    expect(screen.getAllByText('(AI features off)')).toHaveLength(2)
    expect(screen.getByRole('switch', { name: 'Query' })).toHaveAttribute('aria-checked', 'true')
  })
})

describe('what a key test says', () => {
  const t = (over: Record<string, unknown>) => ({ ok: false, status: 'unreachable', models: 0, gatewayHost: 'models.example', message: null, ...over })
  it('names the failure plainly', () => {
    expect(keyTestProblem(t({ status: 'auth' }), '')).toMatch(/^Unauthorized: models\.example refused this key/)
    expect(keyTestProblem(t({ message: 'fetch failed' }), '')).toMatch(/^Unreachable: models\.example could not be reached/)
    expect(keyTestProblem(t({ message: 'The operation was aborted due to timeout' }), '')).toMatch(/did not answer within 8 seconds/)
    expect(keyTestProblem(t({ message: 'Unexpected token < in JSON at position 0' }), '')).toMatch(/not with a list of models.*\/v1/)
    expect(keyTestProblem(t({ message: 'https://models.example/models does not exist (404). An OpenAI-compatible base URL usually ends in /v1.' }), ''))
      .toMatch(/does not exist \(404\)/)
    expect(keyTestProblem(t({ ok: true, status: 'ok', models: 0 }), '')).toBe('No models: models.example answered but offers no models.')
    expect(keyTestProblem(t({ ok: true, status: 'ok', models: 3 }), '')).toBeNull()
  })
})

interface Server {
  appName: string
  accent: string
  aiEnabled: boolean
  primary: string
  indexed: boolean
  startedAt: string
  restartOnSave: boolean
  jobDone: boolean
  sections: string[]
  branding: { light: string | null; dark: string | null }
  pw: boolean
  settings: Record<string, unknown>
  calls: { method: string; url: string; body: unknown }[]
}

const server = (over: Partial<Server> = {}): Server => ({
  appName: 'Studio', accent: 'navy', aiEnabled: true, primary: '/home/u/Notes', indexed: false, startedAt: 'first start',
  restartOnSave: true, jobDone: false, sections: [], branding: { light: null, dark: null }, pw: true, settings: {}, calls: [], ...over,
})

const LISTINGS: Record<string, { path: string; parent: string | null; writable: boolean; dirs: { name: string; path: string }[] }> = {
  '/home/u': { path: '/home/u', parent: '/home', writable: true, dirs: [{ name: 'Notes', path: '/home/u/Notes' }, { name: 'Research', path: '/home/u/Research' }] },
  '/home/u/Notes': { path: '/home/u/Notes', parent: '/home/u', writable: true, dirs: [] },
  '/home/u/Research': { path: '/home/u/Research', parent: '/home/u', writable: true, dirs: [] },
}
LISTINGS['~'] = LISTINGS['/home/u']

function stubServer(s: Server) {
  const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body instanceof FormData ? init.body : init?.body ? JSON.parse(String(init.body)) : undefined
    s.calls.push({ method, url, body })
    if (url === '/api/config') {
      return reply({ appName: s.appName, accent: s.accent, aiEnabled: s.aiEnabled, platform: false, sections: s.sections.length ? s.sections : undefined, user: { id: 'u', username: 'u' } })
    }
    if (url === '/api/settings' && method === 'PUT') {
      const b = body as Record<string, unknown>
      Object.assign(s.settings, b)
      if (b.sections) s.sections = b.sections as string[]
      if (typeof b.appName === 'string') s.appName = b.appName
      if (typeof b.accent === 'string') s.accent = b.accent
      if (typeof b.aiEnabled === 'boolean') s.aiEnabled = b.aiEnabled
      return reply({})
    }
    if (url.startsWith('/api/setup/status')) {
      return reply({
        firstRun: true, standalone: true, canRestart: s.restartOnSave, launcher: s.restartOnSave,
        primary: { path: s.primary, exists: true, writable: true, indexed: s.indexed, ...(s.indexed ? { files: 12 } : {}) },
        gufi: { found: true, bin: '/opt/gufi/bin', semantic: false },
        embedModel: { path: '/x/minilm384.gguf', found: false },
        branding: s.branding,
        libraries: [{ id: 'kb', label: 'Notes', primary: true, writable: true, pinned: true, source: true, caps: { index: s.indexed, fullText: false, vectors: false } }],
        job: null,
      })
    }
    if (url.startsWith('/api/setup/icon')) {
      const dark = url.includes('variant=dark')
      if (method === 'POST') s.branding = { ...s.branding, [dark ? 'dark' : 'light']: `/api/brand-icon?${dark ? 'variant=dark&' : ''}v=1` }
      else s.branding = { ...s.branding, [dark ? 'dark' : 'light']: null }
      return reply(s.branding)
    }
    if (url.startsWith('/api/setup/browse')) {
      const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
      return LISTINGS[p] ? reply(LISTINGS[p]) : reply({ error: `no such folder: ${p}` }, 404)
    }
    if (url === '/api/me/model-key' && method === 'GET') return reply({ authEnabled: false, verified: true, single: true, mode: 'none' })
    if (url === '/api/me/model-key/test') {
      const { key } = body as { key: string }
      if (key === 'bad') return reply({ ok: false, status: 'auth', models: 0, gatewayHost: 'models.example', message: 'models.example rejected this credential (401).' })
      return reply({ ok: true, status: 'ok', models: 2, gatewayHost: 'models.example', message: null, ids: ['small-model', 'large-model'] })
    }
    if (url === '/api/me/model-key' && method === 'POST') {
      const b = body as { baseUrl: string }
      return reply({ authEnabled: false, verified: true, single: true, mode: 'stored', last4: 'good', baseUrl: b.baseUrl })
    }
    if (url === '/api/user-libraries') return reply({ libraries: [], available: true, ssh: true, pw: s.pw })
    if (url === '/api/libraries' && method === 'POST') {
      const b = body as { indexRoot: string }
      const label = b.indexRoot.split('/').pop()!
      return reply({ library: { id: label.toLowerCase(), label, primary: false, writable: false, pinned: false, source: false, caps: { index: true, fullText: false, vectors: false }, indexRoot: b.indexRoot, sourceRoot: null } })
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
const heading = (name: string) => expect(screen.getByRole('heading', { name })).toBeInTheDocument()
const sent = (s: Server, method: string, url: string) => s.calls.filter(c => c.method === method && c.url === url).map(c => c.body)

function open(s: Server, onClose = vi.fn()) {
  stubServer(s)
  render(<SetupWizard onClose={onClose} pollMs={5} />)
  // The configuration is cached between tests; ask for this server's.
  act(() => { window.dispatchEvent(new Event('ade:config-changed')) })
  return onClose
}

describe('the setup wizard', () => {
  afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

  it('walks every step: name, icon, accent, an AI endpoint, sidebar, a new folder, other libraries, done', async () => {
    const s = server()
    const onClose = open(s)

    heading('Welcome')
    expect(screen.getByText('Step 1 of 7')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Back' })).toBeNull()
    next()

    // Your Studio: the name saves as it is left, the icon uploads, the
    // accent saves as it is picked.
    heading('Your Studio')
    const name = screen.getByRole('textbox', { name: 'Name' })
    await waitFor(() => expect(name).toHaveValue('Studio'))
    fireEvent.change(name, { target: { value: 'Lab Studio' } })
    fireEvent.blur(name)
    await waitFor(() => expect(s.settings.appName).toBe('Lab Studio'))
    expect(screen.getAllByText('None')).toHaveLength(2)
    fireEvent.change(screen.getByLabelText('Icon for light mode'), { target: { files: [new File(['<svg xmlns="http://www.w3.org/2000/svg"/>'], 'mark.svg', { type: 'image/svg+xml' })] } })
    await waitFor(() => expect(document.querySelector('.brand-icon-preview.light img')).toHaveAttribute('src', '/api/brand-icon?v=1'))
    const upload = s.calls.find(c => c.url === '/api/setup/icon?variant=light')!
    expect(upload.method).toBe('POST')
    expect((upload.body as FormData).get('file')).toBeInstanceOf(File)
    // Dark mode shows the light icon until it has one of its own.
    expect(document.querySelector('.brand-icon-preview.dark img')).toHaveAttribute('src', '/api/brand-icon?v=1')
    expect(screen.getByText('Uses the light mode icon.')).toBeInTheDocument()
    // Over 1 MB is refused before anything is sent.
    fireEvent.change(screen.getByLabelText('Icon for dark mode'), { target: { files: [new File([new Uint8Array(1024 * 1024 + 1)], 'big.png', { type: 'image/png' })] } })
    expect(await screen.findByText('big.png is over 1 MB.')).toBeInTheDocument()
    expect(s.calls.some(c => c.url === '/api/setup/icon?variant=dark')).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Remove the light mode icon' }))
    await waitFor(() => expect(sent(s, 'DELETE', '/api/setup/icon?variant=light')).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Teal' }))
    await waitFor(() => expect(s.settings.accent).toBe('teal'))
    next()

    // AI features: a refused key, then one that lists models, saved with a default.
    heading('AI features')
    expect(screen.getByRole('switch', { name: 'AI features' })).toHaveAttribute('aria-checked', 'true')
    fireEvent.change(screen.getByLabelText('Base URL'), { target: { value: 'https://models.example/v1' } })
    fireEvent.change(screen.getByLabelText('API key'), { target: { value: 'bad' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText(/^Unauthorized: models\.example refused this key/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
    fireEvent.change(screen.getByLabelText('API key'), { target: { value: 'sk-good' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('2 models offered by models.example')).toBeInTheDocument()
    expect(screen.getByText('small-model, large-model')).toBeInTheDocument()
    expect(sent(s, 'POST', '/api/me/model-key/test').at(-1)).toEqual({ key: 'sk-good', baseUrl: 'https://models.example/v1' })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Saved. The Studio uses small-model through models.example.')).toBeInTheDocument()
    expect(sent(s, 'POST', '/api/me/model-key')).toEqual([{ key: 'sk-good', baseUrl: 'https://models.example/v1', persist: true }])
    expect(s.settings.ragDefaultModel).toBe('small-model')
    expect(localStorage.getItem('aiChat.model')).toBe('"small-model"')
    next()

    heading('Sidebar')
    expect(screen.getByRole('switch', { name: 'Chat' })).not.toHaveAttribute('data-disabled')
    fireEvent.click(screen.getByRole('switch', { name: 'History' }))
    await waitFor(() => expect(s.sections).toEqual(['chat', 'library', 'search', 'query', 'overview', 'workflows', 'agents', 'settings', 'help']))
    next()

    heading('Your folder')
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

    // Other libraries: one on this machine picked by browsing, and one on a cluster.
    heading('Other libraries')
    expect(screen.getByText('No other libraries yet')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Browse for the index…' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Research' }))
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Folder' })).toHaveValue('/home/u/Research'))
    fireEvent.click(screen.getByRole('button', { name: 'Use as the index directory' }))
    expect(screen.getByLabelText('Index directory')).toHaveValue('/home/u/Research')
    fireEvent.click(screen.getByRole('button', { name: 'Add library' }))
    expect(await screen.findByText('Research')).toBeInTheDocument()
    expect(sent(s, 'POST', '/api/libraries')).toEqual([{ id: '', indexRoot: '/home/u/Research' }])
    fireEvent.click(await screen.findByRole('button', { name: 'On a cluster' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add test index over pw' }))
    expect(screen.getByText('Cluster index')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Over ssh' }))
    expect(screen.getByRole('button', { name: 'Add test index over ssh' })).toBeInTheDocument()
    next()

    heading('Done')
    expect(await screen.findByText('Name: Lab Studio')).toBeInTheDocument()
    expect(screen.getByText('AI features: on, through models.example, default model small-model')).toBeInTheDocument()
    expect(screen.getByText('Folder: /home/u/Research. Indexed: 12 files.')).toBeInTheDocument()
    expect(screen.getByText('Other libraries: Research, Cluster index')).toBeInTheDocument()
    expect(screen.getByText(/^Sidebar: Chat, Library, Search/)).not.toHaveTextContent('History')
    fireEvent.click(screen.getByRole('button', { name: 'Finish' }))
    await waitFor(() => expect(onClose).toHaveBeenCalledWith(true))
    expect(s.calls.some(c => c.url === '/api/setup/complete' && c.method === 'POST')).toBe(true)
  })

  it('with AI features off, Chat and Agents leave the sidebar; Back returns to the step', async () => {
    const s = server({ indexed: true, pw: false })
    open(s)
    next()
    next()
    heading('AI features')
    await waitFor(() => expect(screen.getByRole('switch', { name: 'AI features' })).toHaveAttribute('aria-checked', 'true'))
    fireEvent.click(screen.getByRole('switch', { name: 'AI features' }))
    await waitFor(() => expect(s.aiEnabled).toBe(false))
    expect(sent(s, 'PUT', '/api/settings').at(-1)).toEqual({ aiEnabled: false, sections: ['library', 'search', 'query', 'overview', 'history', 'workflows', 'settings', 'help'] })
    expect(await screen.findByText(/^Off: Chat and Agents leave the sidebar/)).toBeInTheDocument()
    expect(screen.queryByLabelText('Base URL')).toBeNull()
    next()

    heading('Sidebar')
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Chat' })).toHaveAttribute('data-disabled'))
    expect(screen.getByRole('switch', { name: 'Agents' })).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    heading('AI features')
    next()
    next()
    next()

    // Without a signed-in pw CLI there is no cluster choice.
    heading('Other libraries')
    expect(screen.getByRole('button', { name: 'Over ssh' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'On a cluster' })).toBeNull()
    next()
    heading('Done')
    expect(screen.getByText('AI features: off')).toBeInTheDocument()
    expect(screen.getByText(/^Sidebar: Library, Search/)).not.toHaveTextContent('Chat')
  })

  it('takes an endpoint with no key, such as a local model server', async () => {
    const s = server()
    open(s)
    next()
    next()
    heading('AI features')
    fireEvent.change(await screen.findByLabelText('Base URL'), { target: { value: 'http://127.0.0.1:11434/v1' } })
    expect(screen.getByLabelText('API key')).toHaveAttribute('placeholder', 'empty if the endpoint needs none')
    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('2 models offered by models.example')).toBeInTheDocument()
    expect(sent(s, 'POST', '/api/me/model-key/test').at(-1)).toEqual({ key: 'none', baseUrl: 'http://127.0.0.1:11434/v1' })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Saved. The Studio uses small-model through 127.0.0.1:11434.')).toBeInTheDocument()
    expect(sent(s, 'POST', '/api/me/model-key')).toEqual([{ key: 'none', baseUrl: 'http://127.0.0.1:11434/v1', persist: true }])
  })

  it('without a launcher says how to change the folder, and Skip setup records it as done', async () => {
    const s = server({ indexed: true, restartOnSave: false })
    const onClose = open(s)
    for (let i = 0; i < 4; i++) next()
    heading('Your folder')
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
