import { afterEach, describe, expect, it, vi } from 'vitest'
import { createStudioAdapter } from '../src/adapter'

afterEach(() => vi.restoreAllMocks())

// A pw code reply as the server streams it: done names the model that wrote it.
const sse = [
  `event: pwcode\ndata: ${JSON.stringify({ session: 's1', model: 'claude-code/haiku' })}`,
  `event: content\ndata: ${JSON.stringify({ text: 'OK' })}`,
  `event: done\ndata: ${JSON.stringify({ content: 'OK', finishReason: 'stop', model: 'pw code · claude-code/haiku', messageId: 'pwc-1' })}`,
].join('\n\n') + '\n\n'

describe('a reply pw code wrote', () => {
  it('reports the model that wrote it, never the picker\'s Responses model', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(sse, { status: 200 }))
    const req = { model: 'acct:codex/gpt-6.2-sol', messages: [], conversationId: 'c1', userMessageId: 'u1', useResponses: true }
    const out = await createStudioAdapter().streamCompletion(req, { onContent: () => {} })
    expect(out.model).toBe('pw code · claude-code/haiku')
    expect(req.useResponses).toBe(false)
  })
  it('leaves a Studio assistant reply to the picker\'s label', async () => {
    const studio = sse.replace(/event: pwcode\ndata: [^\n]*\n\n/, '').replace('pw code · claude-code/haiku', 'gpt-6.2-sol-2026-10-01')
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(studio, { status: 200 }))
    const req = { model: 'acct:codex/gpt-6.2-sol', messages: [], conversationId: 'c1', userMessageId: 'u1', useResponses: true }
    await createStudioAdapter().streamCompletion(req, { onContent: () => {} })
    expect(req.useResponses).toBe(true)
  })
})
