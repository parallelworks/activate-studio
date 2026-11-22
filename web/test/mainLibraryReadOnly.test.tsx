import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from '@parallelworks/ui'
import { LibraryView } from '../src/views/LibraryView'
import { setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { resetTransfersProbe } from '../src/transferApi'
import { createTransfersMock, MOCK_LOCATIONS } from './transfersMock'

// On a Studio with sign-in, the main library is read only for everyone but
// its administrators: the server says why (readOnlyWhy), and the Library
// offers no change there, labels included, with that reason.
const WHY = 'On a Studio with sign-in, the main library is read only: only its administrators change it. Use My library, or a library of your own on one of your systems, for your files.'
const lib = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id, label, primary: id === 'kb', writable: true, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false }, ...extra,
})
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
const dir = (path: string) => ({ name: path.split('/').pop(), path, type: 'dir', size: 0, mtime: 0 })
const file = (path: string) => ({ name: path.split('/').pop(), path, type: 'file', size: 10, mtime: 0 })

function serve(main: Record<string, unknown>) {
  const mock = createTransfersMock({ locations: MOCK_LOCATIONS })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (url.startsWith('/api/transfers')) {
      const r = mock.handle(init?.method ?? 'GET', url, init?.body ? JSON.parse(String(init.body)) : undefined)
      return json(r.body, r.status)
    }
    if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Projects', libraries: [lib('kb', 'Projects', main), lib('u-space-studio', 'My library', { space: true })] })
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
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb') })

/** The page's config is this test's: it is fetched once and kept, so an
 *  earlier test's is replaced by asking for it again. */
const configLoaded = async () => { await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) }) }
const item = (name: string) => screen.getByRole('menuitem', { name: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`) })

describe('the main library for a signed-in person who does not administer the Studio', () => {
  it('offers no change in it, labels included, and says why', async () => {
    serve({ writable: false, readOnlyWhy: WHY, access: { where: 'here', readAs: 'studio', write: false, index: 'built' } })
    render(<LibraryView display={null} onDisplay={() => {}} />)
    await configLoaded()
    fireEvent.contextMenu(await screen.findByText('reports'))
    await screen.findByRole('menuitem', { name: /^Download as zip/ })
    for (const name of ['New folder', 'Upload files…', 'Upload a folder…', 'Rename…', 'Cut', 'Labels…', 'Delete folder…']) {
      expect(item(name), name).toHaveAttribute('aria-disabled', 'true')
    }
    // Reading and copying out stay.
    expect(item('Download as zip')).not.toHaveAttribute('aria-disabled', 'true')
    expect(item('Copy path')).not.toHaveAttribute('aria-disabled', 'true')
    expect(document.body.textContent).toContain('only its administrators change it')
  })

  it('gives the reason on the disabled + Add, as its description and on a click or tap', async () => {
    serve({ writable: false, readOnlyWhy: WHY, access: { where: 'here', readAs: 'studio', write: false, index: 'built' } })
    const info = vi.spyOn(toast, 'info').mockImplementation(() => '')
    render(<LibraryView display={null} onDisplay={() => {}} />)
    await configLoaded()
    await screen.findByText('reports')
    const add = screen.getByRole('button', { name: '+ Add' })
    expect(add).toHaveAttribute('aria-disabled', 'true')
    expect(add).toHaveAttribute('aria-description', WHY)
    expect(info).not.toHaveBeenCalled()
    fireEvent.click(add)
    expect(info).toHaveBeenCalledWith(WHY, expect.anything())
  })

  it('stays as it was for an administrator', async () => {
    serve({})
    render(<LibraryView display={null} onDisplay={() => {}} />)
    await configLoaded()
    fireEvent.contextMenu(await screen.findByText('reports'))
    await screen.findByRole('menuitem', { name: /^Download as zip/ })
    for (const name of ['New folder', 'Upload files…', 'Rename…', 'Labels…', 'Delete folder…']) {
      expect(item(name), name).not.toHaveAttribute('aria-disabled', 'true')
    }
  })
})
