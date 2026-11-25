import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '../src/App'
import { setLibrary } from '../src/api'

// Links to items are under test: the Library is real, the other pages stand-ins.
const { page } = vi.hoisted(() => ({ page: (name: string) => ({ [name]: () => `${name} page` }) }))
vi.mock('../src/views/ChatView', () => page('ChatView'))
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

const ID = 'AbCdEfGhIjKlMnOpQrSt_-'
const lib = (id: string, label: string) => ({ id, label, primary: id === 'kb', writable: true, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false } })
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
const dir = (path: string) => ({ name: path.split('/').pop(), path, type: 'dir', size: 0, mtime: 0 })
const file = (path: string) => ({ name: path.split('/').pop(), path, type: 'file', size: 10, mtime: 0 })
const TREE: Record<string, unknown[]> = {
  '': [dir('kb'), dir('archive')],
  kb: [dir('kb/reports')], 'kb/reports': [dir('kb/reports/2026')], 'kb/reports/2026': [file('kb/reports/2026/plan.md')],
  archive: [dir('archive/old')], 'archive/old': [file('archive/old/note.md')],
}

let answers: { status: number; body: unknown }[] = []
let asked: string[] = []
beforeEach(() => {
  asked = []
  sessionStorage.clear()
  setLibrary('all')
  window.HTMLElement.prototype.scrollIntoView = vi.fn()
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = new URL(String(input), 'http://x')
    if (url.pathname === '/api/config') return json({ appName: 'Studio', kbLabel: 'Projects', libraries: [lib('kb', 'Projects'), lib('archive', 'Archive')], user: { id: 'u', username: 'u' }, linkBase: 'https://studio.example.com' })
    if (url.pathname.startsWith('/api/links/')) { asked.push(url.pathname); const a = answers.length > 1 ? answers.shift()! : answers[0]; return json(a.body, a.status) }
    if (url.pathname === '/api/links') { asked.push(String(init?.body)); return json({ id: ID }) }
    if (url.pathname === '/api/kb/tree') return json({ path: '', entries: TREE[url.searchParams.get('path') ?? ''] ?? [] })
    if (url.pathname === '/api/kb/file') return json({ path: url.searchParams.get('path'), content: 'The text.', size: 9, mtime: 1, source: 'raw', truncated: false })
    if (url.pathname.startsWith('/api/kb/tags') || url.pathname.startsWith('/api/tags')) return json({ own: [], inherited: [], tags: [] })
    return json({})
  }))
})
afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', '/'); sessionStorage.clear() })

const opens = (o: Record<string, unknown>) => { answers = [{ status: 200, body: { id: ID, replaced: false, ...o } }] }
const says = (status: number, error: string, reason: string) => { answers = [{ status, body: { error, reason } }] }
const at = (path: string) => history.replaceState(null, '', path)

describe('following a link to an item', () => {
  it('opens a file in the library it is in, and takes the item off the address', async () => {
    opens({ library: 'archive', path: 'old/note.md', type: 'file' })
    at(`/?item=${ID}#item=${ID}`)
    render(<App />)
    await waitFor(() => expect(location.hash).toBe('#open=file:archive/old/note.md&lib=all'))
    expect(location.search).toBe('')
    expect(asked).toContain(`/api/links/${ID}`)
    await screen.findByRole('button', { name: 'Copy link' })
    expect(screen.queryByText(/replaced after the link was made/)).toBeNull()
  })

  it('opens the library at a folder: the folders above it open, and it is selected', async () => {
    opens({ library: 'kb', path: 'reports/2026', type: 'dir' })
    at(`/?item=${ID}`)
    const { container } = render(<App />)
    await screen.findByText('plan.md')
    expect(container.querySelector('.tree-row.dir.selected')?.getAttribute('data-path')).toBe('kb/reports/2026')
    await waitFor(() => expect(location.hash).toBe('#view=library'))
    // Another row chosen, and it is the one selected.
    fireEvent.click(screen.getByText('reports'))
    expect(container.querySelector('.tree-row.dir.selected')).toBeNull()
  })

  it('says where a folder that is not listed would be', async () => {
    opens({ library: 'kb', path: 'reports/brand-new/deep', type: 'dir' })
    at(`/?item=${ID}`)
    render(<App />)
    await screen.findByText(/The folder the link leads to is not listed here/)
    expect(screen.getByText('2026')).toBeInTheDocument()
  })

  it('says that a file was replaced since the link was made', async () => {
    opens({ library: 'kb', path: 'reports/2026/plan.md', type: 'file', replaced: true })
    at(`/#item=${ID}`)
    render(<App />)
    await screen.findByText(/This file was replaced after the link was made/)
  })

  it('says it of a folder too', async () => {
    opens({ library: 'kb', path: 'reports/2026', type: 'dir', replaced: true })
    at(`/#item=${ID}`)
    render(<App />)
    await screen.findByText(/This folder was replaced after the link was made/)
  })

  it('says what the server says of one that opens nothing, and leaves the Library', async () => {
    says(404, 'This link does not open anything for you.', 'none')
    at(`/?item=${ID}`)
    render(<App />)
    const d = await screen.findByRole('dialog')
    expect(within(d).getByText('This link does not open anything for you.')).toBeInTheDocument()
    expect(within(d).queryByRole('button', { name: 'Try again' })).toBeNull()
    expect(location.search).toBe('')
    await waitFor(() => expect(location.hash).toBe('#view=library'))
  })

  it('says plainly that an item was deleted', async () => {
    says(410, 'The item this link was made for has been deleted, or was moved somewhere the link does not follow.', 'deleted')
    at(`/?item=${ID}`)
    render(<App />)
    expect(await screen.findByText(/has been deleted/)).toBeInTheDocument()
  })

  it('tries again where the item could not be reached', async () => {
    answers = [
      { status: 502, body: { error: 'Archive could not be read now.', reason: 'unreachable' } },
      { status: 200, body: { id: ID, library: 'archive', path: 'old/note.md', type: 'file', replaced: false } },
    ]
    at(`/?item=${ID}`)
    render(<App />)
    const again = await screen.findByRole('button', { name: 'Try again' })
    // A click asks the Studio again and opens the item.
    answers = [answers[answers.length - 1]]
    fireEvent.click(again)
    await waitFor(() => expect(location.hash).toContain('#open=file:archive/old/note.md'))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('keeps the item in the address for a person who is signed out', async () => {
    answers = [{ status: 401, body: { error: 'Sign in to open this link.', reason: 'signed-out' } }]
    at(`/?item=${ID}`)
    render(<App />)
    const d = await screen.findByRole('dialog')
    expect(within(d).getByText('Sign in to open this link.')).toBeInTheDocument()
    expect(location.search).toBe(`?item=${ID}`)
  })

  it('follows one put into the address of an open page', async () => {
    opens({ library: 'archive', path: 'old/note.md', type: 'file' })
    at('/')
    render(<App />)
    await screen.findByText('ChatView page')
    expect(asked).toEqual([])
    history.pushState(null, '', `/#item=${ID}`)
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    await waitFor(() => expect(location.hash).toBe('#open=file:archive/old/note.md&lib=all'))
  })
})

describe('Copy link in the right-click menu', () => {
  it('is offered on a file, a folder and a library, beside Copy path, and names the item by its library', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn(async () => {}) }, configurable: true })
    at('/#view=library')
    const { container } = render(<App />)
    const row = async (p: string) => { await waitFor(() => expect(container.querySelector(`[data-path="${p}"]`)).not.toBeNull()); return container.querySelector(`[data-path="${p}"]`)! }
    for (const [p, body] of [['kb', { path: '', library: 'kb' }], ['archive/old', { path: 'old', library: 'archive' }]] as const) {
      if (p.includes('/')) fireEvent.click(await row('archive'))
      fireEvent.contextMenu(await row(p))
      const item = await screen.findByRole('menuitem', { name: 'Copy link' })
      expect(screen.getByRole('menuitem', { name: 'Copy path' })).toBeInTheDocument()
      fireEvent.click(item)
      await waitFor(() => expect(asked.map(a => JSON.parse(a))).toContainEqual(body))
      await waitFor(() => expect(screen.queryByRole('menuitem', { name: 'Copy link' })).toBeNull())
    }
    expect(navigator.clipboard.writeText).toHaveBeenLastCalledWith(`https://studio.example.com/?item=${ID}#item=${ID}`)
  })
})
