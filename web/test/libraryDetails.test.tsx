import { act, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { formatBytes } from '@parallelworks/ui'
import { setLibrary } from '../src/api'
import type { AppConfig } from '../src/config'
import { reloadPanelState } from '../src/panelState'
import { selectLibraryEntry } from '../src/components/LibraryDetails'
import { StatusPanel, resetStatusPanelForTests } from '../src/components/StatusPanel'

// The Library page's Details card in the status panel: what is selected in
// the tree, described from its index (GET /api/kb/details).

const lib = { id: 'kb', label: 'Notes', primary: true, writable: true, pinned: true, source: true, caps: { index: true, fullText: true, vectors: false } }
const cfg: AppConfig = {
  appName: 'Studio', iconUrl: null, iconUrlDark: null, faviconUrl: null, faviconUrlDark: null, theme: 'light', accent: 'navy', surface: 'neutral',
  kbLabel: 'Notes', suggestedPrompts: [], bannerText: '', bannerColor: '#000', bannerWhenEmbedded: false,
  user: { id: 'u1', username: 'me' }, loaded: true, platform: false, libraries: [lib],
}
const base = { library: { id: 'kb', label: 'Notes', kind: 'Main folder', site: null, writable: true }, index: '/home/alex/.studio/gufi/Notes', fullText: true, ms: 12 }
const DETAILS: Record<string, unknown> = {
  '': { ...base, path: '', name: 'Notes', type: 'dir', size: 5_300_000, files: 120, dirs: 9, mtime: Math.floor(Date.now() / 1000) - 3600,
    types: [{ ext: 'pdf', count: 20, bytes: 4_000_000 }, { ext: 'md', count: 90, bytes: 1_200_000 }], labels: { own: [], inherited: [], within: [{ label: 'project', count: 12 }] } },
  papers: { ...base, path: 'papers', name: 'papers', type: 'dir', size: 4_100_000, files: 22, dirs: 2, mtime: Math.floor(Date.now() / 1000) - 60,
    types: [{ ext: 'pdf', count: 20, bytes: 4_000_000 }], labels: { own: ['research'], inherited: [], within: [{ label: 'draft', count: 3 }] } },
  'papers/a.pdf': { ...base, path: 'papers/a.pdf', name: 'a.pdf', type: 'file', size: 200_000, files: 1, dirs: 0, mtime: Math.floor(Date.now() / 1000) - 60,
    types: [{ ext: 'pdf', count: 1, bytes: 200_000 }], labels: { own: ['draft'], inherited: ['research'], within: [] } },
}
let asked: string[] = []

beforeEach(() => {
  asked = []
  localStorage.clear()
  localStorage.setItem('ade-status-panel', '1')
  reloadPanelState()
  resetStatusPanelForTests()
  setLibrary('kb')
  selectLibraryEntry(null)
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    asked.push(url)
    if (url.startsWith('/api/kb/details')) {
      const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
      if (p === 'fresh') return new Response(JSON.stringify({ error: 'fresh is not in the index yet' }), { status: 404 })
      return new Response(JSON.stringify(DETAILS[p]), { status: 200 })
    }
    return new Response('{}', { status: 404 })
  }))
})
afterEach(() => { vi.unstubAllGlobals() })

const card = () => screen.getByRole('button', { name: /^Details/ }).closest('section') as HTMLElement
const detailsAsked = () => asked.filter(u => u.startsWith('/api/kb/details'))

describe('the Details card', () => {
  it('describes the library with nothing selected, then the folder chosen', async () => {
    render(<StatusPanel view="library" cfg={cfg} />)
    expect(await within(card()).findByText(`Notes · ${formatBytes(5_300_000)}`)).toBeInTheDocument()
    const c = card()
    expect(within(c).getByText('120')).toBeInTheDocument()
    expect(within(c).getByText('Largest types')).toBeInTheDocument()
    expect(within(c).getByText('.pdf')).toBeInTheDocument()
    expect(within(c).getByText('project 12')).toBeInTheDocument()
    expect(within(c).getByText('/home/alex/.studio/gufi/Notes')).toBeInTheDocument()

    act(() => selectLibraryEntry({ path: 'papers', isDir: true }))
    expect(await within(card()).findByText(`papers · ${formatBytes(4_100_000)}`)).toBeInTheDocument()
    expect(within(card()).getByText('research')).toBeInTheDocument()
    expect(within(card()).getByText('draft 3')).toBeInTheDocument()
    expect(detailsAsked()).toEqual(['/api/kb/details?path=', '/api/kb/details?path=papers'])
  })

  it('describes a file: its size, when it changed, and its own and inherited labels', async () => {
    selectLibraryEntry({ path: 'papers/a.pdf', isDir: false })
    render(<StatusPanel view="library" cfg={cfg} />)
    expect(await within(card()).findByText(`a.pdf · ${formatBytes(200_000)}`)).toBeInTheDocument()
    expect(within(card()).getByText('Modified')).toBeInTheDocument()
    expect(within(card()).queryByText('Files')).toBeNull()
    expect(within(card()).getByTitle('From a folder above')).toHaveTextContent('research')
  })

  it('says what the server said when a folder is not indexed yet', async () => {
    selectLibraryEntry({ path: 'fresh', isDir: true })
    render(<StatusPanel view="library" cfg={cfg} />)
    expect(await within(card()).findByText('fresh is not in the index yet')).toBeInTheDocument()
  })

  it('with all libraries, waits for a choice; on other pages it is not there and asks nothing', async () => {
    setLibrary('all')
    const { unmount } = render(<StatusPanel view="library" cfg={cfg} />)
    expect(await within(card()).findByText(/Select a folder or file in the tree/)).toBeInTheDocument()
    expect(detailsAsked()).toEqual([])
    act(() => selectLibraryEntry({ path: 'kb/papers', isDir: true }))
    await waitFor(() => expect(detailsAsked()).toEqual(['/api/kb/details?path=papers']))
    unmount()
    setLibrary('kb')
    asked = []
    render(<StatusPanel view="search" cfg={cfg} />)
    await new Promise(r => setTimeout(r, 200))
    expect(screen.queryByRole('button', { name: /^Details/ })).toBeNull()
    expect(detailsAsked()).toEqual([])
  })
})
