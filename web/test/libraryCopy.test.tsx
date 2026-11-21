import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LibraryView } from '../src/views/LibraryView'
import { setLibrary } from '../src/api'
import { resetTransfersProbe } from '../src/transferApi'
import { defaultDestination } from '../src/components/ResultsTransfer'
import { MOCK_LOCATIONS, createTransfersMock } from './transfersMock'

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom lacks.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const lib = (id: string, label: string, writable = true) => ({
  id, label, primary: id === 'kb', writable, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false },
})
const LIBS = [lib('kb', 'Projects'), lib('archive', 'Archive'), lib('reference', 'Reference', false)]
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
const dir = (path: string) => ({ name: path.split('/').pop(), path, type: 'dir', size: 0, mtime: 0 })
const file = (path: string) => ({ name: path.split('/').pop(), path, type: 'file', size: 10, mtime: 0 })

let mock: ReturnType<typeof createTransfersMock>
let kbPosts: { url: string; body: unknown }[] = []
// The tree in All libraries: a folder per library, then its own folders.
const ALL_TREE: Record<string, unknown[]> = {
  '': [dir('kb'), dir('archive'), dir('reference')],
  kb: [dir('kb/reports'), dir('kb/notes')],
  archive: [dir('archive/2026')],
  reference: [dir('reference/docs')],
}
function serve(o: { transfers?: boolean; all?: boolean } = {}) {
  kbPosts = []
  mock = createTransfersMock({ locations: MOCK_LOCATIONS, locationsStatus: o.transfers === false ? 404 : 200 })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    if (url.startsWith('/api/transfers')) {
      const r = mock.handle(init?.method ?? 'GET', url, body)
      return json(r.body, r.status)
    }
    if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Projects', libraries: LIBS })
    if (url.startsWith('/api/kb/tree')) {
      const at = new URL(url, 'http://x').searchParams.get('path') ?? ''
      if (o.all) return json({ path: at, entries: ALL_TREE[at] ?? [] })
      return json({ path: at, entries: at === '' ? [dir('reports'), file('notes.md')] : [] })
    }
    if (url.startsWith('/api/kb/') && init?.method === 'POST') { kbPosts.push({ url, body }); return json({ id: 'job-1', state: 'done', done: 1, total: 1 }) }
    return json({})
  }))
}

beforeEach(() => {
  resetTransfersProbe()
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb') })

/** A DataTransfer for jsdom, which has none: what a drag sets, a drop reads. */
function transfer() {
  const data: Record<string, string> = {}
  return {
    data, effectAllowed: 'all', dropEffect: 'none', items: [] as unknown[],
    get types() { return Object.keys(data) },
    setData(t: string, v: string) { data[t] = v },
    getData(t: string) { return data[t] ?? '' },
    setDragImage() {},
  }
}
const row = (path: string) => document.querySelector<HTMLElement>(`.tree-row[data-path="${path}"]`)!

describe('dragging between libraries (All libraries)', () => {
  async function openAll() {
    setLibrary('all')
    serve({ all: true })
    render(<LibraryView display={null} onDisplay={() => {}} />)
    for (const id of ['kb', 'archive', 'reference']) fireEvent.click(await screen.findByText(id))
    await screen.findByText('reports')
    await screen.findByText('docs')
  }

  it('marks the folders that take the drop, refuses a read-only library, and opens a copy on the folder dropped on', async () => {
    await openAll()
    const dt = transfer()
    fireEvent.dragStart(row('kb/reports'), { dataTransfer: dt })
    expect(dt.effectAllowed).toBe('copyMove')
    await waitFor(() => expect(row('archive').className).toContain('drop-ok'))
    expect(row('reference').className).toContain('drop-refused')
    expect(row('reference/docs').className).toContain('drop-refused')
    // The folder dragged cannot take itself.
    expect(row('kb/reports').className).toContain('drop-refused')
    expect(row('kb/notes').dataset.drop).toBe('move')

    fireEvent.dragOver(row('reference'), { dataTransfer: dt })
    expect(dt.dropEffect).toBe('none')
    fireEvent.dragOver(row('archive/2026'), { dataTransfer: dt })
    expect(dt.dropEffect).toBe('copy')
    fireEvent.drop(row('archive/2026'), { dataTransfer: dt })

    const dialog = await screen.findByRole('dialog', { name: 'Copy 1 item to another library' })
    expect(await within(dialog).findByText('To Archive: 2026')).toBeInTheDocument()
    expect(kbPosts).toHaveLength(0)
    // The drag is over: no folder is marked.
    expect(row('archive').className).not.toContain('drop-ok')
  })

  it('moves within one library as before', async () => {
    await openAll()
    const dt = transfer()
    fireEvent.dragStart(row('kb/reports'), { dataTransfer: dt })
    fireEvent.dragOver(row('kb/notes'), { dataTransfer: dt })
    expect(dt.dropEffect).toBe('move')
    fireEvent.drop(row('kb/notes'), { dataTransfer: dt })
    await waitFor(() => expect(kbPosts.some(p => p.url.startsWith('/api/kb/move'))).toBe(true))
    expect(kbPosts.find(p => p.url.startsWith('/api/kb/move'))!.body).toMatchObject({ paths: ['reports'], dest: 'notes' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('drags a read-only library\'s folder as a copy only, and never a library\'s own folder', async () => {
    await openAll()
    const dt = transfer()
    fireEvent.dragStart(row('reference/docs'), { dataTransfer: dt })
    expect(dt.effectAllowed).toBe('copy')
    expect(row('reference').getAttribute('draggable')).toBe('false')
    await waitFor(() => expect(row('kb/notes').dataset.drop).toBe('copy'))
    fireEvent.drop(row('kb/notes'), { dataTransfer: dt })
    const dialog = await screen.findByRole('dialog', { name: 'Copy 1 item to another library' })
    expect(await within(dialog).findByText('To Projects: notes')).toBeInTheDocument()
  })

  it('pastes a cut across libraries as a copy through Transfers', async () => {
    await openAll()
    fireEvent.contextMenu(row('kb/reports'))
    fireEvent.click(await screen.findByText('Cut'))
    fireEvent.contextMenu(row('archive/2026'))
    fireEvent.click(await screen.findByText('Paste 1 item'))
    const dialog = await screen.findByRole('dialog', { name: 'Copy 1 item to another library' })
    expect(await within(dialog).findByText('To Archive: 2026')).toBeInTheDocument()
    expect(kbPosts).toHaveLength(0)
  })
})

describe('Copy to another library', () => {
  it('copies a folder through Transfers, copy only, to another writable library first', async () => {
    serve()
    render(<LibraryView display={null} onDisplay={() => {}} />)
    fireEvent.contextMenu(await screen.findByText('reports'))
    fireEvent.click(await screen.findByText('Copy to another library…'))
    const dialog = await screen.findByRole('dialog', { name: 'Copy 1 item to another library' })
    // No Move: a move out of a shared library would delete the originals for everyone.
    expect(within(dialog).queryByRole('radio', { name: 'Move' })).toBeNull()
    expect(within(dialog).getByText(/The originals stay where they are/)).toBeInTheDocument()
    // The items' own library is never the default; the next writable library is.
    expect(await within(dialog).findByText('To Archive: /')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await screen.findByText(/confirm the plan/)
    const planCall = mock.calls.find(c => c.method === 'POST' && c.path === '/api/transfers/plan')
    expect(planCall?.body).toMatchObject({
      action: 'copy',
      source: { location: 'library:kb', paths: ['reports'] },
      destination: { location: 'library:archive', path: '' },
    })
  })

  it('is not offered when Transfers is not there', async () => {
    serve({ transfers: false })
    render(<LibraryView display={null} onDisplay={() => {}} />)
    fireEvent.contextMenu(await screen.findByText('notes.md'))
    await screen.findByText('Copy path')
    await waitFor(() => expect(screen.queryByText('Copy to another library…')).toBeNull())
  })
})

describe('defaultDestination', () => {
  it('prefers a library other than the items\' own when asked, skipping read-only ones', () => {
    const items = [{ library: 'kb', path: 'x' }]
    expect(defaultDestination(MOCK_LOCATIONS, items)?.id).toBe('computer:Downloads')
    expect(defaultDestination(MOCK_LOCATIONS, items, 'library')?.id).toBe('library:archive')
    expect(defaultDestination(MOCK_LOCATIONS, [{ library: 'archive', path: 'x' }], 'library')?.id).toBe('library:kb')
  })
})
