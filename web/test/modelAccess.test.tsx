import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { type AccessStatus, ModelAccessSection } from '../src/components/ModelAccessSection'
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
    cli: { available: true, version: 'v7.1.0', contexts: [...contexts], chosen: null, active: 'work', used: 'work', gatewayFromEnv: false, envKey: false },
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
}

function stub(s: Srv) {
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    s.calls.push({ method, url, body })
    if (url === '/api/access') return reply(s.access)
    if (url === '/api/me/model-key') return reply({ authEnabled: false, single: true, verified: true, mode: s.access.models.source === 'endpoint' ? 'stored' : 'none', baseUrl: s.access.models.baseUrl ?? null })
    if (url.startsWith('/api/ai/health')) return reply(s.health)
    if (url.startsWith('/api/chat/models')) return reply({ models: s.models.map(id => ({ id })) })
    if (url === '/api/access/source' && method === 'PUT') {
      const name = (body as { context: string | null }).context
      s.access = accessOf({ cli: { ...accessOf().cli!, chosen: name, active: name ?? 'work', used: name ?? 'work' }, models: { source: 'cli', host: 'platform-b.example.com', context: name }, platform: { source: 'cli', host: 'platform-b.example.com', context: name } })
      return reply({ ok: true })
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
    expect(await screen.findByText('Models and platform tools: pw CLI context work (platform-a.example.com)')).toBeInTheDocument()
    expect(screen.getByText('me:openai/gpt-5')).toBeInTheDocument()
    const table = screen.getAllByRole('table')[0]
    const work = within(table).getByText('work').closest('tr')!
    expect(within(work).getByText('In use')).toBeInTheDocument()
    expect(within(work).getByText('CLI current')).toBeInTheDocument()
    const lab = within(table).getByText('lab').closest('tr')!
    expect(within(lab).getByText('Sign-in')).toBeInTheDocument()
    fireEvent.click(within(lab).getByRole('button', { name: 'Use' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/access/source' && c.method === 'PUT')).toBe(true))
    expect(s.calls.find(c => c.url === '/api/access/source')!.body).toEqual({ context: 'lab', clearKey: false })
    expect(await screen.findByText('Models and platform tools: pw CLI context lab (platform-b.example.com)')).toBeInTheDocument()
    expect(changed).toHaveBeenCalled()
    window.removeEventListener('ade:model-access-changed', changed)
  })

  it('shows an endpoint for models and the CLI for the platform, with the endpoint form chosen', async () => {
    const endpoint = { source: 'endpoint' as const, host: '127.0.0.1:11434', context: null, baseUrl: 'http://127.0.0.1:11434/v1' }
    stub(srv({ access: accessOf({ models: endpoint }) }))
    render(<ModelAccessSection />)
    expect(await screen.findByText('Models: the endpoint http://127.0.0.1:11434/v1')).toBeInTheDocument()
    expect(screen.getByText(/Platform tools \(Workflows, clusters, runs\): pw CLI context work/)).toBeInTheDocument()
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
    expect(await screen.findByText('Models and platform tools: the pw CLI sign-in on the server (platform-a.example.com)')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Deployment' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'pw CLI' })).toBeNull()
    expect(screen.getByText(/Without a key of your own, requests use the pw CLI sign-in on the server/)).toBeInTheDocument()
    expect(screen.queryByText('Subscriptions through ACTIVATE')).toBeNull()
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
