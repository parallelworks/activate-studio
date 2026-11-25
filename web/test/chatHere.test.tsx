import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@parallelworks/ui'

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

import { createStudioAdapter } from '../src/adapter'
import { setLibrary } from '../src/api'
import { forgetEngines, getLastEngine, readEngine, switchEngine } from '../src/chatEngine'
import { clearChatRecords } from '../src/chatRecord'
import { reloadAppConfig } from '../src/config'
import { askPlace, chatHere, CHAT_HERE_EVENT, fileReference, forgetPlaces, knownPlace } from '../src/pwcode/chatHere'
import { forgetStarts, startOf } from '../src/pwcode/newChatStart'
import { ChatView } from '../src/views/ChatView'
import { LibraryView } from '../src/views/LibraryView'

const own = { id: 'studio', name: 'box', kind: 'existing', status: 'connected', remoteStart: true, directories: true, claudeCodeAllowed: false, claudeInstalled: false }
const gpu = { ...own, id: 'm2', name: 'gpu', permissionModes: ['read-only', 'accept-edits'], remoteMaxPermissionMode: 'accept-edits' }
const there = { machine: 'm2', machineName: 'gpu', folder: '/data/runs', library: 'u-work', path: 'runs', file: '', claudeCodeAllowed: false, claudeInstalled: false }
const here = { machine: 'studio', machineName: 'box', folder: '/kb/plans', library: 'kb', path: 'plans', file: 'plans/budget.md' }
const lib = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id, label, primary: id === 'kb', writable: true, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false }, ...extra,
})

let asked: string[]
let place: unknown
let placeStatus: number
let wait: Promise<void>
let libraries: unknown[]
let sent: Record<string, unknown>[]
let made: number

function studio() {
  asked = []; sent = []; made = 0
  place = there; placeStatus = 200; wait = Promise.resolve()
  libraries = [lib('kb', 'Projects')]
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(input instanceof Request ? input.url : String(input), 'http://studio.example')
    const method = (input instanceof Request ? input.method : init?.method ?? 'GET').toUpperCase()
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } })
    asked.push(`${method} ${url.pathname}${url.search}`)
    switch (url.pathname) {
      case '/api/config': return json({ loaded: true, aiEnabled: true, features: {}, libraries, suggestedPrompts: [], user: { id: 'alex', username: 'alex' } })
      case '/api/chat/pwcode/start/library': await wait; return json(place, placeStatus)
      case '/api/chat/pwcode/start': return json({ available: true, machine: 'studio', machines: [own, gpu], sessions: [], folder: '/kb' })
      case '/api/chat/models': return json({ models: [{ id: 'm', label: 'm', available: true, kind: 'platform' }], unreachableSessions: [] })
      case '/api/chat/engine': return json({ setting: 'auto', default: 'studio', current: 'studio', pwcode: true, reason: null })
      case '/api/chat/pwcode/model': return json({ needed: false })
      case '/api/chat/conversations':
        if (method === 'POST') return json({ id: `c${++made}`, title: 'New', messages: [], createdAt: '2026-11-01T00:00:00Z', updatedAt: '2026-11-01T00:00:00Z', isOwner: true })
        return json([])
      case '/api/chat/stream': sent.push(JSON.parse(String(init?.body))); return new Response('', { status: 500 })
      case '/api/kb/tree': {
        const at = url.searchParams.get('path') ?? ''
        return json({ path: at, entries: at === '' ? [{ name: 'plans', path: 'plans', type: 'dir', size: 0, mtime: 0 }, { name: 'a.md', path: 'a.md', type: 'file', size: 1, mtime: 0 }] : [] })
      }
      case '/api/tags': return json({ tags: [] })
      case '/api/me/model-key': return json({ authEnabled: false })
      case '/api/extensions': return json({ personas: [] })
      default: return json({})
    }
  }))
}

const adapter = () => createStudioAdapter()
const send = (id: string) => adapter().streamCompletion!({ conversationId: id, model: 'm', messages: [{ role: 'user', content: 'Hi' }] } as never, { onToken: () => {}, onDone: () => {}, onError: () => {} } as never, new AbortController().signal).catch(() => {})
const settle = (ms = 60) => act(async () => { await new Promise(r => setTimeout(r, ms)) })
let events: string[]
const heard = (e: Event) => events.push((e as CustomEvent<{ text: string }>).detail.text)

beforeEach(() => {
  localStorage.clear(); forgetStarts(); forgetEngines(); forgetPlaces(); clearChatRecords(); setLibrary('kb')
  location.hash = ''; events = []
  window.addEventListener(CHAT_HERE_EVENT, heard)
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  studio()
})
afterEach(() => { cleanup(); window.removeEventListener(CHAT_HERE_EVENT, heard); vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb') })

describe('New pw code chat here', () => {
  it('opens a chat on pw code, on the machine and in the folder the Studio found, for that chat alone', async () => {
    await chatHere({ library: 'u-work', path: 'runs' }, 'kb')
    expect(events).toEqual([''])
    expect(location.hash).toBe('#view=chat')
    expect(startOf(null)).toMatchObject({ machine: { id: 'm2' }, folder: '/data/runs', library: { id: 'u-work', path: 'runs', machineName: 'gpu' } })
    const c = await adapter().conversations.create('New')
    await send(c.id)
    expect(sent[0]).toMatchObject({ engine: 'pwcode', pwcodeMachine: 'm2', pwcodeFolder: '/data/runs', pwcodeLibrary: 'u-work', pwcodePath: 'runs' })
    // The choice new chats start with is as it was.
    expect(getLastEngine()).toBe('')
    const next = await adapter().conversations.create('New')
    await send(next.id)
    for (const k of ['engine', 'pwcodeMachine', 'pwcodeFolder', 'pwcodeLibrary', 'pwcodePath']) expect(sent[1], k).not.toHaveProperty(k)
    // A retry in the chat that was refused asks for the same.
    await send(c.id)
    expect(sent[2]).toMatchObject({ engine: 'pwcode', pwcodeFolder: '/data/runs', pwcodeLibrary: 'u-work' })
  })

  it('does not go to a chat that was already there', async () => {
    await chatHere({ library: 'u-work', path: 'runs' }, 'kb')
    await send('c-old')
    for (const k of ['engine', 'pwcodeMachine', 'pwcodeFolder', 'pwcodeLibrary']) expect(sent[0], k).not.toHaveProperty(k)
    expect(startOf(null).library?.id).toBe('u-work')
  })

  it('puts a file in the message as a link to it, its folder the one it is in', async () => {
    place = here
    await chatHere({ library: 'kb', path: 'plans/budget.md' }, 'kb')
    expect(events).toEqual(['@[plans/budget.md]'])
    expect(startOf(null)).toEqual({ folder: '/kb/plans', library: { id: 'kb', path: 'plans/budget.md', machineName: 'box' } })
    expect(fileReference('runs/a.md', 'u-work', 'kb')).toBe('@[u-work:runs/a.md]')
    expect(fileReference('runs/a [1] (x).md', 'u-work', 'kb')).toBe('[a 1 (x).md](#open=file:runs/a%20%5B1%5D%20%28x%29.md&lib=u-work)')
  })

  it('says why it cannot, and opens nothing', async () => {
    place = { status: 409, detail: 'gpu has no pw code connected to ACTIVATE as you.' }; placeStatus = 409
    const error = vi.spyOn(toast, 'error').mockImplementation(() => '')
    await chatHere({ library: 'u-work', path: 'runs' }, 'kb')
    expect(error).toHaveBeenCalledWith('gpu has no pw code connected to ACTIVATE as you.')
    expect(events).toEqual([]); expect(startOf(null)).toEqual({}); expect(location.hash).toBe('')
  })

  it('asks once for an answer it has, and again when who is here changed', async () => {
    const item = { library: 'u-work', path: 'runs' }
    await Promise.all([askPlace(item), askPlace(item)])
    await askPlace(item)
    expect(asked.filter(a => a.includes('/start/library'))).toEqual(['GET /api/chat/pwcode/start/library?library=u-work&path=runs'])
    expect(knownPlace(item)?.place).toBeTruthy()
    forgetPlaces()
    expect(knownPlace(item)).toBeUndefined()
  })

  it('a switch of what answers it is the new chat\'s', async () => {
    await chatHere({ library: 'u-work', path: 'runs' }, 'kb')
    await switchEngine('studio')
    const c = await adapter().conversations.create('New')
    await send(c.id)
    expect(sent[0].engine).toBeUndefined()
  })
})

describe('in the Library\'s menu', () => {
  const open = async (text = 'plans') => {
    render(<LibraryView display={null} onDisplay={() => {}} />)
    await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) })
    fireEvent.contextMenu(await screen.findByText(text))
    return screen.findByRole('menuitem', { name: /^New pw code chat here/ })
  }
  const item = () => screen.getByRole('menuitem', { name: /^New pw code chat here/ })

  it('is on folders and files, beside Add to chat, and opens the chat', async () => {
    const entry = await open()
    expect(entry).not.toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByRole('menuitem', { name: 'Add to chat' })).toBeInTheDocument()
    await settle()
    expect(item()).not.toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(item())
    await waitFor(() => expect(events).toEqual(['']))
    cleanup()
    expect(await open('a.md')).not.toHaveAttribute('aria-disabled', 'true')
    expect(asked.filter(a => a.includes('/start/library')).map(a => a.split('?')[1])).toEqual(['library=kb&path=plans', 'library=kb&path=a.md'])
  })

  it('Add to chat is as it was', async () => {
    await open()
    const got: string[] = []
    const on = (e: Event) => got.push(JSON.stringify((e as CustomEvent).detail))
    window.addEventListener('ade-chat-insert', on)
    fireEvent.click(screen.getByRole('menuitem', { name: 'Add to chat' }))
    await settle()
    window.removeEventListener('ade-chat-insert', on)
    expect(got.length).toBe(1)
    expect(events).toEqual([]); expect(startOf(null)).toEqual({})
  })

  it('is disabled with the reason once the Studio says it cannot, in the open menu', async () => {
    place = { status: 409, detail: 'gpu has no pw code connected to ACTIVATE as you.' }; placeStatus = 409
    let go!: () => void
    wait = new Promise(r => { go = r })
    const entry = await open()
    expect(entry).not.toHaveAttribute('aria-disabled', 'true')
    go()
    await waitFor(() => expect(item()).toHaveAttribute('aria-disabled', 'true'))
    expect(item()).toHaveTextContent('gpu has no pw code connected to ACTIVATE as you.')
    expect(screen.getAllByRole('menuitem').length).toBeGreaterThan(3)
  })

  it('does not open a menu that was closed again', async () => {
    place = { status: 409, detail: 'no' }; placeStatus = 409
    let go!: () => void
    wait = new Promise(r => { go = r })
    await open()
    fireEvent.keyDown(item(), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('menuitem')).toBeNull())
    go()
    await settle()
    expect(screen.queryByRole('menuitem')).toBeNull()
  })

  it('knows a bucket and a library read only to the person without asking', async () => {
    libraries = [lib('kb', 'Scans', { access: { where: 'bucket', readAs: 'studio', write: false, index: 'built' } })]
    expect(await open()).toHaveAttribute('aria-disabled', 'true')
    expect(item()).toHaveTextContent('A bucket has no folder')
    cleanup()
    libraries = [lib('kb', 'Projects', { writable: false, readOnlyWhy: 'Only its administrators change it.' })]
    expect(await open()).toHaveAttribute('aria-disabled', 'true')
    expect(item()).toHaveTextContent('read only for you')
    expect(asked.some(a => a.includes('/start/library'))).toBe(false)
  })
})

describe('on the chat page', () => {
  it('opens a new chat with the file in its box, and New chat is a plain one again', async () => {
    Object.defineProperty(window, 'innerWidth', { value: 1440, configurable: true })
    render(<ChatView active />)
    await waitFor(() => expect(document.querySelector('.chat-canvas textarea')).toBeTruthy(), { timeout: 5000 })
    await readEngine(null)
    place = here
    await act(async () => { await chatHere({ library: 'kb', path: 'plans/budget.md' }, 'kb') })
    await settle(400)
    const box = () => document.querySelector<HTMLTextAreaElement>('.chat-canvas textarea')!
    expect(box().value).toBe('@[plans/budget.md] ')
    expect(document.activeElement).toBe(box())
    expect(document.querySelector('.chat-canvas')).toHaveAttribute('data-engine', 'pwcode')
    fireEvent.click(screen.getAllByText('New chat')[0])
    await settle(200)
    expect(startOf(null)).toEqual({})
    expect(document.querySelector('.chat-canvas')).toHaveAttribute('data-engine', 'studio')
  })
})
