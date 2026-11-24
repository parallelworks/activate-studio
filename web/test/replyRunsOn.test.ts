import { afterEach, describe, expect, it, vi } from 'vitest'

// The Studio assistant's reply runs on in the Studio when its page goes.
// A chat opened meanwhile (a reload) carries the reply as far as it has
// got, which the chat package shows and follows to its end; Stop asks the
// Studio to end it.
describe('a reply that runs on without its page', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.resetModules() })

  const chat = { id: 'c1', messages: [{ id: 'u1', role: 'user', content: 'Write the long report.' }] }
  const serve = (running: unknown) => {
    const calls: { url: string; method: string }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, method: init?.method ?? 'GET' })
      const body = url.endsWith('/generating') ? running : url.endsWith('/stop') ? { stopped: true } : chat
      return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) }
    }))
    return calls
  }

  it('comes with the chat when it is opened again mid-reply', async () => {
    serve({ messageId: 'live-1', content: 'Part one.', reasoning: '' })
    const { createStudioAdapter } = await import('../src/adapter')
    const c = await createStudioAdapter().conversations.get('c1') as { generating?: unknown }
    expect(c.generating).toEqual({ messageId: 'live-1', content: 'Part one.', reasoning: '' })
  }, 30_000)

  it('is not there once it has ended', async () => {
    serve(null)
    const { createStudioAdapter } = await import('../src/adapter')
    const a = createStudioAdapter()
    const c = await a.conversations.get('c1') as { generating?: unknown }
    expect(c.generating).toBeUndefined()
    expect(await a.conversations.generating?.('c1')).toBeNull()
  }, 30_000)

  it('shows as Stopped once stored', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const body = url.endsWith('/generating') ? null
        : { id: 'c1', messages: [...chat.messages, { id: 'srv-1', role: 'assistant', content: 'Part one.', finishReason: 'stopped' }, { id: 'srv-2', role: 'assistant', content: 'Done.', finishReason: 'stop' }] }
      return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) }
    }))
    const { createStudioAdapter } = await import('../src/adapter')
    const c = await createStudioAdapter().conversations.get('c1') as { messages: { id: string; stopped?: boolean }[] }
    expect(c.messages.find(m => m.id === 'srv-1')?.stopped).toBe(true)
    expect(c.messages.find(m => m.id === 'srv-2')?.stopped).toBeUndefined()
  }, 30_000)

  it('is ended by Stop', async () => {
    const calls = serve(null)
    const { createStudioAdapter } = await import('../src/adapter')
    await createStudioAdapter().conversations.stop?.('c1')
    expect(calls).toContainEqual({ url: '/api/chat/conversations/c1/stop', method: 'POST' })
  }, 30_000)

  it('says it runs on when the connection drops before any words', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url.endsWith('/generating')) return { ok: true, status: 200, json: async () => ({ messageId: 'live-1', content: '', reasoning: '' }) }
      const body = new ReadableStream({ pull(c) { c.error(new Error('network')) } })
      return { ok: true, status: 200, body }
    }))
    const { createStudioAdapter } = await import('../src/adapter')
    const handlers = { onContent: () => {}, onReasoning: () => {} }
    await expect(createStudioAdapter().streamCompletion({ model: 'm', messages: [], conversationId: 'c1' } as never, handlers as never))
      .rejects.toThrow('the Studio is still writing it')
  }, 30_000)
})
