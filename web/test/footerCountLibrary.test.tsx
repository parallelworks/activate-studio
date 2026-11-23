import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

// The footer's count names the library it is of. Switching libraries while
// the new library's stats are slow (or fail) must not show the last
// library's count beside the new library's name.
describe('the footer count after switching libraries', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('drops the main library\'s count while My library\'s is not known', async () => {
    vi.resetModules()
    let mineAsked = false
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input)
      const path = url.split('?')[0]
      const lib = new Headers(init?.headers).get('x-library') ?? new URL(url, 'http://x').searchParams.get('library') ?? ''
      const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (path === '/api/config') return json({ kbLabel: 'Main', libraries: [{ id: 'kb', label: 'Main', primary: true }, { id: 'u-mine', label: 'Mine', primary: false }] })
      if (path === '/api/kb/stats') {
        if (lib === 'u-mine' || api.currentLibrary() === 'u-mine') { mineAsked = true; return new Promise<Response>(() => {}) }
        return json({ available: true, files: 1234, dirs: 4, totalBytes: 1e6 })
      }
      if (path === '/api/index/status') return json({ running: false, indexed: true, job: null, queued: [], runs: [], lastChanges: [] })
      if (path === '/healthz') return json({ ok: true, gufi: true })
      if (path === '/api/whoami') return json({ authEnabled: false, header: 'x', verified: false, user: null, token: null, claims: null })
      return new Response('{}', { status: 404 })
    }))
    const api = await import('../src/api')
    api.setLibrary('kb')
    const { StatusFooter } = await import('../src/components/StatusFooter')
    render(<StatusFooter />)
    expect(await screen.findByText(/1,234 files/)).toBeInTheDocument()
    act(() => api.setLibrary('u-mine'))
    await waitFor(() => expect(mineAsked).toBe(true))
    expect(screen.queryByText(/1,234 files/)).toBeNull()
  })

  it('says the count in a row that fits the narrowest sidebar, with which libraries it covers in its tooltip, and the sync row in sentence case', async () => {
    vi.resetModules()
    sessionStorage.setItem('ade-library', 'all')
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const path = String(input).split('?')[0]
      const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (path === '/api/config') return json({ kbLabel: 'Main', libraries: [{ id: 'kb', label: 'Main', primary: true }, { id: 'u-mine', label: 'Mine', primary: false }] })
      if (path === '/api/kb/stats') return json({ available: true, files: 22332, dirs: 4, totalBytes: 1e6 })
      if (path === '/api/index/status') return json({ running: false, indexed: true, job: null, queued: [], runs: [], lastChanges: [], lastSweepAt: new Date(Date.now() - 120_000).toISOString() })
      if (path === '/healthz') return json({ ok: true, gufi: true })
      if (path === '/api/whoami') return json({ authEnabled: false, header: 'x', verified: false, user: null, token: null, claims: null })
      return new Response('{}', { status: 404 })
    }))
    const api = await import('../src/api')
    api.settleLibraries(['kb', 'u-mine'])
    const { StatusFooter } = await import('../src/components/StatusFooter')
    render(<StatusFooter />)
    const files = await screen.findByText('22,332 files indexed')
    expect(files.closest('button')).toHaveAttribute('title', '22,332 files indexed in all libraries')
    expect(screen.queryByText(/in all libraries/)).toBeNull()
    expect(await screen.findByText('Synced 2m ago')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Sync now' })).toBeInTheDocument()
    sessionStorage.clear()
  })
})
