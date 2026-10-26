import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLibrary, type PublicLibrary } from '../src/api'
import { LibraryScope } from '../src/components/LibraryScope'
import { readScope, resetScopesForTests, saveScope, scopeBody, scopeParam, settleScope } from '../src/scope'
import { SearchView, searchFromHash, searchHash } from '../src/views/SearchView'

// A switch forwards its press to its hidden input as a PointerEvent, which
// jsdom does not have.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const lib = (id: string, label: string, extra: Partial<PublicLibrary> = {}): PublicLibrary => ({
  id, label, primary: id === 'kb', writable: id === 'kb', pinned: id === 'kb', source: true, caps: { index: true, fullText: true, vectors: false }, meaning: 'off', ...extra,
})
const LIBS = [lib('kb', 'Notes'), lib('papers', 'Papers'), lib('site', 'Site index', { site: 'cluster-a' })]

beforeEach(() => { resetScopesForTests(); act(() => setLibrary('kb')) })
afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

describe('a page\'s libraries', () => {
  it('starts with every library, is kept per page, and settles against the libraries there are', () => {
    expect(readScope('search')).toEqual('all')
    expect(readScope('query')).toEqual('all')
    saveScope('search', ['papers', 'site'])
    expect(readScope('search')).toEqual(['papers', 'site'])
    expect(JSON.parse(localStorage.getItem('ade-scope-search')!)).toEqual(['papers', 'site'])
    expect(readScope('query')).toEqual('all', 'each page its own')
    expect(settleScope(['papers', 'gone'], LIBS)).toEqual(['papers'])
    expect(settleScope(['gone'], LIBS)).toEqual('all')
    expect(settleScope(['kb', 'papers', 'site'], LIBS)).toEqual('all', 'every one ticked is all')
    expect(settleScope('all', [LIBS[0]])).toEqual(['kb'])
  })

  it('goes to the server as all, one id, or ids with commas; in a body, the knowledge base alone is left out', () => {
    expect(scopeParam('all')).toBe('all')
    expect(scopeParam(['papers', 'site'])).toBe('papers,site')
    expect(scopeBody(['kb'])).toEqual({})
    expect(scopeBody(['papers'])).toEqual({ library: 'papers' })
    expect(scopeBody(['kb', 'papers'])).toEqual({ library: ['kb', 'papers'] })
    expect(scopeBody('all')).toEqual({ library: 'all' })
    expect(scopeBody('all', 1)).toEqual({}, 'one library: nothing to name')
  })
})

describe('the library picker', () => {
  it('ticks any of the libraries, All for every one, and keeps one ticked', async () => {
    const changes: unknown[] = []
    const { rerender } = render(<LibraryScope libraries={LIBS} value="all" onChange={s => changes.push(s)} hint="Which libraries to search" />)
    const button = screen.getByRole('button', { name: 'Libraries: All libraries' })
    fireEvent.click(button)
    const group = await screen.findByRole('group', { name: 'Libraries to include' })
    fireEvent.click(within(group).getByRole('checkbox', { name: 'Papers (read only)' }))
    expect(changes.pop()).toEqual(['kb', 'site'])
    rerender(<LibraryScope libraries={LIBS} value={['kb', 'site']} onChange={s => changes.push(s)} hint="Which libraries to search" />)
    expect(screen.getByRole('button', { name: 'Libraries: 2 libraries' })).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole('group', { name: 'Libraries to include' })).getByRole('checkbox', { name: 'Papers (read only)' }))
    expect(changes.pop()).toEqual('all', 'every one ticked again is All')
    rerender(<LibraryScope libraries={LIBS} value={['site']} onChange={s => changes.push(s)} hint="Which libraries to search" />)
    expect(screen.getByRole('button', { name: 'Libraries: Site index' })).toBeInTheDocument()
    const group2 = screen.getByRole('group', { name: 'Libraries to include' })
    expect(within(group2).getByRole('checkbox', { name: 'Site index (on cluster-a)' })).toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(within(group2).getByRole('checkbox', { name: 'All libraries' }))
    expect(changes.pop()).toEqual('all')
  })

  it('is not shown with one library', () => {
    render(<LibraryScope libraries={[LIBS[0]]} value="all" onChange={() => {}} hint="x" />)
    expect(screen.queryByRole('button', { name: /^Libraries/ })).toBeNull()
  })
})

describe('the Search page', () => {
  const stub = (search: (url: URL) => { status?: number; body: unknown }) => {
    const asked: URL[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = new URL(String(input), 'http://studio.test')
      if (url.pathname === '/api/config') return new Response(JSON.stringify({ libraries: LIBS }), { status: 200 })
      if (url.pathname === '/api/search') {
        asked.push(url)
        const r = search(url)
        return new Response(JSON.stringify(r.body), { status: r.status ?? 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify({ tags: [] }), { status: 200 })
    }))
    return asked
  }
  const hits = { hits: [
    { path: 'a.md', name: 'a.md', size: 1, mtime: 0, mode: 'fts', snippet: 'one', library: { id: 'kb', label: 'Notes' } },
    { path: 'b.pdf', name: 'b.pdf', size: 1, mtime: 0, mode: 'name', snippet: '', library: { id: 'papers', label: 'Papers' } },
  ] }

  it('searches the libraries ticked, kept from before, and says which library each match is in', async () => {
    saveScope('search', ['kb', 'papers'])
    const asked = stub(() => ({ body: hits }))
    render(<SearchView onOpen={() => {}} />)
    expect(await screen.findByRole('button', { name: 'Libraries: 2 libraries' })).toBeInTheDocument()
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'notes' } })) })
    await screen.findByText('b.pdf')
    expect(asked[0].searchParams.get('library')).toBe('kb,papers')
    expect(asked[0].searchParams.get('q')).toBe('notes')
    expect(screen.getByText('Papers')).toBeInTheDocument()
    expect(location.hash).toBe('#view=search?q=notes&in=kb%2Cpapers')
  })

  it('refines: names and paths apart from text and meaning, either kind or both, in the address too', async () => {
    const asked = stub(() => ({ body: hits }))
    render(<SearchView onOpen={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Refine' }))
    fireEvent.change(screen.getByLabelText('Name or path'), { target: { value: '*.docx' } })
    fireEvent.change(screen.getByLabelText('Text or meaning'), { target: { value: 'budget NOT draft' } })
    expect(screen.getByText('Shows matches of either kind, each to its own limit.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: 'Only in files whose name or path matches' }))
    expect(screen.getByText('Lists the files that match both.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(asked.length).toBe(1))
    const p = asked[0].searchParams
    expect([p.get('q'), p.get('name'), p.get('text'), p.get('match')]).toEqual([null, '*.docx', 'budget NOT draft', 'all'])
    expect(searchFromHash(location.hash)).toEqual({ q: '', name: '*.docx', text: 'budget NOT draft', match: 'all', scope: null })
    fireEvent.click(screen.getByRole('button', { name: 'One box' }))
    expect(screen.queryByLabelText('Name or path')).toBeNull()
  })

  it('runs a search from the address on arrival, with its fields and libraries', async () => {
    history.replaceState(null, '', location.pathname + searchHash({ q: '', name: '*.pdf', text: 'fusion', match: 'all', scope: ['papers'] }))
    const asked = stub(() => ({ body: hits }))
    render(<SearchView onOpen={() => {}} />)
    await waitFor(() => expect(asked.length).toBe(1))
    expect(Object.fromEntries(asked[0].searchParams)).toMatchObject({ name: '*.pdf', text: 'fusion', match: 'all', library: 'papers' })
    expect(screen.getByLabelText('Name or path')).toHaveValue('*.pdf')
    expect(screen.getByLabelText('Text or meaning')).toHaveValue('fusion')
    expect(await screen.findByRole('button', { name: 'Libraries: Papers' })).toBeInTheDocument()
  })

  it('says why text it cannot read found nothing', async () => {
    stub(() => ({ status: 400, body: { error: 'The text search cannot be read (a bracket is not closed). Put a word with punctuation in double quotes.' } }))
    render(<SearchView onOpen={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Refine' }))
    fireEvent.change(screen.getByLabelText('Text or meaning'), { target: { value: 'a AND (b' } })
    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    expect(await screen.findByText(/The text search cannot be read \(a bracket is not closed\)/)).toBeInTheDocument()
  })

  it('opens a match from another library in that library', async () => {
    saveScope('search', ['kb', 'papers'])
    stub(() => ({ body: hits }))
    const opened: string[] = []
    render(<SearchView onOpen={p => opened.push(p)} />)
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'notes' } })) })
    fireEvent.click(await screen.findByText('b.pdf'))
    const { currentLibrary } = await import('../src/api')
    expect(currentLibrary()).toBe('papers')
    expect(opened).toEqual(['b.pdf'])
  })
})

describe('the Query page', () => {
  it('queries the libraries ticked, as a list, and opens a row in the library it came from', async () => {
    saveScope('query', ['kb', 'papers'])
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
    const bodies: Record<string, unknown>[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/config') return new Response(JSON.stringify({ libraries: LIBS }), { status: 200 })
      if (url === '/api/query' && init?.method === 'POST') {
        bodies.push(JSON.parse(String(init.body)))
        return new Response(JSON.stringify({ columns: ['library', 'path', 'bytes'], rows: [['Papers', 'b.pdf', '9']], rowLibraries: ['papers'], elapsedMs: 3, truncated: false, engine: 'gufi_vt' }), { status: 200 })
      }
      return new Response(JSON.stringify([]), { status: 200 })
    }))
    const { QueryView } = await import('../src/views/QueryView')
    const opened: string[] = []
    render(<QueryView onOpen={p => opened.push(p)} />)
    expect(await screen.findByRole('button', { name: 'Libraries: 2 libraries' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    await waitFor(() => expect(bodies.length).toBe(1))
    expect(bodies[0].library).toEqual(['kb', 'papers'])
    fireEvent.click(await screen.findByText('b.pdf'))
    const { currentLibrary } = await import('../src/api')
    expect(currentLibrary()).toBe('papers')
    expect(opened).toEqual(['b.pdf'])
  })
})
