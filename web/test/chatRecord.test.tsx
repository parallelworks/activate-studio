import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mutate, useSWRConfig } from 'swr'
import { cachedChat, clearChatRecords, prefetchChat, readChat, takeReadAhead } from '../src/chatRecord'
import { clearViewerCaches, setCacheViewer } from '../src/viewerCache'
import { useChatLink } from '../src/pwcode/useChatLink'

/** A Studio whose chats answer as the person signed in at the time. */
function studio(as: () => string) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    calls.push(url)
    const id = decodeURIComponent(url.split('/').pop() ?? '')
    const who = as()
    return new Response(JSON.stringify({ id, title: `${who}'s chat`, owner: who, pwcode: { session: `${who}-session`, relay: true, machine: 'm2' } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

beforeEach(() => { clearChatRecords(); localStorage.clear() })
afterEach(() => { vi.unstubAllGlobals() })

describe("A chat's kept record", () => {
  it('is read once however many parts of the page ask at the same time', async () => {
    const calls = studio(() => 'ada')
    const [a, b, c] = await Promise.all([readChat('c1'), readChat('c1'), readChat('c1')])
    expect(calls).toEqual(['/api/chat/conversations/c1'])
    expect(a).toBe(b)
    expect(b).toBe(c)
    expect(cachedChat('c1')?.title).toBe("ada's chat")
  })

  it('is never handed to someone else signed in on the same page (a shared Studio)', async () => {
    let who = 'ada'
    studio(() => who)
    setCacheViewer('ada')
    await readChat('c1')
    expect(cachedChat('c1')?.owner).toBe('ada')
    // Ada's pw code session read into SWR's cache, as the session view reads it.
    await mutate(['api', '/api/code/machines/{machine}/sessions/{id}', { params: { path: { machine: 'm2', id: 'ada-session' } } }], { id: 'ada-session', history: [{ kind: 'assistant', text: "ada's work" }] }, { revalidate: false })

    who = 'bob'
    setCacheViewer('bob')
    expect(cachedChat('c1')).toBeUndefined()
    const { result } = renderHook(() => useSWRConfig())
    const cache = result.current.cache as unknown as Map<string, { data?: unknown }>
    const ada = [...cache.keys()].filter(k => k.includes('ada-session'))
    expect(ada.length).toBeGreaterThan(0)
    expect(ada.map(k => cache.get(k)?.data)).toEqual(ada.map(() => undefined))
    // What Bob opens is read as Bob.
    expect((await readChat('c1')).owner).toBe('bob')
  })

  it("does not keep a read that was on its way when the viewer's caches were cleared", async () => {
    let finish: (r: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(res => { finish = res })))
    setCacheViewer('ada')
    const going = readChat('c1')
    clearViewerCaches()
    finish(new Response(JSON.stringify({ id: 'c1', owner: 'ada' }), { status: 200 }))
    await going
    expect(cachedChat('c1')).toBeUndefined()
  })

  it('goes when the ACTIVATE sign-in or key changes', async () => {
    studio(() => 'ada')
    setCacheViewer('ada')
    await readChat('c1')
    window.dispatchEvent(new CustomEvent('ade:model-access-changed'))
    expect(cachedChat('c1')).toBeUndefined()
  })

  it('read ahead (a pointer on the chat) serves the open once, and later reads ask again', async () => {
    const calls = studio(() => 'ada')
    prefetchChat('c1')
    prefetchChat('c1')
    const ahead = takeReadAhead('c1')
    expect(ahead).toBeDefined()
    await ahead
    expect(takeReadAhead('c1')).toBeUndefined()
    await readChat('c1')
    expect(calls).toEqual(['/api/chat/conversations/c1', '/api/chat/conversations/c1'])
  })

  it("shows a chat opened before with its session at once, then reads it again", async () => {
    const calls = studio(() => 'ada')
    setCacheViewer('ada')
    await readChat('c1')
    calls.length = 0
    const { result } = renderHook(() => useChatLink('c1'))
    // From the kept record, in the first render: no read to wait for.
    expect(result.current.shown).toBe(true)
    expect(result.current.link?.session).toBe('ada-session')
    expect(result.current.machine).toBe('m2')
    await waitFor(() => expect(calls.filter(u => !u.startsWith('/api/chat/engine'))).toEqual(['/api/chat/conversations/c1']))
  })

  it("goes through the Studio, not the session's own view, while the Studio assistant has the last reply", async () => {
    const answers: { id: string; role: string; model?: string }[] = [{ id: 'u1', role: 'user' }, { id: 'pwc-1', role: 'assistant', model: 'pw code · haiku' }]
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ id: 'c3', engine: 'pwcode', pwcode: { session: 's1' }, messages: answers }), { status: 200 })))
    const { result } = renderHook(() => useChatLink('c3'))
    await waitFor(() => expect(result.current.shown).toBe(true))
    // The Studio assistant answers; switched back, the next message must
    // reach the session through the Studio, with the turn it missed.
    answers.push({ id: 'u2', role: 'user' }, { id: 'a2', role: 'assistant', model: 'acct:codex/gpt-b' })
    act(() => { window.dispatchEvent(new CustomEvent('ade:chat-answered', { detail: { conversationId: 'c3' } })) })
    await waitFor(() => expect(result.current.shown).toBe(false))
    // pw code answers again: its view comes back.
    answers.push({ id: 'u3', role: 'user' }, { id: 'pwc-2', role: 'assistant', model: 'pw code · haiku' })
    act(() => { window.dispatchEvent(new CustomEvent('ade:chat-answered', { detail: { conversationId: 'c3' } })) })
    await waitFor(() => expect(result.current.shown).toBe(true))
  })

  /** A Studio that answers each read of a chat when told to, with what the
   *  chat was as the read arrived. */
  function slowStudio() {
    const chat: { session?: string } = {}
    const answer: ((ok?: boolean) => Promise<void>)[] = []
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (!url.startsWith('/api/chat/conversations/')) return Promise.resolve(new Response(JSON.stringify({ current: 'pwcode', setting: 'auto' }), { status: 200 }))
      const body = JSON.stringify({ id: 'c4', engine: 'pwcode', ...(chat.session ? { pwcode: { session: chat.session } } : {}) })
      return new Promise<Response>((res, rej) => answer.push(async (ok = true) => {
        await act(async () => { if (ok) res(new Response(body, { status: 200 })); else rej(new Error('network')); await new Promise(r => setTimeout(r, 20)) })
      }))
    }))
    const change = (event: string, detail: object = {}) => act(() => { window.dispatchEvent(new CustomEvent(event, { detail: { conversationId: 'c4', ...detail } })) })
    return { chat, answer, change }
  }

  it.each([['in order', [0, 1]], ['out of order', [1, 0]]] as const)('shows a session made while the chat was being read, the answers %s', async (_what, order) => {
    const { chat, answer, change } = slowStudio()
    const { result } = renderHook(() => useChatLink('c4'))
    expect(answer).toHaveLength(1)
    chat.session = 's4'
    change('ade-pwcode-session', { session: 's4' })
    // Not the read already on its way, which began before the session.
    expect(answer).toHaveLength(2)
    for (const n of order) await answer[n]()
    expect(result.current.link?.session).toBe('s4')
    await waitFor(() => expect(result.current.shown).toBe(true))
    expect((cachedChat('c4')?.pwcode as { session: string }).session).toBe('s4')
    // The end of the reply is a change too.
    change('ade:chat-answered')
    expect(answer).toHaveLength(3)
    await answer[2]()
    expect(result.current.shown).toBe(true)
  })

  it('tries a read for a change again, and keeps the session it has', async () => {
    const { chat, answer, change } = slowStudio()
    const { result } = renderHook(() => useChatLink('c4'))
    await answer[0]()
    chat.session = 's4'
    change('ade-pwcode-session', { session: 's4' })
    await answer[1](false)
    await waitFor(() => expect(answer).toHaveLength(3), { timeout: 4000 })
    await answer[2]()
    expect(result.current.link?.session).toBe('s4')
    change('ade:chat-answered')
    await answer[3](false)
    expect(result.current.link?.session).toBe('s4')
  })

  it('a read begun before a change is not handed to what asks after it', async () => {
    const { chat, answer, change } = slowStudio()
    const before = readChat('c4')
    prefetchChat('c4')
    chat.session = 's4'
    change('ade-pwcode-session', { session: 's4' })
    expect(takeReadAhead('c4')).toBeUndefined()
    const after = readChat('c4')
    await answer[1]()
    await answer[0]()
    expect((await before).pwcode).toBeUndefined()
    expect((await after).pwcode?.session).toBe('s4')
    expect(cachedChat('c4')?.pwcode?.session).toBe('s4')
  })

  it('waits for the read of a chat never opened', async () => {
    studio(() => 'ada')
    const { result } = renderHook(() => useChatLink('c2'))
    expect(result.current.shown).toBe(false)
    await waitFor(() => expect(result.current.link?.session).toBe('ada-session'))
  })
})
