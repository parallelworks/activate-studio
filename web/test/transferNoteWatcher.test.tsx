import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const chat = vi.hoisted(() => ({
  currentConversation: { id: 'c1' } as { id: string } | null,
  isStreaming: false,
  loadConversation: vi.fn(async (_id: string) => {}),
}))
vi.mock('@parallelworks/ui/ai', () => ({ useChat: () => chat }))

import { TransferNoteWatcher } from '../src/components/TransferNoteWatcher'

let active: { id: string; conversationId?: string }[] = []
const listed = () => (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.filter(c => String(c[0]).startsWith('/api/transfers?')).length

describe('the open conversation shows a transfer note without a reload', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    chat.currentConversation = { id: 'c1' }
    chat.isStreaming = false
    chat.loadConversation.mockClear()
    active = []
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url.startsWith('/api/transfers?')) {
        expect(url).toContain('state=active')
        expect(url).toContain('startedBy=assistant')
        return new Response(JSON.stringify({ transfers: active.map(t => ({ state: 'running', paths: [], ...t })) }), { status: 200 })
      }
      return new Response('{}', { status: 404 })
    }))
  })
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals() })

  it('reads the conversation again when its transfer ends, and not for another conversation\'s', async () => {
    active = [{ id: 't1', conversationId: 'c1' }, { id: 't2', conversationId: 'c2' }]
    render(<TransferNoteWatcher />)
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(listed()).toBe(1)
    // Running: it looks again after 5 seconds.
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    expect(listed()).toBe(2)
    expect(chat.loadConversation).not.toHaveBeenCalled()
    // The other conversation's transfer ends: nothing to read.
    active = [{ id: 't1', conversationId: 'c1' }]
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    expect(chat.loadConversation).not.toHaveBeenCalled()
    // Its own ends: the conversation is read after the note is written.
    active = []
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    expect(chat.loadConversation).not.toHaveBeenCalled()
    await act(async () => { await vi.advanceTimersByTimeAsync(1_500) })
    expect(chat.loadConversation).toHaveBeenCalledWith('c1')
    // Nothing running now: it looks every 30 seconds.
    const n = listed()
    await act(async () => { await vi.advanceTimersByTimeAsync(29_000) })
    expect(listed()).toBe(n)
    await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
    expect(listed()).toBe(n + 1)
  })

  it('does not look while a reply streams, and looks at once when it ends', async () => {
    chat.isStreaming = true
    const { rerender } = render(<TransferNoteWatcher />)
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(listed()).toBe(0)
    chat.isStreaming = false
    rerender(<TransferNoteWatcher />)
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(listed()).toBe(1)
  })

  it('does nothing with no conversation open', async () => {
    chat.currentConversation = null
    render(<TransferNoteWatcher />)
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(listed()).toBe(0)
  })
})
