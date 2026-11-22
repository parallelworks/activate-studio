import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LibraryView } from '../src/views/LibraryView'
import { api, setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { resetTransfersProbe } from '../src/transferApi'

// Renaming the open file in My library (a library on a system) keeps it open
// under its new name, as in the main library, and a later Delete closes.
const lib = (id: string, label: string, extra: Record<string, unknown> = {}) => ({
  id, label, primary: id === 'kb', writable: true, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false }, ...extra,
})
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
const file = (path: string) => ({ name: path.split('/').pop(), path, type: 'file', size: 10, mtime: 0 })

function serve(renameReply: unknown) {
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Projects', libraries: [lib('kb', 'Projects'), lib('u-mine', 'My library', { mine: true })] })
    if (url.startsWith('/api/kb/tree')) return json({ path: '', entries: [file('notes.md'), file('other.md')] })
    if (url.startsWith('/api/kb/rename')) return json(renameReply)
    if (url.startsWith('/api/kb/file') && init?.method === 'DELETE') return json({ deleted: ['other.md'], indexMs: 0, reindexing: true })
    if (url.startsWith('/api/kb/file')) return json({ path: 'notes.md', type: 'text', content: '# Notes\n', size: 8, mtime: 0 })
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], tags: [] })
    if (url.startsWith('/api/transfers')) return json({ locations: [] })
    return json({})
  }))
}

beforeEach(() => {
  resetTransfersProbe()
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  Element.prototype.scrollIntoView = () => {}
  setLibrary('u-mine')
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb') })

async function renameOpenFile(onDisplay: (d: unknown) => void) {
  render(<LibraryView display={{ kind: 'file', target: 'notes.md' }} onDisplay={onDisplay} />)
  await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) })
  const rows = await screen.findAllByText('notes.md')
  fireEvent.contextMenu(rows[0])
  fireEvent.click(await screen.findByRole('menuitem', { name: /^Rename/ }))
  const dialog = await screen.findByRole('dialog', { name: 'Rename' })
  fireEvent.change(within(dialog).getByRole('textbox', { name: 'New name' }), { target: { value: 'plan.md' } })
  fireEvent.click(within(dialog).getByRole('button', { name: 'Rename' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Rename' })).toBeNull())
}

describe('Rename in My library', () => {
  it('keeps the open file open under its new name', async () => {
    serve({ renamed: { from: 'notes.md', to: 'plan.md' }, indexMs: 0, reindexing: true })
    const onDisplay = vi.fn()
    await renameOpenFile(onDisplay)
    expect(onDisplay).toHaveBeenCalledWith({ kind: 'file', target: 'plan.md' })
  })

  it('never opens "undefined" from a reply without both names', async () => {
    serve({ renamed: 'plan.md', indexMs: 0, reindexing: true })
    const onDisplay = vi.fn()
    await renameOpenFile(onDisplay)
    expect(onDisplay).not.toHaveBeenCalledWith({ kind: 'file', target: undefined })
    expect(onDisplay.mock.calls.every(([d]) => d === null || typeof (d as { target?: unknown }).target === 'string')).toBe(true)
  })

  it('reads a rename reply as from and to', async () => {
    serve({ renamed: { from: 'notes.md', to: 'plan.md' }, indexMs: 0 })
    expect((await api.rename('notes.md', 'plan.md')).renamed).toEqual({ from: 'notes.md', to: 'plan.md' })
    serve({ renamed: 'plan.md', indexMs: 0 })
    expect((await api.rename('notes.md', 'plan.md')).renamed).toBeNull()
  })
})
