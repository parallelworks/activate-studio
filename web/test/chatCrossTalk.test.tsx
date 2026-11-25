import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatProvider, useChat, type ChatAdapter } from '@parallelworks/ui/ai'

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

import { ChatView, LeaveOtherChat } from '../src/views/ChatView'
import { PWCodeSession } from '../src/pwcode/PWCodeSession'
import { forgetModels } from '../src/api'
import { forgetEngines } from '../src/chatEngine'
import { clearChatRecords } from '../src/chatRecord'
import { clearModelPick, pickModel } from '../src/pwcode/newChatPick'
import { heldModels } from '../src/pwcode/ownModel'

type Engine = 'studio' | 'pwcode'
interface Message { id: string; role: string; content: string; parentId?: string | null; model?: string }
interface Chat { id: string; title: string; messages: Message[]; pwcode?: { session: string; owed?: { upto: string } }; engine?: Engine }
interface Stream { send: (event: string, data: unknown) => void; end: () => void; body: { messages: { content: string }[]; model: string }; aborted: boolean }

const now = new Date().toISOString()
let chats: Map<string, Chat>
let streams: Map<string, Stream>
let engine: Engine
let posts: { id: string; text: string }[]
let reads: string[]
let held: Promise<void> | null
let models: unknown[] | 'fails'
let settingsModel: string
let needed: boolean
const ID = (n: number) => `${n}${n}${n}${n}${n}${n}${n}${n}-0000-4000-8000-000000000000`

/** A Studio whose chats answer as each test says. */
function studio() {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : null
    const url = new URL(req ? req.url : String(input), 'http://studio.example')
    const path = url.pathname
    const method = (req?.method ?? init?.method ?? 'GET').toUpperCase()
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } })
    const body = () => JSON.parse(String(init?.body ?? '{}'))
    if (path === '/api/config') return json({ loaded: true, aiEnabled: true, features: {}, libraries: [], suggestedPrompts: [], user: { id: 'alex', username: 'alex' } })
    if (path === '/api/chat/models') return models === 'fails' ? json({ error: 'internal error' }, 500) : json({ models, unreachableSessions: [] })
    if (path === '/api/chat/engine') {
      const c = chats.get(url.searchParams.get('conversation') ?? '')
      const now = (url.searchParams.get('last') as Engine | null) ?? engine
      return json({ setting: 'auto', default: now, current: c?.engine ?? now, answered: !!c?.engine, pwcode: engine === 'pwcode', reason: null, model: settingsModel })
    }
    if (path === '/api/chat/pwcode/model') return json({ needed })
    if (path === '/api/chat/conversations' && method === 'GET') return json([...chats.values()].map(c => ({ id: c.id, title: c.title, createdAt: now, updatedAt: now, isOwner: true })))
    if (path === '/api/chat/conversations' && method === 'POST') {
      const id = ID(chats.size + 1)
      chats.set(id, { id, title: 'New', messages: [] })
      return json({ id, title: 'New', createdAt: now, updatedAt: now, isOwner: true })
    }
    let m = /^\/api\/chat\/conversations\/([^/]+)(\/\w+)?$/.exec(path)
    if (m) {
      const c = chats.get(m[1])
      if (m[2] === '/generating') return json(null)
      if (m[2]) return json({})
      if (!c) return json({ error: 'no such chat' }, 404)
      if (method === 'GET') { reads.push(c.id); if (held) await held }
      return json({ ...c, activeBranchId: c.messages[c.messages.length - 1]?.id ?? null, isOwner: true, canCollaborate: true, createdAt: now })
    }
    if (path === '/api/chat/stream') {
      const b = body()
      const text = b.messages[b.messages.length - 1].content as string
      posts.push({ id: b.conversationId, text })
      const c = chats.get(b.conversationId)!
      c.messages.push({ id: b.userMessageId, role: 'user', content: text, parentId: b.parentMessageId ?? null })
      const enc = new TextEncoder()
      let out!: ReadableStreamDefaultController<Uint8Array>
      const stream = new ReadableStream<Uint8Array>({ start(k) { out = k } })
      const s: Stream = {
        body: b, aborted: false,
        send: (event, data) => out.enqueue(enc.encode(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)),
        end: () => out.close(),
      }
      init?.signal?.addEventListener('abort', () => { s.aborted = true; out.error(new DOMException('stopped', 'AbortError')) })
      streams.set(b.conversationId, s)
      return new Response(stream, { status: 200 })
    }
    m = /^\/api\/code\/machines\/studio\/sessions\/([^/]+)(\/transcript)?$/.exec(path)
    if (m) {
      const detail = session(m[1])
      return json(m[2] ? { tail: detail, from: 0, total: detail.history.length } : detail)
    }
    if (path === '/api/code/sessions') return json({ machines: [{ id: 'studio', name: 'box', kind: 'existing', status: 'connected', remoteStart: true, hostname: 'box.example', daemonId: 'd1' }], sessions: [...chats.values()].filter(c => c.pwcode).map(c => ({ ...session(c.pwcode!.session), history: undefined })) })
    if (path.startsWith('/api/tags')) return json({ tags: [] })
    if (path === '/api/me/model-key') return json({ authEnabled: false })
    if (path === '/api/extensions') return json({ personas: [] })
    return json({})
  }))
}

/** A pw code session whose turn is running, with the person's message. */
function session(id: string) {
  const c = [...chats.values()].find(x => x.pwcode?.session === id)
  const asked = c?.messages.find(x => x.role === 'user')?.content ?? ''
  return {
    id, machineId: 'studio', name: 'a session', workspace: '/w', model: 'm', status: 'running', permissionMode: 'accept-edits', createdAt: now, updatedAt: now, messageCount: 1, messages: 1, lastSeq: 1, turnId: 't1', turnStartedAt: now,
    history: [{ kind: 'user', text: asked }, { kind: 'assistant', text: `${id} says` }],
  }
}

/** Every stream's next word, drawn. */
const word = async (n: number, text: string) => { await act(async () => { streams.get(ID(n))!.send('content', { text }); await new Promise(r => setTimeout(r, 60)) }) }
const toolCall = async (n: number, name: string) => { await act(async () => { streams.get(ID(n))!.send('tool', { phase: 'call', id: `call-${n}`, name, args: '{}' }); await new Promise(r => setTimeout(r, 30)) }) }
const finish = async (n: number, text: string) => {
  await act(async () => {
    chats.get(ID(n))!.messages.push({ id: `srv-${n}`, role: 'assistant', content: text, parentId: chats.get(ID(n))!.messages[chats.get(ID(n))!.messages.length - 1].id })
    streams.get(ID(n))!.send('done', { finishReason: 'stop', messageId: `srv-${n}`, model: 'm' })
    streams.get(ID(n))!.end()
    await new Promise(r => setTimeout(r, 120))
  })
}
const thread = () => [...document.querySelectorAll('.chat-canvas [data-transcript]')].filter(e => !e.closest('[style*="display: none"]') && !e.closest('[data-opening]')).map(e => e.textContent).join(' ')
/** Text the open chat draws, once its reveal has it. */
const drawn = (text: string) => waitFor(() => expect(thread()).toContain(text), { timeout: 5000 })
const ask = async (text: string) => {
  const box = await waitFor(() => { const b = document.querySelector<HTMLTextAreaElement>('.chat-canvas textarea'); expect(b).not.toBeNull(); expect(b).toBeEnabled(); return b! }, { timeout: 5000 })
  fireEvent.change(box, { target: { value: text } })
  await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }); await new Promise(r => setTimeout(r, 30)) })
}
/** Sends a new chat's first message; its stream is open when it returns. */
const start = async (n: number, text: string) => { await ask(text); await waitFor(() => expect(streams.has(ID(n))).toBe(true), { timeout: 5000 }) }
const newChat = async () => { await act(async () => { fireEvent.click(screen.getAllByText('New chat')[0]); await new Promise(r => setTimeout(r, 100)) }); await waitFor(() => { expect(location.hash).not.toContain('chat='); expect(thread()).toBe('') }, { timeout: 5000 }) }
const open = async (n: number) => { await act(async () => { location.hash = `#chat=${ID(n)}`; window.dispatchEvent(new HashChangeEvent('hashchange')); await new Promise(r => setTimeout(r, 120)) }) }
const show = async () => { render(<ChatView />); await waitFor(() => expect(screen.getAllByText('New chat').length).toBeGreaterThan(0), { timeout: 5000 }) }

beforeEach(() => {
  chats = new Map(); streams = new Map(); posts = []; reads = []; held = null; engine = 'studio'; settingsModel = 'inference-server/stub/m'; needed = false
  models = [{ id: 'm', object: 'model', name: 'm', provider: 'p', provider_type: 'local', provider_name: 'local' }]
  localStorage.clear(); forgetEngines(); forgetModels(); clearChatRecords()
  location.hash = ''
  studio()
})
afterEach(() => { cleanup(); for (const s of streams.values()) { try { s.end() } catch { /* ended */ } } vi.unstubAllGlobals(); location.hash = '' })

vi.setConfig({ testTimeout: 30_000 })

describe('the Studio assistant: a chat draws its own reply', () => {
  it('in a new chat, while another chat’s reply is written', async () => {
    await show()
    await start(1, 'first')
    await word(1, 'alpha1 ')
    await drawn('alpha1')
    await newChat()
    await word(1, 'alpha2 ')
    expect(document.querySelector('.chat-canvas')!.textContent).not.toContain('alpha')
    await start(2, 'second')
    await word(2, 'beta1 ')
    await word(1, 'alpha3 ')
    await word(2, 'beta2 ')
    await drawn('beta1 beta2')
    expect(thread()).not.toContain('alpha')
    // The first chat's reply ends, and the second's is still written.
    await finish(1, 'alpha1 alpha2 alpha3 ')
    await word(2, 'beta3 ')
    await drawn('beta1 beta2 beta3')
    expect(thread()).not.toContain('alpha')
  })

  it('back and forth while both are written, whole and in order', async () => {
    await show()
    await start(1, 'first')
    await word(1, 'alpha1 ')
    await toolCall(1, 'alphatool')
    await newChat()
    await start(2, 'second')
    await word(2, 'beta1 ')
    await toolCall(2, 'betatool')
    await word(1, 'alpha2 ')
    for (const k of [3, 4]) {
      await open(1)
      await word(2, `beta${k - 1} `)
      await word(1, `alpha${k} `)
      await drawn('alpha1')
      await drawn(Array.from({ length: k - 1 }, (_, i) => `alpha${i + 2}`).join(' '))
      expect(thread().match(/alpha1/g)).toHaveLength(1)
      expect(thread().match(/alpha2/g)).toHaveLength(1)
      expect(thread()).not.toContain('beta')
      await open(2)
      await drawn(`beta${k - 1}`)
      expect(thread().match(/beta2/g)).toHaveLength(1)
      expect(thread()).not.toContain('alpha')
    }
  })

  it('is not drawn over another chat that is still being read', async () => {
    await show()
    await start(1, 'first')
    await word(1, 'alpha1 ')
    await newChat()
    await start(2, 'second')
    await word(2, 'beta1 ')
    let read = () => {}
    held = new Promise(r => { read = r })
    await open(1)
    await word(2, 'beta2 ')
    expect(thread()).not.toContain('beta')
    // And back before it was read.
    await open(2)
    await act(async () => { held = null; read(); await new Promise(r => setTimeout(r, 120)) })
    await word(2, 'beta3 ')
    await drawn('beta1 beta2 beta3')
    expect(thread()).not.toContain('alpha')
  })

  it('after a reload', async () => {
    await show()
    await start(1, 'first')
    await word(1, 'alpha1 ')
    await newChat()
    await start(2, 'second')
    await word(2, 'beta1 ')
    cleanup()
    render(<ChatView />)
    await drawn('beta1')
    await word(1, 'alpha2 ')
    await word(2, 'beta2 ')
    await drawn('beta1 beta2')
    expect(thread()).not.toContain('alpha')
  })

  it('Stop ends its own reply only', async () => {
    await show()
    await start(1, 'first')
    await word(1, 'alpha1 ')
    await newChat()
    await start(2, 'second')
    await word(2, 'beta1 ')
    await open(1)
    await drawn('alpha1')
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: /stop/i })); await new Promise(r => setTimeout(r, 60)) })
    expect(streams.get(ID(1))!.aborted).toBe(true)
    expect(streams.get(ID(2))!.aborted).toBe(false)
  })

  it('a message that waits for one chat’s reply goes to that chat', async () => {
    await show()
    await start(1, 'first')
    await word(1, 'alpha1 ')
    await ask('a second message for the first chat')
    expect(posts).toHaveLength(1)
    await newChat()
    await start(2, 'second')
    await word(2, 'beta1 ')
    await finish(1, 'alpha1 ')
    await finish(2, 'beta1 ')
    await open(1)
    await waitFor(() => expect(posts).toHaveLength(3), { timeout: 3000 })
    expect(posts.map(p => [p.id, p.text])).toEqual([[ID(1), 'first'], [ID(2), 'second'], [ID(1), 'a second message for the first chat']])
  })
})

describe('pw code: a chat draws its own session', () => {
  beforeEach(() => { engine = 'pwcode' })
  /** pw code takes the message: the chat shows the session from here. */
  const sessionUp = async (n: number) => {
    const c = chats.get(ID(n))!
    c.pwcode = { session: `s${n}` }
    c.engine = 'pwcode'
    await act(async () => { streams.get(ID(n))!.send('pwcode', { conversationId: ID(n), session: `s${n}` }); await new Promise(r => setTimeout(r, 200)) })
    await drawn(`s${n} says`)
  }

  it('in a new chat, while another’s turn streams', async () => {
    await show()
    await start(1, 'first')
    await sessionUp(1)
    await word(1, 'alpha1 ')
    await newChat()
    await start(2, 'second')
    await sessionUp(2)
    await toolCall(2, 'betatool')
    await word(1, 'alpha2 ')
    await toolCall(1, 'alphatool')
    await drawn('s2 says')
    expect(thread()).not.toMatch(/alpha|s1 says/)
    // Its own stream is not drawn over the session either, which draws it.
    expect(thread()).not.toContain('betatool')
  })

  it('back and forth while both stream', async () => {
    await show()
    await start(1, 'first')
    await sessionUp(1)
    await toolCall(1, 'alphatool')
    await newChat()
    await start(2, 'second')
    await sessionUp(2)
    await toolCall(2, 'betatool')
    for (let i = 0; i < 2; i++) {
      await open(1)
      await word(1, 'alpha ')
      await word(2, 'beta ')
      await drawn('s1 says')
      expect(thread()).not.toMatch(/s2 says|beta|alphatool/)
      await open(2)
      await word(1, 'alpha ')
      await drawn('s2 says')
      expect(thread()).not.toMatch(/s1 says|alpha|betatool/)
    }
  })

  it('after a reload', async () => {
    await show()
    await start(1, 'first')
    await sessionUp(1)
    await newChat()
    await start(2, 'second')
    await sessionUp(2)
    await toolCall(2, 'betatool')
    cleanup()
    render(<ChatView />)
    await drawn('s2 says')
    await word(1, 'alpha ')
    await word(2, 'beta ')
    expect(thread()).not.toMatch(/s1 says|alpha|beta/)
  })

  it('beside a Studio assistant chat whose reply is being written', async () => {
    engine = 'pwcode'
    localStorage.setItem('ade-chat-engine', 'studio')
    await show()
    await start(1, 'first')
    await word(1, 'alpha1 ')
    await toolCall(1, 'alphatool')
    chats.set(ID(9), { id: ID(9), title: 'Nine', engine: 'pwcode', pwcode: { session: 's9' }, messages: [{ id: 'u9', role: 'user', content: 'ninth' }] })
    await open(9)
    await drawn('s9 says')
    await word(1, 'alpha2 ')
    expect(thread()).not.toContain('alpha')
    await open(1)
    await word(1, 'alpha3 ')
    await drawn('alpha2 alpha3')
    expect(thread()).not.toContain('s9 says')
  })
})

describe('the Code page’s pane', () => {
  it('draws its session only, under a provider whose own chat streams', async () => {
    chats.set(ID(1), { id: ID(1), title: 'One', messages: [{ id: 'u1', role: 'user', content: 'ninth' }], pwcode: { session: 's9' } })
    let part: ((d: unknown) => void) | null = null
    const adapter = {
      conversations: { list: async () => [], get: async () => null, create: async () => ({ id: 'n1', title: 'x' }), rename: async () => {}, remove: async () => {} },
      models: { list: async () => ({ models: [{ id: 'm', name: 'm' }], unreachableSessions: [], providerIssues: [] }) },
      streamCompletion: (_req: unknown, h: { onPart: (d: unknown) => void; onContent: (t: string) => void }) => { part = d => { h.onPart(d); h.onContent('alpha words ') }; return new Promise(() => {}) },
    } as unknown as ChatAdapter
    let send: ((t: string) => unknown) | null = null
    function Sender() { send = useChat().sendMessage; return null }
    render(
      <ChatProvider adapter={adapter} currentUser={{ id: 'alex', username: 'alex' } as never} navigation={{ toConversation() {}, toNewChat() {} } as never}>
        <Sender />
        <PWCodeSession machine="studio" sessionId="s9" username="alex" />
      </ChatProvider>,
    )
    await waitFor(() => expect(document.body.textContent).toContain('s9 says'), { timeout: 5000 })
    await act(async () => { void send!('hello'); await new Promise(r => setTimeout(r, 100)) })
    await act(async () => { part!({ type: 'tool_start', id: 'c', name: 'alphatool', args: '{}' }); await new Promise(r => setTimeout(r, 120)) })
    expect(document.body.textContent).toContain('s9 says')
    expect(document.body.textContent).not.toContain('alpha')
  })
})

describe('a session view in front', () => {
  it('lets go of the chat open before it, and of none that is its own', async () => {
    const chat = { id: 'c1', title: 'One', messages: [{ id: 'u1', role: 'user', content: 'first' }] }
    const adapter = {
      conversations: { list: async () => [], get: async () => chat, create: async () => chat, rename: async () => {}, remove: async () => {} },
      models: { list: async () => ({ models: [], unreachableSessions: [], providerIssues: [] }) },
    } as unknown as ChatAdapter
    let chatNow: ReturnType<typeof useChat> | null = null
    function Holds() { chatNow = useChat(); return <p data-testid="holds">{chatNow.currentConversation?.id ?? 'none'}</p> }
    const page = (id: string, shown: boolean) => (
      <ChatProvider adapter={adapter} currentUser={{ id: 'alex', username: 'alex' } as never} navigation={{ toConversation() {}, toNewChat() {} } as never}>
        <Holds /><LeaveOtherChat conversationId={id} sessionShown={shown} />
      </ChatProvider>
    )
    const { rerender } = render(page('c1', false))
    await act(async () => { await chatNow!.loadConversation('c1') })
    expect(screen.getByTestId('holds')).toHaveTextContent('c1')
    rerender(page('c1', true))
    expect(screen.getByTestId('holds')).toHaveTextContent('c1')
    rerender(page('c2', true))
    expect(screen.getByTestId('holds')).toHaveTextContent('none')
  })
})

describe('a new chat pw code answers, with no Studio assistant model', () => {
  it.each([['none listed', []], ['the listing failing', 'fails']] as const)('takes a message, %s', async (_why, listed) => {
    engine = 'pwcode'
    models = listed as never
    await show()
    await start(1, 'hello')
    expect(posts).toEqual([{ id: ID(1), text: 'hello' }])
    // Standing in for a model is the page's own: none is named to the server.
    expect(streams.get(ID(1))!.body.model).toBe('')
  })

  it('does not once the Studio assistant is chosen', async () => {
    engine = 'pwcode'
    models = []
    localStorage.setItem('ade-chat-engine', 'studio')
    await show()
    await waitFor(() => expect(screen.getByPlaceholderText('Add a model under Settings, Accounts to start chatting')).toBeDisabled(), { timeout: 5000 })
  })
})

// With no model set and none on the account the box still sent, and the
// error named pw code's terminal.
describe('a new chat pw code answers, with no model for pw code', () => {
  beforeEach(() => { engine = 'pwcode'; settingsModel = ''; needed = true })
  afterEach(() => clearModelPick(null))
  const closed = () => waitFor(() => expect(screen.getByPlaceholderText('Choose or add a model to start chatting')).toBeDisabled(), { timeout: 5000 })
  const opened = () => waitFor(() => expect(screen.getByPlaceholderText('Ask anything')).toBeEnabled(), { timeout: 5000 })

  it.each([['some to choose', 1], ['none listed', 0]] as const)('is closed and says what to do, %s', async (_what, n) => {
    if (!n) models = []
    await show()
    await closed()
    expect(screen.getByText('pw code has no model set. Choose one in the message box, or add one under Settings, Accounts.')).toBeInTheDocument()
    // pw code's own picker lists what there is.
    expect(heldModels()).toHaveLength(n)
    fireEvent.click(screen.getByRole('link', { name: /Open Accounts/ }))
    expect(location.hash).toBe('#view=settings:accounts')
  })

  it('opens once one is chosen, and the message carries it', async () => {
    await show()
    await closed()
    await act(async () => pickModel(null, { model: 'm' }))
    await opened()
    await start(1, 'hello')
    expect(streams.get(ID(1))!.body).toMatchObject({ pwcodeModel: 'm' })
  })

  it('opens where the Studio assistant is chosen instead', async () => {
    localStorage.setItem('ade-chat-engine', 'studio')
    await show()
    await opened()
    expect(heldModels()).toBeNull()
  })

  it.each([['a model of its own', () => { needed = false }], ['the one Settings names', () => { settingsModel = 'm' }]] as const)('is open where pw code has %s', async (_what, has) => {
    has()
    await show()
    await opened()
    await start(1, 'hello')
    expect(posts).toEqual([{ id: ID(1), text: 'hello' }])
  })
})

// A command in a chat the Studio assistant answered last was sent to the
// session as text, with the turns it missed.
it.each([['owed', true], ['not owed', false]] as const)('the session shows where the turns it missed are %s to it', async (_what, owed) => {
  engine = 'pwcode'
  chats.set(ID(1), { id: ID(1), title: 'A chat', engine: 'pwcode', messages: [{ id: 'u0', role: 'user', content: 'What is the budget?' }, { id: 'srv-0', role: 'assistant', content: 'Nine pounds.', parentId: 'u0', model: 'm' }], pwcode: { session: 's1', ...(owed ? { owed: { upto: 'srv-0' } } : {}) } })
  await show()
  await open(1)
  await waitFor(() => expect(document.body.textContent).toContain('Nine pounds.'), { timeout: 5000 })
  await waitFor(() => expect(!!document.querySelector('.pwcode-session')).toBe(owed), { timeout: 5000 })
  if (owed) await waitFor(() => expect(document.body.textContent).toContain('s1 says'), { timeout: 5000 })
  expect(document.body.textContent).toContain('Nine pounds.')
  // "/" in a box the session does not have is that session's list too.
  if (owed) return
  const box = document.querySelector<HTMLTextAreaElement>('.chat-canvas textarea')!
  await act(async () => { fireEvent.input(box, { target: { value: '/' } }); await new Promise(r => setTimeout(r, 60)) })
  const asked = () => vi.mocked(fetch).mock.calls.map(c => String(c[0] instanceof Request ? c[0].url : c[0]))
  await waitFor(() => expect(asked()).toContain('/api/code/machines/studio/sessions/s1/palette'), { timeout: 3000 })
})
