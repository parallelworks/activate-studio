import { renderHook, waitFor } from '@testing-library/react'
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
    await waitFor(() => expect(calls).toEqual(['/api/chat/conversations/c1']))
  })

  it('waits for the read of a chat never opened', async () => {
    studio(() => 'ada')
    const { result } = renderHook(() => useChatLink('c2'))
    expect(result.current.shown).toBe(false)
    await waitFor(() => expect(result.current.link?.session).toBe('ada-session'))
  })
})
