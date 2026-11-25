import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { ChatProvider, type ChatAdapter } from '@parallelworks/ui/ai'
import { loadPwCodeMessages, type PwCodeMachine, type PwCodeSession } from '@parallelworks/code'
import { createTranslator } from 'use-intl'
import App from '../src/App'
import { chatRowState, StudioChatLayout } from '../src/components/ChatList'
import { SharedWithMe } from '../src/components/SharedWithMe'
import { NoticesView } from '../src/views/NoticesView'
import {
  countFor, NOTICES_EVERY_MS, NOTICES_LOOKING_MS, type Notice, type Notices, publishListing, refreshNotices, sessionSeen, setFrontChat, setReplying, setTransfersShown, useNotices, useNoticesRead,
} from '../src/notices'
import { useCodeWaiting } from '../src/code/waiting'
import type { Translate } from '../src/code/parts'

// The navigation is under test, not the pages; each page is a stand-in.
const { page } = vi.hoisted(() => ({ page: (name: string) => ({ [name]: () => `${name} page` }) }))
vi.mock('../src/views/ChatView', () => page('ChatView'))
vi.mock('../src/views/LibraryView', () => page('LibraryView'))
vi.mock('../src/views/SearchView', () => page('SearchView'))
vi.mock('../src/views/QueryView', () => page('QueryView'))
vi.mock('../src/views/OverviewView', () => page('OverviewView'))
vi.mock('../src/views/HistoryView', () => page('HistoryView'))
vi.mock('../src/views/AgentsView', () => page('AgentsView'))
vi.mock('../src/views/WorkflowsView', () => page('WorkflowsView'))
vi.mock('../src/views/SettingsView', () => page('SettingsView'))
vi.mock('../src/views/HelpView', () => page('HelpView'))
vi.mock('../src/components/StatusFooter', () => page('StatusFooter'))
vi.mock('../src/components/RunPane', () => ({ ...page('RunPane'), ...page('FullViewHost') }))

const at = new Date(Date.now() - 5 * 60_000).toISOString()
const notice = (o: Partial<Notice>): Notice => ({ id: 'x', kind: 'chat', what: 'reply', title: 'T', text: 'New reply', at, href: '#chat=aaaaaaaa', nav: ['chat'], ...o })
const machine = { id: 'm1', name: 'example-box', kind: 'existing', status: 'connected', hostname: 'box.example', daemonId: 'd1' } as unknown as PwCodeMachine
const session = (o: Record<string, unknown>) => ({ id: 's1', machineId: 'm1', name: 'mesh', status: 'idle', createdAt: at, updatedAt: at, workspace: '/w', messages: 1, ...o }) as unknown as PwCodeSession

const WAITING = notice({ id: 'chat:c1:ap1', what: 'waiting', title: 'Budget', text: 'Waits for your approval: make test', conversation: 'aaaaaaaa', session: 's1', nav: ['chat', 'code'] })
const REPLY = notice({ id: 'reply:c2:r2', title: 'Weather', conversation: 'bbbbbbbb', href: '#chat=bbbbbbbb' })
const FLEET = notice({ id: 'fleet:a1:x', kind: 'fleet', what: 'waiting', title: 'Reporter', text: 'Which table?', href: '#view=agents:fleet?agent=a1', nav: ['agents'] })
const DONE = notice({ id: 'delegated:d1:finished:t', kind: 'delegated', what: 'finished', title: 'Draft the report', text: 'Delegated work finished', href: '#view=code?session=d1', session: 'd1', nav: [] })
const FAILED = notice({ id: 'transfer:t1', kind: 'transfer', what: 'failed', title: 'Nightly copy', text: 'Transfer failed: refused', href: '#view=transfers?transfer=t1', nav: ['transfers'] })

const SHARED = notice({ id: 'share:s-1:write', kind: 'share', what: 'shared', title: 'ada shared proj with you', text: 'Read and write · /home/ada/studio/proj · cluster-a', href: '#view=notices', nav: [],
  share: { id: 's-1', from: 'ada', label: 'proj', folder: '/home/ada/studio/proj', site: 'cluster-a', system: 'cluster-a', access: 'write' } })
const GROUP = notice({ id: 'share:s-2:read', kind: 'share', what: 'shared', title: 'ada shared runs with your group team', text: 'Read', href: '#view=notices', nav: [],
  share: { id: 's-2', from: 'ada', label: 'runs', folder: '/home/ada/studio/runs', site: 'pw://ada/example-box', system: 'example-box', group: 'team', access: 'read' } })

interface Sent { method: string; path: string; body: Record<string, unknown> | null }
let answer: Partial<Notices>
let sent: Sent[]
let config: Record<string, unknown>
/** What adding a folder shared with the person answers. */
let added: () => Response | Promise<Response>
const asked = (path: string) => sent.filter(s => s.path === path)

beforeEach(() => {
  sent = []
  answer = { items: [], chats: {}, listed: true, machines: [machine], sessions: [] }
  config = { appName: 'Studio', platform: true, user: { id: 'u', username: 'alex' } }
  added = () => new Response(JSON.stringify({ id: 'u-shared-proj', label: 'proj (ada)' }), { status: 200 })
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input instanceof Request ? input.url : input)
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : null
    sent.push({ method: init?.method ?? 'GET', path: url, body })
    if (url.startsWith('/api/user-libraries/shared-with-me/')) return added()
    if (url === '/api/user-libraries/shared-with-me') return new Response(JSON.stringify({ shares: [{ id: 's-1', from: 'ada', space: 'u-space-studio', path: 'proj', label: 'proj', folder: '/home/ada/studio/proj', kind: 'user', name: 'alex', access: 'write', createdAt: '2026-11-20T10:00:00Z', system: 'cluster-a' }], unreached: [] }), { status: 200 })
    if (url === '/api/notices/dismiss') answer = { ...answer, items: answer.items!.filter(i => !(body!.ids as string[]).includes(i.id)) }
    const out = url === '/api/config' ? config : url.startsWith('/api/notices') ? answer : url.startsWith('/api/transfers/locations') ? { locations: [] } : url.startsWith('/api/transfers/summary') ? answer.transfers ?? {} : url.startsWith('/api/setup/status') ? { firstRun: false } : {}
    return new Response(JSON.stringify(out), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  history.replaceState(null, '', location.pathname)
  setFrontChat(null)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
})

function Reader() {
  useNoticesRead(true)
  const n = useNotices()
  const code = useCodeWaiting(true)
  return <p data-testid="read">{n.loaded ? `${countFor(n, 'chat')} ${code} ${n.items.length} ${Object.entries(n.chats).map(([k, c]) => `${k}:${c.working ? 'working' : ''}${c.unread ?? ''}`).join()}` : 'not read'}</p>
}
const shown = () => screen.getByTestId('read').textContent
const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

describe('the notices read', () => {
  beforeEach(() => { vi.useFakeTimers() })

  it('is one request every 30 seconds, idle or with a chat open, and takes the place of the Code listing', async () => {
    render(<Reader />)
    await tick(0)
    expect(sent.map(s => s.path)).toEqual(['/api/notices'])
    await tick(NOTICES_EVERY_MS * 4)
    expect(sent.map(s => s.path)).toEqual(Array(5).fill('/api/notices'))
    // With a chat open too: it is said to be seen as it opens, then only when
    // there is a reply in it.
    act(() => setFrontChat('bbbbbbbb'))
    await tick(1000)
    expect(sent.at(-1)).toMatchObject({ path: '/api/notices/seen', body: { conversations: ['bbbbbbbb'] } })
    sent.length = 0
    await tick(NOTICES_EVERY_MS * 4)
    expect(sent.map(s => s.path)).toEqual(Array(4).fill('/api/notices'))
    answer = { ...answer, chats: { bbbbbbbb: { unread: 'reply' } } }
    sent.length = 0
    await tick(NOTICES_EVERY_MS)
    expect(sent.map(s => s.path)).toEqual(['/api/notices', '/api/notices/seen'])
    answer = { ...answer, chats: {} }
    // Nothing is read in a tab that does not show.
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    sent.length = 0
    await tick(NOTICES_EVERY_MS * 4)
    expect(sent).toEqual([])
  })

  it('goes once, soon, when several things happen, and tells the other tabs', async () => {
    const posted: unknown[] = []
    vi.stubGlobal('BroadcastChannel', class { onmessage: (() => void) | null = null; postMessage(m: unknown) { posted.push(m) } close() {} })
    render(<Reader />)
    await tick(0)
    sent.length = 0
    act(() => {
      window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: {} }))
      window.dispatchEvent(new CustomEvent('ade:chat-answered', { detail: { conversationId: 'bbbbbbbb' } }))
      refreshNotices()
    })
    await tick(2000)
    expect(asked('/api/notices')).toHaveLength(1)
    expect(posted).toEqual(['changed'])
  })

  it('another tab of the browser saying so reads again, without telling it back', async () => {
    let chan: { onmessage: (() => void) | null } | null = null
    const posted: unknown[] = []
    vi.stubGlobal('BroadcastChannel', class { onmessage: (() => void) | null = null; constructor() { chan = this } postMessage(m: unknown) { posted.push(m) } close() {} })
    render(<Reader />)
    await tick(0)
    sent.length = 0
    act(() => chan!.onmessage!())
    await tick(2000)
    expect(asked('/api/notices')).toHaveLength(1)
    expect(posted).toEqual([])
  })

  it('leaves out the chat in front of the person, and says the ones left were seen', async () => {
    answer = { ...answer, items: [WAITING, REPLY], chats: { aaaaaaaa: { waiting: true, session: 's1' }, bbbbbbbb: { unread: 'reply' } }, sessions: [session({ status: 'awaiting_approval' })] }
    render(<Reader />)
    await tick(0)
    expect(shown()).toBe('2 1 2 aaaaaaaa:,bbbbbbbb:reply')
    act(() => setFrontChat('bbbbbbbb'))
    expect(shown()).toBe('1 1 1 aaaaaaaa:,bbbbbbbb:')
    await tick(1000)
    sent.length = 0
    // A chat waiting on the person counts again once they leave it unanswered.
    act(() => setFrontChat('aaaaaaaa'))
    expect(shown()!.startsWith('1 0 1')).toBe(true)
    await tick(1000)
    expect(asked('/api/notices/seen').map(s => s.body)).toEqual([{ conversations: ['bbbbbbbb', 'aaaaaaaa'] }])
    act(() => setFrontChat(null))
    expect(shown()!.startsWith('2 1 2')).toBe(true)
    // Leaving the tab: what the open chat showed was seen, a later reply is not.
    act(() => setFrontChat('bbbbbbbb'))
    await tick(1000)
    sent.length = 0
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true })
    act(() => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(sent.map(s => [s.path, s.body])).toEqual([['/api/notices/seen', { conversations: ['bbbbbbbb'] }]])
    await tick(0)
    expect(shown()!.startsWith('2')).toBe(true)
    // Back, it is read at once.
    sent.length = 0
    await tick(10_000)
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true })
    act(() => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(asked('/api/notices')).toHaveLength(1)
    expect(shown()!.startsWith('1')).toBe(true)
    await tick(0)
    expect(asked('/api/notices/seen')).toHaveLength(1)
  })

  it('shows what the Code page lists, which reads more often', async () => {
    render(<Reader />)
    await tick(0)
    sent.length = 0
    act(() => publishListing({ machines: [machine], sessions: [session({})] }))
    await tick(2000)
    expect(sent).toEqual([])
    // Another session waits there: the notices are read at once.
    answer = { ...answer, items: [notice({ id: 'code:s1:ap', kind: 'code', what: 'waiting', session: 's1', nav: ['code'] })], sessions: [session({ status: 'awaiting_approval' })] }
    act(() => publishListing({ machines: [machine], sessions: [session({ status: 'awaiting_approval' })] }))
    await tick(2000)
    expect(asked('/api/notices')).toHaveLength(1)
    expect(shown()).toBe('0 1 1 ')
    sent.length = 0
    act(() => publishListing({ machines: [machine], sessions: [session({ status: 'awaiting_approval' })] }))
    await tick(2000)
    expect(sent).toEqual([])
  })

  it('marks a reply written here as it starts, has no Code count where nothing can be listed, and leaves a failed transfer to its page', async () => {
    answer = { ...answer, listed: false, items: [FAILED], chats: { bbbbbbbb: { unread: 'reply' } } }
    render(<Reader />)
    await tick(0)
    expect(shown()).toBe('0 null 1 bbbbbbbb:reply')
    act(() => { setReplying('bbbbbbbb', true); setTransfersShown(true) })
    expect(shown()).toBe('0 null 0 bbbbbbbb:working')
    act(() => { setReplying('bbbbbbbb', false); setTransfersShown(false) })
    expect(shown()).toBe('0 null 1 bbbbbbbb:reply')
    expect(sent).toHaveLength(1)
  })

  it('an opened session is the end of its delegated work seen, once', async () => {
    answer = { ...answer, items: [DONE] }
    render(<Reader />)
    await tick(0)
    sent.length = 0
    answer = { ...answer, items: [] }
    act(() => { sessionSeen('other'); sessionSeen('d1') })
    await tick(0)
    expect(sent.map(s => [s.path, s.body])).toEqual([['/api/notices/seen', { conversations: [], session: 'd1' }]])
    act(() => sessionSeen('d1'))
    await tick(0)
    expect(sent).toHaveLength(1)
  })
})

describe('the menu', () => {
  it('counts what needs the person on Assistant, Code, Agents and Notices, from one read', async () => {
    answer = { ...answer, items: [WAITING, REPLY, FLEET, DONE], sessions: [session({ status: 'awaiting_approval' })] }
    const { container } = render(<App />)
    const nav = container.querySelector('.sidenav') as HTMLElement
    expect(await within(nav).findByTestId('nav-chat-waiting')).toHaveTextContent('2 need you')
    expect(within(nav).getByTestId('nav-code-waiting')).toHaveTextContent('1 waiting for you')
    expect(within(nav).getByTestId('nav-agents-waiting')).toHaveTextContent('1 waiting for you')
    // At the sidebar's foot, and in the phone's bar.
    expect(within(nav).getAllByTestId('nav-notices-waiting').map(m => m.textContent)).toEqual(['44 need you', '44 need you'])
    expect(container.querySelector('.nav-notices-phone [data-testid="nav-notices-waiting"]')).not.toBeNull()
    expect(asked('/api/code-page/sessions')).toEqual([])
    expect(asked('/api/notices')).toHaveLength(1)
    // On a phone the pages in More keep their marks.
    fireEvent.click(within(nav).getByTitle('More'))
    const sheet = container.querySelector('.nav-more-sheet') as HTMLElement
    expect(within(sheet).getByTestId('nav-code-waiting')).toBeInTheDocument()
    expect(within(sheet).queryByText('Notices')).toBeNull()

    fireEvent.click(within(nav).getAllByTitle('Notices')[0])
    await waitFor(() => expect(location.hash).toBe('#view=notices'))
    expect(await screen.findByText('Draft the report')).toBeInTheDocument()
  })

  it('has the Transfers mark from the same read, and follows its summary', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    answer = { ...answer, transfers: { active: 2, awaitingApproval: 0, failedLast24h: 0 } }
    const { container } = render(<App />)
    await tick(1000)
    expect(container.querySelector('.sidenav')).toHaveTextContent('2')
    sent.length = 0
    await tick(120_000)
    expect(asked('/api/notices')).toHaveLength(4)
    // Every 15 seconds until the socket is open (live.test.ts).
    expect(asked('/api/transfers/summary')).toHaveLength(8)
  })

  it('has Notices where pages are switched off, and no mark where nothing needs them', async () => {
    config = { ...config, sections: ['library', 'settings', 'help'] }
    const { container } = render(<App />)
    await waitFor(() => expect(asked('/api/notices')).toHaveLength(1))
    const nav = container.querySelector('.sidenav') as HTMLElement
    expect(within(nav).getAllByTitle('Notices')).toHaveLength(2)
    expect(within(nav).queryByTestId('nav-notices-waiting')).toBeNull()
    fireEvent.click(within(nav).getAllByTitle('Notices')[0])
    expect(await screen.findByText('Nothing needs you')).toBeInTheDocument()
  })
})

describe('the Notices page', () => {
  it('lists what waits first, each with its link, and dismisses', async () => {
    answer = { ...answer, items: [WAITING, FLEET, REPLY, DONE, FAILED] }
    render(<><Reader /><NoticesView /></>)
    await screen.findByText('Budget')
    const rows = screen.getAllByTestId('notice')
    expect(rows.map(r => `${r.dataset.kind} ${within(r).getByRole('link').getAttribute('href')}`)).toEqual([
      'chat #chat=aaaaaaaa', 'fleet #view=agents:fleet?agent=a1', 'chat #chat=bbbbbbbb', 'delegated #view=code?session=d1', 'transfer #view=transfers?transfer=t1',
    ])
    expect(rows[0]).toHaveTextContent('Waits for your approval: make test')
    expect(rows[3]).toHaveTextContent('Delegated work')
    expect(screen.getAllByRole('heading', { level: 2 }).map(h => h.textContent)).toEqual(['Waiting on you 2', 'New 3'])
    // The end of delegated work is dealt with by opening it; what waits is not.
    fireEvent.click(within(rows[3]).getByRole('link'))
    fireEvent.click(within(rows[0]).getByRole('link'))
    await waitFor(() => expect(screen.getAllByTestId('notice')).toHaveLength(4))
    expect(asked('/api/notices/dismiss').map(s => s.body)).toEqual([{ ids: [DONE.id] }])
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss Weather' }))
    await waitFor(() => expect(screen.getAllByTestId('notice')).toHaveLength(3))
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss all' }))
    expect(await screen.findByText('Nothing needs you')).toBeInTheDocument()
    expect(asked('/api/notices/dismiss').at(-1)!.body).toEqual({ ids: [WAITING.id, FLEET.id, FAILED.id] })
  })
})

describe('a folder shared with the person', () => {
  it('says who shared what, where and for what, is added in one press, and dismisses', async () => {
    answer = { ...answer, items: [SHARED, GROUP] }
    render(<><Reader /><NoticesView /></>)
    const rows = await screen.findAllByTestId('notice')
    expect(rows[0]).toHaveTextContent('ada shared proj with you')
    expect(rows[0]).toHaveTextContent('Read and write')
    expect(rows[0]).toHaveTextContent('cluster-a')
    expect(rows[0]).toHaveTextContent('/home/ada/studio/proj')
    expect(rows[1]).toHaveTextContent('ada shared runs with your group team')
    expect(within(rows[0]).getByTestId('machine-label')).toHaveTextContent('cluster-a')
    expect(within(rows[1]).getByTestId('machine-label')).toHaveTextContent('example-box')
    expect(rows[1]).toHaveTextContent('Read')
    expect(rows[1]).not.toHaveTextContent('Read and write')
    expect(within(rows[0]).queryByRole('link')).toBeNull()

    let done: (r: Response) => void = () => {}
    added = () => new Promise<Response>(f => { done = f })
    fireEvent.click(within(rows[0]).getByRole('button', { name: 'Add to my libraries: proj' }))
    expect(within(rows[0]).getByRole('button', { name: /Dismiss/ })).toBeDisabled()
    expect(asked('/api/user-libraries/shared-with-me/s-1/add').map(s => [s.method, s.body])).toEqual([['POST', {}]])
    answer = { ...answer, items: [GROUP] }
    await act(async () => { done(new Response(JSON.stringify({ id: 'u-shared-proj', label: 'proj (ada)' }), { status: 200 })) })
    await waitFor(() => expect(screen.getAllByTestId('notice')).toHaveLength(1))
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss ada shared runs with your group team' }))
    expect(await screen.findByText('Nothing needs you')).toBeInTheDocument()
    expect(asked('/api/notices/dismiss').at(-1)!.body).toEqual({ ids: [GROUP.id] })
  })

  it('says why it could not be added, and goes when it is no longer shared', async () => {
    answer = { ...answer, items: [SHARED] }
    render(<><Reader /><NoticesView /></>)
    added = () => new Response(JSON.stringify({ error: 'GUFI does not run there.' }), { status: 409 })
    fireEvent.click(await screen.findByRole('button', { name: 'Add to my libraries: proj' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('GUFI does not run there.')
    expect(screen.getByRole('button', { name: 'Add to my libraries: proj' })).toBeEnabled()
    added = () => { answer = { ...answer, items: [] }; return new Response(JSON.stringify({ error: 'no such share' }), { status: 404 }) }
    fireEvent.click(screen.getByRole('button', { name: 'Add to my libraries: proj' }))
    expect(await screen.findByText('Nothing needs you')).toBeInTheDocument()
  })

  it('is asked for once a key is set, and goes when the folder is added in Add a library', async () => {
    vi.useFakeTimers()
    render(<><Reader /><NoticesView /><SharedWithMe onAdded={() => {}} /></>)
    await tick(0)
    expect(screen.queryByTestId('notice')).toBeNull()
    answer = { ...answer, items: [SHARED] }
    act(() => { window.dispatchEvent(new CustomEvent('ade:model-access-changed')) })
    await tick(2_000)
    expect(screen.getByTestId('notice')).toHaveTextContent('ada shared proj with you')
    answer = { ...answer, items: [] }
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    await tick(100)
    expect(screen.queryByTestId('notice')).toBeNull()
  })

  it('is read again soon while the person’s machines are asked, and not after', async () => {
    vi.useFakeTimers()
    answer = { ...answer, items: [], looking: true }
    render(<><Reader /><NoticesView /></>)
    await tick(0)
    const n = asked('/api/notices').length
    answer = { ...answer, items: [SHARED], looking: false }
    await tick(NOTICES_LOOKING_MS + 100)
    expect(asked('/api/notices')).toHaveLength(n + 1)
    expect(screen.getByTestId('notice')).toHaveTextContent('ada shared proj with you')
    await tick(NOTICES_LOOKING_MS * 3)
    expect(asked('/api/notices')).toHaveLength(n + 1)
  })
})

describe('a chat in the list', () => {
  let t: Translate
  beforeEach(async () => { t = createTranslator({ locale: 'en', messages: await loadPwCodeMessages('en'), namespace: 'agents' }) as unknown as Translate })
  const state = (n: Parameters<typeof chatRowState>[0], s?: PwCodeSession, m: PwCodeMachine | undefined = machine) => chatRowState(n, s, m, true, t)

  it('has its session’s state, in the Code page’s mark', () => {
    const marked = (n: Parameters<typeof chatRowState>[0], s: PwCodeSession) => {
      const r = state(n, s)!
      const { container, unmount } = render(<>{r.mark('row')}</>)
      const out = `${container.querySelector('[data-testid="agent-session-working"]') ? 'ring' : 'dot'} ${r.active ? 'active' : 'by time'} ${r.detail ? r.text : ''}`.trim()
      expect(container.querySelector('.code-mark')).not.toBeNull()
      unmount()
      return out
    }
    expect(marked({ session: 's1' }, session({}))).toBe('dot by time')
    expect(marked({ session: 's1' }, session({ status: 'running' }))).toBe('ring active')
    expect(marked({ session: 's1', waiting: true }, session({ status: 'awaiting_approval' }))).toBe('dot active Waiting on you')
    // A reply being written here, and a request, the listing has yet to show.
    expect(marked({ session: 's1', working: true }, session({}))).toBe('ring active')
    expect(marked({ session: 's1', waiting: true }, session({ status: 'running' }))).toBe('dot active Waiting on you')
    expect(marked({ session: 's1', unread: 'reply' }, session({}))).toBe('dot by time New reply')
    expect(state({ session: 's1' }, session({ archivedAt: at }))!.text).toBe('Session ended')
    expect(state({ session: 's1' }, undefined, undefined)!.text).toBe('Its session is not listed now')
  })

  it('of the Studio assistant is working while a reply is written', () => {
    expect(state(undefined)).toBeNull()
    expect(state({})).toBeNull()
    expect(state({ working: true })).toMatchObject({ active: true, text: 'Working' })
    expect(state({ waiting: true })).toMatchObject({ active: true, waiting: true, text: 'Waiting on you' })
    expect(state({ unread: 'note' })).toMatchObject({ active: false, text: 'New note', detail: true })
  })

  it('is under Active while it works or waits, what waits first, and the rest follow by time', async () => {
    const day = 86_400_000
    const conv = (id: string, title: string, age: number) => ({ id, title, createdAt: new Date(Date.now() - age).toISOString(), updatedAt: new Date(Date.now() - age).toISOString(), isOwner: true })
    const list = [conv('c-new', 'Newest', 1000), conv('c-work', 'Working one', 2000), conv('c-read', 'Unread one', 3000), conv('c-wait', 'Waiting one', day * 3), { ...conv('c-old', 'Old one', day * 4), isOwner: false }]
    answer = { ...answer, sessions: [session({ id: 's2', status: 'running' })], chats: { 'c-work': { session: 's2' }, 'c-wait': { waiting: true }, 'c-read': { unread: 'reply', session: 's-gone', machineName: 'gpu-07' } } }
    const adapter = { conversations: { list: async () => list, get: async () => null, create: async () => list[0], rename: async () => {}, delete: async () => {} }, models: { list: async () => ({ models: [], unreachableSessions: [], providerIssues: [] }) } } as unknown as ChatAdapter
    function Page() {
      const [id, setId] = useState<string | null>(null)
      return (
        <ChatProvider adapter={adapter} currentUser={{ id: 'u', username: 'alex' } as never} activeConversationId={id} navigation={{ toConversation: setId, toNewChat: () => setId(null) } as never}>
          <StudioChatLayout sidebarMode="inline" sidebarTop={() => null}><p>thread</p></StudioChatLayout>
        </ChatProvider>
      )
    }
    const { container } = render(<><Reader /><Page /></>)
    await screen.findByText('Waiting one')
    await waitFor(() => expect(container.querySelector('[data-testid="agent-session-working"]')).not.toBeNull())
    expect(screen.getAllByTestId('chat-list-group')[0]).toHaveTextContent('Active')
    const titles = [...container.querySelectorAll('[data-sidebar-row]')].map(r => r.textContent)
    expect(titles.map(x => ['Waiting one', 'Working one', 'Newest', 'Unread one', 'Old one'].find(n => x!.includes(n)))).toEqual(['Waiting one', 'Working one', 'Newest', 'Unread one', 'Old one'])
    expect(titles[0]).toContain('Waiting on you')
    expect(titles[3]).toContain('New reply')
    // A pw code chat says its machine, on one second line with its state; a
    // Studio assistant chat's row is as it was.
    expect(titles[1]).toContain('example-box')
    expect(titles[3]).toContain('gpu-07')
    expect(titles[0]).not.toContain('example-box')
    expect(screen.getAllByTestId('machine-label').map(x => [x.textContent, x.title])).toEqual([['example-box', 'example-box (box.example)'], ['gpu-07', 'gpu-07']])
    // Another person's chat, shared, keeps its icon.
    expect(screen.getAllByTitle(/shared/i)).toHaveLength(1)
    expect(within(container.querySelectorAll<HTMLElement>('[data-sidebar-row]')[4]).getByTitle(/shared/i)).toBeInTheDocument()
    expect(screen.getAllByTestId('chat-row-mark').map(m => m.dataset.state)).toEqual(['waiting', 'working', 'reply'])
    expect(screen.getByText('Waiting one').tagName).toBe('B')

    // A chat whose session the notices list opens it in Code from its menu;
    // one with no session, or none listed, has nothing to open.
    const menuOf = async (title: string) => {
      fireEvent.contextMenu(screen.getByText(title))
      const items = await screen.findAllByRole('menuitem')
      return { items: items.map(m => m.textContent), pick: (name: string) => fireEvent.click(items.find(m => m.textContent === name)!) }
    }
    const waiting = await menuOf('Waiting one')
    expect(waiting.items).not.toContain('Open in Code')
    fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })
    const working = await menuOf('Working one')
    expect(working.items[0]).toBe('Open in Code')
    working.pick('Open in Code')
    expect(location.hash).toBe('#view=code?machine=m1&session=s2')
  })

  describe('folded to its rail', () => {
    const day = 86_400_000
    const conv = (id: string, title: string, age: number) => ({ id, title, createdAt: new Date(Date.now() - age).toISOString(), updatedAt: new Date(Date.now() - age).toISOString(), isOwner: true })
    const list = [
      conv('c-live', 'Live one', 1000), conv('c-plain', 'Plain one', 1500), conv('c-work', 'Working one', 2000), conv('c-rest', 'Resting one', 2500),
      conv('c-read', 'Unread one', day * 2), conv('c-wait', 'Waiting one', day * 3), conv('c-ended', 'Ended one', day * 4), conv('c-live2', 'Older live one', day * 40),
    ]
    const marks = () => screen.queryAllByTestId('chat-rail-mark')
    const names = () => marks().map(m => m.getAttribute('aria-label'))
    const opened: string[] = []
    let modes: string[] = []

    function Page({ start = null, phone = false }: { start?: string | null; phone?: boolean }) {
      const [id, setId] = useState<string | null>(start)
      const [side, setSide] = useState<'expanded' | 'collapsed' | 'hidden'>('collapsed')
      const adapter = useState(() => ({ conversations: { list: async () => list, get: async () => null, create: async () => list[0], rename: async () => {}, delete: async () => {} }, models: { list: async () => ({ models: [], unreachableSessions: [], providerIssues: [] }) } }) as unknown as ChatAdapter)[0]
      return (
        <ChatProvider adapter={adapter} currentUser={{ id: 'u', username: 'alex' } as never} activeConversationId={id}
          sidebar={side} onSidebarChange={s => { modes.push(s); setSide(s) }}
          navigation={{ toConversation: (c: string) => { opened.push(c); setId(c) }, toNewChat: () => setId(null) } as never}>
          <StudioChatLayout sidebarMode={phone ? 'drawer' : 'inline'} sidebarTop={() => null}><p>thread</p></StudioChatLayout>
        </ChatProvider>
      )
    }

    beforeEach(() => {
      opened.length = 0
      modes = []
      answer = {
        ...answer,
        sessions: [
          session({ id: 's-live', liveHost: 'example-box' }), session({ id: 's-work', status: 'running', liveHost: 'example-box' }), session({ id: 's-rest' }),
          session({ id: 's-ended', archivedAt: at }), session({ id: 's-live2', liveHost: 'example-box' }),
        ],
        chats: {
          'c-live': { session: 's-live' }, 'c-work': { session: 's-work' }, 'c-rest': { session: 's-rest' }, 'c-read': { unread: 'reply' },
          'c-wait': { waiting: true }, 'c-ended': { session: 's-ended' }, 'c-live2': { session: 's-live2' },
        },
      }
    })

    it('has a mark for each chat that works, waits, is live or has a new reply, in the list’s order', async () => {
      const { container } = render(<><Reader /><Page /></>)
      await waitFor(() => expect(marks()).toHaveLength(5))
      await waitFor(() => expect(container.querySelector('[data-testid="agent-session-working"]')).not.toBeNull())
      expect(names()).toEqual(['Waiting one, Waiting on you', 'Working one, running', 'Live one, idle', 'Unread one, New reply', 'Older live one, idle'].map(x => expect.stringMatching(new RegExp(`^${x}$`, 'i'))))
      expect(marks().map(m => m.querySelector('[data-state]')!.getAttribute('data-state'))).toEqual(['waiting', 'working', 'idle', 'reply', 'idle'])
      // What it shows is what the notices read said: nothing else is read.
      expect(asked('/api/notices')).toHaveLength(1)
      expect(sent.map(s => s.path).filter(p => /session|pwcode|code/.test(p))).toEqual([])
      // The rows themselves are not behind it, to be reached by Tab unseen.
      expect(container.querySelectorAll('[data-sidebar-row]')).toHaveLength(0)
    })

    it('opens a chat in place, and keeps the mark of the open one', async () => {
      render(<><Reader /><Page /></>)
      await waitFor(() => expect(marks()).toHaveLength(5))
      const unread = marks()[3]
      unread.focus()
      fireEvent.click(unread)
      expect(opened).toEqual(['c-read'])
      expect(modes).toEqual([])
      // Read now, it stays where it was, marked as the one open, with the focus.
      answer = { ...answer, chats: { ...answer.chats, 'c-read': {} } }
      act(() => setFrontChat('c-read'))
      await waitFor(() => expect(names()[3]).toBe('Unread one'))
      expect(marks()[3]).toBe(unread)
      expect(unread).toHaveAttribute('aria-current', 'page')
      expect(document.activeElement).toBe(unread)
      fireEvent.click(marks()[0])
      await waitFor(() => expect(marks()).toHaveLength(4))
      expect(opened).toEqual(['c-read', 'c-wait'])
    })

    it('marks the open chat where it has no state of its own', async () => {
      render(<><Reader /><Page start="c-plain" /></>)
      await waitFor(() => expect(marks()).toHaveLength(6))
      expect(names()[3]).toBe('Plain one')
      expect(marks()[3]).toHaveAttribute('aria-current', 'page')
      expect(marks()[3].querySelector('[data-lamp="off"]')).not.toBeNull()
    })

    it('moves between its marks with the arrow keys, Home and End', async () => {
      render(<><Reader /><Page /></>)
      await waitFor(() => expect(marks()).toHaveLength(5))
      const key = (k: string, o = {}) => fireEvent.keyDown(document.activeElement!, { key: k, ...o })
      marks()[0].focus()
      key('ArrowDown')
      expect(document.activeElement).toBe(marks()[1])
      key('End')
      expect(document.activeElement).toBe(marks()[4])
      key('ArrowDown')
      expect(document.activeElement).toBe(marks()[4])
      key('ArrowUp')
      expect(document.activeElement).toBe(marks()[3])
      key('ArrowUp', { altKey: true })
      expect(document.activeElement).toBe(marks()[3])
      key('Home')
      expect(document.activeElement).toBe(marks()[0])
    })

    it('has Search, which opens the list with the cursor in it, and what was searched for does not hide a mark', async () => {
      render(<><Reader /><Page /></>)
      await waitFor(() => expect(marks()).toHaveLength(5))
      fireEvent.click(screen.getByRole('button', { name: /^Search conversations/ }))
      expect(modes).toEqual(['expanded'])
      const box = await screen.findByRole('searchbox')
      await waitFor(() => expect(document.activeElement).toBe(box))
      fireEvent.change(box, { target: { value: 'Plain' } })
      await waitFor(() => expect(document.querySelectorAll('[data-sidebar-row]')).toHaveLength(1))
      fireEvent.click(screen.getByRole('button', { name: 'Close sidebar' }))
      await waitFor(() => expect(marks()).toHaveLength(5))
    })

    it('is not on a phone, where the list is a drawer', async () => {
      render(<><Reader /><Page phone /></>)
      await waitFor(() => expect(asked('/api/notices')).toHaveLength(1))
      await act(async () => {})
      expect(marks()).toEqual([])
      expect(screen.queryByRole('button', { name: /^Search conversations/ })).toBeNull()
    })
  })
})
