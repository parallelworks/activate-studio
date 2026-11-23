import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { WorkflowsView } from '../src/views/WorkflowsView'
import { WorkflowsSection } from '../src/components/WorkflowsSection'
import { CodeView } from '../src/views/CodeView'
import { needsActivate } from '../src/accounts'

const reply = (body: unknown, code = 200) => new Response(JSON.stringify(body), { status: code, headers: { 'Content-Type': 'application/json' } })

/** A Studio with Accounts and no ACTIVATE credential for the viewer. */
function noCredential(extra: (url: string) => Response | null = () => null) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    calls.push(url)
    if (url === '/api/config') return reply({ appName: 'Studio', platform: false, accounts: true, user: { id: 'user', username: 'user' } })
    return extra(url) ?? reply({}, 404)
  }))
  return calls
}

describe('one prompt for every ACTIVATE feature without a credential', () => {
  afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

  it('knows a server message about a missing credential by the Accounts it names', () => {
    expect(needsActivate('No platform credential: add your ACTIVATE API key under Settings, Accounts.')).toBe(true)
    expect(needsActivate('The desktop did not answer.')).toBe(false)
    expect(needsActivate(null)).toBe(false)
  })

  it('the Workflows page asks to sign in, opens Accounts, and asks the platform nothing', async () => {
    const calls = noCredential()
    render(<WorkflowsView />)
    expect(await screen.findByText('Sign in with ACTIVATE to use this')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open Accounts' }))
    expect(location.hash).toBe('#view=settings:accounts')
    expect(calls.some(u => u.startsWith('/api/workflows'))).toBe(false)
  })

  it('Settings, Workflows says what it is for, and asks to sign in', async () => {
    const calls = noCredential()
    render(<WorkflowsSection />)
    expect(await screen.findByText('Sign in with ACTIVATE to use this')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Meridian/ })).toBeInTheDocument()
    expect(calls.some(u => u.startsWith('/api/workflows'))).toBe(false)
  })

  it('the Code page shows the prompt with what the server said', async () => {
    noCredential(url => url === '/api/code-sessions'
      ? reply({ error: 'The Code page lists your pw code sessions with your own ACTIVATE sign-in. Sign in with ACTIVATE under Settings, Accounts, then try again.' }, 409)
      : url === '/api/chat/pwcode/here' ? reply({ available: false, reason: 'x', chats: [] }) : null)
    render(<CodeView />)
    expect(await screen.findByText('Sign in with ACTIVATE to use this')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText(/lists your pw code sessions with your own ACTIVATE sign-in/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Open Accounts' })).toBeInTheDocument()
  })
})
