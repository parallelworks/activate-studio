import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { type AccessStatus, type KeyTestResult, ModelAccessSection, endpointBase, readPlatformHost, testNote } from '../src/components/ModelAccessSection'
import { UpdatesSection } from '../src/components/UpdatesSection'
import { useAppConfig } from '../src/config'

const reply = (body: unknown, code = 200) => new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } })

const contexts = [
  { name: 'work', server: 'platform-a.example.com', organization: 'org-a', kind: 'api-key', expiresAt: null, expired: false, current: true, renewError: null },
  { name: 'lab', server: 'platform-b.example.com', organization: 'org-b', kind: 'sign-in', expiresAt: null, expired: false, current: false, renewError: null },
] as const

function accessOf(over: Partial<AccessStatus> = {}): AccessStatus {
  const cli = { source: 'cli' as const, host: 'platform-a.example.com', context: 'work' }
  return {
    single: true, authEnabled: false,
    models: cli, platform: cli, deployment: cli,
    gatewayHost: 'platform-a.example.com', platformOrigin: 'https://platform-a.example.com',
    cli: { available: true, version: 'v7.1.0', path: '/usr/local/bin/pw', source: 'path', chosenPath: null, commonDirs: ['/home/me/.local/bin', '/usr/local/bin'], contexts: [...contexts], chosen: null, active: 'work', used: 'work', gatewayFromEnv: false, envKey: false },
    login: null,
    ...over,
  }
}

interface Srv {
  access: AccessStatus
  health: Record<string, unknown>
  models: string[]
  calls: { method: string; url: string; body: unknown }[]
  login?: Record<string, unknown> | null
  /** Extra fields of /api/me/model-key, such as a stored endpoint's options. */
  key?: Record<string, unknown>
  /** What /api/me/model-key/test answers for a request body. */
  test?: (body: Record<string, unknown>) => unknown
}

function stub(s: Srv) {
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    s.calls.push({ method, url, body })
    if (/^\/api\/access(\?|$)/.test(url)) return reply(s.access)
    if (url === '/api/me/model-key/test' && method === 'POST') return reply(s.test?.(body as Record<string, unknown>) ?? { status: 'ok', models: 1, ids: ['m'] })
    if (url === '/api/me/model-key' && method === 'POST') return reply({ authEnabled: false, single: true, verified: true, mode: 'stored', ...s.key })
    if (url === '/api/me/model-key') return reply({ authEnabled: false, single: true, verified: true, mode: s.access.models.source === 'endpoint' ? 'stored' : 'none', baseUrl: s.access.models.baseUrl ?? null, ...s.key })
    if (url.startsWith('/api/ai/health')) return reply(s.health)
    if (url.startsWith('/api/chat/models')) return reply({ models: s.models.map(id => ({ id })) })
    if (url === '/api/access/source' && method === 'PUT') {
      const name = (body as { context: string | null }).context
      s.access = accessOf({ cli: { ...accessOf().cli!, chosen: name, active: name ?? 'work', used: name ?? 'work' }, models: { source: 'cli', host: 'platform-b.example.com', context: name }, platform: { source: 'cli', host: 'platform-b.example.com', context: name } })
      return reply({ ok: true })
    }
    if (url === '/api/access/cli/path' && method === 'PUT') {
      const p = (body as { path: string | null }).path
      s.access = accessOf({ cli: { ...accessOf().cli!, path: p, source: p ? 'chosen' : 'path', chosenPath: p } })
      return reply({ cli: s.access.cli })
    }
    if (url === '/api/access/cli/login' && method === 'POST') {
      s.login = { host: 'platform-c.example.com', state: 'waiting', url: 'https://platform-c.example.com/device', code: 'ABCD-EFGH', context: null, message: null }
      return reply({ login: s.login })
    }
    if (url === '/api/access/cli/login') return reply({ login: s.login ?? null })
    if (url === '/api/access/subscriptions') return reply({ available: true, user: 'alex', org: 'org-a', codex: { offered: true, providers: [] }, copilot: { offered: false, providers: [], github: null } })
    if (url === '/api/config') return reply({ appName: 'Studio', platform: true })
    return reply({ error: 'not found' }, 404)
  }))
}

const srv = (over: Partial<Srv> = {}): Srv => ({
  access: accessOf(), health: { status: 'ok', models: 2, gatewayHost: 'platform-a.example.com', message: null, platformAccess: null },
  models: ['me:openai/gpt-5', 'local/llama3.2'], calls: [], ...over,
})

describe('Model access', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); vi.useRealTimers() })

  it('names the pw CLI context in use, lists the contexts, and switches to another one', async () => {
    const s = srv()
    stub(s)
    const changed = vi.fn()
    window.addEventListener('ade:model-access-changed', changed)
    render(<ModelAccessSection />)
    expect(await screen.findByText('Models: pw CLI context work (platform-a.example.com)')).toBeInTheDocument()
    expect(screen.getByText('me:openai/gpt-5')).toBeInTheDocument()
    const table = document.querySelector('table.cli-contexts') as HTMLElement
    const work = within(table).getByText('work').closest('tr')!
    expect(within(work).getByText('In use')).toBeInTheDocument()
    expect(within(work).getByText('CLI current')).toBeInTheDocument()
    const lab = within(table).getByText('lab').closest('tr')!
    expect(within(lab).getByText('Sign-in')).toBeInTheDocument()
    fireEvent.click(within(lab).getByRole('button', { name: 'Use' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/access/source' && c.method === 'PUT')).toBe(true))
    expect(s.calls.find(c => c.url === '/api/access/source')!.body).toEqual({ context: 'lab', clearKey: false })
    expect(await screen.findByText('Models: pw CLI context lab (platform-b.example.com)')).toBeInTheDocument()
    expect(changed).toHaveBeenCalled()
    window.removeEventListener('ade:model-access-changed', changed)
  })

  it('shows an endpoint for models and the CLI for the platform, with the endpoint form chosen', async () => {
    const endpoint = { source: 'endpoint' as const, host: '127.0.0.1:11434', context: null, baseUrl: 'http://127.0.0.1:11434/v1' }
    stub(srv({ access: accessOf({ models: endpoint }) }))
    render(<ModelAccessSection />)
    expect(await screen.findByText('Models: the endpoint http://127.0.0.1:11434/v1')).toBeInTheDocument()
    const uses = within(document.querySelector('table.access-uses-table') as HTMLElement)
    expect(uses.getByText('Models in chat and for the assistant').closest('tr')).toHaveTextContent('the endpoint http://127.0.0.1:11434/v1')
    expect(uses.getByText('Workflows page and the assistant\'s platform tools').closest('tr')).toHaveTextContent('pw CLI context work (platform-a.example.com)')
    expect(screen.getByLabelText('Base URL')).toHaveValue('http://127.0.0.1:11434/v1')
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument()
  })

  it('says offline plainly, as a warning rather than an error', async () => {
    stub(srv({ health: { status: 'offline', models: 0, gatewayHost: 'platform-a.example.com', message: 'Could not reach platform-a.example.com (ENOTFOUND). This computer may be offline, or the server is down. Local libraries and a local model keep working.' }, models: [] }))
    render(<ModelAccessSection />)
    expect(await screen.findByText(/Could not reach platform-a\.example\.com \(ENOTFOUND\)/)).toBeInTheDocument()
  })

  it('signs the CLI in with a device code, and reports when it is done', async () => {
    const s = srv()
    stub(s)
    render(<ModelAccessSection />)
    const host = await screen.findByLabelText('Platform host to sign in to')
    fireEvent.change(host, { target: { value: 'platform-c.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /platform-c\.example\.com\/device/ })).toHaveAttribute('href', 'https://platform-c.example.com/device')
    expect(s.calls.find(c => c.url === '/api/access/cli/login' && c.method === 'POST')!.body).toEqual({ host: 'platform-c.example.com' })
    s.login = { host: 'platform-c.example.com', state: 'done', url: null, code: null, context: 'alex@platform-c.example.com', message: 'Signed in. The Studio now uses the pw CLI context alex@platform-c.example.com.' }
    expect(await screen.findByText('Signed in. The Studio now uses the pw CLI context alex@platform-c.example.com.', {}, { timeout: 4000 })).toBeInTheDocument()
  })

  it('offers a ChatGPT plan through the platform when it is offered there', async () => {
    stub(srv())
    render(<ModelAccessSection />)
    expect(await screen.findByText('Subscriptions through ACTIVATE')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Connect' })).toBeInTheDocument()
    expect(screen.getAllByText('This platform does not offer it.')).toHaveLength(1)
  })
})

describe('Model access on a deployment that signs people in', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('says which credential each part uses, and where the pw CLI runs from', async () => {
    stub(srv())
    render(<ModelAccessSection />)
    await screen.findByText('Models: pw CLI context work (platform-a.example.com)')
    for (const part of ['Models in chat and for the assistant', 'Workflows page and the assistant\'s platform tools', 'Libraries on ACTIVATE systems (pw ssh)', 'ChatGPT and Copilot subscriptions']) {
      expect(screen.getByText(part).closest('tr')).toHaveTextContent('pw CLI context work (platform-a.example.com)')
    }
    expect(screen.queryByText('pw CLI not found')).toBeNull()
    expect(screen.getByText('/usr/local/bin/pw').closest('p')).toHaveTextContent('pw v7.1.0 at /usr/local/bin/pw, found on the PATH.')
  })

  it('says when the Studio cannot find the pw CLI, and takes its location', async () => {
    const s = srv({ access: accessOf({ cli: { ...accessOf().cli!, available: false, version: null, path: null, source: 'none' } }) })
    stub(s)
    render(<ModelAccessSection />)
    expect(await screen.findByText('The Studio cannot find the pw CLI')).toBeInTheDocument()
    expect(screen.getAllByText('pw CLI not found')).toHaveLength(2)
    expect(screen.getByText('/home/me/.local/bin')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Where the pw CLI is'), { target: { value: '/opt/tools/pw' } })
    fireEvent.click(screen.getByRole('button', { name: 'Use this location' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/access/cli/path' && c.method === 'PUT' && (c.body as { path: string }).path === '/opt/tools/pw')).toBe(true))
    expect(await screen.findByText('The Studio now runs the pw CLI at /opt/tools/pw.')).toBeInTheDocument()
    expect(await screen.findByText('/opt/tools/pw')).toBeInTheDocument()
    expect(screen.getByText('/opt/tools/pw').closest('p')).toHaveTextContent('chosen here')
    expect(s.calls.some(c => c.url === '/api/access?fresh=1')).toBe(true)
  })

  it('names the deployment credential by its host only, and offers no pw CLI controls', async () => {
    const dep = { source: 'cli' as const, host: 'platform-a.example.com', context: null }
    const s = srv({ access: accessOf({ single: false, authEnabled: true, models: dep, platform: dep, deployment: dep, cli: null }) })
    stub(s)
    // A signed-in viewer with no key of their own.
    const base = globalThis.fetch as unknown as (input: string, init?: RequestInit) => Promise<Response>
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => String(input) === '/api/me/model-key'
      ? reply({ authEnabled: true, verified: true, mode: 'none', sharingAllowed: false, shared: { active: false } })
      : base(input, init)))
    render(<ModelAccessSection />)
    expect(await screen.findByText('Models: the pw CLI sign-in on the server (platform-a.example.com)')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'ACTIVATE (deployment)' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'ACTIVATE (pw CLI)' })).toBeNull()
    expect(screen.getByText(/Without a key of your own, requests use the pw CLI sign-in on the server/)).toBeInTheDocument()
    expect(screen.queryByText('Subscriptions through ACTIVATE')).toBeNull()
  })
})

describe('the sources, named for what they are', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('labels each tab and leads each with one line on what it is for', async () => {
    stub(srv())
    render(<ModelAccessSection />)
    await screen.findByText('Models: pw CLI context work (platform-a.example.com)')
    for (const name of ['ACTIVATE (pw CLI)', 'ACTIVATE API key', 'OpenAI-compatible endpoint']) expect(screen.getByRole('button', { name })).toBeInTheDocument()
    expect(screen.getByText(/^The ACTIVATE platforms the pw CLI on this computer is signed in to/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'ACTIVATE API key' }))
    expect(screen.getByText(/^A token or API key from an ACTIVATE account/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'OpenAI-compatible endpoint' }))
    expect(screen.getByText(/^Any service that speaks the OpenAI API, such as a site AI portal/)).toBeInTheDocument()
  })
})

describe('an endpoint address on the ACTIVATE API key tab', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('reads a platform host as its gateway, and an address with a path as an endpoint', () => {
    expect(readPlatformHost('')).toEqual({ host: '', origin: '', platformUrl: '', endpointUrl: null })
    expect(readPlatformHost('platform.example.com')).toEqual({ host: 'platform.example.com', origin: 'https://platform.example.com', platformUrl: 'https://platform.example.com/api/openai/v1', endpointUrl: null })
    expect(readPlatformHost('http://127.0.0.1:8080').platformUrl).toBe('http://127.0.0.1:8080/api/openai/v1')
    expect(readPlatformHost('https://platform.example.com/api/openai/v1').endpointUrl).toBeNull()
    expect(readPlatformHost('portal.example.org/v1').endpointUrl).toBe('https://portal.example.org/v1')
    expect(readPlatformHost('https://portal.example.org/v1/chat/completions').endpointUrl).toBe('https://portal.example.org/v1')
    expect(readPlatformHost('http://127.0.0.1:4000/v1/').endpointUrl).toBe('http://127.0.0.1:4000/v1')
    expect(endpointBase('https://portal.example.org/v1/models/')).toBe('https://portal.example.org/v1')
  })

  it('says a typed path is an endpoint, and moves the address and the key there in one click', async () => {
    const s = srv()
    stub(s)
    render(<ModelAccessSection />)
    await screen.findByText('Models: pw CLI context work (platform-a.example.com)')
    fireEvent.click(screen.getByRole('button', { name: 'ACTIVATE API key' }))
    fireEvent.change(screen.getByLabelText('Platform key or token'), { target: { value: 'sk-virtual' } })
    fireEvent.change(screen.getByLabelText('Platform host'), { target: { value: 'portal.example.org/v1/chat/completions' } })
    expect(screen.getByText('This is an endpoint address, not a platform host')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save and use' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Use as an endpoint' }))
    expect(screen.getByLabelText('Base URL')).toHaveValue('https://portal.example.org/v1')
    expect(screen.getByLabelText('Endpoint key')).toHaveValue('sk-virtual')
    expect(screen.getByText(/^Moved to OpenAI-compatible endpoint, with https:\/\/portal\.example\.org\/v1 as the base URL/)).toBeInTheDocument()
  })

  it('tests a host that is not an ACTIVATE platform at /v1 too, and says what it is', async () => {
    const s = srv({
      test: b => b.baseUrl === 'https://portal.example.org/api/openai/v1'
        ? { status: 'unreachable', models: 0, notFound: 'https://portal.example.org/api/openai/v1/models', message: 'not an ACTIVATE platform' }
        : { status: 'ok', models: 12, ids: ['m1'] },
    })
    stub(s)
    render(<ModelAccessSection />)
    await screen.findByText('Models: pw CLI context work (platform-a.example.com)')
    fireEvent.click(screen.getByRole('button', { name: 'ACTIVATE API key' }))
    fireEvent.change(screen.getByLabelText('Platform key or token'), { target: { value: 'sk-virtual' } })
    fireEvent.change(screen.getByLabelText('Platform host'), { target: { value: 'portal.example.org' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('portal.example.org is not an ACTIVATE platform, since https://portal.example.org/api/openai/v1/models does not exist (404).')).toBeInTheDocument()
    expect(screen.getByText('https://portal.example.org/v1 answers as an OpenAI-compatible endpoint with 12 models. Use it under OpenAI-compatible endpoint.')).toBeInTheDocument()
    const tests = s.calls.filter(c => c.url === '/api/me/model-key/test').map(c => c.body)
    expect(tests).toEqual([{ key: 'sk-virtual', baseUrl: 'https://portal.example.org/api/openai/v1' }, { key: 'sk-virtual', baseUrl: 'https://portal.example.org/v1', tools: false }])
    fireEvent.click(screen.getByRole('button', { name: 'Use as an endpoint' }))
    expect(screen.getByLabelText('Base URL')).toHaveValue('https://portal.example.org/v1')
  })
})

describe('an endpoint\'s Advanced options and the tool check', () => {
  afterEach(() => { vi.unstubAllGlobals(); localStorage.removeItem('aiChat.model') })

  it('says in plain words whether the model calls tools', () => {
    const base: KeyTestResult = { status: 'ok', models: 3, ids: ['a'] }
    expect(testNote(base)).toEqual({ tone: 'success', text: 'Connected: 3 models available.' })
    expect(testNote({ ...base, toolCheck: { model: 'a', result: 'no-call', message: 'a answered without calling the test tool.' } }))
      .toEqual({ tone: 'warning', text: 'Connected: 3 models available.', detail: 'a answered without calling the test tool.' })
    expect(testNote({ ...base, checked: 'completion', toolCheck: { model: 'a', result: 'calls', message: 'a calls tools.' } }).text).toBe('Connected: a chat completion with a answered.')
    expect(testNote({ status: 'unreachable', models: 0, message: 'x does not exist (404).' })).toEqual({ tone: 'error', text: 'x does not exist (404).' })
  })

  it('shows stored headers by name only, keeps the stored key and values on save, and reports the tool check', async () => {
    const endpoint = { source: 'endpoint' as const, host: 'portal.example.org', context: null, baseUrl: 'https://portal.example.org/v1' }
    const summary = { auth: 'api-key', headers: ['X-Tenant'], models: [], query: 'api-version=2026-01-01' }
    const s = srv({
      access: accessOf({ models: endpoint }), key: { endpoint: summary },
      test: () => ({ status: 'ok', models: 2, ids: ['tiny-model', 'big-model'], toolCheck: { model: 'tiny-model', result: 'no-call', message: 'tiny-model answered without calling the test tool, so chat cannot search the libraries with this model.' } }),
    })
    stub(s)
    localStorage.setItem('aiChat.model', JSON.stringify('tiny-model'))
    render(<ModelAccessSection />)
    expect(await screen.findByText('Advanced: key in api-key, 1 header, query string')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Advanced: key in api-key, 1 header, query string'))
    expect(screen.getByLabelText('Header 1 name')).toHaveValue('X-Tenant')
    expect(screen.getByLabelText('Header 1 value')).toHaveValue('')
    expect(screen.getByLabelText('Header 1 value')).toHaveAttribute('placeholder', 'Stored; type to replace')
    expect(screen.getByLabelText('Query string')).toHaveValue('api-version=2026-01-01')
    fireEvent.change(screen.getByLabelText('Model names'), { target: { value: 'tiny-model\nbig-model' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('tiny-model answered without calling the test tool, so chat cannot search the libraries with this model.')).toBeInTheDocument()
    const sentTest = s.calls.find(c => c.url === '/api/me/model-key/test')!.body
    expect(sentTest).toEqual({
      key: '', baseUrl: 'https://portal.example.org/v1', model: 'tiny-model',
      endpoint: { auth: 'api-key', headers: [{ name: 'X-Tenant' }], models: ['tiny-model', 'big-model'], query: 'api-version=2026-01-01' },
    })

    fireEvent.click(screen.getByRole('button', { name: 'Save and use' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/me/model-key' && c.method === 'POST')).toBe(true))
    expect(s.calls.find(c => c.url === '/api/me/model-key' && c.method === 'POST')!.body).toEqual({
      key: '', persist: true, baseUrl: 'https://portal.example.org/v1',
      endpoint: { auth: 'api-key', headers: [{ name: 'X-Tenant' }], models: ['tiny-model', 'big-model'], query: 'api-version=2026-01-01' },
    })
  })

  it('needs a key for a new endpoint unless its key header is None', async () => {
    stub(srv())
    render(<ModelAccessSection />)
    await screen.findByText('Models: pw CLI context work (platform-a.example.com)')
    fireEvent.click(screen.getByRole('button', { name: 'OpenAI-compatible endpoint' }))
    fireEvent.change(screen.getByLabelText('Base URL'), { target: { value: 'https://portal.example.org/v1' } })
    expect(screen.getByRole('button', { name: 'Save and use' })).toBeDisabled()
    fireEvent.click(screen.getByText('Advanced, for portals that differ'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a header' }))
    fireEvent.change(screen.getByLabelText('Header 1 name'), { target: { value: 'Authorization' } })
    fireEvent.change(screen.getByLabelText('Header 1 value'), { target: { value: 'Basic abc' } })
    expect(screen.getByText('Advanced: 1 header')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save and use' })).toBeDisabled()
    // With the key header None, the header above is how the portal is reached.
    fireEvent.click(screen.getByRole('combobox', { name: 'Key header' }))
    const none = await screen.findByRole('option', { name: 'None: no key is sent' })
    fireEvent.pointerDown(none)
    fireEvent.click(none)
    expect(await screen.findByText('Advanced: no key header, 1 header')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save and use' })).toBeEnabled()
    expect(screen.getByLabelText('Endpoint key')).toHaveAttribute('placeholder', 'No key is sent (Advanced, Key header)')
  })
})

describe('a change of model access', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('has the app read its configuration again, so the Workflows page shows without a reload', async () => {
    const s = srv()
    stub(s)
    function Probe() { const cfg = useAppConfig(); return <span>{cfg.platform ? 'platform' : 'no platform'}</span> }
    render(<Probe />)
    await screen.findByText('platform')
    const before = s.calls.filter(c => c.url === '/api/config').length
    act(() => { window.dispatchEvent(new CustomEvent('ade:model-access-changed')) })
    await waitFor(() => expect(s.calls.filter(c => c.url === '/api/config').length).toBe(before + 1))
  })
})

describe('the update check offline', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('says it will check again later, with no error', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => String(input) === '/api/updates'
      ? reply({ mode: 'app', current: { version: 'v1.140', builtAt: null }, policy: { check: true, install: 'manual', channel: 'stable' }, latest: null, releases: [], available: false, canInstall: false, reason: null, state: 'idle', progress: null, error: null, checkedAt: '2026-10-21T08:00:00Z', offline: true, previous: null })
      : reply({}, 404)))
    render(<UpdatesSection />)
    expect(await screen.findByText('Offline; the Studio checks again later')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText(/This is the latest version/)).toBeNull()
  })
})
