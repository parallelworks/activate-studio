import { readFileSync } from 'node:fs'
import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatMessage } from '@parallelworks/ui/ai'

const chat = vi.hoisted(() => ({ loadConversation: vi.fn(async () => {}), streaming: true, id: 'c9' }))
vi.mock('@parallelworks/ui/ai', () => ({
  useChat: () => ({ currentConversation: { id: chat.id }, isStreaming: chat.streaming, loadConversation: chat.loadConversation }),
}))

import { EngineNoteReader, PWCodeEngine } from '../src/components/PWCodeEngine'
import { knownEngine } from '../src/labelScope'
import { forgetEngines } from '../src/chatEngine'
import { typedPart, withEarlier, withoutCarried } from '../src/pwcode/carried'

const sse = (events: [string, unknown][]) => new ReadableStream({
  start(c) {
    for (const [e, d] of events) c.enqueue(new TextEncoder().encode(`event: ${e}\ndata: ${JSON.stringify(d)}\n\n`))
    c.close()
  },
})

beforeEach(() => { localStorage.clear(); forgetEngines(); chat.loadConversation.mockClear(); chat.streaming = true; chat.id = 'c9' })
afterEach(() => { vi.unstubAllGlobals() })

// A person pw code is not for here gets the Studio assistant in a pw code
// chat, with a note the server stores after its reply.
describe('When the Studio assistant answers in pw code\'s place', () => {
  it('sends no engine of its own, only the last choice for a new chat, and takes the chat as the Studio assistant\'s', async () => {
    localStorage.setItem('ade-chat-engine', 'pwcode')
    const bodies: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body)))
      return new Response(sse([
        ['engine_fallback', { conversationId: 'c1', note: 'pw code is not available to you here, so the Studio assistant answered.' }],
        ['content', { text: 'Hello' }],
        ['done', { finishReason: 'stop', model: 'm', messageId: 'srv-1' }],
      ]), { status: 200 })
    }))
    const told: unknown[] = []
    const on = (e: Event) => told.push((e as CustomEvent).detail)
    window.addEventListener('ade:engine-fallback', on)
    const { createStudioAdapter } = await import('../src/adapter')
    await createStudioAdapter().streamCompletion({ model: 'm', messages: [{ role: 'user', content: 'hi' }], conversationId: 'c1' } as never, { onContent: () => {} } as never)
    window.removeEventListener('ade:engine-fallback', on)
    // What answers is the server's to decide: the stored engine, or the
    // last choice for a chat nothing answered yet; a pick is a switch.
    expect(bodies[0]).not.toHaveProperty('engine')
    expect(bodies[0].newChatEngine).toBe('pwcode')
    expect(told).toEqual([{ conversationId: 'c1', note: 'pw code is not available to you here, so the Studio assistant answered.' }])
    expect(knownEngine('c1')).toBe('studio')
  }, 30_000)

  it('reads the chat again once the reply ends, so the stored note shows', async () => {
    vi.useFakeTimers()
    const { rerender } = render(<EngineNoteReader />)
    act(() => {
      window.dispatchEvent(new CustomEvent('ade:engine-fallback', { detail: { conversationId: 'c9', note: 'n' } }))
      window.dispatchEvent(new CustomEvent('ade:engine-fallback', { detail: { conversationId: 'other', note: 'n' } }))
    })
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(chat.loadConversation).not.toHaveBeenCalled()
    // The reply ends; the chat settles it, and many renders follow.
    chat.streaming = false
    rerender(<EngineNoteReader />)
    await act(async () => { await vi.advanceTimersByTimeAsync(400) })
    rerender(<EngineNoteReader />)
    await act(async () => { await vi.advanceTimersByTimeAsync(1100) })
    expect(chat.loadConversation).toHaveBeenCalledTimes(1)
    expect(chat.loadConversation).toHaveBeenCalledWith('c9')
    rerender(<EngineNoteReader />)
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(chat.loadConversation).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })
})

// Opened by a person pw code is not for here, a chat pw code answered is
// the Studio assistant's, as its next message will be: the chat shows its
// own thread, and the message goes through the Studio.
describe('A pw code chat opened by a person pw code is not for', () => {
  it("is shown as the Studio assistant's, from the menu's read of it", async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.startsWith('/api/chat/engine')
      ? { setting: 'auto', default: 'studio', current: 'studio', answered: true, pwcode: false, reason: 'pw code runs as you: sign in with ACTIVATE under Settings, Accounts.' }
      : {}), { status: 200 })))
    render(<PWCodeEngine conversationId="c7" />)
    await vi.waitFor(() => expect(knownEngine('c7')).toBe('studio'))
  })
})

// A chat switched to pw code sends its session the turns it missed before
// the person's message; the chat's session view shows what they typed.
describe("A pw code session's transcript in the chat", () => {
  const earlier = "Earlier in this conversation, before you joined it:\n\nThe person: Tell me about tides.\n\nThe Studio assistant: Tides rise and fall twice a day [cut]\n\nThe person's new message:\n\nWhat did I first ask?"
  const since = "In this conversation since your last reply:\n\nThe Studio assistant: Two.\n\nThe person's new message:\n\nAnd now?"

  it('shows the message the person typed, not the turns carried over', () => {
    expect(typedPart(earlier)).toBe('What did I first ask?')
    expect(typedPart(since)).toBe('And now?')
    expect(typedPart('Earlier in this conversation, before you joined it: a sentence of my own')).toBe('Earlier in this conversation, before you joined it: a sentence of my own')
    expect(typedPart('Just a question')).toBe('Just a question')
  })

  it("draws the session's transcript, and the chat's stored record on a reload, the same way", () => {
    const live: ChatMessage[] = [
      { id: 'm1', role: 'user', content: earlier },
      { id: 'm2', role: 'assistant', content: 'You asked about tides.' },
      { id: 'm3', role: 'user', content: '', parts: [{ kind: 'text', text: since }] },
    ]
    const shown = withoutCarried(live)
    expect(shown.map(m => m.content)).toEqual(['What did I first ask?', 'You asked about tides.', ''])
    expect(shown[2].parts).toEqual([{ kind: 'text', text: 'And now?' }])
    expect(shown[1]).toBe(live[1])
    // An assistant's words are never cut.
    expect(withoutCarried([{ id: 'a', role: 'assistant', content: earlier }])[0].content).toBe(earlier)
    // The stored record (shown until the transcript arrives) holds what
    // the person typed: it is drawn as it is.
    const stored: ChatMessage[] = [{ id: 'u2', role: 'user', content: 'What did I first ask?' }]
    expect(withoutCarried(stored)).toBe(stored)
    expect(withoutCarried(undefined)).toBeUndefined()
  })
})

// While a chat switched to pw code is on pw code, its view drew only the
// session's turns: everything before the switch was hidden.
describe("A pw code session's view of a chat switched to it", () => {
  const record: ChatMessage[] = [
    { id: 'u1', role: 'user', content: 'Tell me about tides.' },
    { id: 'a1', role: 'assistant', content: 'Tides rise and fall twice a day.', model: 'gpt-b' } as ChatMessage,
    { id: 'note-engine-1', role: 'assistant', content: 'A note.' },
    { id: 'u2', role: 'user', content: 'What did I first ask?' },
    { id: 'pwc-2', role: 'assistant', content: 'You asked about tides.', model: 'pw code · haiku' } as ChatMessage,
    { id: 'u3', role: 'user', content: 'Two more?' },
    { id: 'a3', role: 'assistant', content: 'Two.', model: 'gpt-b' } as ChatMessage,
    { id: 'u4', role: 'user', content: 'And now?' },
    { id: 'pwc-4', role: 'assistant', content: 'Now.', model: 'pw code · haiku' } as ChatMessage,
  ]
  const session: ChatMessage[] = [
    { id: 's1', role: 'user', content: "Earlier in this conversation, before you joined it:\n\nThe person: Tell me about tides.\n\nThe Studio assistant: Tides rise [cut]\n\nThe person's new message:\n\nWhat did I first ask?" },
    { id: 's2', role: 'assistant', content: 'You asked about tides.' },
    { id: 's3', role: 'user', content: "In this conversation since your last reply:\n\nThe person: Two more?\n\nThe Studio assistant: Two.\n\nThe person's new message:\n\nAnd now?" },
    { id: 's4', role: 'assistant', content: 'Now.' },
  ]

  it('draws the turns before the switch above its own, and those the Studio assistant had since where they came, from the chat', () => {
    const shown = withoutCarried(withEarlier(session, record))
    expect(shown.map(m => `${m.id}: ${m.content}`)).toEqual([
      'earlier-u1: Tell me about tides.',
      'earlier-studio-a1: Tides rise and fall twice a day.',
      's1: What did I first ask?',
      's2: You asked about tides.',
      'earlier-u3: Two more?',
      'earlier-studio-a3: Two.',
      's3: And now?',
      's4: Now.',
    ])
    expect((shown[1] as { model?: string }).model).toBe('gpt-b')
    // A session that started in the chat has nothing earlier to draw.
    const own: ChatMessage[] = [{ id: 's1', role: 'user', content: 'What did I first ask?' }]
    expect(withEarlier(own, record)).toBe(own)
    expect(withEarlier(session, undefined)).toBe(session)
  })

  it('marks those turns as earlier in the chat', () => {
    const css = readFileSync('src/styles.css', 'utf8')
    expect(css).toMatch(/\.pwcode-session \[data-row\^='earlier-'\][^{]*::before \{\s*content: 'Earlier in this chat'/)
    // Its Studio assistant's replies say so, as in the chat's own thread.
    expect(css).toContain(".pwcode-session [data-row^='earlier-studio-'] .chat-meta > :nth-child(2)::before { content: 'Studio assistant · '; }")
  })
})
