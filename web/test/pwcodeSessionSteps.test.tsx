import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactElement } from 'react'
import { ChatProvider, type ChatAdapter } from '@parallelworks/ui/ai'
import { CodePage } from '../src/code/CodePage'
import { PWCodeSession } from '../src/pwcode/PWCodeSession'

// The session view's client takes the page's fetch as it loads.
vi.hoisted(() => {
  const real = globalThis.fetch
  const current = ((...a: Parameters<typeof fetch>) => (globalThis.fetch === current ? real(...a) : globalThis.fetch(...a))) as typeof fetch
  globalThis.fetch = current
})

const now = new Date().toISOString()
const machine = (id: string) => ({ id, name: 'box', kind: 'existing', status: 'connected', remoteStart: true, hostname: 'box.example', daemonId: 'd1' })
const listed = (machineId: string) => ({ machineId, id: 's1', name: 'reading two files', status: 'idle', workspace: '/w', createdAt: now, updatedAt: now, messageCount: 1 })
const detail = {
  id: 's1', machineId: 'studio', workspace: '/w', model: 'm', status: 'idle', permissionMode: 'default', createdAt: now, updatedAt: now, messages: 1, lastSeq: 1,
  history: [
    { kind: 'user', text: 'Read a.txt and b.txt.' },
    { kind: 'tool_call', toolName: 'ReadFile', toolArgs: '{"path":"a.txt"}', callId: 'c1' },
    { kind: 'tool_call', toolName: 'ReadFile', toolArgs: '{"path":"b.txt"}', callId: 'c2' },
    { kind: 'tool_result', text: '     1\talpha line\n', callId: 'c1' },
    { kind: 'tool_result', text: '     1\tbeta line\n', callId: 'c2' },
    { kind: 'assistant', text: 'Both were read.' },
    { kind: 'turn_end', turnEnd: { durationMs: 3000, endedAt: now } },
  ],
}

function studio() {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://studio.example'), init)
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    switch (new URL(req.url, 'http://studio.example').pathname) {
      case '/api/code/sessions': return json({ machines: [machine('studio')], sessions: [listed('studio')] })
      case '/api/code-page/sessions': return json({ machines: [machine('m1')], sessions: [listed('m1')] })
      case '/api/code-page/where': return json({ local: false, codeUrl: 'https://platform.example/code' })
      case '/api/code-page/openai/models': return json({ object: 'list', data: [] })
      case '/api/delegated': return json({ sessions: [] })
      case '/api/mcp/bridge/machines': return json({ available: true, machines: [] })
      case '/api/chat/pwcode/here': return json({ available: true, daemonId: 'd1', hostname: 'box.example', chats: [] })
      case '/api/code/machines/studio/sessions/s1': return json(detail)
      case '/api/code/machines/studio/sessions/s1/transcript': return json({ tail: detail, from: 0, total: detail.history.length })
      default: return json({})
    }
  }))
}

beforeEach(() => { location.hash = '#view=code?machine=m1&session=s1'; localStorage.clear() })
afterEach(() => { cleanup(); vi.unstubAllGlobals(); location.hash = '' })

const adapter = { listConversations: async () => [], getConversation: async () => null } as unknown as ChatAdapter
const navigation = { push: () => {}, replace: () => {} } as never
const chat = (
  <ChatProvider adapter={adapter} currentUser={{ username: 'alex' }} navigation={navigation}>
    <PWCodeSession conversationId="c1" machine="studio" sessionId="s1" username="alex" />
  </ChatProvider>
)

describe("a session's steps", () => {
  it.each<[string, ReactElement]>([['a chat', chat], ["the Code page's pane", <CodePage />]])('open on their own in %s, with no button for all of them', async (_where, view) => {
    studio()
    render(view)
    const run = await screen.findByRole('button', { name: /read 2 files/i }, { timeout: 5000 })
    expect(screen.getByRole('button', { name: /terminal/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /(open|fold) steps|every step/i })).toBeNull()

    expect(screen.queryByText(/alpha line/)).toBeNull()
    fireEvent.click(run)
    const first = await screen.findByRole('button', { name: /a\.txt/ })
    fireEvent.click(first)
    await waitFor(() => expect(first).toHaveAttribute('aria-expanded', 'true'))
    expect(screen.getByText(/alpha line/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /b\.txt/ })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText(/beta line/)).toBeNull()
  })
})
