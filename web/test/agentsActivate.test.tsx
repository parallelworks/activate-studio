import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentsView } from '../src/views/AgentsView'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
let config: object
let standing: unknown[]

beforeEach(() => {
  config = { platform: false }; standing = []
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url === '/api/config') return json({ aiEnabled: true, features: {}, user: { id: 'alex', username: 'alex' }, ...config })
    if (url === '/api/fleet') return json({ agents: standing, unavailable: 'Standing agents run on pw code, which needs ACTIVATE. Sign in with ACTIVATE under Settings, Accounts.' })
    return json({ runs: [], tasks: [], personas: [], skills: [], templates: [] })
  }))
})
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); location.hash = '' })

/** The page with the configuration read afresh: it is kept between renders. */
async function open() {
  render(<AgentsView onOpen={() => {}} />)
  await act(async () => { window.dispatchEvent(new Event('ade:config-changed')); await new Promise(r => setTimeout(r, 20)) })
}
const tabs = () => screen.getAllByRole('tab').map(t => t.textContent)

describe('the Agents page without ACTIVATE', () => {
  it('has no Fleet, and opens on instructions and skills', async () => {
    await open()
    expect(tabs()).toEqual(['Earlier tasks', 'Instructions and skills'])
    expect(screen.getByRole('tab', { name: 'Instructions and skills' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByText('Sign in with ACTIVATE to use this')).toBeNull()
  })

  it('shows Fleet again with a sign-in, with no reload', async () => {
    await open()
    config = { platform: true }
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:model-access-changed')) })
    await waitFor(() => expect(tabs()[0]).toBe('Fleet'))
    expect(screen.getByRole('tab', { name: 'Fleet' })).toHaveAttribute('aria-selected', 'true')
  })

  it('keeps Fleet where there are standing agents, to read or retire', async () => {
    standing = [{ id: 'a', name: 'Queue watcher', goal: 'Watch.', state: 'idle', triggers: { every: '1h' }, budget: {}, note: '', ticks: 0, tokens: 0 }]
    await open()
    await waitFor(() => expect(tabs()[0]).toBe('Fleet'))
    expect(await screen.findByText('Queue watcher')).toBeInTheDocument()
  })

  it('does not send Earlier tasks to the Code page, which is not shown', async () => {
    localStorage.setItem('ade-agents-page', 'tasks')
    await open()
    expect(screen.getByText(/Work you delegate now goes to pw code/)).toBeInTheDocument()
    expect(document.querySelector('a[href^="#view=code"]')).toBeNull()
    config = { platform: true }
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:model-access-changed')) })
    await waitFor(() => expect(document.querySelector('a[href^="#view=code"]')).toBeTruthy())
  })
})
