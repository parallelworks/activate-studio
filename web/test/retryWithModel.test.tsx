import { act, render } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// The chat package's state, as the retry handler sees it.
const chat = {
  currentConversation: { id: 'c1', messages: [
    { id: 'u1', role: 'user', content: 'first question', parentId: null },
    { id: 'a1', role: 'assistant', content: 'answer', parentId: 'u1' },
    { id: 'u2', role: 'user', content: 'second question', parentId: 'a1', attachments: [{ id: 'att-1' }] },
    { id: 'a2', role: 'assistant', content: 'The model returned an empty reply.', parentId: 'u2' },
  ] },
  selectedProvider: 'u:prov/model-a',
  isStreaming: false,
  setSelectedProvider: vi.fn(),
  sendMessage: vi.fn(async () => true),
}
vi.mock('@parallelworks/ui/ai', () => ({ useChat: () => chat }))
const { RetryWithModel } = await import('../src/views/ChatView')

describe('retry with another model', () => {
  beforeEach(() => { chat.setSelectedProvider.mockClear(); chat.sendMessage.mockClear(); chat.selectedProvider = 'u:prov/model-a' })

  it('selects the model, then asks the same question again as a branch', () => {
    const view = render(<><RetryWithModel /><a href="#retry-with=u%3Aprov%2Fmodel-b&reply-to=u2">model-b</a></>)
    act(() => view.getByText('model-b').click())
    expect(chat.setSelectedProvider).toHaveBeenCalledWith('u:prov/model-b')
    // Nothing is sent until the selection has taken.
    expect(chat.sendMessage).not.toHaveBeenCalled()
    chat.selectedProvider = 'u:prov/model-b'
    view.rerender(<><RetryWithModel /><a href="#retry-with=u%3Aprov%2Fmodel-b&reply-to=u2">model-b</a></>)
    expect(chat.sendMessage).toHaveBeenCalledWith('second question', ['att-1'], 'a1')
    expect(location.hash).not.toContain('retry-with')
  })

  it('without a reply-to, retries the last question', () => {
    chat.selectedProvider = 'u:prov/model-c'
    const view = render(<><RetryWithModel /><a href="#retry-with=u%3Aprov%2Fmodel-c">again</a></>)
    act(() => view.getByText('again').click())
    expect(chat.sendMessage).toHaveBeenCalledWith('second question', ['att-1'], 'a1')
  })
})
