import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { menuDivider, type MenuEntry } from '@parallelworks/ui'

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

import { setChatListFilter } from '../src/adapter'
import { clearChatRecords } from '../src/chatRecord'
import { readerItems } from '../src/components/ChatList'
import { ChatView } from '../src/views/ChatView'

const ID = '11111111-0000-4000-8000-000000000000'
const now = new Date().toISOString()

/** A Studio that shares chats, with one chat, the viewer's own or not. */
function studio(own: boolean, pwcode = false) {
  const chat = { id: ID, title: 'Budget', owner: own ? 'alex' : 'ada', isOwner: own, canCollaborate: own, createdAt: now, updatedAt: now, ...(pwcode ? { engine: 'pwcode', pwcode: { session: 's1' } } : {}) }
  const messages = [{ id: 'u1', role: 'user', content: 'What is in the plan?', parentId: null }, { id: 'pwc-1', role: 'assistant', content: 'The plan has three parts.', parentId: 'u1' }]
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(input instanceof Request ? input.url : String(input), 'http://studio.example')
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    switch (url.pathname) {
      case '/api/config': return json({ loaded: true, aiEnabled: true, features: {}, libraries: [], suggestedPrompts: [], user: { id: 'alex', username: 'alex' } })
      case '/api/chat/models': return json({ models: [{ id: 'm', object: 'model', name: 'm', provider: 'p', provider_type: 'local', provider_name: 'local' }], unreachableSessions: [] })
      case '/api/tags': return json({ tags: [] })
      case '/api/extensions': return json({ personas: [] })
      case '/api/chat/engine': return json({ setting: 'auto', default: 'studio', current: pwcode ? 'pwcode' : 'studio', answered: true, pwcode: true, reason: null, model: '' })
      case '/api/me/model-key': return json({ authEnabled: true, sharedHistory: true, verified: true, mode: 'stored' })
      case '/api/chat/conversations': return json([{ ...chat, messageCount: 2 }])
      case `/api/chat/conversations/${ID}`: return json({ ...chat, messages, activeBranchId: 'pwc-1' })
      case `/api/chat/conversations/${ID}/generating`: return json(null)
    }
    return json({})
  }))
}

async function open() {
  setChatListFilter('all')
  location.hash = `#chat=${ID}`
  render(<ChatView />)
  await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')); await new Promise(r => setTimeout(r, 200)) })
  await waitFor(() => expect(document.querySelector('.chat-canvas')!.textContent).toContain('The plan has three parts.'), { timeout: 5000 })
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); clearChatRecords(); localStorage.clear(); location.hash = '' })

vi.setConfig({ testTimeout: 30_000 })

describe("another person's chat, where chats are shared", () => {
  it('is read and not added to, saved or switched', async () => {
    studio(false)
    await open()
    const box = screen.getByPlaceholderText('Only the person whose chat this is can add to it')
    expect(box).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull()
    expect(document.querySelector('.engine-btn')).toBeNull()
    expect(screen.getByTitle("Someone else's chat")).toBeInTheDocument()
  })

  it('that runs in a pw code session shows its messages, not the session', async () => {
    studio(false, true)
    await open()
    expect(screen.getByPlaceholderText('Only the person whose chat this is can add to it')).toBeDisabled()
  })

  it('and one of their own stays as it was', async () => {
    studio(true)
    await open()
    expect(screen.queryByPlaceholderText('Only the person whose chat this is can add to it')).toBeNull()
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
    expect(document.querySelector('.engine-btn')).not.toBeNull()
  })

  it('has a menu with Download alone', () => {
    const items = [{ label: 'Rename', onClick() {} }, { label: 'Download', onClick() {} }, menuDivider, { label: 'Delete', onClick() {}, destructive: true }] as MenuEntry[]
    expect(readerItems({ isOwner: false }, items).map(i => (i as { label?: string }).label)).toEqual(['Download'])
    expect(readerItems({ isOwner: true }, items)).toHaveLength(4)
    expect(readerItems({}, items)).toHaveLength(4)
  })
})
