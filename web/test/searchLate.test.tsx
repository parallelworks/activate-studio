import { act, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SearchView } from '../src/views/SearchView'

describe('a search across libraries with one that had not answered in time', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('shows the results it has and names the library left out', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const body = String(input).startsWith('/api/search')
        ? { hits: [{ path: 'notes/a.md', name: 'a.md', size: 1, mtime: 0, mode: 'name', snippet: '', library: { id: 'kb', label: 'Notes' } }], slow: ['Remote A'] }
        : { tags: [] }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    render(<SearchView onOpen={() => {}} />)
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'notes' } })) })
    expect(await screen.findByText(/Still waiting for Remote A, so it is left out of these results\. Choose it alone in Searching to wait for it\./)).toBeInTheDocument()
    expect(screen.getByText('notes/a.md')).toBeInTheDocument()
  })
})
