import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { platformCredential } from '../src/components/accessShared'
import { SubscriptionsPanel } from '../src/components/AccountsSection'

const answer = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const REFUSED = 'The platform refused this credential (401): Unauthorized. A platform token or platform API key is needed; an AI gateway key is not enough.'
const AVAILABLE = {
  available: true, user: 'someone', org: 'org',
  codex: { offered: true, providers: [] },
  copilot: { offered: false, providers: [], github: null },
}

const KEY = platformCredential({ source: 'key', host: 'platform.example', context: null, last4: 'ab12', kind: 'key' })
const SIGNIN = platformCredential({ source: 'signin', host: 'platform.example', context: null })

describe('subscriptions through the platform', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  it('asks again after signing in, so the refusal of a pasted gateway key does not stay', async () => {
    const fetchMock = vi.fn(async () => answer(401, { error: REFUSED }))
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<SubscriptionsPanel platformOrigin="https://platform.example" host="platform.example" credential={KEY} onChanged={() => {}} />)
    expect(await screen.findByText(REFUSED)).toBeInTheDocument()

    // Signing in keeps the same host; only the credential changes.
    fetchMock.mockImplementation(async () => answer(200, AVAILABLE))
    rerender(<SubscriptionsPanel platformOrigin="https://platform.example" host="platform.example" credential={SIGNIN} onChanged={() => {}} />)
    expect(await screen.findByText('Not connected')).toBeInTheDocument()
    expect(screen.queryByText(REFUSED)).toBeNull()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('shows only the answer for the credential in use when an earlier answer arrives late', async () => {
    let release: (r: Response) => void = () => {}
    const fetchMock = vi.fn()
      .mockImplementationOnce(() => new Promise<Response>(resolve => { release = resolve }))
      .mockImplementationOnce(async () => answer(200, AVAILABLE))
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<SubscriptionsPanel platformOrigin={null} host="platform.example" credential={KEY} onChanged={() => {}} />)
    rerender(<SubscriptionsPanel platformOrigin={null} host="platform.example" credential={SIGNIN} onChanged={() => {}} />)
    expect(await screen.findByText('Not connected')).toBeInTheDocument()
    release(answer(401, { error: REFUSED }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByText(REFUSED)).toBeNull()
    expect(screen.getByText('Not connected')).toBeInTheDocument()
  })

  it('does not ask again when nothing about the credential changed', async () => {
    const fetchMock = vi.fn(async () => answer(200, AVAILABLE))
    vi.stubGlobal('fetch', fetchMock)
    const { rerender } = render(<SubscriptionsPanel platformOrigin={null} host="platform.example" credential={SIGNIN} onChanged={() => {}} />)
    expect(await screen.findByText('Not connected')).toBeInTheDocument()
    rerender(<SubscriptionsPanel platformOrigin={null} host="platform.example" credential={SIGNIN} onChanged={() => {}} />)
    await new Promise(r => setTimeout(r, 20))
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
