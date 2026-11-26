import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The session view's client takes the page's fetch as it loads, and the chat
// page asks the window's size.
vi.hoisted(() => {
  const real = globalThis.fetch
  const current = ((...a: Parameters<typeof fetch>) => (globalThis.fetch === current ? real(...a) : globalThis.fetch(...a))) as typeof fetch
  globalThis.fetch = current
  globalThis.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })) as never
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as never
  Element.prototype.scrollTo ??= (() => {}) as never
})

import App from '../src/App'
import { forgetModels, setLibrary } from '../src/api'
import { forgetEngines } from '../src/chatEngine'
import { clearChatRecords } from '../src/chatRecord'
import { reloadAppConfig, useAppConfig } from '../src/config'
import { AccountsSection } from '../src/components/AccountsSection'
import { StatusFooter } from '../src/components/StatusFooter'
import { StatusPanel, resetStatusPanelForTests } from '../src/components/StatusPanel'
import { Viewer } from '../src/components/Viewer'
import { reloadPanelState, setPanelView } from '../src/panelState'
import { ChatView } from '../src/views/ChatView'
import { accessOf } from './accessFixtures'

/** Who is at the Studio: one person's own, one people sign in to, and one
 *  whose models need an account of each person's own (strict). */
type Posture = 'one' | 'signin' | 'strict'
const SAYS: Record<Posture, string> = {
  one: 'No model credential. Sign in with ACTIVATE, or add an OpenAI-compatible endpoint, under Settings, Accounts.',
  signin: 'No model credential of your own. Sign in with ACTIVATE, or add an OpenAI-compatible endpoint, under Settings, Accounts.',
  strict: 'Sign in with ACTIVATE first, under Settings, Accounts. The models here need an account of your own.',
}
const none = { source: 'none' as const, host: null, context: null }

let posture: Posture
/** The person's kept ACTIVATE sign-in stopped working. */
let stopped: boolean
let admin: boolean
/** What the model listing answers in place of having no account. */
let models: Record<string, unknown> | null
let job: unknown
let posts: string[]

beforeEach(() => {
  localStorage.clear(); sessionStorage.clear(); forgetEngines(); clearChatRecords(); forgetModels(); setLibrary('kb')
  resetStatusPanelForTests()
  location.hash = ''
  posture = 'strict'; stopped = false; admin = false; models = null; job = null; posts = []
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input), 'http://studio.example')
    const method = (input instanceof Request ? input.method : init?.method ?? 'GET').toUpperCase()
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (method === 'POST') posts.push(url.pathname)
    const signIn = posture !== 'one'
    switch (url.pathname) {
      case '/api/config': return json({
        appName: 'Studio', kbLabel: 'Shared library', aiEnabled: true, accounts: true, signIn, ...(posture === 'strict' ? { signInFirst: true } : {}), ...(stopped ? { signInStopped: true } : {}),
        user: { id: 'ada', username: 'ada' }, suggestedPrompts: [],
        libraries: [{
          id: 'kb', label: 'Shared library', primary: true, writable: true, pinned: true, source: true, caps: { index: true, fullText: true, vectors: false },
          ...(signIn && !admin ? { readOnlyWhy: 'Only its administrators change it.' } : {}),
        }],
      })
      case '/api/chat/models': return json(models ?? { models: [], unreachableSessions: [], error: SAYS[posture], kind: 'credential' })
      case '/api/chat/engine': return json({ setting: 'auto', default: 'studio', current: 'studio', pwcode: false, reason: null })
      case '/api/chat/conversations': return json([])
      case '/api/access': return json(accessOf({ single: !signIn, authEnabled: signIn, models: none, platform: none, deployment: none, cli: null, signInOffered: signIn }))
      case '/api/me/model-key': return json({ authEnabled: signIn, single: !signIn, verified: true, mode: 'none' })
      case '/api/me/platform-sign-in': return json({ state: 'none', host: 'platform-a.example.com' })
      case '/api/ai/health': return json({ ok: false, status: posture === 'strict' ? 'key-required' : 'unconfigured', models: 0, gatewayHost: '', message: SAYS[posture], lastCallFailure: null, checkedAt: new Date().toISOString() })
      case '/api/kb/stats': return json({ available: true, files: 12, dirs: 4, totalBytes: 1000 })
      case '/api/index/status': return json({ running: false, indexed: true, job, queued: [], runs: [], lastChanges: [], lastSweepAt: new Date(Date.now() - 120_000).toISOString(), sweepIntervalSec: 300 })
      case '/healthz': return json({ ok: true, gufi: true })
      case '/api/tags': return json({ tags: [] })
      case '/api/kb/tags': return json({ own: [], inherited: [], stored: 'files' })
      case '/api/kb/sheet': return json({ sheets: [{ name: 'Summary' }], index: 0, name: 'Summary', rows: 1, cols: 1, totalRows: 1, totalCols: 1, colWidths: [80], cells: [{ r: 0, c: [[0, 'x']] }] })
      case '/api/notices': return json({ notices: [], unread: 0 })
      case '/api/extensions': return json({ personas: [] })
      case '/api/kb/file': return json({ path: 'a.xlsx', kind: 'other', size: 10, mtime: 1_795_429_000, truncated: false, content: null, source: 'none', format: 'text', index: { state: 'newer', syncEvery: 300, catchUp: 'sync' } })
      default: return json({})
    }
  }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb'); location.hash = '' })

// The configuration is kept between tests.
function Config() { useAppConfig(); return null }
const draw = async (ui: React.ReactElement, p: Posture, as: { admin?: boolean } = {}) => {
  posture = p; admin = !!as.admin
  render(<Config />)
  await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) })
  return render(ui)
}

describe('a new chat with no account for models', () => {
  const said = async (p: Posture) => {
    Object.defineProperty(window, 'innerWidth', { value: 1440, configurable: true })
    await draw(<ChatView active />, p)
    await waitFor(() => expect(screen.queryAllByText('Open Accounts').length).toBeGreaterThan(0), { timeout: 5000 })
    await act(async () => { await new Promise(r => setTimeout(r, 100)) })
    return document.querySelector('.chat-canvas')!.textContent!
  }
  it.each(['one', 'signin', 'strict'] as const)('says so once, not over the thread as well (%s)', async p => {
    await said(p)
    expect(document.querySelector('.cred-banner')).toBeNull()
    expect(screen.getAllByText(/Open Accounts/)).toHaveLength(1)
  })
  it('asks for the ACTIVATE sign-in first where everyone uses an account of their own, and opens its row', async () => {
    const text = await said('strict')
    expect(text).toContain('Sign in with ACTIVATE first')
    expect(text).toContain('The models here need an account of your own.')
    expect(text).not.toMatch(/this computer|A model is needed/)
    expect(document.querySelector('textarea')).toHaveAttribute('placeholder', 'Sign in with ACTIVATE to start chatting')
    act(() => screen.getByText('Open Accounts').click())
    expect(location.hash).toBe('#view=settings:accounts?open=activate')
  })
  it('asks to sign in again where the kept sign-in stopped working, and opens its row', async () => {
    stopped = true
    Object.defineProperty(window, 'innerWidth', { value: 1440, configurable: true })
    await draw(<ChatView active />, 'strict')
    await waitFor(() => expect(screen.queryAllByText('Sign in again').length).toBeGreaterThan(0), { timeout: 5000 })
    await act(async () => { await new Promise(r => setTimeout(r, 100)) })
    const text = document.querySelector('.chat-canvas')!.textContent!
    expect(text).toContain('Your ACTIVATE sign-in stopped working')
    expect(text).toContain('The platform no longer accepts your ACTIVATE sign-in. Sign in again under Settings, Accounts.')
    expect(text).not.toMatch(/Sign in with ACTIVATE|Open Accounts/)
    expect(document.querySelector('textarea')).toHaveAttribute('placeholder', 'Sign in again to start chatting')
    act(() => screen.getByText('Sign in again').click())
    expect(location.hash).toBe('#view=settings:accounts?open=activate')
  })
  it('speaks of no model on this computer where people sign in, which is a server', async () => {
    const text = await said('signin')
    expect(text).toContain('an OpenAI-compatible endpoint of your own')
    expect(text).not.toContain('this computer')
  })
  it('is as it was on a Studio of one', async () => {
    const text = await said('one')
    expect(text).toContain('A model is needed')
    expect(text).toContain('such as a model server on this computer')
  })
  it('does not ask for a sign-in that is there, and says over the thread what failed', async () => {
    models = { models: [], unreachableSessions: [], error: 'The gateway did not answer.' }
    Object.defineProperty(window, 'innerWidth', { value: 1440, configurable: true })
    await draw(<ChatView active />, 'strict')
    expect(await screen.findByText('The gateway did not answer.', {}, { timeout: 5000 })).toBeInTheDocument()
    expect(await screen.findByText(/No model is listed for your account/)).toBeInTheDocument()
    expect(screen.queryByText('Sign in with ACTIVATE first')).toBeNull()
  })
})

describe('Settings, Accounts before signing in', () => {
  it('asks for ACTIVATE first in the strict posture, with its row open', async () => {
    await draw(<AccountsSection />, 'strict')
    expect(await screen.findByText(/^Not signed in\. Sign in with ACTIVATE first/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'ACTIVATE' })).toHaveAttribute('aria-expanded', 'true')
    const text = document.querySelector('.accounts')!.textContent!
    expect(text).toContain('Sign in with ACTIVATE first. My library, buckets, pw code, workflows and desktops run with your own ACTIVATE sign-in or key, and so do models, unless you save an endpoint of your own. Browsing and searching Shared library work without one.')
    expect(text).not.toMatch(/Studio works without|a model server on this computer/)
  })
  it('says what works without one on another sign-in Studio', async () => {
    await draw(<AccountsSection />, 'signin')
    expect(await screen.findByText('Not signed in. Browsing and search work without ACTIVATE; sign in to use the parts below.')).toBeInTheDocument()
    const text = document.querySelector('.accounts')!.textContent!
    expect(text).not.toMatch(/Studio works without|a model server on this computer|libraries on this computer/)
    expect(screen.getByRole('button', { name: 'ACTIVATE' })).toHaveAttribute('aria-expanded', 'false')
  })
  it('is as it was on a Studio of one', async () => {
    await draw(<AccountsSection />, 'one')
    expect(await screen.findByText(/^Not signed in\. The Studio works without ACTIVATE/)).toBeInTheDocument()
    expect(screen.getByText(/The Studio works without any of them: chat with a model of/)).toBeInTheDocument()
  })
})

describe('the footer, once a kept sign-in stopped working', () => {
  it('asks to sign in again, not to sign in with ACTIVATE', async () => {
    stopped = true
    await draw(<StatusFooter />, 'strict')
    expect(await screen.findByText('Sign in again')).toBeInTheDocument()
    expect(screen.queryByText('Sign in with ACTIVATE')).toBeNull()
  })
  it('still asks to sign in with ACTIVATE before a first sign-in', async () => {
    await draw(<StatusFooter />, 'strict')
    expect(await screen.findByText('Sign in with ACTIVATE')).toBeInTheDocument()
  })
})

describe('Sync now', () => {
  const footer = async (p: Posture, as: { admin?: boolean } = {}) => {
    await draw(<StatusFooter />, p, as)
    await screen.findByText('Synced 2m ago')
  }
  it('is not offered to a person who may not change the main library', async () => {
    await footer('strict')
    expect(screen.queryByRole('button', { name: 'Sync now' })).toBeNull()
    act(() => screen.getByText('12 files indexed').click())
    const panel = await screen.findByRole('dialog', { name: 'Index health' })
    expect(within(panel).queryByRole('button', { name: 'Sync now' })).toBeNull()
  })
  it('nor is Stop, beside a job', async () => {
    job = { id: 1, kind: 'build', state: 'running', label: 'Indexing', phases: [], startedAt: new Date().toISOString(), log: [] }
    await draw(<StatusFooter />, 'strict')
    await waitFor(() => expect(document.querySelector('.status-line[title="Click for index health details"] button')).not.toBeNull())
    act(() => document.querySelector<HTMLElement>('.status-line[title="Click for index health details"] button')!.click())
    const panel = await screen.findByRole('dialog', { name: 'Index health' })
    expect(panel.querySelector('.ix-job')).not.toBeNull()
    expect(within(panel).queryByRole('button', { name: 'Stop' })).toBeNull()
  })
  it.each([['an administrator', 'strict', true], ['the one person here', 'one', false]] as const)('is there for %s', async (_, p, isAdmin) => {
    await footer(p, { admin: isAdmin })
    act(() => screen.getByRole('button', { name: 'Sync now' }).click())
    await waitFor(() => expect(posts).toContain('/api/index/sweep'))
  })
  it.each([['strict', false, false], ['strict', true, true], ['one', false, true]] as const)('in the Status panel: %s, administrator %s', async (p, isAdmin, there) => {
    posture = p; admin = isAdmin
    localStorage.setItem('ade-status-panel', '1'); localStorage.setItem('ade-status-cards', JSON.stringify({ libraries: true }))
    reloadPanelState(); setPanelView('search')
    function Panel() { return <StatusPanel view="search" cfg={useAppConfig()} /> }
    await draw(<Panel />, p, { admin: isAdmin })
    const head = await screen.findByRole('button', { name: /^Libraries/ })
    if (head.getAttribute('aria-expanded') !== 'true') act(() => head.click())
    expect(await screen.findByText(/^Synced/)).toBeInTheDocument()
    expect(!!screen.queryByRole('button', { name: 'Sync now' })).toBe(there)
  })
  it('is not named as what brings a file\'s text up to date, where it is not offered', async () => {
    await draw(<Viewer path="a.xlsx" onDeleted={() => {}} />, 'strict')
    act(() => screen.queryByRole('tab', { name: 'Indexed text' })?.click())
    const tab = await screen.findByRole('tab', { name: 'Indexed text' })
    act(() => tab.click())
    expect(await screen.findByText("Added or changed since the library's last index pass. The background sync, every 5 minutes, adds its text.")).toBeInTheDocument()
  })
})

describe('the welcome on a first visit', () => {
  it('is a part of the page, at its foot, so it lies over nothing', async () => {
    location.hash = '#view=notices'
    await draw(<App />, 'strict')
    const card = await screen.findByRole('region', { name: 'Welcome to Studio' })
    expect(card.parentElement).toBe(document.querySelector('main.content'))
    expect(card).toBe(document.querySelector('main.content')!.lastElementChild)
    const css = readFileSync('src/styles.css', 'utf8').match(/^\.first-visit \{[^}]*\}/m)![0]
    expect(css).not.toMatch(/position:\s*(fixed|absolute|sticky)/)
    expect(card).not.toHaveTextContent('….')
  })
})
