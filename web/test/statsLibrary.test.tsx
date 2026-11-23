import { act, render, renderHook, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { resetMapLibrariesForTests, saveMapLibrary, useMapLibrary } from '../src/mapLibrary'

// Stats for a library other than the main one shows that library: its maps
// follow the page's choice, and the main library's index runs are not
// listed as its own.
const LIBS = [{ id: 'kb' }, { id: 'u-mine' }, { id: 'ref' }]

describe('the maps follow the library Stats shows', () => {
  beforeEach(() => resetMapLibrariesForTests())

  it('opens on the page\'s library when it is not the main one', () => {
    saveMapLibrary('topology', 'ref')
    const { result } = renderHook(() => useMapLibrary('topology', LIBS, 'kb', 'u-mine'))
    expect(result.current[0]).toBe('u-mine')
  })

  it('keeps the choice made here while the page shows the main library, and follows a switch', () => {
    saveMapLibrary('topology', 'ref')
    const { result, rerender } = renderHook(({ follow }) => useMapLibrary('topology', LIBS, 'kb', follow), { initialProps: { follow: 'kb' as string } })
    expect(result.current[0]).toBe('ref')
    rerender({ follow: 'u-mine' })
    expect(result.current[0]).toBe('u-mine')
    rerender({ follow: 'kb' })
    expect(result.current[0]).toBe('kb')
    // All libraries has no map of its own: the map stays where it is.
    rerender({ follow: 'all' })
    expect(result.current[0]).toBe('kb')
  })

  it('falls back when the page\'s library has no map', () => {
    const { result } = renderHook(() => useMapLibrary('topology', [{ id: 'kb' }], 'kb', 'u-mine'))
    expect(result.current[0]).toBe('kb')
  })
})

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
const lib = (id: string, label: string) => ({ id, label, primary: id === 'kb', writable: true, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false } })

describe('Index runs on Stats', () => {
  beforeEach(() => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Projects', libraries: [lib('kb', 'Projects'), lib('u-mine', 'zz-mine')] })
      if (url.startsWith('/api/tags')) return json({ tags: [] })
      if (url.startsWith('/api/semantic-maps')) return json({ maps: [] })
      if (url.startsWith('/api/index/status')) return json({ indexed: true, running: false, runs: [{ id: 1, kind: 'sync', label: 'Syncing changes', state: 'done', ms: 54, finishedAt: new Date().toISOString(), phases: [] }] })
      return json({})
    }))
  })
  afterEach(() => { vi.unstubAllGlobals(); setLibrary('kb') })

  it('are the main library\'s, so another library says where its own index is', async () => {
    setLibrary('u-mine')
    const { OverviewView } = await import('../src/views/OverviewView')
    render(<OverviewView onOpen={() => {}} />)
    await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 30)) })
    // The view is imported and drawn here: under a full parallel run that
    // takes longer than the defaults allow.
    expect(await screen.findByText(/The index runs listed here are the main library's\. zz-mine has its own index/, undefined, { timeout: 10_000 })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Update index' })).toBeNull()
  }, 20_000)
})
