import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AccountsSection, activateParts } from '../src/components/AccountsSection'
import { accessOf, choices, choose, reply, srv, stub } from './accessFixtures'

const signedInCli = 'Signed in: pw CLI context work (platform-a.example.com).'
/** The parts ACTIVATE turns on, each with its state, as the table shows them. */
const parts = () => Object.fromEntries([...(document.querySelector('table.access-uses-table') as HTMLElement).querySelectorAll('tbody tr, tr')]
  .map(tr => [...tr.querySelectorAll('td')].map(td => td.textContent ?? ''))
  .filter(r => r.length === 2))

/** A signed-in viewer of a Studio several people use, with what
 *  /api/me/model-key and /api/me/platform-sign-in answer. */
function sharedViewer(key: Record<string, unknown>, signIn: () => Record<string, unknown> = () => ({ state: 'none', host: 'platform-a.example.com' }),
  onSignIn?: () => void) {
  const base = globalThis.fetch as unknown as (input: string, init?: RequestInit) => Promise<Response>
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (url === '/api/me/model-key') return reply({ authEnabled: true, verified: true, sharingAllowed: false, shared: { active: false }, ...key })
    if (url === '/api/me/platform-sign-in' && init?.method === 'POST') { onSignIn?.(); return reply(signIn()) }
    if (url === '/api/me/platform-sign-in') return reply(signIn())
    return base(input, init)
  }))
}

describe('Settings, Accounts on a Studio of one', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  it('says who ACTIVATE is signed in as, and what it turns on, each part with its state', async () => {
    stub(srv())
    render(<AccountsSection />)
    expect(await screen.findByText(signedInCli)).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'ACTIVATE' })).toBeInTheDocument()
    expect(parts()).toEqual({
      'pw code and the Code page': 'On',
      'Libraries on ACTIVATE systems (pw ssh)': 'On',
      Workflows: 'On',
      Desktops: 'On',
      'The assistant\'s platform tools': 'On',
      Models: 'On',
    })
    expect(choices()).toEqual(['pw CLI on this computer', 'ACTIVATE API key'])
    expect(screen.getByRole('radio', { name: 'pw CLI on this computer' })).toBeChecked()
    // Then other model services, and the place kept for the Claude account.
    expect(screen.getByRole('heading', { level: 2, name: 'OpenAI-compatible endpoint' })).toBeInTheDocument()
    expect(screen.getByText('/usr/local/bin/pw').closest('p')).toHaveTextContent('pw v7.1.0 at /usr/local/bin/pw, found on the PATH.')
  })

  it('works with no ACTIVATE at all: every part asks to sign in, and an endpoint serves models', async () => {
    const none = { source: 'none' as const, host: null, context: null }
    const endpoint = { source: 'endpoint' as const, host: '127.0.0.1:11434', context: null, baseUrl: 'http://127.0.0.1:11434/v1' }
    stub(srv({ access: accessOf({ models: endpoint, platform: none, deployment: none, cli: { ...accessOf().cli!, contexts: [], active: null, used: null } }) }))
    render(<AccountsSection />)
    expect(await screen.findByText(/^Not signed in\. The Studio works without ACTIVATE/)).toBeInTheDocument()
    const p = parts()
    expect(p['pw code and the Code page']).toBe('Sign in to use')
    expect(p.Workflows).toBe('Sign in to use')
    expect(p.Models).toBe('From your endpoint http://127.0.0.1:11434/v1')
    expect(screen.getByLabelText('Base URL')).toHaveValue('http://127.0.0.1:11434/v1')
    expect(screen.getByText('In use for models')).toBeInTheDocument()
    expect(screen.queryByText('Subscriptions through ACTIVATE')).toBeNull()
  })

  it('lists the pw CLI contexts and switches to another one, keeping an endpoint', async () => {
    const endpoint = { source: 'endpoint' as const, host: '127.0.0.1:11434', context: null, baseUrl: 'http://127.0.0.1:11434/v1' }
    const s = srv({ access: accessOf({ models: endpoint }) })
    stub(s)
    const changed = vi.fn()
    window.addEventListener('ade:model-access-changed', changed)
    render(<AccountsSection />)
    await screen.findByText(signedInCli)
    const table = document.querySelector('table.cli-contexts') as HTMLElement
    const work = within(table).getByText('work').closest('tr')!
    // An endpoint serves models only, so the context still serves the platform.
    expect(within(work).getByText('In use')).toBeInTheDocument()
    expect(within(work).getByText('CLI current')).toBeInTheDocument()
    const lab = within(table).getByText('lab').closest('tr')!
    expect(within(lab).getByText('Sign-in')).toBeInTheDocument()
    fireEvent.click(within(lab).getByRole('button', { name: 'Use' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/access/source' && c.method === 'PUT')).toBe(true))
    expect(s.calls.find(c => c.url === '/api/access/source')!.body).toEqual({ context: 'lab', clearKey: false })
    expect(await screen.findByText('Signed in: pw CLI context lab (platform-b.example.com).')).toBeInTheDocument()
    expect(changed).toHaveBeenCalled()
    window.removeEventListener('ade:model-access-changed', changed)
  })

  it('signs the CLI in with a device code, and reports when it is done', async () => {
    const s = srv()
    stub(s)
    render(<AccountsSection />)
    const host = await screen.findByLabelText('Platform host to sign in to')
    fireEvent.change(host, { target: { value: 'platform-c.example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /platform-c\.example\.com\/device/ })).toHaveAttribute('href', 'https://platform-c.example.com/device')
    expect(s.calls.find(c => c.url === '/api/access/cli/login' && c.method === 'POST')!.body).toEqual({ host: 'platform-c.example.com' })
    s.login = { host: 'platform-c.example.com', state: 'done', url: null, code: null, context: 'alex@platform-c.example.com', message: 'Signed in. The Studio now uses the pw CLI context alex@platform-c.example.com.' }
    expect(await screen.findByText('Signed in. The Studio now uses the pw CLI context alex@platform-c.example.com.', {}, { timeout: 4000 })).toBeInTheDocument()
  })

  it('says when the Studio cannot find the pw CLI, marks the parts that need it, and takes its location', async () => {
    const s = srv({ access: accessOf({ cli: { ...accessOf().cli!, available: false, version: null, path: null, source: 'none' } }) })
    stub(s)
    render(<AccountsSection />)
    expect(await screen.findByText('The Studio cannot find the pw CLI')).toBeInTheDocument()
    expect(parts().Workflows).toBe('pw CLI not found')
    expect(screen.getByText('/home/me/.local/bin')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Where the pw CLI is'), { target: { value: '/opt/tools/pw' } })
    fireEvent.click(screen.getByRole('button', { name: 'Use this location' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/access/cli/path' && c.method === 'PUT' && (c.body as { path: string }).path === '/opt/tools/pw')).toBe(true))
    expect(await screen.findByText('The Studio now runs the pw CLI at /opt/tools/pw.')).toBeInTheDocument()
    expect(await screen.findByText('/opt/tools/pw')).toBeInTheDocument()
    expect(screen.getByText('/opt/tools/pw').closest('p')).toHaveTextContent('chosen here')
    expect(s.calls.some(c => c.url === '/api/access?fresh=1')).toBe(true)
  })

  it('offers a ChatGPT plan through the platform when it is offered there', async () => {
    stub(srv())
    render(<AccountsSection />)
    expect(await screen.findByText('Subscriptions through ACTIVATE')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Connect' })).toBeInTheDocument()
    expect(screen.getAllByText('This platform does not offer it.')).toHaveLength(1)
  })

  it('with AI features off, keeps ACTIVATE only: no models, no endpoint, no subscriptions', async () => {
    stub(srv())
    render(<AccountsSection aiEnabled={false} />)
    await screen.findByText(signedInCli)
    expect(Object.keys(parts())).toEqual(['pw code and the Code page', 'Libraries on ACTIVATE systems (pw ssh)', 'Workflows', 'Desktops'])
    expect(screen.queryByRole('heading', { level: 2, name: 'OpenAI-compatible endpoint' })).toBeNull()
    expect(screen.queryByText('Subscriptions through ACTIVATE')).toBeNull()
    expect(screen.queryByRole('link', { name: 'Model access' })).toBeNull()
  })
})

describe('an ACTIVATE API key and an endpoint, each in its own card', () => {
  afterEach(() => { vi.unstubAllGlobals(); localStorage.removeItem('aiChat.model') })

  it('says a typed path is an endpoint, and moves the address and the key to the endpoint card in one click', async () => {
    stub(srv())
    render(<AccountsSection />)
    await screen.findByText(signedInCli)
    choose('ACTIVATE API key')
    fireEvent.change(screen.getByLabelText('Platform key or token'), { target: { value: 'sk-virtual' } })
    fireEvent.change(screen.getByLabelText('Platform host'), { target: { value: 'portal.example.org/v1/chat/completions' } })
    expect(screen.getByText('This is an endpoint address, not a platform host')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Save and use' })[0]).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Use as an endpoint' }))
    expect(screen.getByLabelText('Base URL')).toHaveValue('https://portal.example.org/v1')
    expect(screen.getByLabelText('Endpoint key')).toHaveValue('sk-virtual')
    expect(screen.getByLabelText('Platform key or token')).toHaveValue('')
    expect(screen.getByText(/^Moved here, with https:\/\/portal\.example\.org\/v1 as the base URL/)).toBeInTheDocument()
  })

  it('tests a host that is not an ACTIVATE platform at /v1 too, and says what it is', async () => {
    const s = srv({
      test: b => b.baseUrl === 'https://portal.example.org/api/openai/v1'
        ? { status: 'unreachable', models: 0, notFound: 'https://portal.example.org/api/openai/v1/models', message: 'not an ACTIVATE platform' }
        : { status: 'ok', models: 12, ids: ['m1'] },
    })
    stub(s)
    render(<AccountsSection />)
    await screen.findByText(signedInCli)
    choose('ACTIVATE API key')
    fireEvent.change(screen.getByLabelText('Platform key or token'), { target: { value: 'sk-virtual' } })
    fireEvent.change(screen.getByLabelText('Platform host'), { target: { value: 'portal.example.org' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Test' })[0])
    expect(await screen.findByText('portal.example.org is not an ACTIVATE platform, since https://portal.example.org/api/openai/v1/models does not exist (404).')).toBeInTheDocument()
    expect(screen.getByText('https://portal.example.org/v1 answers as an OpenAI-compatible endpoint with 12 models. Use it under OpenAI-compatible endpoint.')).toBeInTheDocument()
    const tests = s.calls.filter(c => c.url === '/api/me/model-key/test').map(c => c.body)
    expect(tests).toEqual([{ key: 'sk-virtual', baseUrl: 'https://portal.example.org/api/openai/v1' }, { key: 'sk-virtual', baseUrl: 'https://portal.example.org/v1', tools: false }])
    fireEvent.click(screen.getByRole('button', { name: 'Use as an endpoint' }))
    expect(screen.getByLabelText('Base URL')).toHaveValue('https://portal.example.org/v1')
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
    render(<AccountsSection />)
    expect(await screen.findByText('Advanced: key in api-key, 1 header, query string')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Advanced: key in api-key, 1 header, query string'))
    expect(screen.getByLabelText('Header 1 name')).toHaveValue('X-Tenant')
    expect(screen.getByLabelText('Header 1 value')).toHaveValue('')
    expect(screen.getByLabelText('Header 1 value')).toHaveAttribute('placeholder', 'Stored; type to replace')
    expect(screen.getByLabelText('Query string')).toHaveValue('api-version=2026-01-01')
    fireEvent.change(screen.getByLabelText('Model names'), { target: { value: 'tiny-model\nbig-model' } })

    const card = screen.getByRole('heading', { level: 2, name: 'OpenAI-compatible endpoint' }).closest('section')!
    fireEvent.click(within(card).getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('tiny-model answered without calling the test tool, so chat cannot search the libraries with this model.')).toBeInTheDocument()
    expect(s.calls.find(c => c.url === '/api/me/model-key/test')!.body).toEqual({
      key: '', baseUrl: 'https://portal.example.org/v1', model: 'tiny-model',
      endpoint: { auth: 'api-key', headers: [{ name: 'X-Tenant' }], models: ['tiny-model', 'big-model'], query: 'api-version=2026-01-01' },
    })
    fireEvent.click(within(card).getByRole('button', { name: 'Save and use' }))
    await waitFor(() => expect(s.calls.some(c => c.url === '/api/me/model-key' && c.method === 'POST')).toBe(true))
    expect(s.calls.find(c => c.url === '/api/me/model-key' && c.method === 'POST')!.body).toEqual({
      key: '', persist: true, baseUrl: 'https://portal.example.org/v1',
      endpoint: { auth: 'api-key', headers: [{ name: 'X-Tenant' }], models: ['tiny-model', 'big-model'], query: 'api-version=2026-01-01' },
    })
  })

  it('needs a key for a new endpoint unless its key header is None', async () => {
    stub(srv())
    render(<AccountsSection />)
    await screen.findByText(signedInCli)
    const card = screen.getByRole('heading', { level: 2, name: 'OpenAI-compatible endpoint' }).closest('section')!
    fireEvent.change(screen.getByLabelText('Base URL'), { target: { value: 'https://portal.example.org/v1' } })
    expect(within(card).getByRole('button', { name: 'Save and use' })).toBeDisabled()
    fireEvent.click(screen.getByText('Advanced, for portals that differ'))
    fireEvent.click(screen.getByRole('button', { name: 'Add a header' }))
    fireEvent.change(screen.getByLabelText('Header 1 name'), { target: { value: 'Authorization' } })
    fireEvent.change(screen.getByLabelText('Header 1 value'), { target: { value: 'Basic abc' } })
    expect(screen.getByText('Advanced: 1 header')).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Save and use' })).toBeDisabled()
    fireEvent.click(screen.getByRole('combobox', { name: 'Key header' }))
    const none = await screen.findByRole('option', { name: 'None: no key is sent' })
    fireEvent.pointerDown(none)
    fireEvent.click(none)
    expect(await screen.findByText('Advanced: no key header, 1 header')).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Save and use' })).toBeEnabled()
    expect(screen.getByLabelText('Endpoint key')).toHaveAttribute('placeholder', 'No key is sent (Advanced, Key header)')
  })
})

describe('Settings, Accounts on a Studio several people use', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('puts signing in with ACTIVATE first, and names the sign-in once it is in use', async () => {
    const none = { source: 'none' as const, host: null, context: null }
    const s = srv({ access: accessOf({ single: false, authEnabled: true, models: none, platform: none, deployment: none, cli: null, signInOffered: true }) })
    stub(s)
    let state: Record<string, unknown> = { state: 'none', host: 'platform-a.example.com' }
    sharedViewer({ mode: 'stored', last4: 'abcd' }, () => state, () => {
      state = { state: 'signed-in', account: 'alex', host: 'platform-a.example.com' }
      const me = { source: 'signin' as const, host: 'platform-a.example.com', context: null }
      s.access = { ...s.access, models: me, platform: me }
    })
    render(<AccountsSection />)
    await screen.findByRole('radio', { name: 'Sign in with ACTIVATE' })
    expect(choices()).toEqual(['Sign in with ACTIVATE', 'ACTIVATE API key'])
    expect(screen.getByRole('radio', { name: 'Sign in with ACTIVATE' })).toBeChecked()
    expect(screen.getByRole('radio', { name: 'Sign in with ACTIVATE' })).toHaveAccessibleDescription(/^Recommended\./)
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with ACTIVATE' }))
    expect(await screen.findByText('Signed in: your ACTIVATE sign-in (platform-a.example.com).')).toBeInTheDocument()
    expect(screen.getByText('Signed in as alex on platform-a.example.com')).toBeInTheDocument()
    expect(screen.getByText(/The key you added under ACTIVATE API key is kept, and used only while you are signed out\./)).toBeInTheDocument()
    expect(await screen.findByText('Subscriptions through ACTIVATE')).toBeInTheDocument()
    expect(parts()['pw code and the Code page']).toBe('On')
  })

  it('never lends the deployment\'s credential: with no sign-in of their own, every part asks for one', async () => {
    const none = { source: 'none' as const, host: null, context: null }
    stub(srv({ access: accessOf({ single: false, authEnabled: true, models: none, platform: none, deployment: none, cli: null, signInOffered: true }) }))
    sharedViewer({ mode: 'none' })
    render(<AccountsSection />)
    expect(await screen.findByText(/^Not signed in\. The Studio works without ACTIVATE/)).toBeInTheDocument()
    expect(parts()).toEqual({
      'pw code and the Code page': 'Sign in to use',
      'Libraries on ACTIVATE systems (pw ssh)': 'Sign in to use',
      Workflows: 'Sign in to use',
      Desktops: 'Sign in to use',
      'The assistant\'s platform tools': 'Sign in to use',
      Models: 'Sign in to use',
    })
    // No way to choose the deployment's credential, and no pw CLI controls: that CLI is the deployment's.
    expect(screen.queryByRole('radio', { name: /deployment/ })).toBeNull()
    expect(screen.queryByText(/deployment's credential/)).toBeNull()
    expect(screen.queryByRole('radio', { name: 'pw CLI on this computer' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Stop using the pw CLI' })).toBeNull()
    expect(screen.queryByText('Subscriptions through ACTIVATE')).toBeNull()
    expect(screen.queryByText(/shared with everyone/)).toBeNull()
  })

  it('names an OpenAI-compatible endpoint the administrator set as where everyone\'s models come from', async () => {
    const none = { source: 'none' as const, host: null, context: null }
    const admin = { source: 'admin-endpoint' as const, host: 'models.example.org', context: null, baseUrl: 'https://models.example.org/v1' }
    stub(srv({ access: accessOf({ single: false, authEnabled: true, models: admin, platform: none, deployment: admin, cli: null, signInOffered: true }) }))
    sharedViewer({ mode: 'none' })
    render(<AccountsSection />)
    await screen.findByRole('radio', { name: 'Sign in with ACTIVATE' })
    expect(parts().Models).toBe('From the administrator\'s endpoint')
    expect(parts().Workflows).toBe('Sign in to use')
  })

  it('asks a person whose connection carries no verified identity to open the app through the platform', async () => {
    const dep = { source: 'env' as const, host: 'platform-a.example.com', context: null }
    stub(srv({ access: accessOf({ single: false, authEnabled: true, models: dep, platform: dep, deployment: dep, cli: null }) }))
    sharedViewer({ verified: false, mode: 'none' })
    render(<AccountsSection />)
    expect(await screen.findByText(/Your accounts attach to your platform-verified identity/)).toBeInTheDocument()
    expect(screen.queryByRole('radio')).toBeNull()
  })
})

describe('Stop using the pw CLI', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('lets a Studio of one work as for someone without ACTIVATE, and use it again', async () => {
    const s = srv()
    stub(s)
    render(<AccountsSection />)
    fireEvent.click(await screen.findByRole('button', { name: 'Stop using the pw CLI' }))
    expect(await screen.findByText('The Studio is not using the pw CLI')).toBeInTheDocument()
    expect(s.calls.find(c => c.url === '/api/access/cli/use')?.body).toEqual({ use: false })
    expect(screen.getByText(/The pw CLI is still signed in, for the other tools on this computer\./)).toBeInTheDocument()
    expect(screen.getByText(/^Not using the pw CLI\./)).toBeInTheDocument()
    expect(Object.values(parts()).every(v => v === 'Sign in to use')).toBe(true)
    // Nothing was signed out, and the contexts wait until it is used again.
    expect(s.calls.some(c => /logout|sign-out|signout/.test(c.url))).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Use it again' }))
    expect(await screen.findByText(signedInCli)).toBeInTheDocument()
    expect(s.calls.filter(c => c.url === '/api/access/cli/use').map(c => c.body)).toEqual([{ use: false }, { use: true }])
    expect(parts()['pw code and the Code page']).toBe('On')
  })
})

describe('the pw CLI card with no sign-ins', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('offers no Stop, and fills in the platform the Studio knows', async () => {
    const none = { source: 'none' as const, host: null, context: null }
    stub(srv({ access: accessOf({ models: none, platform: none, deployment: none, cli: { ...accessOf().cli!, contexts: [], active: null, used: null } }) }))
    render(<AccountsSection />)
    expect(await screen.findByText(/The pw CLI has no sign-ins yet/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Stop using the pw CLI' })).toBeNull()
    expect(screen.getByLabelText('Platform host to sign in to')).toHaveValue('platform-a.example.com')
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled()
    expect(screen.queryByText(/Type the platform's host to sign in to/)).toBeNull()
    // Emptied, the field says what Sign in needs, in a line everyone sees.
    fireEvent.change(screen.getByLabelText('Platform host to sign in to'), { target: { value: '' } })
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled()
    expect(screen.getByText(/Type the platform's host to sign in to/)).toBeInTheDocument()
  })

  it('says once, in the card, that it stopped', async () => {
    const s = srv()
    stub(s)
    render(<AccountsSection />)
    fireEvent.click(await screen.findByRole('button', { name: 'Stop using the pw CLI' }))
    expect(await screen.findByText('The Studio is not using the pw CLI')).toBeInTheDocument()
    expect(screen.queryByText(/The Studio no longer uses the pw CLI/)).toBeNull()
  })
})

describe('what ACTIVATE turns on', () => {
  it('on a Studio of one, any ACTIVATE credential serves every part', () => {
    const key = { source: 'key' as const, host: 'platform-a.example.com', context: null }
    expect(activateParts(accessOf({ platform: key, models: key }), true).every(p => p.state === 'on')).toBe(true)
    expect(activateParts(accessOf(), false).map(p => p.part)).not.toContain('Models')
  })

  it('on a Studio several people use, a key of the person\'s own counts as theirs', () => {
    const key = { source: 'key' as const, host: 'platform-a.example.com', context: null }
    const a = accessOf({ single: false, authEnabled: true, platform: key, models: key, cli: null })
    expect(activateParts(a, true).every(p => p.state === 'on')).toBe(true)
  })
})
