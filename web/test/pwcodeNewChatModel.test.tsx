import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { startEffortPreference } from '@parallelworks/code'
import { type BoundSetting, EffortPicker } from '@parallelworks/ui/ai'

// pw code's picker itself is the package's (AgentModelControls); here it
// shows the model it is given and picks Sonnet when pressed.
vi.mock('../src/code/AgentModelControls', () => ({
  AgentModelControls: ({ model, effort, allocation, claudeReady }: { model: BoundSetting; effort?: BoundSetting; allocation: BoundSetting; claudeReady: boolean }) => (
    <>
      <button type="button" data-ready={String(claudeReady)} onClick={() => model.onChange('claude-code/sonnet')}>{model.value ?? 'none'}</button>
      <button type="button" onClick={() => { model.onChange('org:openai/gpt-5'); allocation.onChange('alloc-2') }}>both</button>
      <button type="button" onClick={() => allocation.onChange('alloc-3')}>{`billed to ${allocation.value ?? 'none'}`}</button>
      {effort && <EffortPicker effort={effort.value} onPick={effort.onChange} disabled={effort.disabled} />}
    </>
  ),
}))
vi.mock('../src/pwcode/PWCodeSession', () => ({ PwCodeWords: ({ children }: { children: React.ReactNode }) => <>{children}</> }))
vi.mock('../src/pwcode/session/usePlatformModels', () => ({ usePlatformModels: () => undefined }))

import { setEffort } from '../src/labelScope'
import { ComposerEngineSlot } from '../src/pwcode/NewChatModel'
import { clearModelPick, firstMessagePick, pickModel, pickedModel } from '../src/pwcode/newChatPick'
import { setStart } from '../src/pwcode/newChatStart'
import { createStudioAdapter } from '../src/adapter'
import { forgetEngines, readEngine } from '../src/chatEngine'

let canvas: HTMLElement
beforeEach(() => {
  forgetEngines()
  canvas = document.createElement('div')
  canvas.className = 'chat-canvas'
  canvas.setAttribute('data-engine', 'pwcode')
  document.body.appendChild(canvas)
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.startsWith('/api/chat/engine')
    ? { model: 'claude-code/haiku' }
    : { machines: [{ id: 'studio', claudeInstalled: true, claudeCodeAllowed: true }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
})
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); document.body.innerHTML = '' })

describe('a new pw code chat, before its first message', () => {
  it('offers pw code\'s models from the start, from the model Settings names, and the first message carries the pick', async () => {
    render(<ComposerEngineSlot conversationId={null} hasSession={false} />, { container: canvas.appendChild(document.createElement('div')) })
    const picker = await screen.findByRole('button', { name: 'claude-code/haiku' })
    await waitFor(() => expect(picker).toHaveAttribute('data-ready', 'true'))
    fireEvent.click(picker)
    expect(await screen.findByRole('button', { name: 'claude-code/sonnet' })).toBeInTheDocument()
    expect(pickedModel(null)?.model).toBe('claude-code/sonnet')
    // The chat gets its conversation with the first message, which says
    // what the session is to run on.
    expect(firstMessagePick('c1')).toEqual({ pwcodeModel: 'claude-code/sonnet' })
  })
  it('takes the model Settings names from what the page already knows, without reading it again', async () => {
    // The chat bar's menu has read what answers a new chat (and reads it
    // again as the person comes back to the tab); the picker that appears
    // because pw code now answers does not add a read of its own.
    await readEngine(null)
    const engineReads = () => vi.mocked(fetch).mock.calls.filter(([u]) => String(u).startsWith('/api/chat/engine')).length
    expect(engineReads()).toBe(1)
    render(<ComposerEngineSlot conversationId={null} hasSession={false} />, { container: canvas.appendChild(document.createElement('div')) })
    // The picker shows the model the page already had.
    const picker = await screen.findByRole('button', { name: /^(none|claude-code)/ })
    await waitFor(() => expect(picker).toHaveTextContent(/\/haiku$/))
    await act(async () => { await new Promise(r => setTimeout(r, 100)) })
    expect(engineReads()).toBe(1)
  })

  it('leaves the picking to the session once there is one, and to the Studio assistant\'s picker when it answers', async () => {
    const { rerender } = render(<ComposerEngineSlot conversationId="c2" hasSession />, { container: canvas.appendChild(document.createElement('div')) })
    expect(screen.queryByRole('button')).toBeNull()
    act(() => canvas.setAttribute('data-engine', 'studio'))
    rerender(<ComposerEngineSlot conversationId="c2" hasSession={false} />)
    await waitFor(() => expect(screen.queryByRole('button')).toBeNull())
    act(() => canvas.setAttribute('data-engine', 'pwcode'))
    expect(await screen.findByRole('button', { name: 'claude-code/haiku' })).toBeInTheDocument()
  })
  it('offers the models of the machine the chat is set to start on', async () => {
    render(<ComposerEngineSlot conversationId="c8" hasSession={false} />, { container: canvas.appendChild(document.createElement('div')) })
    await screen.findByRole('button', { name: 'claude-code/haiku' })
    // It runs no Claude Code, so the model Settings names is not its model.
    act(() => setStart('c8', { machine: { id: 'm2', claudeInstalled: false } }))
    expect(await screen.findByRole('button', { name: 'none' })).toHaveAttribute('data-ready', 'false')
    act(() => setStart('c8', { machine: { id: 'm3', claudeInstalled: true, claudeCodeAllowed: true } }))
    expect(await screen.findByRole('button', { name: 'claude-code/haiku' })).toHaveAttribute('data-ready', 'true')
    act(() => setStart('c8', {}))
  })
  describe('with no model in Settings', () => {
    let engine: Record<string, unknown>
    beforeEach(() => {
      engine = { model: '', lastModel: 'me:openai/gpt-5', lastAllocation: 'alloc-1' }
      vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.startsWith('/api/chat/engine')
        ? engine : { machines: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    })
    afterEach(() => { for (const id of ['c10', 'c11', 'c12']) { clearModelPick(id); setStart(id, {}) } })
    const show = (id: string) => render(<ComposerEngineSlot conversationId={id} hasSession={false} />, { container: canvas.appendChild(document.createElement('div')) })

    it('starts from the model the person chose last, with its allocation, which the Studio keeps', async () => {
      show('c10')
      expect(await screen.findByRole('button', { name: 'me:openai/gpt-5' })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'billed to alloc-1' })).toBeInTheDocument()
      // The Studio knows it: the first message says nothing of it.
      expect(firstMessagePick('c10')).toEqual({})
      fireEvent.click(screen.getByRole('button', { name: 'billed to alloc-1' }))
      expect(firstMessagePick('c10')).toEqual({ pwcodeModel: 'me:openai/gpt-5', pwcodeAllocation: 'alloc-3' })
    })
    it('takes another with its allocation', async () => {
      show('c11')
      await screen.findByRole('button', { name: 'me:openai/gpt-5' })
      fireEvent.click(screen.getByRole('button', { name: 'both' }))
      expect(firstMessagePick('c11')).toEqual({ pwcodeModel: 'org:openai/gpt-5', pwcodeAllocation: 'alloc-2' })
      expect(await screen.findByRole('button', { name: 'billed to alloc-2' })).toBeInTheDocument()
    })
    it('has it on another machine only where that machine runs it', async () => {
      show('c12')
      await screen.findByRole('button', { name: 'me:openai/gpt-5' })
      act(() => setStart('c12', { machine: { id: 'm2' } }))
      expect(await screen.findByRole('button', { name: 'me:openai/gpt-5' })).toBeInTheDocument()
      // The Studio's machine's own, an inference server set up there.
      engine = { model: '', lastModel: 'inference-server/lab/m1', lastAllocation: '' }
      await act(async () => { await readEngine('c12') })
      expect(await screen.findByRole('button', { name: 'none' })).toBeInTheDocument()
      act(() => setStart('c12', {}))
      expect(await screen.findByRole('button', { name: 'inference-server/lab/m1' })).toBeInTheDocument()
    })
    it('is the one Settings names where it names one', async () => {
      engine = { model: 'claude-code/haiku', lastModel: '', lastAllocation: '' }
      show('c10')
      expect(await screen.findByRole('button', { name: 'claude-code/haiku' })).toBeInTheDocument()
    })
  })
  it('offers pw code\'s own effort slider until there is a session, and the Studio\'s menu only to the Studio assistant', async () => {
    const { rerender } = render(<ComposerEngineSlot conversationId="c9" hasSession={false} />, { container: canvas.appendChild(document.createElement('div')) })
    // Nothing chosen: pw code's own default, high. The chat's own choice for
    // the Studio assistant is not pw code's.
    setEffort('low', 'c9')
    fireEvent.click(await screen.findByRole('button', { name: /^Effort: high$/i }))
    expect(screen.queryByTestId('composer-effort')).toBeNull()
    expect(firstMessagePick('c9')).toEqual({})
    const slider = await screen.findByRole('slider')
    expect(slider).toHaveAttribute('max', '4')
    fireEvent.keyDown(slider, { key: 'ArrowRight' })
    fireEvent.change(slider, { target: { value: '3' } })
    await waitFor(() => expect(firstMessagePick('c9')).toEqual({ pwcodeEffort: 'xhigh' }))
    rerender(<ComposerEngineSlot conversationId="c9" hasSession />)
    await waitFor(() => expect(screen.queryByRole('button', { name: /^Effort/ })).toBeNull())
    expect(screen.queryByTestId('composer-effort')).toBeNull()
    act(() => canvas.setAttribute('data-engine', 'studio'))
    const menu = await screen.findByRole<HTMLSelectElement>('combobox', { name: 'Reasoning effort' })
    expect([...menu.options].map(o => o.text)).toEqual(['Default', 'Low', 'Medium', 'High', 'Extra high', 'Max'])
    expect(menu.value).toBe('low')
  })
  it('is sent with the chat\'s first message to the Studio', async () => {
    pickModel(null, { model: 'claude-code/opus', allocation: 'alloc-1' })
    localStorage.setItem(`studio.code.${startEffortPreference.key}`, '"max"')
    let sent: Record<string, unknown> = {}
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      sent = JSON.parse(String(init?.body))
      return new Response('', { status: 500 })
    }))
    const stream = createStudioAdapter().streamCompletion!
    await stream({ conversationId: 'c3', model: 'm', messages: [{ role: 'user', content: 'Hi' }] } as never, { onToken: () => {}, onDone: () => {}, onError: () => {} } as never, new AbortController().signal).catch(() => {})
    expect(sent).toMatchObject({ conversationId: 'c3', pwcodeModel: 'claude-code/opus', pwcodeAllocation: 'alloc-1', pwcodeEffort: 'max' })
  })
})
