import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SearchView } from '../src/views/SearchView'

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

describe('a search across libraries with one stopped at the time limit', () => {
  afterEach(() => { vi.unstubAllGlobals(); location.hash = '' })

  it('shows the results it has, names the library left out, and links the setting', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => json(String(input).startsWith('/api/search')
      ? { hits: [{ path: 'notes/a.md', name: 'a.md', size: 1, mtime: 0, mode: 'name', snippet: '', library: { id: 'kb', label: 'Notes' } }], slow: ['Remote A'],
          omitted: [{ id: 'remote-a', label: 'Remote A', reason: 'did not answer within 20 s', partial: false, timeLimit: true }] }
      : { tags: [] })))
    render(<SearchView onOpen={() => {}} />)
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'notes' } })) })
    const line = await screen.findByRole('status')
    expect(line).toHaveTextContent('Left out: Remote A (did not answer within 20 s).')
    expect(screen.getByText('notes/a.md')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Change the search time limit' }))
    expect(location.hash).toBe('#view=settings:sync-search')
  })

  it('has no link to the setting when nothing passed the limit', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => json(String(input).startsWith('/api/search')
      ? { hits: [], omitted: [{ id: 'remote-a', label: 'Remote A', reason: 'its index is missing', partial: false }] }
      : { tags: [] })))
    render(<SearchView onOpen={() => {}} />)
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'notes' } })) })
    await screen.findByRole('status')
    expect(screen.queryByRole('button', { name: 'Change the search time limit' })).toBeNull()
  })
})

describe('stopping a search', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('aborts the request and says the search stopped', async () => {
    let signal: AbortSignal | undefined
    vi.stubGlobal('fetch', vi.fn((input: string, init?: RequestInit) => {
      if (!String(input).startsWith('/api/search')) return Promise.resolve(json({ tags: [] }))
      signal = init?.signal ?? undefined
      // A search that runs until it is stopped.
      return new Promise<Response>((_, reject) => {
        signal?.addEventListener('abort', () => reject(new DOMException('The operation was aborted.', 'AbortError')))
      })
    }))
    render(<SearchView onOpen={() => {}} />)
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'huge' } })) })
    const stop = await screen.findByRole('button', { name: 'Stop' })
    expect(signal?.aborted).toBe(false)
    await act(async () => { fireEvent.click(stop) })
    expect(signal?.aborted).toBe(true)
    expect(await screen.findByText('Search stopped.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
  })
})
