import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatProvider, useChat, type ChatAdapter } from '@parallelworks/ui/ai'
import { createStudioAdapter } from '../src/adapter'
import { describeError } from '../src/pwcode/describeError'
import { firstMessage, handFirstMessage, takeFirstMessage, takeHanded } from '../src/pwcode/firstMessage'
import { PWCodeSession } from '../src/pwcode/PWCodeSession'
import { LeaveOtherChat } from '../src/views/ChatView'

// A pw code command as a new chat's first message was sent to its session
// as a message, refused, and lost with why (pwcodefirst.go).

// The session view's client takes the page's fetch as it loads.
vi.hoisted(() => {
  const real = globalThis.fetch
  const current = ((...a: Parameters<typeof fetch>) => (globalThis.fetch === current ? real(...a) : globalThis.fetch(...a))) as typeof fetch
  globalThis.fetch = current
  globalThis.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })) as never
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as never
})

const now = new Date().toISOString()
const detail = { id: 's1', machineId: 'studio', workspace: '/w', model: 'm', status: 'idle', permissionMode: 'accept-edits', createdAt: now, updatedAt: now, messages: 0, lastSeq: 0, history: [] as unknown[] }
const UNKNOWN = 'unknown command /nosuch: no such skill or custom command in this workspace'

let posts: { path: string; body: Record<string, unknown> }[]
let ready: Promise<void>
let turnedTo: string
function studio() {
  posts = []
  ready = Promise.resolve()
  turnedTo = 'idle'
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://studio.example'), init)
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } })
    const path = new URL(req.url).pathname
    const body = await req.clone().json().catch(() => ({})) as Record<string, unknown>
    if (req.method === 'POST') posts.push({ path: path.replace('/api/code/machines/studio/sessions/s1', ''), body })
    const at = { ...detail, status: turnedTo, ...(turnedTo === 'live_elsewhere' ? { liveHost: 'other' } : {}) }
    switch (path) {
      case '/api/code/sessions': return json({ machines: [{ id: 'studio', name: 'box', kind: 'existing', status: 'connected', remoteStart: true, hostname: 'box.example', daemonId: 'd1' }], sessions: [{ ...at, history: undefined, messageCount: 0 }] })
      case '/api/code/machines/studio/sessions/s1': await ready; return json(at)
      case '/api/code/machines/studio/sessions/s1/transcript': await ready; return json({ tail: at, from: 0, total: 0 })
      case '/api/code/machines/studio/sessions/s1/palette': return json({ commands: [{ name: 'help' }, { name: 'plan' }, { name: 'compact' }], prompts: ['code-review'] })
      case '/api/code/machines/studio/sessions/s1/commands': return json({ output: 'Available commands: many' })
      case '/api/code/machines/studio/sessions/s1/plan': return json({ action: 'turn' })
      case '/api/code/machines/studio/sessions/s1/messages': return String(body.text).startsWith('/nosuch') ? json({ type: '/problems/pwcode/command_unknown', status: 422, detail: UNKNOWN, code: 'command_unknown' }, 422) : json({ turnId: 't1' })
      default: return json({})
    }
  }))
}

const adapter = { listConversations: async () => [], getConversation: async () => null } as unknown as ChatAdapter
const navigation = { push: () => {}, replace: () => {} } as never
const show = () => render(
  <ChatProvider adapter={adapter} currentUser={{ username: 'alex' }} navigation={navigation}>
    <PWCodeSession conversationId="c1" machine="studio" sessionId="s1" username="alex" />
  </ChatProvider>,
)
const box = () => screen.getByRole<HTMLTextAreaElement>('textbox', { name: 'Message to this session' })
const sent = () => posts.filter(p => ['/commands', '/plan', '/messages', '/compact', '/goal'].includes(p.path)).map(p => `${p.path} ${p.body.text ?? p.body.args ?? ''}`.trim())
const until = (what: () => void) => waitFor(what, { timeout: 5000 })

beforeEach(() => { localStorage.clear(); studio() })
afterEach(() => { cleanup(); takeFirstMessage('c1'); takeHanded('c1'); vi.unstubAllGlobals() })

describe('a command as a new chat’s first message', () => {
  it('runs in the session once it is read, as a later one does', async () => {
    let read = () => {}
    ready = new Promise(r => { read = r })
    handFirstMessage('c1', { text: '/help me', refused: '' })
    show()
    // Shown as said until the session is there.
    expect(await screen.findByText('/help me', {}, { timeout: 5000 })).toBeInTheDocument()
    expect(sent()).toEqual([])
    await act(async () => { read(); await new Promise(r => setTimeout(r, 50)) })
    expect(await screen.findByText('Available commands: many', {}, { timeout: 5000 })).toBeInTheDocument()
    expect(sent()).toEqual(['/commands me'])
    expect(posts.find(p => p.path === '/commands')!.body.name).toBe('help')
    expect(firstMessage('c1')).toBeUndefined()
    expect(box().value).toBe('')
  })
  it('that plans asks the session to, then sends what follows', async () => {
    handFirstMessage('c1', { text: '/plan say the tide', refused: '' })
    show()
    await until(() => expect(sent()).toEqual(['/plan say the tide', '/messages say the tide']))
  })
  it('that pw code does not know goes back to the box with why', async () => {
    handFirstMessage('c1', { text: '/nosuch x', refused: '' })
    show()
    await until(() => expect(screen.getByText(UNKNOWN)).toBeInTheDocument())
    await until(() => expect(box().value).toBe('/nosuch x'))
    expect(sent()).toEqual(['/messages /nosuch x'])
  })
  it('handed over while the session is shown runs too', async () => {
    show()
    await until(() => expect(box()).toBeEnabled())
    act(() => handFirstMessage('c1', { text: '/plan say the tide', refused: '' }))
    await until(() => expect(sent()).toEqual(['/plan say the tide', '/messages say the tide']))
  })
  it('for a session that takes nothing goes to its box', async () => {
    turnedTo = 'live_elsewhere'
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'], shouldAdvanceTime: true, advanceTimeDelta: 40 })
    try {
      handFirstMessage('c1', { text: '/help me', refused: '' })
      show()
      await waitFor(() => expect(box()).toBeDisabled(), { timeout: 9000 })
      await act(async () => { await vi.advanceTimersByTimeAsync(9000) })
      await waitFor(() => expect(box().value).toBe('/help me'), { timeout: 9000 })
      expect(sent()).toEqual([])
    } finally { vi.useRealTimers() }
  }, 20_000)
})

describe('a first message the session refused', () => {
  it('is in its box, with why', async () => {
    handFirstMessage('c1', { text: 'What is the budget?', refused: 'the session is being moved' })
    show()
    await until(() => expect(box().value).toBe('What is the budget?'))
    expect(screen.getByText('the session is being moved')).toBeInTheDocument()
    expect(sent()).toEqual([])
  })
})

describe('the chat’s stream', () => {
  it('hands the message to the session’s view, and the page says it runs one', async () => {
    const sse = [
      `event: pwcode\ndata: ${JSON.stringify({ session: 's1' })}`,
      `event: pwcode_first\ndata: ${JSON.stringify({ text: '/plan say the tide', refused: '' })}`,
      `event: done\ndata: ${JSON.stringify({ finishReason: 'stop', model: null })}`,
    ].join('\n\n') + '\n\n'
    const asked: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (url !== '/api/chat/stream') return new Response('{}', { status: 200 })
      asked.push(JSON.parse(String(init?.body)))
      return new Response(sse, { status: 200 })
    }))
    await createStudioAdapter().streamCompletion({ model: 'm', messages: [], conversationId: 'c1', userMessageId: 'u1' }, { onContent: () => {} })
    expect(asked[0].opensSession).toBe(true)
    expect(firstMessage('c1')).toEqual({ text: '/plan say the tide', refused: '' })
  })
})

// The chat package has the message as sent, with a reply of nothing: the
// Studio assistant, chosen next, would show both.
it('the chat lets go of a message its session was handed', async () => {
  let current: unknown
  const Current = () => { current = useChat().currentConversation?.id ?? null; return null }
  const load = async () => ({ id: 'c1', title: 't', messages: [], activeBranchId: null, createdAt: now, isOwner: true, canCollaborate: true })
  const Load = () => { const { loadConversation } = useChat(); return <button type="button" onClick={() => void loadConversation('c1')}>load</button> }
  const chat = { conversations: { list: async () => [], get: load, create: load, rename: async () => {}, delete: async () => {} }, models: { list: async () => ({ models: [], unreachableSessions: [], providerIssues: [] }) } } as unknown as ChatAdapter
  render(
    <ChatProvider adapter={chat} currentUser={{ username: 'alex' }} navigation={navigation}>
      <LeaveOtherChat conversationId="c1" sessionShown /><Current /><Load />
    </ChatProvider>,
  )
  await act(async () => { screen.getByText('load').click(); await new Promise(r => setTimeout(r, 50)) })
  expect(current).toBe('c1')
  await act(async () => { handFirstMessage('c1', { text: '/help', refused: '' }); await new Promise(r => setTimeout(r, 20)) })
  expect(current).toBeNull()
})

it('a refusal for want of a model never names the terminal', () => {
  const said = { detail: 'model is required: pass one or set a default with the interactive TUI' }
  expect(describeError(said)).toBe('No model is set. Choose one beside the message box, or add one under Settings, Accounts.')
  expect(describeError(new Error(said.detail))).not.toContain('TUI')
  expect(describeError({ detail: 'model is required' })).toBe('model is required')
})
