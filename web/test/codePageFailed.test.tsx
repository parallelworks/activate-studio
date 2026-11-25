import { cleanup, render, screen, within } from '@testing-library/react'
import { ApiError } from '@parallelworks/problem'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatProvider, type ChatAdapter } from '@parallelworks/ui/ai'
import { SWRConfig } from 'swr'
import { CodePage } from '../src/code/CodePage'
import { describeError } from '../src/pwcode/describeError'
import { PWCodeSession } from '../src/pwcode/PWCodeSession'

// The session view's client takes the page's fetch as it loads.
vi.hoisted(() => {
  const real = globalThis.fetch
  const current = ((...a: Parameters<typeof fetch>) => (globalThis.fetch === current ? real(...a) : globalThis.fetch(...a))) as typeof fetch
  globalThis.fetch = current
})

const DOWN = 'The platform did not answer.'

/** A Studio whose platform does not answer. */
function studio() {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://studio.example'), init)
    const path = new URL(req.url, 'http://studio.example').pathname
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': status < 400 ? 'application/json' : 'application/problem+json' } })
    if (path === '/api/code-page/where') return json({ local: false, codeUrl: 'https://platform.example/code' })
    if (path === '/api/chat/pwcode/here') return json({ available: true, daemonId: 'd1', hostname: 'box.example', chats: [] })
    if (path === '/api/delegated') return json({ sessions: [] })
    if (path === '/api/mcp/bridge/machines') return json({ available: true, machines: [] })
    if (path === '/api/code/sessions') return json({ machines: [], sessions: [] })
    return json({ title: 'Bad Gateway', status: 502, detail: DOWN }, 502)
  }))
}

beforeEach(() => { location.hash = '#view=code'; localStorage.clear(); studio() })
afterEach(() => { cleanup(); vi.unstubAllGlobals(); location.hash = '' })

describe('the Code page with the platform not answering', () => {
  it('says so in the pane as in the list', async () => {
    render(<SWRConfig value={{ provider: () => new Map() }}><CodePage /></SWRConfig>)
    const pane = await screen.findByTestId('agents-list-error')
    expect(pane).toHaveTextContent(new RegExp(`^${DOWN}$`))
    expect(await screen.findAllByText(DOWN)).toHaveLength(2)
  })

  it('says so in place of a session it could not read', async () => {
    const adapter = { listConversations: async () => [], getConversation: async () => null } as unknown as ChatAdapter
    render(
      <SWRConfig value={{ provider: () => new Map() }}>
        <ChatProvider adapter={adapter} currentUser={{ username: 'alex' }} navigation={{ push: () => {}, replace: () => {} } as never}>
          <PWCodeSession conversationId="c1" machine="m2" sessionId="s9" username="alex" />
        </ChatProvider>
      </SWRConfig>,
    )
    const retry = await screen.findByRole('button', { name: 'Try again' }, { timeout: 5000 })
    expect(within(retry.parentElement!).getByRole('alert')).toHaveTextContent(new RegExp(`^${DOWN}$`))
  })

  it('never says an error by its bare code', () => {
    for (const code of ['invalid_request', 'unauthenticated', 'forbidden', 'not_found', 'conflict', 'rate_limited', 'internal', 'unavailable', 'network']) {
      expect(describeError(new ApiError({ status: 0, code }))).toMatch(/^[A-Z].* .*\.$/)
    }
    expect(describeError(new ApiError({ status: 502, code: 'internal', message: DOWN }))).toBe(DOWN)
    expect(describeError({ error: 'pw code is busy', status: 409 })).toBe('pw code is busy')
  })
})
