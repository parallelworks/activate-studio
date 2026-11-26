import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CodePage } from '../src/code/CodePage'
import { AccountsSection } from '../src/components/AccountsSection'
import { FOLLOW_MS, PlatformSignInPanel } from '../src/components/PlatformSignInPanel'
import { connectionLines, modelSummary } from '../src/components/StatusPanel'
import { reloadAppConfig } from '../src/config'
import { accessOf, reply, srv, stub } from './accessFixtures'

const WHY = 'ACTIVATE no longer accepts your sign-in. Sign in again under Settings, Accounts.'
const none = { source: 'none' as const, host: null, context: null }

/** A person on a Studio several people use whose kept sign-in stopped
 *  working: what /api/me/platform-sign-in answers, and starting again. */
function stoppedSignIn(onStart?: () => void) {
  const stubbed = vi.isMockFunction(globalThis.fetch)
  const base = globalThis.fetch as unknown as (input: string, init?: RequestInit) => Promise<Response>
  const calls: string[] = []
  let st: Record<string, unknown> = { state: 'expired', account: 'ada', host: 'platform-a.example.com', signedInAt: '2026-11-25T23:55:00Z', message: WHY }
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    calls.push(`${init?.method ?? 'GET'} ${url}`)
    if (url === '/api/me/model-key') return reply({ authEnabled: true, verified: true, sharingAllowed: false, shared: { active: false }, mode: 'none' })
    if (url === '/api/me/platform-sign-in' && init?.method === 'POST') {
      onStart?.()
      st = { state: 'waiting', host: 'platform-a.example.com', url: 'https://platform-a.example.com/device', code: 'ABCD-EFGH' }
      return reply(st)
    }
    if (url === '/api/me/platform-sign-in') return reply(st)
    if (url === '/api/code-page/where') return reply({ signIn: true })
    if (url === '/api/config') return reply({ appName: 'Studio', platform: false, accounts: true, signIn: true, signInStopped: true, user: { id: 'ada', username: 'ada' } })
    return stubbed ? base(input, init) : reply({}, 404)
  }))
  return calls
}

describe('a kept ACTIVATE sign-in that stopped working', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); history.replaceState(null, '', location.pathname) })

  it('Accounts says so, not Signed in, and offers Sign in again', async () => {
    stub(srv({ access: accessOf({ single: false, authEnabled: true, models: none, platform: none, deployment: none, cli: null, signInOffered: true, signInStopped: WHY }) }))
    const calls = stoppedSignIn()
    render(<AccountsSection />)
    const row = await screen.findByRole('button', { name: 'ACTIVATE' })
    expect(within(row).getByText('Sign in again')).toBeInTheDocument()
    expect(within(row).queryByText('Signed in')).toBeNull()
    // It opens on its own, and says why.
    await waitFor(() => expect(row).toHaveAttribute('aria-expanded', 'true'))
    expect(await screen.findByText('Your ACTIVATE sign-in stopped working: the platform no longer accepts it. Sign in again to use the parts below.')).toBeInTheDocument()
    expect(await screen.findByText('Your ACTIVATE sign-in stopped working (ada on platform-a.example.com)')).toBeInTheDocument()
    expect(screen.getByText(WHY)).toBeInTheDocument()
    expect(screen.queryByText(/^Signed in as ada/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }))
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument()
    expect(calls).toContain('POST /api/me/platform-sign-in')
  })

  it('the sign-in panel tells the page once it stops working, and again once signed in', async () => {
    let st: Record<string, unknown> = { state: 'signed-in', account: 'ada', host: 'platform-a.example.com' }
    vi.stubGlobal('fetch', vi.fn(async () => reply(st)))
    const changed = vi.fn()
    const { unmount } = render(<PlatformSignInPanel onChanged={changed} />)
    expect(await screen.findByText('Signed in as ada on platform-a.example.com')).toBeInTheDocument()
    unmount()
    st = { state: 'expired', account: 'ada', host: 'platform-a.example.com', message: WHY }
    render(<PlatformSignInPanel onChanged={changed} />)
    expect(await screen.findByRole('button', { name: 'Sign in again' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
    // Found stopped on its first read: the page reads again.
    await waitFor(() => expect(changed).toHaveBeenCalledTimes(1))
  })

  it('an open page sees the sign-in stop working, and work again, without leaving', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      let st: Record<string, unknown> = { state: 'signed-in', account: 'ada', host: 'platform-a.example.com' }
      vi.stubGlobal('fetch', vi.fn(async () => reply(st)))
      const changed = vi.fn()
      render(<PlatformSignInPanel onChanged={changed} />)
      expect(await screen.findByText('Signed in as ada on platform-a.example.com')).toBeInTheDocument()
      st = { state: 'expired', account: 'ada', host: 'platform-a.example.com', message: WHY }
      await act(async () => { await vi.advanceTimersByTimeAsync(FOLLOW_MS + 100) })
      expect(await screen.findByRole('button', { name: 'Sign in again' })).toBeInTheDocument()
      expect(screen.queryByText('Signed in as ada on platform-a.example.com')).toBeNull()
      await waitFor(() => expect(changed).toHaveBeenCalledTimes(1))
      st = { state: 'signed-in', account: 'ada', host: 'platform-a.example.com' }
      await act(async () => { await vi.advanceTimersByTimeAsync(FOLLOW_MS + 100) })
      expect(await screen.findByText('Signed in as ada on platform-a.example.com')).toBeInTheDocument()
      await waitFor(() => expect(changed).toHaveBeenCalledTimes(2))
    } finally { vi.useRealTimers() }
  })

  it('the Status panel asks to sign in again for the platform and the models', () => {
    const go = connectionLines({ platform: false, authEnabled: true, signInStopped: true, ai: null, mcp: null, libs: [], userLibs: null })
    expect(go.find(r => r.key === 'platform')?.meta).toBe('sign in again to use it')
    expect(connectionLines({ platform: false, authEnabled: true, ai: null, mcp: null, libs: [], userLibs: null }).find(r => r.key === 'platform')?.meta).toBe('sign in with ACTIVATE to use it')
    const ai = { ok: false, status: 'key-required' as const, models: 0, gatewayHost: '', message: null, lastCallFailure: null, checkedAt: '' }
    expect(modelSummary(ai, null, true).text).toBe('sign in again')
    expect(modelSummary(ai, null).text).toBe('sign in with ACTIVATE')
  })

  it('a page that needs ACTIVATE says it stopped working, and Sign in again opens Accounts', async () => {
    stoppedSignIn()
    render(<CodePage />)
    // The configuration this file's other tests read is kept; read it again.
    act(() => reloadAppConfig())
    expect(await screen.findByText('Your ACTIVATE sign-in stopped working')).toBeInTheDocument()
    expect(screen.queryByText('Sign in with ACTIVATE to use this')).toBeNull()
    expect(screen.getByText(/lists, starts and changes your pw code sessions as you/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in again' }))
    expect(location.hash).toBe('#view=settings:accounts?open=activate')
  })
})
