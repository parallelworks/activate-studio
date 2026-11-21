import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LibraryView } from '../src/views/LibraryView'
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
function serve(o: { transfers?: boolean } = {}) {
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
      return json({ path: at, entries: at === '' ? [dir('reports'), file('notes.md')] : [] })
    }
    return json({})
  }))
}

beforeEach(() => {
  resetTransfersProbe()
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

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
