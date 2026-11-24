import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { type KeyTestResult, ModelAccessSection, endpointBase, readPlatformHost, testNote } from '../src/components/ModelAccessSection'
import { UpdatesSection } from '../src/components/UpdatesSection'
import { useAppConfig } from '../src/config'
import { accessOf, choices, choose, reply, srv, stub } from './accessFixtures'

const endpoint = { source: 'endpoint' as const, host: '127.0.0.1:11434', context: null, baseUrl: 'http://127.0.0.1:11434/v1' }

describe('Model access', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  it('says where models come from and lists them, with the accounts behind them under Accounts', async () => {
    stub(srv())
    render(<ModelAccessSection />)
    expect(await screen.findByText('Models: pw CLI context work (platform-a.example.com)')).toBeInTheDocument()
    expect(screen.getByText('me:openai/gpt-5')).toBeInTheDocument()
    expect(choices('Where models come from')).toEqual(['ACTIVATE', 'OpenAI-compatible endpoint'])
    expect(screen.getByRole('radio', { name: 'ACTIVATE' })).toBeChecked()
    expect(screen.getByRole('link', { name: 'Accounts' })).toHaveAttribute('href', '#view=settings:accounts')
    // No credential is entered here.
    expect(screen.queryByLabelText('Platform key or token')).toBeNull()
    expect(screen.queryByLabelText('Base URL')).toBeNull()
    expect(screen.queryByText('Subscriptions through ACTIVATE')).toBeNull()
  })

  it('says offline plainly, as a warning rather than an error', async () => {
    stub(srv({ health: { status: 'offline', models: 0, gatewayHost: 'platform-a.example.com', message: 'Could not reach platform-a.example.com (ENOTFOUND). This computer may be offline, or the server is down. Local libraries and a local model keep working.' }, models: [] }))
    render(<ModelAccessSection />)
    expect(await screen.findByText(/Could not reach platform-a\.example\.com \(ENOTFOUND\)/)).toBeInTheDocument()
  })

  it('choosing an endpoint before there is one sends the person to Accounts', async () => {
    stub(srv())
    render(<ModelAccessSection />)
    await screen.findByText('Models: pw CLI context work (platform-a.example.com)')
    choose('OpenAI-compatible endpoint')
    expect(screen.getByText('Add an endpoint under Accounts')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open Accounts' }))
    expect(location.hash).toBe('#view=settings:accounts?open=endpoint')
    history.replaceState(null, '', location.pathname)
  })

  it('choosing ACTIVATE while an endpoint serves models says the endpoint goes, and removes it only when asked', async () => {
    const s = srv({ access: accessOf({ models: endpoint }) })
    stub(s)
    render(<ModelAccessSection />)
    expect(await screen.findByText('Models: the endpoint http://127.0.0.1:11434/v1')).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'OpenAI-compatible endpoint' })).toBeChecked()
    choose('ACTIVATE')
    expect(screen.getByText(/Using ACTIVATE removes the endpoint at http:\/\/127\.0\.0\.1:11434\/v1/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Keep the endpoint' }))
    expect(screen.getByRole('radio', { name: 'OpenAI-compatible endpoint' })).toBeChecked()
    expect(s.calls.some(c => c.url === '/api/me/model-key/clear')).toBe(false)
    choose('ACTIVATE')
    const cli = { source: 'cli' as const, host: 'platform-a.example.com', context: 'work' }
    const base = globalThis.fetch as unknown as (input: string, init?: RequestInit) => Promise<Response>
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      if (String(input) === '/api/me/model-key/clear') { s.calls.push({ method: 'POST', url: '/api/me/model-key/clear', body: {} }); s.access = accessOf({ models: cli }); return reply({ mode: 'none' }) }
      return base(input, init)
    }))
    fireEvent.click(screen.getByRole('button', { name: 'Remove the endpoint and use ACTIVATE' }))
    expect(await screen.findByText('Models: pw CLI context work (platform-a.example.com)')).toBeInTheDocument()
    expect(s.calls.some(c => c.url === '/api/me/model-key/clear')).toBe(true)
  })

  it('choosing ACTIVATE with no ACTIVATE account asks to sign in, from Accounts', async () => {
    const none = { source: 'none' as const, host: null, context: null }
    stub(srv({ access: accessOf({ models: endpoint, platform: none, deployment: none }) }))
    render(<ModelAccessSection />)
    await screen.findByText('Models: the endpoint http://127.0.0.1:11434/v1')
    expect(screen.getByRole('radio', { name: 'ACTIVATE' })).toHaveAccessibleDescription(/Sign in with ACTIVATE under Accounts first\./)
    choose('ACTIVATE')
    expect(screen.getByText('Sign in with ACTIVATE to use this')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Open Accounts' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Remove the endpoint and use ACTIVATE' })).toBeNull()
  })

  it('with no model at all, points to Accounts', async () => {
    const none = { source: 'none' as const, host: null, context: null }
    stub(srv({ access: accessOf({ models: none, platform: none, deployment: none }), models: [], health: { status: 'unconfigured', models: 0 } }))
    render(<ModelAccessSection />)
    expect(await screen.findByText('No model access yet')).toBeInTheDocument()
    expect(screen.getByText(/Sign in with ACTIVATE, or add an OpenAI-compatible endpoint, under/)).toBeInTheDocument()
  })
})

describe('reading what was typed', () => {
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

  it('says in plain words whether the model calls tools', () => {
    const base: KeyTestResult = { status: 'ok', models: 3, ids: ['a'] }
    expect(testNote(base)).toEqual({ tone: 'success', text: 'Connected: 3 models available.' })
    expect(testNote({ ...base, toolCheck: { model: 'a', result: 'no-call', message: 'a answered without calling the test tool.' } }))
      .toEqual({ tone: 'warning', text: 'Connected: 3 models available.', detail: 'a answered without calling the test tool.' })
    expect(testNote({ ...base, checked: 'completion', toolCheck: { model: 'a', result: 'calls', message: 'a calls tools.' } }).text).toBe('Connected: a chat completion with a answered.')
    expect(testNote({ status: 'unreachable', models: 0, message: 'x does not exist (404).' })).toEqual({ tone: 'error', text: 'x does not exist (404).' })
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
