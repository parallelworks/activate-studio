import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AccountsSection } from '../src/components/AccountsSection'
import { accessOf, choose, reply, srv, stub } from './accessFixtures'

/**
 * A person keeps an ACTIVATE key and their own OpenAI-compatible endpoint
 * side by side: each row shows and removes its own, and nothing says
 * saving one replaces the other. Where Node keeps the keys (no `activate`
 * in /api/me/model-key) there is still one place, and the rows say so.
 */

const url = 'https://models.example.org/v1'
const endpointSource = { source: 'endpoint' as const, host: 'models.example.org', context: null, baseUrl: url }
const keySource = { source: 'key' as const, host: 'platform-a.example.com', context: null }
const replaces = /Saving one replaces your/

/** A signed-in person on a Studio several people use, with what
 *  /api/me/model-key answers; the clear requests are recorded. */
function person(key: Record<string, unknown>, cleared: unknown[]) {
  const base = globalThis.fetch as unknown as (input: string, init?: RequestInit) => Promise<Response>
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const u = String(input)
    if (u === '/api/me/model-key/clear') { cleared.push(JSON.parse(String(init?.body ?? '{}'))); return reply({ mode: 'none' }) }
    if (u === '/api/me/model-key') return reply({ authEnabled: true, verified: true, sharingAllowed: false, shared: { active: false }, ...key })
    if (u === '/api/me/platform-sign-in') return reply({ state: 'none', host: 'platform-a.example.com' })
    return base(input, init)
  }))
}

const openRow = async (name: string) => {
  const b = await screen.findByRole('button', { name })
  if (b.getAttribute('aria-expanded') !== 'true') fireEvent.click(b)
}

describe('an ACTIVATE key and an endpoint kept apart', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('shows both saved, says neither replaces the other, and removes only the one asked for', async () => {
    stub(srv({ access: accessOf({ single: false, authEnabled: true, models: endpointSource, platform: keySource, deployment: { source: 'none', host: null, context: null }, cli: null, signInOffered: true }) }))
    const cleared: unknown[] = []
    person({
      mode: 'session', last4: '5678', baseUrl: url, sessionExpiresAt: '2026-11-25T07:00:00.000Z',
      activate: { mode: 'session', last4: '1234', baseUrl: null, sessionExpiresAt: '2026-11-25T07:00:00.000Z', kind: 'api-key' },
    }, cleared)
    render(<AccountsSection />)
    await openRow('OpenAI-compatible endpoint')
    expect(within(screen.getByRole('button', { name: 'OpenAI-compatible endpoint' })).getByText('Saved')).toBeInTheDocument()
    await openRow('ACTIVATE')
    choose('ACTIVATE API key')
    expect(await screen.findByText(/^Held in memory until/)).toBeInTheDocument()
    expect(screen.queryByText(replaces)).toBeNull()

    const removes = screen.getAllByRole('button', { name: 'Remove' })
    expect(removes).toHaveLength(2)
    // The endpoint's Remove comes first, in its row.
    fireEvent.click(removes[0])
    await waitFor(() => expect(cleared).toEqual([{ which: 'endpoint' }]))
    await waitFor(() => expect(screen.getAllByRole('button', { name: 'Remove' })[1]).not.toBeDisabled())
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove' })[1])
    await waitFor(() => expect(cleared).toEqual([{ which: 'endpoint' }, { which: 'activate' }]))
  })

  it('where Node keeps the keys, still says saving one replaces the other', async () => {
    stub(srv({ access: accessOf({ single: false, authEnabled: true, models: endpointSource, platform: { source: 'none', host: null, context: null }, deployment: { source: 'none', host: null, context: null }, cli: null, signInOffered: true }) }))
    person({ mode: 'session', last4: '5678', baseUrl: url }, [])
    render(<AccountsSection />)
    await openRow('ACTIVATE')
    choose('ACTIVATE API key')
    expect(await screen.findByText(replaces)).toHaveTextContent('Saving one replaces your OpenAI-compatible endpoint, which is kept in the same place.')
  })

  it('Model access, moving models to ACTIVATE, removes the endpoint only', async () => {
    const { ModelAccessSection } = await import('../src/components/ModelAccessSection')
    stub(srv({ access: accessOf({ single: false, authEnabled: true, models: endpointSource, platform: keySource, deployment: { source: 'none', host: null, context: null }, cli: null, signInOffered: true }) }))
    const cleared: unknown[] = []
    person({ mode: 'session', last4: '5678', baseUrl: url, activate: { mode: 'session', last4: '1234', baseUrl: null } }, cleared)
    render(<ModelAccessSection />)
    await screen.findByRole('radio', { name: 'ACTIVATE' })
    choose('ACTIVATE')
    fireEvent.click(await screen.findByRole('button', { name: 'Remove the endpoint and use ACTIVATE' }))
    await waitFor(() => expect(cleared).toEqual([{ which: 'endpoint' }]))
  })
})
