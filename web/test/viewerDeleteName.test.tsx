import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { Viewer } from '../src/components/Viewer'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

// Phase 4, L: the viewer's Delete dialog showed the library's id
// (u-space-zz/uploads/a.txt) where its name should be.
describe('the viewer in All libraries', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); setLibrary('kb') })

  it('names the library in its header and its Delete dialog', async () => {
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url === '/api/config') return json({ appName: 'Studio', kbLabel: 'My library', libraries: [{ id: 'kb', label: 'My library', primary: true, writable: true }, { id: 'u-space-zz', label: 'zz-test-mylib', primary: false, writable: true }] })
      if (url.startsWith('/api/kb/file')) return json({ path: 'u-space-zz/uploads/a.txt', kind: 'text', size: 5, mtime: 1, truncated: false, content: 'hello' })
      if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: 'files' })
      if (url.startsWith('/api/tags')) return json({ tags: [] })
      return json({})
    }))
    setLibrary('all')
    render(<Viewer path="u-space-zz/uploads/a.txt" onDeleted={() => {}} />)
    act(() => reloadAppConfig())
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    expect(await screen.findByText('Delete this file?')).toBeInTheDocument()
    expect(screen.getAllByText('zz-test-mylib/uploads/a.txt').length).toBeGreaterThan(1)
    expect(document.body.textContent).not.toContain('u-space-zz')
  })
})
