import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BoundSetting } from '@parallelworks/ui/ai'

// pw code's picker itself is the package's (AgentModelControls); here it
// shows the model it is given and picks Sonnet when pressed.
vi.mock('../src/code/AgentModelControls', () => ({
  AgentModelControls: ({ model, claudeReady }: { model: BoundSetting; claudeReady: boolean }) => (
    <button type="button" data-ready={String(claudeReady)} onClick={() => model.onChange('claude-code/sonnet')}>{model.value ?? 'none'}</button>
  ),
}))
vi.mock('../src/pwcode/PWCodeSession', () => ({ PwCodeWords: ({ children }: { children: React.ReactNode }) => <>{children}</> }))
vi.mock('../src/pwcode/session/usePlatformModels', () => ({ usePlatformModels: () => undefined }))

import { NewChatModelSlot } from '../src/pwcode/NewChatModel'
import { firstMessagePick, pickModel, pickedModel } from '../src/pwcode/newChatPick'
import { createStudioAdapter } from '../src/adapter'

let canvas: HTMLElement
beforeEach(() => {
  canvas = document.createElement('div')
  canvas.className = 'chat-canvas'
  canvas.setAttribute('data-engine', 'pwcode')
  document.body.appendChild(canvas)
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.startsWith('/api/chat/engine')
    ? { model: 'claude-code/haiku' }
    : { machines: [{ id: 'studio', claudeInstalled: true, claudeCodeAllowed: true }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
})
afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })

describe('a new pw code chat, before its first message', () => {
  it('offers pw code\'s models from the start, from the model Settings names, and the first message carries the pick', async () => {
    render(<NewChatModelSlot conversationId={null} hasSession={false} />, { container: canvas.appendChild(document.createElement('div')) })
    const picker = await screen.findByRole('button', { name: 'claude-code/haiku' })
    await waitFor(() => expect(picker).toHaveAttribute('data-ready', 'true'))
    fireEvent.click(picker)
    expect(await screen.findByRole('button', { name: 'claude-code/sonnet' })).toBeInTheDocument()
    expect(pickedModel(null)?.model).toBe('claude-code/sonnet')
    // The chat gets its conversation with the first message, which says
    // what the session is to run on.
    expect(firstMessagePick('c1')).toEqual({ pwcodeModel: 'claude-code/sonnet' })
  })
  it('leaves the picking to the session once there is one, and to the Studio assistant\'s picker when it answers', async () => {
    const { rerender } = render(<NewChatModelSlot conversationId="c2" hasSession />, { container: canvas.appendChild(document.createElement('div')) })
    expect(screen.queryByRole('button')).toBeNull()
    act(() => canvas.setAttribute('data-engine', 'studio'))
    rerender(<NewChatModelSlot conversationId="c2" hasSession={false} />)
    await waitFor(() => expect(screen.queryByRole('button')).toBeNull())
    act(() => canvas.setAttribute('data-engine', 'pwcode'))
    expect(await screen.findByRole('button', { name: 'claude-code/haiku' })).toBeInTheDocument()
  })
  it('is sent with the chat\'s first message to the Studio', async () => {
    pickModel(null, { model: 'claude-code/opus', allocation: 'alloc-1' })
    let sent: Record<string, unknown> = {}
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      sent = JSON.parse(String(init?.body))
      return new Response('', { status: 500 })
    }))
    const stream = createStudioAdapter().streamCompletion!
    await stream({ conversationId: 'c3', model: 'm', messages: [{ role: 'user', content: 'Hi' }] } as never, { onToken: () => {}, onDone: () => {}, onError: () => {} } as never, new AbortController().signal).catch(() => {})
    expect(sent).toMatchObject({ conversationId: 'c3', pwcodeModel: 'claude-code/opus', pwcodeAllocation: 'alloc-1' })
  })
})
